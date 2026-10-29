// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	appsnapshot "github.com/diggsweden/forge-ci/internal/app/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/stretchr/testify/require"
)

func contextBundle(t *testing.T, p snapshotimage.Plan) string {
	t.Helper()
	require.NoError(t, appsnapshot.WriteJSON("snapshot-plan.json", p))
	bundle := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(bundle, "files"), 0o700))
	plan, err := os.ReadFile("snapshot-plan.json")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(bundle, "snapshot-plan.json"), plan, 0o600))
	inventory, err := os.ReadFile(snapshotimage.DefaultDependenciesFile)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(bundle, "files", snapshotimage.DefaultDependenciesFile), inventory, 0o600))
	var recorded snapshotimage.DependencyInventory
	require.NoError(t, appsnapshot.ReadJSON(snapshotimage.DefaultDependenciesFile, &recorded))
	for _, dep := range recorded.Dependencies {
		for _, file := range dep.Files {
			body, err := os.ReadFile(file.Path)
			require.NoError(t, err)
			target := filepath.Join(bundle, "files", file.Path)
			require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o700))
			require.NoError(t, os.WriteFile(target, body, 0o700))
		}
	}
	require.NoError(t, os.WriteFile(filepath.Join(bundle, "source-sha"), []byte(p.SourceSHA+"\n"), 0o600))
	updateBundleHashes(t, bundle)
	return bundle
}

func updateBundleHashes(t *testing.T, bundle string) {
	t.Helper()
	plan, err := os.ReadFile(filepath.Join(bundle, "snapshot-plan.json"))
	require.NoError(t, err)
	inventory, err := os.ReadFile(filepath.Join(bundle, "files", snapshotimage.DefaultDependenciesFile))
	require.NoError(t, err)
	manifest := fmt.Sprintf("%x  snapshot-plan.json\n%x  files/snapshot-dependencies.json\n", sha256.Sum256(plan), sha256.Sum256(inventory))
	require.NoError(t, os.WriteFile(filepath.Join(bundle, "manifest.sha256"), []byte(manifest), 0o600))
}

func restoreFixture(t *testing.T) (snapshotimage.Plan, string) {
	t.Helper()
	p, _ := dependencyFixture(t)
	require.NoError(t, os.WriteFile("dist/linux-amd64/a-first", []byte("incoming first artifact"), 0o700))
	require.NoError(t, appsnapshot.RecordContext(t.Context(), &git{head: p.SourceSHA}, p, snapshotimage.DefaultDependenciesFile))
	bundle := contextBundle(t, p)
	require.NoError(t, os.WriteFile("dist/linux-amd64/a-first", []byte("previous checkout artifact"), 0o700))
	return p, bundle
}

func TestRestoreContextConfinesWritesAndPreflightsWholeDestinationSet(t *testing.T) {
	for _, defect := range []string{"file symlink", "directory symlink", "inventory symlink", "fifo destination"} {
		t.Run(defect, func(t *testing.T) {
			p, bundle := restoreFixture(t)
			outside := t.TempDir()
			target := filepath.Join(outside, "keep")
			require.NoError(t, os.WriteFile(target, []byte("outside unchanged"), 0o600))
			first := "dist/linux-amd64/a-first"
			switch defect {
			case "file symlink":
				require.NoError(t, os.Remove("dist/linux-amd64/server-linux-amd64"))
				require.NoError(t, os.Symlink(target, "dist/linux-amd64/server-linux-amd64"))
			case "directory symlink":
				require.NoError(t, os.Rename("dist", "original-dist"))
				require.NoError(t, os.Symlink(outside, "dist"))
				first = "original-dist/linux-amd64/a-first"
			case "inventory symlink":
				require.NoError(t, os.Remove(snapshotimage.DefaultDependenciesFile))
				require.NoError(t, os.Symlink(target, snapshotimage.DefaultDependenciesFile))
			case "fifo destination":
				require.NoError(t, os.Remove("dist/linux-amd64/server-linux-amd64"))
				require.NoError(t, syscall.Mkfifo("dist/linux-amd64/server-linux-amd64", 0o600))
			}
			err := appsnapshot.RestoreContext(t.Context(), &git{head: p.SourceSHA}, p, "snapshot-plan.json", bundle)
			require.Error(t, err)
			body, err := os.ReadFile(target)
			require.NoError(t, err)
			require.Equal(t, "outside unchanged", string(body))
			body, err = os.ReadFile(first)
			require.NoError(t, err)
			require.Equal(t, "previous checkout artifact", string(body), "an earlier regular target must not be overwritten before a later symlink is refused")
		})
	}
}

