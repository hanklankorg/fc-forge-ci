// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	appversion "github.com/diggsweden/forge-ci/internal/app/version"
	"github.com/diggsweden/forge-ci/internal/archive"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

func planDigest(p snapshotimage.Plan) string {
	body, _ := json.Marshal(p)
	return fmt.Sprintf("%x", sha256.Sum256(body))
}

func newOutput(path string, p snapshotimage.Plan) error {
	if path == "" || filepath.Clean(path) == "." || filepath.Clean(path) == string(filepath.Separator) {
		return fmt.Errorf("snapshot output must be a new directory")
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if strings.EqualFold(part, ".git") {
			return fmt.Errorf("snapshot output cannot replace Git metadata")
		}
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("snapshot output directory already exists: %s", path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		root, err := pathsafe.OpenRoot(parent)
		if err == nil {
			_ = root.Close()
			break
		}
		if !errors.Is(err, fs.ErrNotExist) || filepath.Dir(parent) == parent {
			return err
		}
	}
	for _, b := range p.Builds {
		for _, d := range b.RequiredArtifacts {
			if inside(d.Destination, path) || inside(path, d.Destination) {
				return fmt.Errorf("snapshot output overlaps dependency inputs")
			}
		}
	}
	return nil
}

func exportTree(source, output string, p snapshotimage.Plan) error {
	output = filepath.Clean(output)
	if err := newOutput(output, p); err != nil {
		return err
	}
	files, err := dependencyFilesAt(source, ".")
	if err != nil {
		return err
	}
	stage, err := pathsafe.NewArtifactStaging(filepath.Dir(output))
	if err != nil {
		return err
	}
	defer func() { _ = stage.Close() }()
	for _, file := range files {
		original := file.Path
		file.Path = filepath.Join(filepath.Base(output), original)
		if err := stageContextFile(filepath.Join(source, original), stage.Root(), file); err != nil {
			return err
		}
	}
	return stage.Install()
}

func npmTarballs(dir string) ([]string, error) {
	root, err := pathsafe.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tgz") || strings.HasSuffix(e.Name(), ".tar.gz") {
			if !e.Type().IsRegular() {
				return nil, fmt.Errorf("npm tarball must be a regular file")
			}
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out, nil
}

func npmIdentity(body []byte) (string, string, error) {
	if err := uniqueJSONKeys(body); err != nil {
		return "", "", err
	}
	var value struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &value); err != nil {
		return "", "", err
	}
	if value.Name == "" || value.Version == "" {
		return "", "", fmt.Errorf("npm package requires name and version")
	}
	return value.Name, value.Version, nil
}

func compareNPMPackage(a pipeline.PlannedArtifact, body []byte) error {
	source, err := readJSONFile(filepath.Join(a.WorkingDirectory, "package.json"))
	if err != nil {
		return err
	}
	name, version, err := npmIdentity(source)
	if err != nil {
		return err
	}
	actualName, actualVersion, err := npmIdentity(body)
	if err != nil {
		return err
	}
	if name != actualName || version != actualVersion {
		return fmt.Errorf("npm producer package identity differs from committed source")
	}
	return nil
}

