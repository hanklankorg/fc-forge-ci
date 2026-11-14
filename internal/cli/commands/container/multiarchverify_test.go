// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	containercmd "github.com/diggsweden/forge-ci/internal/cli/commands/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestMultiarchCommandRejectsConflictingTagAndMalformedLabelsBeforeIO(t *testing.T) {
	auth := filepath.Join(t.TempDir(), "auth.json")
	require.NoError(t, os.WriteFile(auth, []byte(`{"auths":{}}`), 0o600))
	base := []string{"container", "multiarch-image", "assemble", "--auth-file", auth, "--source-sha", strings.Repeat("a", 64), "--server-url", "https://forge.invalid", "--repository", "acme/widget", "--image-repository", "registry.invalid/acme/widget"}

	for _, extra := range [][]string{
		{"--tag", "staging-v1.2.3", "--tag-prefix", "conflict-"},
		{"--require-label", "missing-equals"},
		// The gate scans staged layouts; a registry assembly has none.
		{"--tag", "staging-v1.2.3", "--scan-output-template", "trivy-{arch}.json"},
	} {
		args := append(append([]string{}, base...), extra...)
		err := containercmd.New().Run(t.Context(), args)
		require.ErrorIs(t, err, errs.ErrUsage)
	}

	err := containercmd.New().Run(t.Context(), []string{"container", "multiarch-image", "verify", "--ref", "registry.invalid/acme/widget:mutable"})
	require.ErrorIs(t, err, errs.ErrUsage)
}

// Publishing staged layouts always scans them; the caller cannot leave the
// scan out, and the administrator policy decides whether findings block.
func TestMultiarchAssembleFromLayoutsRequiresTheScan(t *testing.T) {
	auth := filepath.Join(t.TempDir(), "auth.json")
	require.NoError(t, os.WriteFile(auth, []byte(`{"auths":{}}`), 0o600))

	err := containercmd.New().Run(t.Context(), []string{"container", "multiarch-image", "assemble", "--from-layouts",
		"--auth-file", auth, "--source-sha", strings.Repeat("a", 40), "--server-url", "https://forge.invalid",
		"--repository", "acme/widget", "--image-repository", "registry.invalid/acme/widget", "--tag", "staging-v1.2.3",
		"--name", "widget", "--archs", "amd64"})
	require.ErrorIs(t, err, errs.ErrUsage)
	require.Contains(t, err.Error(), "--scan-output-template")
	require.Contains(t, err.Error(), "administrator policy")
}

// build-push-oci-image publishes a final tag, so it scans like every other
// publish route; leaving the scan out is a usage error, refused before a build.
func TestBuildPushOCIImageRequiresTheScan(t *testing.T) {
	err := containercmd.New().Run(t.Context(), []string{"container", "build-push-oci-image",
		"--tag", "v1.2.3", "--containerfile", "Containerfile", "--builds-json", `[{"platform":"linux/amd64"}]`,
		"--image", "registry.invalid/acme/widget", "--server-url", "https://forge.invalid", "--repository", "acme/widget"})
	require.ErrorIs(t, err, errs.ErrUsage)
	require.Contains(t, err.Error(), "--scan-output-template")
	require.Contains(t, err.Error(), "administrator policy")
}

func TestMultiarchCommandsRejectStdoutSnapshotsBeforeReadingMetadata(t *testing.T) {
	auth := filepath.Join(t.TempDir(), "auth.json")
	require.NoError(t, os.WriteFile(auth, []byte(`{"auths":{}}`), 0o600))

	for _, command := range []string{"assemble", "verify"} {
		args := []string{"container", "multiarch-image", command, "--auth-file", auth,
			"--source-sha", strings.Repeat("a", 40), "--server-url", "https://forge.invalid",
			"--repository", "acme/widget", "--image-repository", "registry.invalid/acme/widget",
			"--manifest-output", "-"}
		if command == "verify" {
			args = append(args, "--ref", "registry.invalid/acme/widget@sha256:"+strings.Repeat("1", 64))
		}

		err := containercmd.New().Run(t.Context(), args)
		require.ErrorIs(t, err, errs.ErrUsage)
		require.Contains(t, err.Error(), "stdout (-) is not supported")
	}
}
