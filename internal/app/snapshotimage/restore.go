// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"

	appversion "github.com/diggsweden/forge-ci/internal/app/version"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// RestoreContext validates the entire frozen handoff and every checkout target
// before staging writes. It never restores source files, Git metadata or plans.
// The existing confined artifact installer provides atomic file replacement and
// rollback, rather than copying through existing checkout symlinks.
func RestoreContext(ctx context.Context, ops appversion.GenerateSnapshotOps, p snapshotimage.Plan, planFile, bundle string) error {
	if err := CheckSource(ctx, ops, p.SourceSHA); err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return err
	}
	source, err := readJSONFile(filepath.Join(bundle, "source-sha"))
	if err != nil {
		return err
	}
	if strings.TrimSuffix(string(source), "\n") != p.SourceSHA {
		return fmt.Errorf("snapshot context source-sha differs from plan")
	}
	localPlan, err := readJSONFile(planFile)
	if err != nil {
		return err
	}
	var frozen snapshotimage.Plan
	if err := decodeJSON(localPlan, &frozen); err != nil {
		return err
	}
	if planDigest(p) != planDigest(frozen) {
		return fmt.Errorf("snapshot plan changed while restoring context")
	}
	bundlePlan, err := readJSONFile(filepath.Join(bundle, "snapshot-plan.json"))
	if err != nil {
		return err
	}
	if !bytes.Equal(localPlan, bundlePlan) {
		return fmt.Errorf("snapshot context differs from exact frozen plan")
	}
	inventoryPath := filepath.Join(bundle, "files", snapshotimage.DefaultDependenciesFile)
	inventoryBytes, err := readJSONFile(inventoryPath)
	if err != nil {
		return err
	}
	manifest, err := readJSONFile(filepath.Join(bundle, "manifest.sha256"))
	if err != nil {
		return err
	}
	expectedManifest := fmt.Sprintf("%x  snapshot-plan.json\n%x  files/snapshot-dependencies.json", sha256.Sum256(bundlePlan), sha256.Sum256(inventoryBytes))
	if strings.TrimSuffix(string(manifest), "\n") != expectedManifest {
		return fmt.Errorf("snapshot context fixed plan/inventory hashes differ from manifest")
	}
	var recorded snapshotimage.DependencyInventory
	if err := decodeJSON(inventoryBytes, &recorded); err != nil {
		return err
	}
	if recorded.Version != snapshotimage.Version || recorded.SourceSHA != p.SourceSHA || recorded.SourceBranch != p.SourceBranch {
		return fmt.Errorf("snapshot context inventory source differs from plan")
	}
	if inside(snapshotimage.DefaultDependenciesFile, planFile) || inside(planFile, snapshotimage.DefaultDependenciesFile) {
		return fmt.Errorf("snapshot inventory destination conflicts with frozen plan")
	}
	files := map[string]snapshotimage.DependencyFile{}
	for _, b := range p.Builds {
		for _, d := range b.RequiredArtifacts {
			if err := restorePath(d.Destination, planFile); err != nil {
				return err
			}
		}
	}
	for _, dep := range recorded.Dependencies {
		for _, file := range dep.Files {
			if _, err := file.Permissions(); err != nil {
				return err
			}
			if err := restorePath(file.Path, planFile); err != nil {
				return err
			}
			if previous, ok := files[file.Path]; ok && previous != file {
				return fmt.Errorf("conflicting snapshot context file %s", file.Path)
			}
			files[file.Path] = file
		}
	}
	actual, err := dependencyInventoryAt(p, filepath.Join(bundle, "files"))
	if err != nil {
		return err
	}
	if len(actual.Dependencies) != len(recorded.Dependencies) {
		return fmt.Errorf("snapshot context dependency set differs")
	}
	for i := range actual.Dependencies {
		if err := validateTransportFiles(actual.Dependencies[i].Files, recorded.Dependencies[i].Files); err != nil {
			return err
		}
		actual.Dependencies[i].Files = recorded.Dependencies[i].Files
	}
	if !reflect.DeepEqual(recorded, actual) {
		return fmt.Errorf("snapshot context inventory artifact, platform, file set or digest differs from bundle")
	}
	files[snapshotimage.DefaultDependenciesFile] = snapshotimage.DependencyFile{Path: snapshotimage.DefaultDependenciesFile, SHA256: fmt.Sprintf("%x", sha256.Sum256(inventoryBytes)), Size: int64(len(inventoryBytes)), Mode: "0644"}
	if err := checkBundleTree(bundle, files); err != nil {
		return err
	}
	checkout, err := pathsafe.OpenRoot(".")
	if err != nil {
		return err
	}
	defer func() { _ = checkout.Close() }()
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err := preflightRestoreTarget(checkout, path); err != nil {
			return err
		}
	}
	// Existing unrecorded artifacts would make the restored inventory unusable;
	// reject them rather than deleting checkout files or failing after writes.
	for _, b := range p.Builds {
		for _, d := range b.RequiredArtifacts {
			root, err := pathsafe.OpenRoot(d.Destination)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			err = fs.WalkDir(root.FS(), ".", func(path string, e fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if e.IsDir() {
					return nil
				}
				if _, ok := files[filepath.ToSlash(filepath.Join(d.Destination, path))]; !ok {
					return fmt.Errorf("unplanned checkout dependency file %s", path)
				}
				return nil
			})
			_ = root.Close()
			if err != nil {
				return err
			}
		}
	}
	stage, err := pathsafe.NewArtifactStaging(".")
	if err != nil {
		return err
	}
	defer func() { _ = stage.Close() }()
	for _, path := range paths {
		if err := stageContextFile(filepath.Join(bundle, "files", path), stage.Root(), files[path]); err != nil {
			return err
		}
	}
	return stage.Install()
}

