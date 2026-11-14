// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage

import (
	"bytes"
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
	"sort"
	"strings"
	"syscall"

	appversion "github.com/diggsweden/forge-ci/internal/app/version"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

var (
	errContextSourceDiffers      = errors.New("snapshot context source-sha differs from plan")
	errPlanChangedRestoring      = errors.New("snapshot plan changed while restoring context")
	errContextPlanDiffers        = errors.New("snapshot context differs from exact frozen plan")
	errContextManifestDiffers    = errors.New("snapshot context fixed plan/inventory hashes differ from manifest")
	errContextInventorySource    = errors.New("snapshot context inventory source differs from plan")
	errInventoryConflictsPlan    = errors.New("snapshot inventory destination conflicts with frozen plan")
	errConflictingContextFile    = errors.New("conflicting snapshot context file")
	errContextDependencySet      = errors.New("snapshot context dependency set differs")
	errContextInventoryDiffers   = errors.New("snapshot context inventory artifact, platform, file set or digest differs from bundle")
	errUnplannedCheckoutFile     = errors.New("unplanned checkout dependency file")
	errUnsafeContextPath         = errors.New("unsafe snapshot context path")
	errReservedContextPath       = errors.New("reserved snapshot context path")
	errContextReplacesPlan       = errors.New("snapshot context cannot replace frozen plan path")
	errUnplannedBundleDirectory  = errors.New("unplanned snapshot bundle directory")
	errUnplannedBundleFile       = errors.New("unplanned or nonregular snapshot bundle file")
	errRestoreDestinationType    = errors.New("is a symlink or incompatible file type")
	errContextSourceNotRegular   = errors.New("snapshot context source must be a regular file")
	errContextSourceChanged      = errors.New("snapshot context source changed while opening")
	errContextFileChangedInstall = errors.New("snapshot context file changed before install")
)

// RestoreContext validates the entire frozen handoff and every checkout target
// before staging writes. It never restores source files, Git metadata or plans.
// The existing confined artifact installer provides atomic file replacement and
// rollback, rather than copying through existing checkout symlinks.
func RestoreContext(ctx context.Context, ops appversion.GenerateSnapshotOps, plan snapshotimage.Plan, planFile, bundle string) error {
	if err := CheckSource(ctx, ops, plan.SourceSHA); err != nil {
		return err
	}

	if err := plan.Validate(); err != nil {
		return err
	}

	recorded, inventoryBytes, err := readContextBundle(plan, planFile, bundle)
	if err != nil {
		return err
	}

	files, err := contextFiles(plan, recorded, planFile)
	if err != nil {
		return err
	}

	if err := verifyContextInventory(plan, bundle, recorded); err != nil {
		return err
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

	if err := rejectUnplannedCheckoutFiles(plan, files); err != nil {
		return err
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

// readContextBundle binds the bundle's source, plan, manifest and inventory to
// the local frozen plan and returns the recorded inventory with its bytes.
func readContextBundle(plan snapshotimage.Plan, planFile, bundle string) (snapshotimage.DependencyInventory, []byte, error) {
	var recorded snapshotimage.DependencyInventory

	source, err := readJSONFile(filepath.Join(bundle, "source-sha"))
	if err != nil {
		return recorded, nil, err
	}

	if strings.TrimSuffix(string(source), "\n") != plan.SourceSHA {
		return recorded, nil, errContextSourceDiffers
	}

	localPlan, err := readJSONFile(planFile)
	if err != nil {
		return recorded, nil, err
	}

	same, err := samePlan(plan, localPlan)
	if err != nil {
		return recorded, nil, err
	}

	if !same {
		return recorded, nil, errPlanChangedRestoring
	}

	bundlePlan, err := readJSONFile(filepath.Join(bundle, "snapshot-plan.json"))
	if err != nil {
		return recorded, nil, err
	}

	if !bytes.Equal(localPlan, bundlePlan) {
		return recorded, nil, errContextPlanDiffers
	}

	inventoryPath := filepath.Join(bundle, "files", snapshotimage.DefaultDependenciesFile)

	inventoryBytes, err := readJSONFile(inventoryPath)
	if err != nil {
		return recorded, nil, err
	}

	if err := requireBundleManifest(bundle, bundlePlan, inventoryBytes); err != nil {
		return recorded, nil, err
	}

	if err := decodeJSON(inventoryBytes, &recorded); err != nil {
		return recorded, nil, err
	}

	if recorded.Version != snapshotimage.Version || recorded.SourceSHA != plan.SourceSHA || recorded.SourceBranch != plan.SourceBranch {
		return recorded, nil, errContextInventorySource
	}

	return recorded, inventoryBytes, nil
}

// requireBundleManifest checks the bundle's fixed plan and inventory hashes.
func requireBundleManifest(bundle string, bundlePlan, inventoryBytes []byte) error {
	manifest, err := readJSONFile(filepath.Join(bundle, "manifest.sha256"))
	if err != nil {
		return err
	}

	expectedManifest := fmt.Sprintf("%x  snapshot-plan.json\n%x  files/snapshot-dependencies.json", sha256.Sum256(bundlePlan), sha256.Sum256(inventoryBytes))
	if strings.TrimSuffix(string(manifest), "\n") != expectedManifest {
		return errContextManifestDiffers
	}

	return nil
}

// samePlan reports whether the plan file body still decodes to plan.
func samePlan(plan snapshotimage.Plan, body []byte) (bool, error) {
	var frozen snapshotimage.Plan
	if err := decodeJSON(body, &frozen); err != nil {
		return false, err
	}

	planSum, err := planDigest(plan)
	if err != nil {
		return false, err
	}

	frozenSum, err := planDigest(frozen)
	if err != nil {
		return false, err
	}

	return planSum == frozenSum, nil
}

// contextFiles checks the inventory destination, every planned destination and
// every recorded file path, and returns the recorded files keyed by path.
func contextFiles(plan snapshotimage.Plan, recorded snapshotimage.DependencyInventory, planFile string) (map[string]snapshotimage.DependencyFile, error) {
	if inside(snapshotimage.DefaultDependenciesFile, planFile) || inside(planFile, snapshotimage.DefaultDependenciesFile) {
		return nil, errInventoryConflictsPlan
	}

	files := map[string]snapshotimage.DependencyFile{}

	for _, build := range plan.Builds {
		for _, dep := range build.RequiredArtifacts {
			if err := restorePath(dep.Destination, planFile); err != nil {
				return nil, err
			}
		}
	}

	for _, dep := range recorded.Dependencies {
		for _, file := range dep.Files {
			if _, err := file.Permissions(); err != nil {
				return nil, err
			}

			if err := restorePath(file.Path, planFile); err != nil {
				return nil, err
			}

			if previous, ok := files[file.Path]; ok && previous != file {
				return nil, fmt.Errorf("%w %s", errConflictingContextFile, file.Path)
			}

			files[file.Path] = file
		}
	}

	return files, nil
}

// verifyContextInventory recomputes the bundle's inventory and compares it with
// the recorded one, tolerating only transport-erased permissions.
func verifyContextInventory(plan snapshotimage.Plan, bundle string, recorded snapshotimage.DependencyInventory) error {
	actual, err := dependencyInventoryAt(plan, filepath.Join(bundle, "files"))
	if err != nil {
		return err
	}

	if len(actual.Dependencies) != len(recorded.Dependencies) {
		return errContextDependencySet
	}

	for i := range actual.Dependencies {
		if err := validateTransportFiles(actual.Dependencies[i].Files, recorded.Dependencies[i].Files); err != nil {
			return err
		}

		actual.Dependencies[i].Files = recorded.Dependencies[i].Files
	}

	if !reflect.DeepEqual(recorded, actual) {
		return errContextInventoryDiffers
	}

	return nil
}

// rejectUnplannedCheckoutFiles refuses existing unrecorded artifacts: they would
// make the restored inventory unusable, and rejecting them avoids deleting
// checkout files or failing after writes.
func rejectUnplannedCheckoutFiles(plan snapshotimage.Plan, files map[string]snapshotimage.DependencyFile) error {
	for _, build := range plan.Builds {
		for _, dep := range build.RequiredArtifacts {
			if err := rejectUnplannedDestinationFiles(dep.Destination, files); err != nil {
				return err
			}
		}
	}

	return nil
}

func rejectUnplannedDestinationFiles(destination string, files map[string]snapshotimage.DependencyFile) error {
	root, err := pathsafe.OpenRoot(destination)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			return nil
		}

		if _, ok := files[filepath.ToSlash(filepath.Join(destination, path))]; !ok {
			return fmt.Errorf("%w %s", errUnplannedCheckoutFile, path)
		}

		return nil
	})
	_ = root.Close()

	return err
}

func restorePath(path, planFile string) error {
	if !pathsafe.Relative(path) || filepath.ToSlash(filepath.Clean(path)) != path || path == "." || strings.Contains(path, "\\") {
		return fmt.Errorf("%w %q", errUnsafeContextPath, path)
	}

	for part := range strings.SplitSeq(path, "/") {
		switch strings.ToLower(part) {
		case ".git", "snapshot-plan.json", "source-sha", "manifest.sha256", snapshotimage.DefaultDependenciesFile:
			return fmt.Errorf("%w %q", errReservedContextPath, path)
		}
	}

	if inside(path, planFile) || inside(planFile, path) {
		return errContextReplacesPlan
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
				return fmt.Errorf("%w %s", errUnplannedBundleDirectory, path)
			}

			return nil
		}

		if !e.Type().IsRegular() || !allowed[path] {
			return fmt.Errorf("%w %s", errUnplannedBundleFile, path)
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
			return fmt.Errorf("snapshot restore destination %s %w", rel, errRestoreDestinationType)
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
		return errContextSourceNotRegular
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
		return errContextSourceChanged
	}

	return writeVerifiedCopy(stage, file, expected, mode)
}

// writeVerifiedCopy copies source into a new staged file, verifying its size
// and digest before applying the frozen permissions.
func writeVerifiedCopy(stage *os.Root, source io.Reader, expected snapshotimage.DependencyFile, mode fs.FileMode) error {
	if err := stage.MkdirAll(filepath.Dir(expected.Path), 0o755); err != nil {
		return err
	}

	out, err := stage.OpenFile(expected.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}

	defer func() { _ = out.Close() }()

	hash := sha256.New()

	size, copyErr := io.Copy(io.MultiWriter(out, hash), source)
	if copyErr != nil {
		return copyErr
	}

	if size != expected.Size || hex.EncodeToString(hash.Sum(nil)) != expected.SHA256 {
		return fmt.Errorf("%w: %s", errContextFileChangedInstall, expected.Path)
	}

	if err := out.Chmod(mode); err != nil {
		return err
	}

	return out.Close()
}