func TestRestoreContextRejectsForeignTamperedAndUnplannedBundlesBeforeWrites(t *testing.T) {
	for _, defect := range []string{"foreign HEAD", "foreign source", "foreign plan", "tampered plan", "tampered inventory", "foreign inventory", "wrong artifact", "wrong platform", "substituted bytes", "missing bytes", "extra file", "reserved git file", "reserved plan file", "reserved inventory path", "traversal inventory path", "unknown inventory field", "manifest arbitrary filename", "unplanned checkout file", "source symlink"} {
		t.Run(defect, func(t *testing.T) {
			p, bundle := restoreFixture(t)
			ops := &git{head: p.SourceSHA}
			inventoryPath := filepath.Join(bundle, "files", snapshotimage.DefaultDependenciesFile)
			var inventory snapshotimage.DependencyInventory
			require.NoError(t, appsnapshot.ReadJSON(inventoryPath, &inventory))
			switch defect {
			case "foreign HEAD":
				ops.head = strings.Repeat("b", 40)
			case "foreign source":
				require.NoError(t, os.WriteFile(filepath.Join(bundle, "source-sha"), []byte(strings.Repeat("b", 40)), 0o600))
			case "foreign plan":
				foreign := p
				foreign.SourceSHA = strings.Repeat("b", 40)
				require.NoError(t, appsnapshot.WriteJSON(filepath.Join(bundle, "snapshot-plan.json"), foreign))
				updateBundleHashes(t, bundle)
			case "tampered plan":
				f, err := os.OpenFile(filepath.Join(bundle, "snapshot-plan.json"), os.O_WRONLY|os.O_APPEND, 0)
				require.NoError(t, err)
				_, err = f.WriteString("\n")
				require.NoError(t, err)
				require.NoError(t, f.Close())
			case "tampered inventory":
				require.NoError(t, os.WriteFile(inventoryPath, []byte("{}"), 0o600))
			case "foreign inventory":
				inventory.SourceSHA = strings.Repeat("b", 40)
			case "wrong artifact":
				inventory.Dependencies[0].ArtifactName = "foreign-artifact"
			case "wrong platform":
				inventory.Dependencies[0].Platform = "linux/arm64"
			case "reserved inventory path":
				inventory.Dependencies[0].Files[0].Path = ".git/config"
			case "traversal inventory path":
				inventory.Dependencies[0].Files[0].Path = "../outside"
			case "unknown inventory field":
				body, err := os.ReadFile(inventoryPath)
				require.NoError(t, err)
				body = append([]byte(`{"unexpected":true,`), body[1:]...)
				require.NoError(t, os.WriteFile(inventoryPath, body, 0o600))
				updateBundleHashes(t, bundle)
			case "substituted bytes":
				require.NoError(t, os.WriteFile(filepath.Join(bundle, "files/dist/linux-amd64/server-linux-amd64"), []byte(strings.Repeat("x", len("producer-built bytes"))), 0o700))
			case "missing bytes":
				require.NoError(t, os.Remove(filepath.Join(bundle, "files/dist/linux-amd64/server-linux-amd64")))
			case "extra file":
				require.NoError(t, os.WriteFile(filepath.Join(bundle, "files/extra"), []byte("unplanned"), 0o600))
			case "reserved git file":
				require.NoError(t, os.MkdirAll(filepath.Join(bundle, "files/.git"), 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(bundle, "files/.git/config"), []byte("unplanned"), 0o600))
			case "reserved plan file":
				require.NoError(t, os.WriteFile(filepath.Join(bundle, "files/snapshot-plan.json"), []byte("unplanned"), 0o600))
			case "manifest arbitrary filename":
				require.NoError(t, os.WriteFile(filepath.Join(bundle, "manifest.sha256"), []byte(strings.Repeat("a", 64)+"  ../../outside\n"), 0o600))
			case "unplanned checkout file":
				require.NoError(t, os.WriteFile("dist/extra", []byte("keep checkout"), 0o600))
			case "source symlink":
				path := filepath.Join(bundle, "files/dist/linux-amd64/server-linux-amd64")
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Symlink("a-first", path))
			}
			if defect == "foreign inventory" || defect == "wrong artifact" || defect == "wrong platform" || defect == "reserved inventory path" || defect == "traversal inventory path" {
				require.NoError(t, appsnapshot.WriteJSON(inventoryPath, inventory))
				updateBundleHashes(t, bundle)
			}
			priorInventory, err := os.ReadFile(snapshotimage.DefaultDependenciesFile)
			require.NoError(t, err)
			err = appsnapshot.RestoreContext(t.Context(), ops, p, "snapshot-plan.json", bundle)
			require.Error(t, err)
			body, err := os.ReadFile("dist/linux-amd64/a-first")
			require.NoError(t, err)
			require.Equal(t, "previous checkout artifact", string(body))
			body, err = os.ReadFile(snapshotimage.DefaultDependenciesFile)
			require.NoError(t, err)
			require.Equal(t, priorInventory, body)
		})
	}
}

