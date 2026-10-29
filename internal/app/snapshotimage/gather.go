// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage

import (
	"cmp"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	appversion "github.com/diggsweden/forge-ci/internal/app/version"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// GatherContext is the all-producer barrier. Provider transport establishes run
// origin; this boundary verifies exact producer identities and bytes, creates
// the existing frozen bundle, and uses RestoreContext for confined installation.
func GatherContext(ctx context.Context, ops appversion.GenerateSnapshotOps, p snapshotimage.Plan, planFile, input, output string) error {
	if err := CheckSource(ctx, ops, p.SourceSHA); err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if err := newOutput(output, p); err != nil {
		return err
	}
	input = cmp.Or(input, snapshotimage.DefaultProducerDir)
	if inside(input, output) || inside(output, input) {
		return fmt.Errorf("snapshot gathered output must not overlap producer inputs")
	}
	if err := producerDirectories(input, p); err != nil {
		return err
	}
	planBytes, err := readJSONFile(planFile)
	if err != nil {
		return err
	}
	var frozen snapshotimage.Plan
	if err := decodeJSON(planBytes, &frozen); err != nil {
		return err
	}
	if planDigest(frozen) != planDigest(p) {
		return fmt.Errorf("snapshot frozen plan changed before gathering")
	}
	files := map[string]snapshotimage.DependencyFile{}
	sources := map[string]string{}
	for _, a := range p.Artifacts {
		dir := filepath.Join(input, a.Name)
		receipt, err := readProducer(p, a, dir)
		if err != nil {
			return err
		}
		for _, file := range receipt.Files {
			if previous, ok := files[file.Path]; ok && previous != file {
				return fmt.Errorf("snapshot producers conflict at %s", file.Path)
			}
			files[file.Path] = file
			sources[file.Path] = filepath.Join(dir, "files", file.Path)
		}
	}
	tmp, err := os.MkdirTemp("", "forge-ci-snapshot-gather-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	root, err := pathsafe.OpenRoot(tmp)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := root.Mkdir("files", 0o700); err != nil {
		return err
	}
	for path, file := range files {
		file.Path = filepath.Join("files", path)
		if err := stageContextFile(sources[path], root, file); err != nil {
			return err
		}
	}
	inventory, err := dependencyInventoryAt(p, filepath.Join(tmp, "files"))
	if err != nil {
		return err
	}
	if err := WriteJSON(filepath.Join(tmp, "files", snapshotimage.DefaultDependenciesFile), inventory); err != nil {
		return err
	}
	inventoryBytes, err := readJSONFile(filepath.Join(tmp, "files", snapshotimage.DefaultDependenciesFile))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, "snapshot-plan.json"), planBytes, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, "source-sha"), []byte(p.SourceSHA+"\n"), 0o600); err != nil {
		return err
	}
	manifest := fmt.Sprintf("%x  snapshot-plan.json\n%x  files/snapshot-dependencies.json\n", sha256.Sum256(planBytes), sha256.Sum256(inventoryBytes))
	if err := os.WriteFile(filepath.Join(tmp, "manifest.sha256"), []byte(manifest), 0o600); err != nil {
		return err
	}
	if err := RestoreContext(ctx, ops, p, planFile, tmp); err != nil {
		return err
	}
	return exportTree(tmp, output, p)
}
