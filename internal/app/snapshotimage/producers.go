// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
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

var (
	errOutputNotNewDirectory    = errors.New("snapshot output must be a new directory")
	errOutputReplacesGit        = errors.New("snapshot output cannot replace Git metadata")
	errOutputExists             = errors.New("snapshot output directory already exists")
	errOutputOverlapsInputs     = errors.New("snapshot output overlaps dependency inputs")
	errTarballNotRegular        = errors.New("npm tarball must be a regular file")
	errNPMIdentityRequired      = errors.New("npm package requires name and version")
	errNPMIdentityDiffers       = errors.New("npm producer package identity differs from committed source")
	errNPMTarballCount          = errors.New("npm producer requires exactly one tarball")
	errNPMTarballChanged        = errors.New("npm tarball changed between inspection and extraction")
	errMissingPlannedExecutable = errors.New("is missing planned executable")
	errProducerSetDiffers       = errors.New("snapshot inputs must contain exactly the planned producers")
	errProducerNotDirectory     = errors.New("snapshot producer input must be a real directory")
	errUnplannedProducer        = errors.New("unplanned snapshot producer")
	errProducerIdentityDiffers  = errors.New("differs from frozen plan/source/platform identity")
	errUnplannedProducerFile    = errors.New("unplanned snapshot producer file")
	errProducerUnplannedEntries = errors.New("snapshot producer contains unplanned entries")
)

func planDigest(plan snapshotimage.Plan) (string, error) {
	body, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%x", sha256.Sum256(body)), nil
}

func newOutput(path string, plan snapshotimage.Plan) error {
	if path == "" || filepath.Clean(path) == "." || filepath.Clean(path) == string(filepath.Separator) {
		return errOutputNotNewDirectory
	}

	for part := range strings.SplitSeq(filepath.ToSlash(path), "/") {
		if strings.EqualFold(part, ".git") {
			return errOutputReplacesGit
		}
	}

	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%w: %s", errOutputExists, path)
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

	return requireNoDependencyOverlap(path, plan)
}

func requireNoDependencyOverlap(path string, plan snapshotimage.Plan) error {
	for _, build := range plan.Builds {
		for _, dep := range build.RequiredArtifacts {
			if inside(dep.Destination, path) || inside(path, dep.Destination) {
				return errOutputOverlapsInputs
			}
		}
	}

	return nil
}

func exportTree(source, output string, plan snapshotimage.Plan) error {
	output = filepath.Clean(output)
	if err := newOutput(output, plan); err != nil {
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
				return nil, errTarballNotRegular
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
		return "", "", errNPMIdentityRequired
	}

	return value.Name, value.Version, nil
}

func compareNPMPackage(planned pipeline.PlannedArtifact, body []byte) error {
	source, err := readJSONFile(filepath.Join(planned.WorkingDirectory, "package.json"))
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
		return errNPMIdentityDiffers
	}

	return nil
}

func prepareProducer(plan snapshotimage.Plan, planned pipeline.PlannedArtifact, from, output string) error {
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
		source = producerSource(planned)
	}

	if planned.ProjectType == projecttype.NPM {
		source, err = extractNPMProducer(planned, source, tmp)
		if err != nil {
			return err
		}
	}

	files, err := stageProducerFiles(plan, planned, source, root)
	if err != nil {
		return err
	}

	if err := requireProducerFiles(plan, planned, files); err != nil {
		return err
	}

	digest, err := planDigest(plan)
	if err != nil {
		return err
	}

	manifest := snapshotimage.ArtifactReceipt{Version: snapshotimage.Version, SourceSHA: plan.SourceSHA, SourceBranch: plan.SourceBranch, PlanSHA256: digest, Artifact: planned, Platforms: plan.ArtifactPlatforms(planned.Name), Files: []snapshotimage.DependencyFile{}}
	for _, file := range files {
		manifest.Files = append(manifest.Files, file)
	}

	slices.SortFunc(manifest.Files, func(left, right snapshotimage.DependencyFile) int { return strings.Compare(left.Path, right.Path) })

	if err := WriteJSON(filepath.Join(tmp, snapshotimage.ProducerManifest), manifest); err != nil {
		return err
	}

	if planned.ProjectType == projecttype.NPM {
		if err := os.RemoveAll(filepath.Join(tmp, "package")); err != nil {
			return err
		}
	}

	return exportTree(tmp, output, plan)
}