func TestRestoreContextInstallsOnlyBoundInputsAndInventory(t *testing.T) {
	p, bundle := restoreFixture(t)
	require.NoError(t, os.RemoveAll("dist"))
	require.NoError(t, os.Remove(snapshotimage.DefaultDependenciesFile))
	require.NoError(t, os.WriteFile("keep-source", []byte("source unchanged"), 0o600))
	planBefore, err := os.ReadFile("snapshot-plan.json")
	require.NoError(t, err)
	require.NoError(t, appsnapshot.RestoreContext(t.Context(), &git{head: p.SourceSHA}, p, "snapshot-plan.json", bundle))
	body, err := os.ReadFile("dist/linux-amd64/a-first")
	require.NoError(t, err)
	require.Equal(t, "incoming first artifact", string(body))
	planAfter, err := os.ReadFile("snapshot-plan.json")
	require.NoError(t, err)
	require.Equal(t, planBefore, planAfter)
	body, err = os.ReadFile("keep-source")
	require.NoError(t, err)
	require.Equal(t, "source unchanged", string(body))
	require.FileExists(t, snapshotimage.DefaultDependenciesFile)
	info, err := os.Stat("dist/linux-amd64/server-linux-amd64")
	require.NoError(t, err)
	require.NotZero(t, info.Mode().Perm()&0o111)
}

func TestRestoreContextAcceptsBoundEmptyInventory(t *testing.T) {
	t.Chdir(t.TempDir())
	in := input(t, "containers:\n  - name: app\n    platforms: linux/amd64\n")
	p, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
	require.NoError(t, err)
	require.NoError(t, appsnapshot.RecordContext(t.Context(), &git{head: p.SourceSHA}, p, snapshotimage.DefaultDependenciesFile))
	bundle := contextBundle(t, p)
	require.NoError(t, os.Remove(snapshotimage.DefaultDependenciesFile))
	require.NoError(t, appsnapshot.RestoreContext(t.Context(), &git{head: p.SourceSHA}, p, "snapshot-plan.json", bundle))
	require.FileExists(t, snapshotimage.DefaultDependenciesFile)
}

func TestRestoreContextSupportsFrozenPlanOutsideSourceCheckout(t *testing.T) {
	p, bundle := restoreFixture(t)
	root, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Mkdir("source-checkout", 0o700))
	t.Chdir(filepath.Join(root, "source-checkout"))
	require.NoError(t, appsnapshot.RestoreContext(t.Context(), &git{head: p.SourceSHA}, p, "../snapshot-plan.json", bundle))
	require.FileExists(t, "dist/linux-amd64/server-linux-amd64")
	require.FileExists(t, snapshotimage.DefaultDependenciesFile)
	require.NoFileExists(t, "snapshot-plan.json")
}
