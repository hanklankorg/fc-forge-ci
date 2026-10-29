// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage_test

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appsnapshot "github.com/diggsweden/forge-ci/internal/app/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/stretchr/testify/require"
)

// Match upload/download-artifact's regular-file permission normalization without
// changing bytes, filenames, or the producer's recorded execution policy.
func simulateArtifactZIP(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.Type().IsRegular() {
			return os.Chmod(path, 0o644)
		}
		return nil
	}))
}

func nativeModeFixture(t *testing.T, kind string) (snapshotimage.Plan, string, string) {
	t.Helper()
	t.Chdir(t.TempDir())
	in := input(t, "artifacts:\n  - name: server\n    project-type: "+kind+"\n    sboms: none\n    config:\n      binary-name: renamed\ncontainers:\n  - name: app\n    from: [server]\n    platforms: linux/amd64\n")
	p, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
	require.NoError(t, err)
	require.NoError(t, appsnapshot.WriteJSON("snapshot-plan.json", p))
	require.NoError(t, os.MkdirAll("raw/linux-amd64", 0o700))
	require.NoError(t, os.WriteFile("raw/linux-amd64/renamed-linux-amd64", []byte("planned executable bytes"), 0o644))
	require.NoError(t, os.WriteFile("raw/linux-amd64/z-data.txt", []byte("not executable"), 0o644))
	return p, "dist/linux-amd64/renamed-linux-amd64", "dist/linux-amd64/z-data.txt"
}

func TestNativeExecutablesSurviveRawProducerAndContextZIPTransports(t *testing.T) {
	for _, kind := range []string{"go", "cargo"} {
		t.Run(kind, func(t *testing.T) {
			p, binary, data := nativeModeFixture(t, kind)
			require.NoError(t, appsnapshot.BuildArtifact(t.Context(), &git{head: p.SourceSHA}, nil, p, appsnapshot.BuildArtifactInput{Name: "server", FromArtifact: "raw"}))
			var producer snapshotimage.ArtifactReceipt
			require.NoError(t, appsnapshot.ReadJSON("_snapshot-artifacts/server/snapshot-artifact.json", &producer))
			for _, file := range producer.Files {
				if file.Path == binary {
					require.Equal(t, "0755", file.Mode)
				}
				if file.Path == data {
					require.Equal(t, "0644", file.Mode)
				}
			}
			raw, err := os.Stat("raw/linux-amd64/renamed-linux-amd64")
			require.NoError(t, err)
			require.Equal(t, fs.FileMode(0o644), raw.Mode().Perm(), "preparation must not chmod the downloaded source")
			simulateArtifactZIP(t, snapshotimage.DefaultProducerDir)
			require.NoError(t, appsnapshot.GatherContext(t.Context(), &git{head: p.SourceSHA}, p, "snapshot-plan.json", snapshotimage.DefaultProducerDir, "bundle"))
			simulateArtifactZIP(t, "bundle")
			require.NoError(t, os.RemoveAll("dist"))
			require.NoError(t, os.Remove(snapshotimage.DefaultDependenciesFile))
			require.NoError(t, appsnapshot.RestoreContext(t.Context(), &git{head: p.SourceSHA}, p, "snapshot-plan.json", "bundle"))
			info, err := os.Stat(binary)
			require.NoError(t, err)
			require.Equal(t, fs.FileMode(0o755), info.Mode().Perm())
			info, err = os.Stat(data)
			require.NoError(t, err)
			require.Equal(t, fs.FileMode(0o644), info.Mode().Perm(), "adjacent text must not gain execute permission")
			// A build checks restored permissions, rather than ignoring them as a
			// transport detail after inputs have crossed the restore boundary.
			require.NoError(t, os.Chmod(binary, 0o644))
			builder := &countingBuilder{}
			err = appsnapshot.Build(t.Context(), &git{head: p.SourceSHA}, builder, ociregistry.NewAnonymous(), io.Discard, output.Annotator{}, p, appsnapshot.BuildInput{Name: "app", Arch: "amd64", HostArch: "amd64", MetadataDir: "metadata"})
			require.Error(t, err)
			require.Zero(t, builder.calls)
		})
	}
}