// downloadSource is where the build stage's uploaded artifact, downloaded to
// dir, holds what the build left in producerSource. The Maven build uploads
// its POMs beside target/, so its jars keep that directory. The Gradle build
// uploads build/libs of the project and of every subproject, so the project's
// own jars keep build/libs. Every other build uploads the content of its
// output directory alone.
func downloadSource(planned pipeline.PlannedArtifact, dir string) string {
	switch planned.ProjectType {
	case projecttype.Maven:
		return filepath.Join(dir, "target")
	case projecttype.Gradle:
		return filepath.Join(dir, "build/libs")
	default:
		return dir
	}
}

// producerSource is the existing build CLI's output directory for a project type.
func producerSource(planned pipeline.PlannedArtifact) string {
	switch planned.ProjectType {
	case projecttype.Go, projecttype.Cargo:
		return filepath.Join(planned.WorkingDirectory, "dist")
	case projecttype.Maven:
		return filepath.Join(planned.WorkingDirectory, "target")
	case projecttype.Gradle:
		return filepath.Join(planned.WorkingDirectory, "build/libs")
	case projecttype.NPM:
		return planned.WorkingDirectory
	default:
		return ""
	}
}

// extractNPMProducer verifies the single npm tarball against the committed
// package identity and extracts it below tmp, returning the extracted root.
func extractNPMProducer(planned pipeline.PlannedArtifact, source, tmp string) (string, error) {
	tarballs, err := npmTarballs(source)
	if err != nil {
		return "", err
	}

	if len(tarballs) != 1 {
		return "", fmt.Errorf("%w, found %d", errNPMTarballCount, len(tarballs))
	}

	body, err := archive.ReadTarGzipMember(tarballs[0], "package/package.json", 4<<20)
	if err != nil {
		return "", err
	}

	if err := compareNPMPackage(planned, body); err != nil {
		return "", err
	}

	extracted := filepath.Join(tmp, "package")
	if err := archive.UntarStripOne(tarballs[0], extracted); err != nil {
		return "", err
	}

	preparedManifest, err := readJSONFile(filepath.Join(extracted, "package.json"))
	if err != nil {
		return "", err
	}

	if !bytes.Equal(body, preparedManifest) {
		return "", errNPMTarballChanged
	}

	return extracted, nil
}

// stageProducerFiles selects the artifact's files for every planned
// destination, stages them below root and returns them keyed by target path.
func stageProducerFiles(plan snapshotimage.Plan, planned pipeline.PlannedArtifact, source string, root *os.Root) (map[string]snapshotimage.DependencyFile, error) {
	rawFiles, err := dependencyFilesAt(source, ".")
	if err != nil {
		return nil, err
	}

	files := map[string]snapshotimage.DependencyFile{}
	destinations := map[string]bool{}

	for _, build := range plan.Builds {
		for _, dep := range build.RequiredArtifacts {
			if dep.Name == planned.Name {
				destinations[dep.Destination] = true
			}
		}
	}

	for destination := range destinations {
		for _, file := range rawFiles {
			if !producerFileSelected(plan, planned, file) {
				continue
			}

			file.Mode = producerFileMode(plan, planned, file)

			target := filepath.ToSlash(filepath.Join(destination, file.Path))
			if err := restorePath(target, "snapshot-plan.json"); err != nil {
				return nil, err
			}

			bound := file
			bound.Path = target
			files[target] = bound
			staged := bound

			staged.Path = filepath.Join("files", target)
			if err := stageContextFile(filepath.Join(source, file.Path), root, staged); err != nil {
				return nil, err
			}
		}
	}

	return files, nil
}

