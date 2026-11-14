// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

var (
	errInventoryInsideInputs         = errors.New("dependency inventory output must be outside dependency inputs")
	errInventorySourceDiffers        = errors.New("snapshot dependency inventory source identity differs from plan")
	errInventoryDiffers              = errors.New("snapshot dependency inventory artifact, platform, file set or digest differs from inputs")
	errUnsupportedDependencyType     = errors.New("unsupported snapshot dependency type")
	errNoNonemptyInputs              = errors.New("has no nonempty")
	errDependencyNotRegular          = errors.New("dependency input must be a regular file")
	errDependencyNotRegularNoSymlink = errors.New("dependency input must be a regular non-symlink file")
	errDependencyChangedOpening      = errors.New("dependency input changed while opening")
	errDependencyChangedHashing      = errors.New("dependency input changed while hashing")
	errDependencyDirectoryEmpty      = errors.New("dependency input directory is empty")
	errTransportedSetDiffers         = errors.New("snapshot transported file set differs")
	errTransportedFileDiffers        = errors.New("snapshot transported file identity, size or digest differs")
)

// RecordContext runs at the trusted context producer, after source-bound CI
// downloads. Builds only verify this frozen handoff; they never regenerate it.
func RecordContext(ctx context.Context, ops appversion.GenerateSnapshotOps, plan snapshotimage.Plan, destination string) error {
	if err := plan.Validate(); err != nil {
		return err
	}

	if err := CheckSource(ctx, ops, plan.SourceSHA); err != nil {
		return err
	}

	for _, b := range plan.Builds {
		for _, d := range b.RequiredArtifacts {
			if inside(d.Destination, destination) {
				return errInventoryInsideInputs
			}
		}
	}

	manifest, err := dependencyInventory(plan)
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

// validateDependencies checks the recorded inventory at path against the
// checkout's dependency inputs and returns it once every file agrees.
func validateDependencies(plan snapshotimage.Plan, path string) (snapshotimage.DependencyInventory, error) {
	var recorded snapshotimage.DependencyInventory
	if err := ReadJSON(path, &recorded); err != nil {
		return recorded, fmt.Errorf("read snapshot dependencies: %w", err)
	}

	if recorded.Version != snapshotimage.Version || recorded.SourceSHA != plan.SourceSHA || recorded.SourceBranch != plan.SourceBranch {
		return recorded, errInventorySourceDiffers
	}

	actual, err := dependencyInventory(plan)
	if err != nil {
		return recorded, err
	}

	if !reflect.DeepEqual(recorded, actual) {
		return recorded, errInventoryDiffers
	}

	return recorded, nil
}

func dependencyInventory(plan snapshotimage.Plan) (snapshotimage.DependencyInventory, error) {
	return dependencyInventoryAt(plan, ".")
}

func dependencyInventoryAt(plan snapshotimage.Plan, base string) (snapshotimage.DependencyInventory, error) {
	manifest := snapshotimage.DependencyInventory{Version: snapshotimage.Version, SourceSHA: plan.SourceSHA, SourceBranch: plan.SourceBranch, Dependencies: []snapshotimage.DependencyRecord{}}
	for _, b := range plan.Builds {
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
func requireDependencyFiles(dep snapshotimage.Dependency, arch string, files []snapshotimage.DependencyFile) error {
	directory := filepath.ToSlash(dependencyDirectory(dep, arch))

	for _, file := range files {
		if file.Size == 0 {
			continue
		}

		switch dep.ProjectType {
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
			return fmt.Errorf("%w %s", errUnsupportedDependencyType, dep.ProjectType)
		}
	}

	return fmt.Errorf("required artifact %s (%s) %w %s inputs for linux/%s", dep.Name, dep.ArtifactName, errNoNonemptyInputs, dep.ProjectType, arch)
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
			return fmt.Errorf("%w: %s", errDependencyNotRegular, path)
		}

		file, err := hashDependencyFile(actualDirectory, directory, path)
		if err != nil {
			return err
		}

		files = append(files, file)

		return nil
	})
	if err == nil && len(files) == 0 {
		err = fmt.Errorf("%w: %s", errDependencyDirectoryEmpty, directory)
	}

	return files, err
}

// hashDependencyFile opens a checked parent and hashes one regular file,
// refusing symlinks, special files and files that change while being read.
func hashDependencyFile(actualDirectory, directory, path string) (snapshotimage.DependencyFile, error) {
	// Open a checked parent and refuse final symlinks/special files without
	// blocking on a substituted FIFO. Stream binary inputs without a size cap.
	parent, err := pathsafe.OpenRoot(filepath.Join(actualDirectory, filepath.Dir(path)))
	if err != nil {
		return snapshotimage.DependencyFile{}, err
	}
	defer func() { _ = parent.Close() }()

	initial, err := parent.Lstat(filepath.Base(path))
	if err != nil {
		return snapshotimage.DependencyFile{}, err
	}

	if !initial.Mode().IsRegular() {
		return snapshotimage.DependencyFile{}, fmt.Errorf("%w: %s", errDependencyNotRegularNoSymlink, path)
	}

	file, err := parent.OpenFile(filepath.Base(path), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return snapshotimage.DependencyFile{}, err
	}

	defer func() { _ = file.Close() }()

	before, err := file.Stat()
	if err != nil {
		return snapshotimage.DependencyFile{}, err
	}

	if !before.Mode().IsRegular() || before.Mode()&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0 {
		return snapshotimage.DependencyFile{}, fmt.Errorf("%w: %s", errDependencyNotRegular, path)
	}

	current, err := parent.Lstat(filepath.Base(path))
	if err != nil {
		return snapshotimage.DependencyFile{}, err
	}

	if !current.Mode().IsRegular() || !os.SameFile(initial, before) || !os.SameFile(current, before) {
		return snapshotimage.DependencyFile{}, fmt.Errorf("%w: %s", errDependencyChangedOpening, path)
	}

	hash := sha256.New()

	size, err := io.Copy(hash, file)
	if err != nil {
		return snapshotimage.DependencyFile{}, err
	}

	after, err := file.Stat()
	if err != nil {
		return snapshotimage.DependencyFile{}, err
	}

	if !unchangedWhileHashing(size, before, after) {
		return snapshotimage.DependencyFile{}, fmt.Errorf("%w: %s", errDependencyChangedHashing, path)
	}

	return snapshotimage.DependencyFile{Path: filepath.ToSlash(filepath.Join(directory, path)), SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size, Mode: artifact.CanonicalFileMode(before.Mode())}, nil
}

// unchangedWhileHashing reports whether the hashed byte count and the file's
// size, modification time and mode stayed stable across the read.
func unchangedWhileHashing(size int64, before, after fs.FileInfo) bool {
	return size == before.Size() && size == after.Size() && before.ModTime().Equal(after.ModTime()) && before.Mode() == after.Mode()
}

// Transport may erase Unix permissions. Check every recorded mode and all
// content bindings, but restore permissions from the producer's frozen policy,
// never from a downloaded file's mode. Build validation still compares modes.
func validateTransportFiles(actual, recorded []snapshotimage.DependencyFile) error {
	if len(actual) != len(recorded) {
		return errTransportedSetDiffers
	}

	for i, expected := range recorded {
		if _, err := expected.Permissions(); err != nil {
			return err
		}

		file := actual[i]

		file.Mode = expected.Mode
		if file != expected {
			return fmt.Errorf("%w: %s", errTransportedFileDiffers, expected.Path)
		}
	}

	return nil
}
