// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/container"
)

func TestValidateImageIndexExactChildren(t *testing.T) {
	t.Parallel()

	one, two := "sha256:"+strings.Repeat("1", 64), "sha256:"+strings.Repeat("2", 64)
	expected := []container.PlatformDigest{{Platform: "linux/amd64", Digest: one}, {Platform: "linux/arm64", Digest: two}}
	raw := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":%q,"platform":{"os":"linux","architecture":"arm64","variant":"v8"}},{"digest":%q,"platform":{"os":"linux","architecture":"amd64"}}]}`, two, one)
	require.NoError(t, container.ValidateImageIndex([]byte(raw), expected))

	for name, body := range map[string]string{
		"wrong digest":       strings.ReplaceAll(raw, two, "sha256:"+strings.Repeat("3", 64)),
		"duplicate child":    strings.ReplaceAll(strings.ReplaceAll(raw, two, one), "arm64", "amd64"),
		"foreign OS":         strings.ReplaceAll(raw, "linux", "windows"),
		"missing children":   `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`,
		"unknown media type": strings.ReplaceAll(raw, "application/vnd.oci.image.index.v1+json", "unknown"),
		"malformed":          `{`,
		"null":               `null`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Error(t, container.ValidateImageIndex([]byte(body), expected))
		})
	}

	require.Error(t, container.ValidateImageIndex([]byte(raw), expected[:1]))
	require.Error(t, container.ValidateImageIndex([]byte(raw), nil))
	require.Error(t, container.ValidateImageIndex([]byte(raw), []container.PlatformDigest{expected[0], expected[0]}))
}

func TestValidateImageIndexSingleManifestMustBeTheExpectedBytes(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{},"layers":[]}`)
	expected := []container.PlatformDigest{{Platform: "linux/arm64", Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(raw))}}
	require.NoError(t, container.ValidateImageIndex(raw, expected))
	expected[0].Digest = "sha256:" + strings.Repeat("0", 64)
	require.Error(t, container.ValidateImageIndex(raw, expected))
	require.Error(t, container.ValidateImageIndex([]byte(`{}`), expected))
}

func TestValidateImageIndexChecksExplicitVariants(t *testing.T) {
	t.Parallel()

	digest := "sha256:" + strings.Repeat("1", 64)
	raw := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.list.v2+json","manifests":[{"digest":%q,"platform":{"os":"linux","architecture":"arm","variant":"v7"}}]}`, digest))
	require.NoError(t, container.ValidateImageIndex(raw, []container.PlatformDigest{{Platform: "linux/arm/v7", Digest: digest}}))
	require.Error(t, container.ValidateImageIndex(raw, []container.PlatformDigest{{Platform: "linux/arm/v6", Digest: digest}}))
	require.Error(t, container.ValidateImageIndex(raw, []container.PlatformDigest{{Platform: "linux/arm/", Digest: digest}}))
}