// producerFileSelected reports whether a raw producer file is an input the
// project type's consumers use.
func producerFileSelected(plan snapshotimage.Plan, planned pipeline.PlannedArtifact, file snapshotimage.DependencyFile) bool {
	switch planned.ProjectType {
	case projecttype.Go, projecttype.Cargo:
		selected := false

		for _, platform := range plan.ArtifactPlatforms(planned.Name) {
			if strings.HasPrefix(file.Path, strings.ReplaceAll(platform, "/", "-")+"/") {
				selected = true
			}
		}

		return selected
	case projecttype.Maven, projecttype.Gradle:
		return filepath.Dir(file.Path) == "." && strings.HasSuffix(file.Path, ".jar")
	default:
		return planned.ProjectType == projecttype.NPM
	}
}

// requireProducerFiles checks that every planned destination received usable
// inputs and, for native artifacts, the planned executable.
func requireProducerFiles(plan snapshotimage.Plan, planned pipeline.PlannedArtifact, files map[string]snapshotimage.DependencyFile) error {
	for _, build := range plan.Builds {
		for _, dep := range build.RequiredArtifacts {
			if dep.Name != planned.Name {
				continue
			}

			list := []snapshotimage.DependencyFile{}

			for path, file := range files {
				if strings.HasPrefix(path, dep.Destination+"/") {
					list = append(list, file)
				}
			}

			if err := requireDependencyFiles(dep, build.Arch, list); err != nil {
				return err
			}

			if err := requireNativeExecutable(planned, dep, build.Arch, list); err != nil {
				return err
			}
		}
	}

	return nil
}

func requireNativeExecutable(planned pipeline.PlannedArtifact, dep snapshotimage.Dependency, arch string, list []snapshotimage.DependencyFile) error {
	if planned.ProjectType != projecttype.Go && planned.ProjectType != projecttype.Cargo {
		return nil
	}

	wanted := dep.Destination + "/" + plannedNativeBinary(planned, "linux/"+arch)
	if !slices.ContainsFunc(list, func(file snapshotimage.DependencyFile) bool {
		return file.Path == wanted && file.Size > 0 && file.Mode == "0755"
	}) {
		return fmt.Errorf("snapshot producer %s %w %s", planned.Name, errMissingPlannedExecutable, wanted)
	}

	return nil
}

// PrepareInputs imports already-built, exact CI artifact downloads. It performs
// no ecosystem builds and is the GitHub reuse path for the existing build stage.
func PrepareInputs(ctx context.Context, ops appversion.GenerateSnapshotOps, plan snapshotimage.Plan, input, output string) error {
	if err := CheckSource(ctx, ops, plan.SourceSHA); err != nil {
		return err
	}

	if err := plan.Validate(); err != nil {
		return err
	}

	output = cmp.Or(output, snapshotimage.DefaultProducerDir)
	if err := newOutput(output, plan); err != nil {
		return err
	}

	if err := producerDirectories(input, plan); err != nil {
		return err
	}

	if len(plan.Artifacts) == 0 {
		return nil
	}

	stage, err := os.MkdirTemp("", "forge-ci-snapshot-import-")
	if err != nil {
		return err
	}

	defer func() { _ = os.RemoveAll(stage) }()

	for _, planned := range plan.Artifacts {
		if err := prepareProducer(plan, planned, downloadSource(planned, filepath.Join(input, planned.Name)), filepath.Join(stage, planned.Name)); err != nil {
			return err
		}
	}

	return exportTree(stage, output, plan)
}

func producerDirectories(dir string, plan snapshotimage.Plan) error {
	if len(plan.Artifacts) == 0 {
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

	if len(entries) != len(plan.Artifacts) {
		return errProducerSetDiffers
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			return errProducerNotDirectory
		}

		if !slices.ContainsFunc(plan.Artifacts, func(candidate pipeline.PlannedArtifact) bool { return candidate.Name == entry.Name() }) {
			return fmt.Errorf("%w %s", errUnplannedProducer, entry.Name())
		}
	}

	return nil
}