func prepareProducer(p snapshotimage.Plan, a pipeline.PlannedArtifact, from, output string) error {
	tmp, err := os.MkdirTemp("", "forge-ci-snapshot-producer-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	root, err := pathsafe.OpenRoot(tmp)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	source := from
	if source == "" {
		switch a.ProjectType {
		case projecttype.Go, projecttype.Cargo:
			source = filepath.Join(a.WorkingDirectory, "dist")
		case projecttype.Maven:
			source = filepath.Join(a.WorkingDirectory, "target")
		case projecttype.Gradle:
			source = filepath.Join(a.WorkingDirectory, "build/libs")
		case projecttype.NPM:
			source = a.WorkingDirectory
		}
	}
	if a.ProjectType == projecttype.NPM {
		tarballs, err := npmTarballs(source)
		if err != nil {
			return err
		}
		if len(tarballs) != 1 {
			return fmt.Errorf("npm producer requires exactly one tarball, found %d", len(tarballs))
		}
		body, err := archive.ReadTarGzipMember(tarballs[0], "package/package.json", 4<<20)
		if err != nil {
			return err
		}
		if err := compareNPMPackage(a, body); err != nil {
			return err
		}
		extracted := filepath.Join(tmp, "package")
		if err := archive.UntarStripOne(tarballs[0], extracted); err != nil {
			return err
		}
		preparedManifest, err := readJSONFile(filepath.Join(extracted, "package.json"))
		if err != nil {
			return err
		}
		if !bytes.Equal(body, preparedManifest) {
			return fmt.Errorf("npm tarball changed between inspection and extraction")
		}
		source = extracted
	}
	rawFiles, err := dependencyFilesAt(source, ".")
	if err != nil {
		return err
	}
	files := map[string]snapshotimage.DependencyFile{}
	destinations := map[string]bool{}
	for _, b := range p.Builds {
		for _, d := range b.RequiredArtifacts {
			if d.Name == a.Name {
				destinations[d.Destination] = true
			}
		}
	}
	for destination := range destinations {
		for _, file := range rawFiles {
			selected := a.ProjectType == projecttype.NPM
			switch a.ProjectType {
			case projecttype.Go, projecttype.Cargo:
				for _, platform := range p.ArtifactPlatforms(a.Name) {
					if strings.HasPrefix(file.Path, strings.ReplaceAll(platform, "/", "-")+"/") {
						selected = true
					}
				}
			case projecttype.Maven, projecttype.Gradle:
				selected = filepath.Dir(file.Path) == "." && strings.HasSuffix(file.Path, ".jar")
			}
			if !selected {
				continue
			}
			file.Mode = producerFileMode(p, a, file)
			target := filepath.ToSlash(filepath.Join(destination, file.Path))
			if err := restorePath(target, "snapshot-plan.json"); err != nil {
				return err
			}
			bound := file
			bound.Path = target
			files[target] = bound
			copy := bound
			copy.Path = filepath.Join("files", target)
			if err := stageContextFile(filepath.Join(source, file.Path), root, copy); err != nil {
				return err
			}
		}
	}
	for _, b := range p.Builds {
		for _, d := range b.RequiredArtifacts {
			if d.Name == a.Name {
				list := []snapshotimage.DependencyFile{}
				for path, file := range files {
					if strings.HasPrefix(path, d.Destination+"/") {
						list = append(list, file)
					}
				}
				if err := requireDependencyFiles(d, b.Arch, list); err != nil {
					return err
				}
				if a.ProjectType == projecttype.Go || a.ProjectType == projecttype.Cargo {
					wanted := d.Destination + "/" + plannedNativeBinary(a, "linux/"+b.Arch)
					if !slices.ContainsFunc(list, func(file snapshotimage.DependencyFile) bool {
						return file.Path == wanted && file.Size > 0 && file.Mode == "0755"
					}) {
						return fmt.Errorf("snapshot producer %s is missing planned executable %s", a.Name, wanted)
					}
				}
			}
		}
	}
	manifest := snapshotimage.ArtifactReceipt{Version: snapshotimage.Version, SourceSHA: p.SourceSHA, SourceBranch: p.SourceBranch, PlanSHA256: planDigest(p), Artifact: a, Platforms: p.ArtifactPlatforms(a.Name), Files: []snapshotimage.DependencyFile{}}
	for _, file := range files {
		manifest.Files = append(manifest.Files, file)
	}
	slices.SortFunc(manifest.Files, func(a, b snapshotimage.DependencyFile) int { return strings.Compare(a.Path, b.Path) })
	if err := WriteJSON(filepath.Join(tmp, snapshotimage.ProducerManifest), manifest); err != nil {
		return err
	}
	if a.ProjectType == projecttype.NPM {
		if err := os.RemoveAll(filepath.Join(tmp, "package")); err != nil {
			return err
		}
	}
	return exportTree(tmp, output, p)
}

// PrepareInputs imports already-built, exact CI artifact downloads. It performs
// no ecosystem builds and is the GitHub reuse path for the existing build stage.
func PrepareInputs(ctx context.Context, ops appversion.GenerateSnapshotOps, p snapshotimage.Plan, input, output string) error {
	if err := CheckSource(ctx, ops, p.SourceSHA); err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return err
	}
	output = cmp.Or(output, snapshotimage.DefaultProducerDir)
	if err := newOutput(output, p); err != nil {
		return err
	}
	if err := producerDirectories(input, p); err != nil {
		return err
	}
	if len(p.Artifacts) == 0 {
		return nil
	}
	stage, err := os.MkdirTemp("", "forge-ci-snapshot-import-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	for _, a := range p.Artifacts {
		if err := prepareProducer(p, a, filepath.Join(input, a.Name), filepath.Join(stage, a.Name)); err != nil {
			return err
		}
	}
	return exportTree(stage, output, p)
}