func restorePath(path, planFile string) error {
	if !pathsafe.Relative(path) || filepath.ToSlash(filepath.Clean(path)) != path || path == "." || strings.Contains(path, "\\") {
		return fmt.Errorf("unsafe snapshot context path %q", path)
	}
	for _, part := range strings.Split(path, "/") {
		switch strings.ToLower(part) {
		case ".git", "snapshot-plan.json", "source-sha", "manifest.sha256", snapshotimage.DefaultDependenciesFile:
			return fmt.Errorf("reserved snapshot context path %q", path)
		}
	}
	if inside(path, planFile) || inside(planFile, path) {
		return fmt.Errorf("snapshot context cannot replace frozen plan path")
	}
	return nil
}

func checkBundleTree(bundle string, files map[string]snapshotimage.DependencyFile) error {
	allowed := map[string]bool{"source-sha": true, "snapshot-plan.json": true, "manifest.sha256": true}
	dirs := map[string]bool{".": true}
	for path := range files {
		allowed["files/"+path] = true
	}
	for path := range allowed {
		for dir := filepath.ToSlash(filepath.Dir(path)); dir != "."; dir = filepath.ToSlash(filepath.Dir(dir)) {
			dirs[dir] = true
		}
	}
	root, err := pathsafe.OpenRoot(bundle)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return fs.WalkDir(root.FS(), ".", func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			if !dirs[path] {
				return fmt.Errorf("unplanned snapshot bundle directory %s", path)
			}
			return nil
		}
		if !e.Type().IsRegular() || !allowed[path] {
			return fmt.Errorf("unplanned or nonregular snapshot bundle file %s", path)
		}
		return nil
	})
}

func preflightRestoreTarget(root *os.Root, path string) error {
	parts := strings.Split(path, "/")
	for i := range parts {
		rel := filepath.Join(parts[:i+1]...)
		info, err := root.Lstat(rel)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 || (i < len(parts)-1 && !info.IsDir()) || (i == len(parts)-1 && !info.Mode().IsRegular()) {
			return fmt.Errorf("snapshot restore destination %s is a symlink or incompatible file type", rel)
		}
	}
	return nil
}

func stageContextFile(source string, stage *os.Root, expected snapshotimage.DependencyFile) error {
	mode, err := expected.Permissions()
	if err != nil {
		return err
	}
	parent, err := pathsafe.OpenRoot(filepath.Dir(source))
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	initial, err := parent.Lstat(filepath.Base(source))
	if err != nil {
		return err
	}
	if !initial.Mode().IsRegular() {
		return fmt.Errorf("snapshot context source must be a regular file")
	}
	file, err := parent.OpenFile(filepath.Base(source), os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	current, err := parent.Lstat(filepath.Base(source))
	if err != nil {
		return err
	}
	if !current.Mode().IsRegular() || opened.Mode()&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0 || !os.SameFile(initial, opened) || !os.SameFile(current, opened) {
		return fmt.Errorf("snapshot context source changed while opening")
	}
	if err := stage.MkdirAll(filepath.Dir(expected.Path), 0o755); err != nil {
		return err
	}
	out, err := stage.OpenFile(expected.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(out, hash), file)
	if copyErr != nil {
		return copyErr
	}
	if size != expected.Size || fmt.Sprintf("%x", hash.Sum(nil)) != expected.SHA256 {
		return fmt.Errorf("snapshot context file changed before install: %s", expected.Path)
	}
	if err := out.Chmod(mode); err != nil {
		return err
	}
	return out.Close()
}
