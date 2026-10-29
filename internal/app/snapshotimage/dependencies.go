// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

	appversion "github.com/diggsweden/forge-ci/internal/app/version"
	"github.com/diggsweden/forge-ci/internal/domain/artifact"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// RecordContext runs at the trusted context producer, after source-bound CI
// downloads. Builds only verify this frozen handoff; they never regenerate it.
func RecordContext(ctx context.Context, ops appversion.GenerateSnapshotOps, p snapshotimage.Plan, destination string) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if err := CheckSource(ctx, ops, p.SourceSHA); err != nil {
		return err
	}
	for _, b := range p.Builds {
		for _, d := range b.RequiredArtifacts {
			if inside(d.Destination, destination) {
				return fmt.Errorf("dependency inventory output must be outside dependency inputs")
			}
		}
	}
	manifest, err := dependencyInventory(p)
	if err != nil {
		return err
	}
	return WriteJSON(destination, manifest)
}

func inside(directory, path string) bool {
	dir, err := filepath.Abs(directory)
	if err != nil {
		return true
	}
	file, err := filepath.Abs(path)
	if err != nil {
		return true
	}
	rel, err := filepath.Rel(dir, file)
	return err != nil || filepath.IsLocal(rel)
}

func validateDependencies(p snapshotimage.Plan, path string) error {
	if path == "" {
		path = snapshotimage.DefaultDependenciesFile
	}
	var recorded snapshotimage.DependencyInventory
	if err := ReadJSON(path, &recorded); err != nil {
		return fmt.Errorf("read snapshot dependencies: %w", err)
	}
	if recorded.Version != snapshotimage.Version || recorded.SourceSHA != p.SourceSHA || recorded.SourceBranch != p.SourceBranch {
		return fmt.Errorf("snapshot dependency inventory source identity differs from plan")
	}
	actual, err := dependencyInventory(p)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(recorded, actual) {
		return fmt.Errorf("snapshot dependency inventory artifact, platform, file set or digest differs from inputs")
	}
	return nil
}

func dependencyInventory(p snapshotimage.Plan) (snapshotimage.DependencyInventory, error) {
	return dependencyInventoryAt(p, ".")
}

func dependencyInventoryAt(p snapshotimage.Plan, base string) (snapshotimage.DependencyInventory, error) {
	manifest := snapshotimage.DependencyInventory{Version: snapshotimage.Version, SourceSHA: p.SourceSHA, SourceBranch: p.SourceBranch, Dependencies: []snapshotimage.DependencyRecord{}}
	for _, b := range p.Builds {
		for _, d := range b.RequiredArtifacts {
			files, err := dependencyFilesAt(base, d.Destination)
			if err != nil {
				return manifest, err
			}
			if err := requireDependencyFiles(d, b.Arch, files); err != nil {
				return manifest, err
			}
			manifest.Dependencies = append(manifest.Dependencies, snapshotimage.DependencyRecord{ImageName: b.Name, Platform: "linux/" + b.Arch, Dependency: d, Files: files})
		}
	}
	return manifest, nil
}

// Check the expected native/JVM inputs from the already opened and hashed file
// set. This intentionally performs no advisory reread of a Containerfile.
func requireDependencyFiles(d snapshotimage.Dependency, arch string, files []snapshotimage.DependencyFile) error {
	directory := filepath.ToSlash(dependencyDirectory(d, arch))
	for _, file := range files {
		if file.Size == 0 {
			continue
		}
		switch d.ProjectType {
		case projecttype.Go, projecttype.Cargo:
			if strings.HasPrefix(file.Path, directory+"/") {
				return nil
			}
		case projecttype.Maven, projecttype.Gradle:
			if filepath.ToSlash(filepath.Dir(file.Path)) == directory && strings.HasSuffix(file.Path, ".jar") {
				return nil
			}
		case projecttype.NPM:
			if file.Path == directory+"/package.json" {
				return nil
			}
		default:
			return fmt.Errorf("unsupported snapshot dependency type %s", d.ProjectType)
		}
	}
	return fmt.Errorf("required artifact %s (%s) has no nonempty %s inputs for linux/%s", d.Name, d.ArtifactName, d.ProjectType, arch)
}

func dependencyDirectory(d snapshotimage.Dependency, arch string) string {
	if d.ProjectType == projecttype.Go || d.ProjectType == projecttype.Cargo {
		return filepath.Join(d.Destination, "linux-"+arch)
	}
	return d.Destination
}

func dependencyFilesAt(base, directory string) ([]snapshotimage.DependencyFile, error) {
	actualDirectory := filepath.Join(base, directory)
	root, err := pathsafe.OpenRoot(actualDirectory)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	files := []snapshotimage.DependencyFile{}
	err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("dependency input must be a regular file: %s", path)
		}
		// Open a checked parent and refuse final symlinks/special files without
		// blocking on a substituted FIFO. Stream binary inputs without a size cap.
		parent, err := pathsafe.OpenRoot(filepath.Join(actualDirectory, filepath.Dir(path)))
		if err != nil {
			return err
		}
		defer func() { _ = parent.Close() }()
		initial, err := parent.Lstat(filepath.Base(path))
		if err != nil {
			return err
		}
		if !initial.Mode().IsRegular() {
			return fmt.Errorf("dependency input must be a regular non-symlink file: %s", path)
		}
		file, err := parent.OpenFile(filepath.Base(path), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		before, err := file.Stat()
		if err != nil {
			return err
		}
		if !before.Mode().IsRegular() || before.Mode()&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0 {
			return fmt.Errorf("dependency input must be a regular file: %s", path)
		}
		current, err := parent.Lstat(filepath.Base(path))
		if err != nil {
			return err
		}
		if !current.Mode().IsRegular() || !os.SameFile(initial, before) || !os.SameFile(current, before) {
			return fmt.Errorf("dependency input changed while opening: %s", path)
		}
		hash := sha256.New()
		size, err := io.Copy(hash, file)
		if err != nil {
			return err
		}
		after, err := file.Stat()
		if err != nil {
			return err
		}
		if size != before.Size() || size != after.Size() || !before.ModTime().Equal(after.ModTime()) || before.Mode() != after.Mode() {
			return fmt.Errorf("dependency input changed while hashing: %s", path)
		}
		files = append(files, snapshotimage.DependencyFile{Path: filepath.ToSlash(filepath.Join(directory, path)), SHA256: fmt.Sprintf("%x", hash.Sum(nil)), Size: size, Mode: artifact.CanonicalFileMode(before.Mode())})
		return nil
	})
	if err == nil && len(files) == 0 {
		err = fmt.Errorf("dependency input directory is empty: %s", directory)
	}
	return files, err
}

// Transport may erase Unix permissions. Check every recorded mode and all
// content bindings, but restore permissions from the producer's frozen policy,
// never from a downloaded file's mode. Build validation still compares modes.
func validateTransportFiles(actual, recorded []snapshotimage.DependencyFile) error {
	if len(actual) != len(recorded) {
		return fmt.Errorf("snapshot transported file set differs")
	}
	for i, expected := range recorded {
		if _, err := expected.Permissions(); err != nil {
			return err
		}
		file := actual[i]
		file.Mode = expected.Mode
		if file != expected {
			return fmt.Errorf("snapshot transported file identity, size or digest differs: %s", expected.Path)
		}
	}
	return nil
}
