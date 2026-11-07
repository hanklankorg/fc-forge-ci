// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/stretchr/testify/require"
)

// The existing loopback registry and mock cosign are inert test fixtures, not
// live providers or signing authority. Exercise the actual CLI action, including
// its default pattern, rather than supplying a pattern only in an app test.
func TestReleaseImagesSignCanonicalHandoffRelocatedRoot(t *testing.T) {
	fixture := newBaseSignFixture(t)
	source := filepath.Join(t.TempDir(), "payload")
	require.NoError(t, os.MkdirAll(filepath.Join(source, "dist"), 0o700))

	sbomPath := "dist/image-sbom-native.cyclonedx.json"
	sbom := []byte("{\n\"bomFormat\":\"CycloneDX\",\"serialNumber\":\"producer-pinned\"\n}\n")
	require.NoError(t, os.WriteFile(filepath.Join(source, sbomPath), sbom, 0o600))

	repository := fixture.host + "/owner/app"
	digest := "sha256:" + strings.Repeat("a", 64)
	ledger, err := imageledger.Marshal([]imageledger.Entry{{
		Role: "native", Ref: repository + "@" + digest, Digest: digest,
		FinalTag: repository + ":v1.2.3", SBOM: sbomPath, SBOMSHA256: fmt.Sprintf("%x", sha256.Sum256(sbom)),
	}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(source, "dist/release-images.json"), ledger, 0o600))

	envelope := []byte(`{"predicate":{"buildDefinition":{"externalParameters":{}}}}`)
	require.NoError(t, os.WriteFile(filepath.Join(source, "dist/slsa-provenance.intoto.json"), envelope, 0o600))
	handoffDigest, err := apprelease.CanonicalPayloadDigest(source)
	require.NoError(t, err)
	verified := filepath.Join(t.TempDir(), "relocated-verified-root")
	fixture.run(t, "release", "validate-dist", "--canonical-payload", "--dist-dir", source,
		"--expected-digest", handoffDigest, "--stage-dir", verified)

	for _, scenario := range []string{"default", "explicit override", "refused override"} {
		t.Run(scenario, func(t *testing.T) {
			args := []string{"container", "release-images", "sign", "--dist-dir", filepath.Join(verified, "dist"),
				"--evidence-root", verified, "--server-url", fixture.serverURL, "--repository", baseSlug,
				"--tag", "v1.2.3", "--stage", "release", "--method", "kms", "--key", "env://COSIGN_KEY",
				"--provenance-envelope", filepath.Join(verified, "dist/slsa-provenance.intoto.json")}

			switch scenario {
			case "explicit override":
				args = append(args, "--sbom-path-pattern", `^dist/image-sbom-native[.]cyclonedx[.]json$`)
			case "refused override":
				args = append(args, "--sbom-path-pattern", `^never$`)
			}

			var signErr error

			calls, predicates := fixture.given(t, func() { signErr = fixture.runErr(t, args...) })
			if scenario == "refused override" {
				require.ErrorContains(t, signErr, "must match")
				require.Empty(t, calls)

				return
			}

			require.NoError(t, signErr)
			require.Len(t, calls, 3)
			require.Equal(t, string(sbom), predicates[0])
			require.Contains(t, predicates[1], sbomPath)
			require.NotContains(t, predicates[1], verified)
		})
	}

	require.NoError(t, apprelease.VerifyCanonicalPayload(verified, handoffDigest))

	// Without evidence-root the legacy pattern must still follow dist-dir.
	legacyDir := "custom-dist"
	require.NoError(t, os.Mkdir(legacyDir, 0o700))
	legacySBOM := legacyDir + "/image-sbom-native.cyclonedx.json"

	var entries []imageledger.Entry

	entries, err = imageledger.Parse(ledger)
	require.NoError(t, err)

	entries[0].SBOM = legacySBOM
	legacyLedger, err := imageledger.Marshal(entries)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(legacySBOM, sbom, 0o600))
	require.NoError(t, os.WriteFile(legacyDir+"/release-images.json", legacyLedger, 0o600))
	fixture.run(t, "container", "release-images", "sign", "--dist-dir", legacyDir,
		"--server-url", fixture.serverURL, "--repository", baseSlug,
		"--tag", "v1.2.3", "--stage", "release", "--method", "kms", "--key", "env://COSIGN_KEY",
		"--provenance-envelope", filepath.Join(verified, "dist/slsa-provenance.intoto.json"))
}
