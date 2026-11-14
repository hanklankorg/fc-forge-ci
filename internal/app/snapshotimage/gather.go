// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage

import (
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	appversion "github.com/diggsweden/forge-ci/internal/app/version"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

var (
	errGatherOverlapsInputs = errors.New("snapshot gathered output must not overlap producer inputs")
	errPlanChangedGathering = errors.New("snapshot frozen plan changed before gathering")
	errProducersConflict    = errors.New("snapshot producers conflict at")
)

// GatherContext is the all-producer barrier. Provider transport establishes run
// origin; this boundary verifies exact producer identities and bytes, creates
// the existing frozen bundle, and uses RestoreContext for confined installation.
func GatherContext(ctx context.Context, ops appversion.GenerateSnapshotOps, plan snapshotimage.Plan, planFile, input, output string) error {
	if err := CheckSource(ctx, ops, plan.SourceSHA); err != nil {
		return err
	}

	if err := plan.Validate(); err != nil {
		return err
	}

	if err := newOutput(output, plan); err != nil {
		return err
	}

	input = cmp.Or(input, snapshotimage.DefaultProducerDir)
	if inside(input, output) || inside(output, input) {
		return errGatherOverlapsInputs
	}

	if err := producerDirectories(input, plan); err != nil {
		return err
	}

	planBytes, err := readJSONFile(planFile)
	if err != nil {
		return err
	}

	same, err := samePlan(plan, planBytes)
	if err != nil {
		return err
	}

	if !same {
		return errPlanChangedGathering
	}

	files, sources, err := gatherProducerFiles(plan, input)
	if err != nil {
		return err
	}

	tmp, err := os.MkdirTemp("", "forge-ci-snapshot-gather-")
	if err != nil {
		return err
	}

	defer func() { _ = os.RemoveAll(tmp) }()

	if err := writeGatheredBundle(plan, tmp, planBytes, files, sources); err != nil {
		return err
	}

	if err := RestoreContext(ctx, ops, plan, planFile, tmp); err != nil {
		return err
	}

	return exportTree(tmp, output, plan)
}

// gatherProducerFiles reads every planned producer and merges their files,
// refusing two producers that disagree about one path.
func gatherProducerFiles(plan snapshotimage.Plan, input string) (map[string]snapshotimage.DependencyFile, map[string]string, error) {
	files := map[string]snapshotimage.DependencyFile{}
	sources := map[string]string{}

	for _, planned := range plan.Artifacts {
		dir := filepath.Join(input, planned.Name)

		receipt, err := readProducer(plan, planned, dir)
		if err != nil {
			return nil, nil, err
		}

		for _, file := range receipt.Files {
			if previous, ok := files[file.Path]; ok && previous != file {
				return nil, nil, fmt.Errorf("%w %s", errProducersConflict, file.Path)
			}

			files[file.Path] = file
			sources[file.Path] = filepath.Join(dir, "files", file.Path)
		}
	}

	return files, sources, nil
}

// writeGatheredBundle stages the gathered files below tmp and writes the frozen
// bundle's inventory, plan, source and manifest beside them.
func writeGatheredBundle(plan snapshotimage.Plan, tmp string, planBytes []byte, files map[string]snapshotimage.DependencyFile, sources map[string]string) error {
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

	inventory, err := dependencyInventoryAt(plan, filepath.Join(tmp, "files"))
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

	if err := os.WriteFile(filepath.Join(tmp, "source-sha"), []byte(plan.SourceSHA+"\n"), 0o600); err != nil {
		return err
	}

	manifest := fmt.Sprintf("%x  snapshot-plan.json\n%x  files/snapshot-dependencies.json\n", sha256.Sum256(planBytes), sha256.Sum256(inventoryBytes))
	if err := os.WriteFile(filepath.Join(tmp, "manifest.sha256"), []byte(manifest), 0o600); err != nil {
		return err
	}

	return nil
}
