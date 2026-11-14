// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package sbom_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	sbomcmd "github.com/diggsweden/forge-ci/internal/cli/commands/sbom"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestIndexCommandIsOfflineAndBindsProducerDocuments(t *testing.T) {
	root := t.TempDir()
	outputs := filepath.Join(root, "ci-output")
	t.Setenv("FORGEJO_OUTPUT", outputs)
	t.Setenv("FORGEJO_ACTIONS", "true")
	t.Setenv("GITHUB_ACTIONS", "true")

	native := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6"}`)
	digest := "sha256:" + strings.Repeat("1", 64)
	index := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":%q,"platform":{"os":"linux","architecture":"arm64"}}]}`, digest))
	ref := fmt.Sprintf("registry.example/acme/app@sha256:%x", sha256.Sum256(index))
	parts := fmt.Sprintf(`{"version":1,"platforms":[{"platform":"linux/arm64","image_ref":"registry.example/acme/app@%s","file":"native.json","url":"https://downloads.example/native.json","sha256":"%x"}]}`, digest, sha256.Sum256(native))
	require.NoError(t, os.WriteFile(filepath.Join(root, "native.json"), native, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "index.json"), index, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "parts.json"), []byte(parts), 0o600))
	out := filepath.Join(root, "out.json")
	// An empty PATH prevents accidental tool invocation; URLs use inert hosts.
	t.Setenv("PATH", t.TempDir())

	args := []string{"sbom", "index", "--root", root, "--index-file", "index.json", "--platform-sboms", "parts.json", "--image-ref", ref, "--output", out}
	require.NoError(t, sbomcmd.New().Run(t.Context(), args))
	require.FileExists(t, out)

	ciOutput, err := os.ReadFile(outputs)
	require.NoError(t, err)
	require.Contains(t, string(ciOutput), "evidence-files<<")
	require.Contains(t, string(ciOutput), filepath.Join(root, "native.json"))
	require.Contains(t, string(ciOutput), out)
	require.NoError(t, os.Remove(out))
	require.NoError(t, os.WriteFile(filepath.Join(root, "native.json"), []byte(`{}`), 0o600))
	require.Error(t, sbomcmd.New().Run(t.Context(), args))
	require.NoFileExists(t, out)
}

func TestIndexCommandRejectsStdoutBeforeReadingInputs(t *testing.T) {
	err := sbomcmd.New().Run(t.Context(), []string{"sbom", "index",
		"--root", filepath.Join(t.TempDir(), "missing"), "--index-file", "index.json",
		"--platform-sboms", "parts.json", "--image-ref", "registry.invalid/acme/widget@sha256:" + strings.Repeat("1", 64),
		"--output", "-"})
	require.ErrorIs(t, err, errs.ErrUsage)
	require.Contains(t, err.Error(), "stdout (-) is not supported")
}
