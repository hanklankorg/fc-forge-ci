// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package sbom_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	appsbom "github.com/diggsweden/forge-ci/internal/app/sbom"
)

func localIndexBOM(t *testing.T) (appsbom.IndexBOMInput, []byte) {
	t.Helper()
	root := t.TempDir()
	native := []byte("{\"bomFormat\":\"CycloneDX\",\"specVersion\":\"1.6\",\"components\":[]}\n")
	digest := "sha256:" + strings.Repeat("a", 64)
	index := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":%q,"platform":{"os":"linux","architecture":"arm64"}}]}`, digest))
	require.NoError(t, os.WriteFile(filepath.Join(root, "index.json"), index, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "native.json"), native, 0o600))
	manifest, err := json.Marshal(map[string]any{"version": 1, "platforms": []appsbom.IndexBOMFile{{Platform: "linux/arm64", ImageRef: "registry.example/acme/widget@" + digest, File: "native.json", URL: "https://downloads.example/native.json", SHA256: fmt.Sprintf("%x", sha256.Sum256(native))}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "parts.json"), manifest, 0o600))

	return appsbom.IndexBOMInput{Root: root, IndexRef: fmt.Sprintf("registry.example/acme/widget@sha256:%x", sha256.Sum256(index)), IndexFile: "index.json", PartsFile: "parts.json", Output: filepath.Join(root, "result.json")}, native
}

func TestComposeIndexSBOMWritesPinnedOutputAndPreservesInputs(t *testing.T) {
	t.Parallel()
	in, native := localIndexBOM(t)
	result, err := appsbom.ComposeIndex(in)
	require.NoError(t, err)
	body, err := os.ReadFile(result.Path)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(body)), result.SHA256)
	require.Equal(t, []string{filepath.Join(in.Root, "native.json")}, result.EvidenceFiles)
	after, err := os.ReadFile(filepath.Join(in.Root, "native.json"))
	require.NoError(t, err)
	require.Equal(t, native, after)
}

func TestComposeIndexSBOMRejectsInputFailuresWithoutOutput(t *testing.T) {
	t.Parallel()

	for _, defect := range []string{"changed-native", "missing-native", "escape", "symlink", "unknown-field", "trailing-json", "version"} {
		t.Run(defect, func(t *testing.T) {
			t.Parallel()
			in, _ := localIndexBOM(t)
			root, rootErr := os.OpenRoot(in.Root)
			require.NoError(t, rootErr)
			t.Cleanup(func() { require.NoError(t, root.Close()) })

			partsPath := filepath.Join(in.Root, "parts.json")
			body, err := os.ReadFile(partsPath)
			require.NoError(t, err)

			switch defect {
			case "changed-native":
				require.NoError(t, os.WriteFile(filepath.Join(in.Root, "native.json"), []byte(`{}`), 0o600))
			case "missing-native":
				require.NoError(t, os.Remove(filepath.Join(in.Root, "native.json")))
			case "escape":
				body = []byte(strings.ReplaceAll(string(body), `"file":"native.json"`, `"file":"../native.json"`))
			case "symlink":
				require.NoError(t, os.Rename(filepath.Join(in.Root, "native.json"), filepath.Join(in.Root, "real.json")))
				require.NoError(t, os.Symlink("real.json", filepath.Join(in.Root, "native.json")))
			case "unknown-field":
				body = []byte(strings.Replace(string(body), `"version":1`, `"version":1,"unknown":true`, 1))
			case "trailing-json":
				body = append(body, []byte(` {}`)...)
			case "version":
				body = []byte(strings.Replace(string(body), `"version":1`, `"version":9`, 1))
			}

			require.NoError(t, root.WriteFile("parts.json", body, 0o600))

			_, err = appsbom.ComposeIndex(in)
			require.Error(t, err)
			require.NoFileExists(t, in.Output)
		})
	}
}

func TestComposeIndexSBOMCannotOverwriteNativeEvidence(t *testing.T) {
	t.Parallel()
	in, native := localIndexBOM(t)
	in.Output = filepath.Join(in.Root, "native.json")
	_, err := appsbom.ComposeIndex(in)
	require.Error(t, err)
	body, err := os.ReadFile(in.Output)
	require.NoError(t, err)
	require.Equal(t, native, body)

	alias := filepath.Join(in.Root, "alias.json")
	require.NoError(t, os.Link(in.Output, alias))
	in.Output = alias
	_, err = appsbom.ComposeIndex(in)
	require.Error(t, err)
}