func readProducer(plan snapshotimage.Plan, planned pipeline.PlannedArtifact, dir string) (snapshotimage.ArtifactReceipt, error) {
	var manifest snapshotimage.ArtifactReceipt
	if err := ReadJSON(filepath.Join(dir, snapshotimage.ProducerManifest), &manifest); err != nil {
		return manifest, err
	}

	if err := requireProducerIdentity(plan, planned, manifest); err != nil {
		return manifest, err
	}

	actual, err := dependencyFilesAt(filepath.Join(dir, "files"), ".")
	if err != nil {
		return manifest, err
	}

	if err := validateTransportFiles(actual, manifest.Files); err != nil {
		return manifest, fmt.Errorf("snapshot producer %s: %w", planned.Name, err)
	}

	for _, file := range manifest.Files {
		if err := restorePath(file.Path, "snapshot-plan.json"); err != nil {
			return manifest, err
		}

		inPlan, err := plannedProducerFile(plan, planned, dir, file.Path)
		if err != nil {
			return manifest, err
		}

		if !inPlan {
			return manifest, fmt.Errorf("%w %s", errUnplannedProducerFile, file.Path)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return manifest, err
	}

	if len(entries) != 2 {
		return manifest, errProducerUnplannedEntries
	}

	return manifest, nil
}

// requireProducerIdentity binds a producer receipt to the frozen plan, source
// and platform set.
func requireProducerIdentity(plan snapshotimage.Plan, planned pipeline.PlannedArtifact, manifest snapshotimage.ArtifactReceipt) error {
	artifactJSON, err := json.Marshal(planned)
	if err != nil {
		return err
	}

	recordedJSON, err := json.Marshal(manifest.Artifact)
	if err != nil {
		return err
	}

	digest, err := planDigest(plan)
	if err != nil {
		return err
	}

	if manifest.Version != snapshotimage.Version || manifest.SourceSHA != plan.SourceSHA || manifest.SourceBranch != plan.SourceBranch || manifest.PlanSHA256 != digest || string(artifactJSON) != string(recordedJSON) || !slices.Equal(manifest.Platforms, plan.ArtifactPlatforms(planned.Name)) {
		return fmt.Errorf("snapshot producer %s %w", planned.Name, errProducerIdentityDiffers)
	}

	return nil
}

// plannedProducerFile reports whether path lies in one of the artifact's
// planned destinations, checking an npm package manifest against the source.
func plannedProducerFile(plan snapshotimage.Plan, planned pipeline.PlannedArtifact, dir, path string) (bool, error) {
	inPlan := false

	for _, build := range plan.Builds {
		for _, dep := range build.RequiredArtifacts {
			if dep.Name != planned.Name || !strings.HasPrefix(path, dep.Destination+"/") {
				continue
			}

			inPlan = true

			if planned.ProjectType != projecttype.NPM || path != dep.Destination+"/package.json" {
				continue
			}

			body, err := readJSONFile(filepath.Join(dir, "files", path))
			if err != nil {
				return inPlan, err
			}

			if err := compareNPMPackage(planned, body); err != nil {
				return inPlan, err
			}
		}
	}

	return inPlan, nil
}

// GitHub raw artifact ZIPs already lost execute bits before a producer receipt
// exists. Only the exact Linux binary filenames selected by the existing Go /
// Cargo build CLI receive executable intent; adjacent data files do not.
func producerFileMode(plan snapshotimage.Plan, planned pipeline.PlannedArtifact, file snapshotimage.DependencyFile) string {
	if planned.ProjectType != projecttype.Go && planned.ProjectType != projecttype.Cargo {
		return file.Mode
	}

	for _, platform := range plan.ArtifactPlatforms(planned.Name) {
		if file.Path == plannedNativeBinary(planned, platform) {
			return "0755"
		}
	}

	return file.Mode
}

func plannedNativeBinary(planned pipeline.PlannedArtifact, platform string) string {
	name := planned.Name
	if planned.Go != nil && planned.Go.BinaryName != "" {
		name = planned.Go.BinaryName
	}

	if planned.Cargo != nil && planned.Cargo.BinaryName != "" {
		name = planned.Cargo.BinaryName
	}

	suffix := strings.ReplaceAll(platform, "/", "-")

	return suffix + "/" + name + "-" + suffix
}