func producerDirectories(dir string, p snapshotimage.Plan) error {
	if len(p.Artifacts) == 0 {
		if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
			return nil
		}
	}
	root, err := pathsafe.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return err
	}
	if len(entries) != len(p.Artifacts) {
		return fmt.Errorf("snapshot inputs must contain exactly the planned producers")
	}
	for _, e := range entries {
		if !e.IsDir() {
			return fmt.Errorf("snapshot producer input must be a real directory")
		}
		if !slices.ContainsFunc(p.Artifacts, func(a pipeline.PlannedArtifact) bool { return a.Name == e.Name() }) {
			return fmt.Errorf("unplanned snapshot producer %s", e.Name())
		}
	}
	return nil
}

func readProducer(p snapshotimage.Plan, a pipeline.PlannedArtifact, dir string) (snapshotimage.ArtifactReceipt, error) {
	var manifest snapshotimage.ArtifactReceipt
	if err := ReadJSON(filepath.Join(dir, snapshotimage.ProducerManifest), &manifest); err != nil {
		return manifest, err
	}
	artifactJSON, _ := json.Marshal(a)
	recordedJSON, _ := json.Marshal(manifest.Artifact)
	if manifest.Version != snapshotimage.Version || manifest.SourceSHA != p.SourceSHA || manifest.SourceBranch != p.SourceBranch || manifest.PlanSHA256 != planDigest(p) || string(artifactJSON) != string(recordedJSON) || !slices.Equal(manifest.Platforms, p.ArtifactPlatforms(a.Name)) {
		return manifest, fmt.Errorf("snapshot producer %s differs from frozen plan/source/platform identity", a.Name)
	}
	actual, err := dependencyFilesAt(filepath.Join(dir, "files"), ".")
	if err != nil {
		return manifest, err
	}
	if err := validateTransportFiles(actual, manifest.Files); err != nil {
		return manifest, fmt.Errorf("snapshot producer %s: %w", a.Name, err)
	}
	for _, file := range manifest.Files {
		if err := restorePath(file.Path, "snapshot-plan.json"); err != nil {
			return manifest, err
		}
		planned := false
		for _, b := range p.Builds {
			for _, d := range b.RequiredArtifacts {
				if d.Name == a.Name && strings.HasPrefix(file.Path, d.Destination+"/") {
					planned = true
					if a.ProjectType == projecttype.NPM && file.Path == d.Destination+"/package.json" {
						body, err := readJSONFile(filepath.Join(dir, "files", file.Path))
						if err != nil {
							return manifest, err
						}
						if err := compareNPMPackage(a, body); err != nil {
							return manifest, err
						}
					}
				}
			}
		}
		if !planned {
			return manifest, fmt.Errorf("unplanned snapshot producer file %s", file.Path)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return manifest, err
	}
	if len(entries) != 2 {
		return manifest, fmt.Errorf("snapshot producer contains unplanned entries")
	}
	return manifest, nil
}

// GitHub raw artifact ZIPs already lost execute bits before a producer receipt
// exists. Only the exact Linux binary filenames selected by the existing Go /
// Cargo build CLI receive executable intent; adjacent data files do not.
func producerFileMode(p snapshotimage.Plan, a pipeline.PlannedArtifact, file snapshotimage.DependencyFile) string {
	if a.ProjectType != projecttype.Go && a.ProjectType != projecttype.Cargo {
		return file.Mode
	}
	for _, platform := range p.ArtifactPlatforms(a.Name) {
		if file.Path == plannedNativeBinary(a, platform) {
			return "0755"
		}
	}
	return file.Mode
}

func plannedNativeBinary(a pipeline.PlannedArtifact, platform string) string {
	name := a.Name
	if a.Go != nil && a.Go.BinaryName != "" {
		name = a.Go.BinaryName
	}
	if a.Cargo != nil && a.Cargo.BinaryName != "" {
		name = a.Cargo.BinaryName
	}
	suffix := strings.ReplaceAll(platform, "/", "-")
	return suffix + "/" + name + "-" + suffix
}
