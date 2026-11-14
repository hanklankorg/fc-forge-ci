// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	releasecmd "github.com/diggsweden/forge-ci/internal/cli/commands/release"
	"github.com/stretchr/testify/require"
)

func TestCanonicalPayloadCLIStagesBothRootsAndKeepsLegacyDigest(t *testing.T) {
	t.Setenv("EXPECTED_DIGEST", "")
	t.Setenv("DIST_DIGEST", "")

	source := t.TempDir()
	for _, name := range []string{"dist/base-images.json", "premade-sboms/base-sbom-native.cyclonedx.json"} {
		full := filepath.Join(source, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o700))
		require.NoError(t, os.WriteFile(full, []byte("{}\n"), 0o600))
	}

	digest := capturePayloadDigest(t, "--dist-dir", source, "--canonical-payload")
	want, err := apprelease.CanonicalPayloadDigest(source)
	require.NoError(t, err)
	require.Equal(t, want, digest)
	digestFile := filepath.Join(t.TempDir(), "digest")
	require.NoError(t, os.WriteFile(digestFile, []byte(digest+"\n"), 0o600))
	destination := filepath.Join(t.TempDir(), "verified-root")
	require.NoError(t, releasecmd.New().Run(t.Context(), []string{"release", "validate-dist", "--dist-dir", source, "--canonical-payload", "--digest-file", digestFile, "--stage-dir", destination}))
	require.NoError(t, apprelease.VerifyCanonicalPayload(destination, digest))
	require.FileExists(t, filepath.Join(destination, "premade-sboms/base-sbom-native.cyclonedx.json"))

	legacy, err := apprelease.DistDigest(source)
	require.NoError(t, err)
	require.Equal(t, legacy, capturePayloadDigest(t, "--dist-dir", source))
	require.NotEqual(t, legacy, digest, "legacy path-sensitive default remains unchanged")
	require.NoError(t, releasecmd.New().Run(t.Context(), []string{"release", "validate-dist", "--dist-dir", source, "--expected-digest", legacy}))

	for _, verb := range []string{"dist-digest", "validate-dist"} {
		err := releasecmd.New().Run(t.Context(), []string{"release", verb, "--dist-dir", source, "--canonical-payload", "--manifest-root", "."})
		require.ErrorContains(t, err, "fixes --manifest-root")
	}

	require.ErrorContains(t, releasecmd.New().Run(t.Context(), []string{"release", "validate-dist", "--dist-dir", source, "--canonical-payload", "--digest-file", digestFile, "--expected-digest", digest}), "not both")
	require.NoError(t, os.WriteFile(filepath.Join(source, "dist/base-images-publication.json"), []byte("[]"), 0o600))
	require.ErrorContains(t, releasecmd.New().Run(t.Context(), []string{"release", "dist-digest", "--dist-dir", source, "--canonical-payload"}), "reserved publication ledger")
}

func capturePayloadDigest(t *testing.T, flags ...string) string {
	t.Helper()

	reader, writer, err := os.Pipe()
	require.NoError(t, err)

	original := os.Stdout
	os.Stdout = writer

	t.Cleanup(func() { os.Stdout = original })
	err = releasecmd.New().Run(t.Context(), append([]string{"release", "dist-digest"}, flags...))
	os.Stdout = original

	require.NoError(t, writer.Close())
	require.NoError(t, err)
	body, err := io.ReadAll(reader)
	require.NoError(t, reader.Close())
	require.NoError(t, err)

	return strings.TrimSpace(string(body))
}