func executableNPMPackage(t *testing.T) snapshotimage.Plan {
	t.Helper()
	p := npmProducerPlan(t)
	body := []byte(`{"name":"snapshot-fixture","version":"1.2.3","bin":{"fixture":"bin/tool.js"}}`)
	require.NoError(t, os.WriteFile("web/package.json", body, 0o600))
	require.NoError(t, os.Mkdir("raw", 0o700))
	file, err := os.Create("raw/package.tgz")
	require.NoError(t, err)
	gz := gzip.NewWriter(file)
	writer := tar.NewWriter(gz)
	for _, entry := range []struct {
		path string
		body []byte
		mode int64
	}{{"package/package.json", body, 0o644}, {"package/bin/tool.js", []byte("#!/usr/bin/env node\nconsole.log('fixture');\n"), 0o755}, {"package/README.txt", []byte("package data"), 0o644}} {
		require.NoError(t, writer.WriteHeader(&tar.Header{Name: entry.path, Typeflag: tar.TypeReg, Mode: entry.mode, Size: int64(len(entry.body))}))
		_, err = writer.Write(entry.body)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	require.NoError(t, gz.Close())
	require.NoError(t, file.Close())
	return p
}

func TestNPMPackagedExecutableSurvivesBothZIPTransports(t *testing.T) {
	p := executableNPMPackage(t)
	require.NoError(t, appsnapshot.BuildArtifact(t.Context(), &git{head: p.SourceSHA}, nil, p, appsnapshot.BuildArtifactInput{Name: "web", FromArtifact: "raw"}))
	simulateArtifactZIP(t, snapshotimage.DefaultProducerDir)
	require.NoError(t, appsnapshot.GatherContext(t.Context(), &git{head: p.SourceSHA}, p, "snapshot-plan.json", snapshotimage.DefaultProducerDir, "bundle"))
	simulateArtifactZIP(t, "bundle")
	require.NoError(t, os.RemoveAll("web/.forge-ci"))
	require.NoError(t, os.Remove(snapshotimage.DefaultDependenciesFile))
	require.NoError(t, appsnapshot.RestoreContext(t.Context(), &git{head: p.SourceSHA}, p, "snapshot-plan.json", "bundle"))
	for _, test := range []struct {
		path string
		mode fs.FileMode
	}{{"bin/tool.js", 0o755}, {"README.txt", 0o644}, {"package.json", 0o644}} {
		info, err := os.Stat(filepath.Join("web/.forge-ci/snapshot-inputs/web", test.path))
		require.NoError(t, err)
		require.Equal(t, test.mode, info.Mode().Perm())
	}
}

func TestUnsafeOrMissingRecordedModesRefuseBeforeAnyRestore(t *testing.T) {
	for _, mode := range []string{"", "04755", "02755", "01755", "0777", "0100755", "0755 ", "0640"} {
		t.Run(mode, func(t *testing.T) {
			p, binary, data := nativeModeFixture(t, "go")
			require.NoError(t, appsnapshot.BuildArtifact(t.Context(), &git{head: p.SourceSHA}, nil, p, appsnapshot.BuildArtifactInput{Name: "server", FromArtifact: "raw"}))
			manifestPath := "_snapshot-artifacts/server/snapshot-artifact.json"
			var producer snapshotimage.ArtifactReceipt
			require.NoError(t, appsnapshot.ReadJSON(manifestPath, &producer))
			originalMode := producer.Files[0].Mode
			producer.Files[0].Mode = mode
			require.NoError(t, appsnapshot.WriteJSON(manifestPath, producer))
			require.Error(t, appsnapshot.GatherContext(t.Context(), &git{head: p.SourceSHA}, p, "snapshot-plan.json", snapshotimage.DefaultProducerDir, "rejected-bundle"))
			require.NoDirExists(t, "dist")
			require.NoDirExists(t, "rejected-bundle")
			producer.Files[0].Mode = originalMode
			require.NoError(t, appsnapshot.WriteJSON(manifestPath, producer))
			require.NoError(t, appsnapshot.GatherContext(t.Context(), &git{head: p.SourceSHA}, p, "snapshot-plan.json", snapshotimage.DefaultProducerDir, "bundle"))
			inventoryPath := "bundle/files/snapshot-dependencies.json"
			var inventory snapshotimage.DependencyInventory
			require.NoError(t, appsnapshot.ReadJSON(inventoryPath, &inventory))
			inventory.Dependencies[0].Files[0].Mode = mode
			require.NoError(t, appsnapshot.WriteJSON(inventoryPath, inventory))
			updateBundleHashes(t, "bundle")
			require.NoError(t, os.WriteFile(binary, []byte("previous checkout"), 0o700))
			outside := filepath.Join(t.TempDir(), "outside")
			require.NoError(t, os.WriteFile(outside, []byte("outside unchanged"), 0o600))
			require.NoError(t, os.Remove(data))
			require.NoError(t, os.Symlink(outside, data))
			require.Error(t, appsnapshot.RestoreContext(t.Context(), &git{head: p.SourceSHA}, p, "snapshot-plan.json", "bundle"))
			body, err := os.ReadFile(binary)
			require.NoError(t, err)
			require.Equal(t, "previous checkout", string(body))
			body, err = os.ReadFile(outside)
			require.NoError(t, err)
			require.Equal(t, "outside unchanged", string(body))
		})
	}
}

func TestRawSpecialFilePermissionsAreRejected(t *testing.T) {
	p, _, _ := nativeModeFixture(t, "go")
	require.NoError(t, os.Chmod("raw/linux-amd64/renamed-linux-amd64", os.ModeSetuid|0o755))
	info, err := os.Stat("raw/linux-amd64/renamed-linux-amd64")
	require.NoError(t, err)
	if info.Mode()&os.ModeSetuid == 0 {
		t.Skip("filesystem strips setuid")
	}
	require.Error(t, appsnapshot.BuildArtifact(t.Context(), &git{head: p.SourceSHA}, nil, p, appsnapshot.BuildArtifactInput{Name: "server", FromArtifact: "raw"}))
	require.NoDirExists(t, "_snapshot-artifacts/server")
}
