// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/stretchr/testify/require"
)

func TestNativeImageEvidenceWiredBeforeDigest(t *testing.T) {
	root := reporoot.Path(t)
	body, err := os.ReadFile(filepath.Join(root, ".gitlab/bin/release.sh"))
	require.NoError(t, err)

	text := string(body)
	start := strings.Index(text, "rc_images_publish() {")
	end := strings.Index(text[start:], "\n}\n")
	publish := text[start : start+end]
	require.Contains(t, publish, "--prepublication-sboms")
	// The index SBOM names each inventory by file name and checksum: the
	// inventories are in the SBOM bundle, so no download address is given.
	require.NotContains(t, publish, "--sbom-url-prefix")
	require.NotContains(t, publish, "install_syft")
	require.Less(t, strings.Index(publish, "command -v syft"), strings.Index(publish, "rc_release_checkout"))
	require.Less(t, strings.Index(publish, "rc_collect_release_images"), strings.Index(publish, "rc_bundle_release_image_sboms"))
	require.Less(t, strings.Index(publish, "rc_bundle_release_image_sboms"), strings.Index(publish, "release dist-digest"))
}

// The SBOM bundle is made final before anything that covers it exists. The
// job that completes it holds no key and writes neither the provenance
// statement nor a signature; the signing job, which writes them, never
// changes the bundle, and writes the statement before it signs.
func TestSBOMBundleIsFinalBeforeProvenanceAndSignatures(t *testing.T) {
	publish := releaseShellFunction(t, "rc_images_publish")
	bundle := releaseShellFunction(t, "rc_bundle_release_image_sboms")
	sign := releaseShellFunction(t, "rc_sign_release")

	require.Contains(t, bundle, "release files manifest --unsigned")
	require.Contains(t, bundle, "--release-images-ledger")
	require.Contains(t, bundle, "--image-sbom ")
	require.NotContains(t, bundle, "--evidence-file", "an image SBOM is no release file of its own")

	for _, forbidden := range []string{"release provenance", "release sign", "rc_sign_release_files", "release-images sign", "gpg sign", "COSIGN_KEY", "GPG_"} {
		require.NotContains(t, publish, forbidden, "the job that completes the bundle must not sign or write provenance")
		require.NotContains(t, bundle, forbidden)
	}

	for _, forbidden := range []string{"--unsigned --manifest \"${RELEASE_FILES_MANIFEST}\" --dist-dir \"${DIST_DIR}\" --release-images-ledger", "rc_bundle_release_image_sboms", "--image-sbom", "sbom-bundle"} {
		require.NotContains(t, sign, forbidden, "the signing job must not change the SBOM bundle")
	}

	validate := strings.Index(sign, "release files validate --unsigned")
	provenance := strings.Index(sign, "rc_engine release provenance")
	files := strings.Index(sign, "rc_sign_release_files")
	images := strings.Index(sign, "release-images sign")

	for _, position := range []int{validate, provenance, files, images} {
		require.NotEqual(t, -1, position)
	}

	require.Less(t, validate, provenance)
	require.Less(t, provenance, files, "the statement names the checksums file, which lists the final bundle")
	require.Less(t, files, images)
}

func TestNativeImageEvidenceRequiresJobImageSyftBeforeCheckout(t *testing.T) {
	root := reporoot.Path(t)
	bash, err := exec.LookPath("bash")
	require.NoError(t, err)
	command := exec.CommandContext(t.Context(), bash, "-c", `set -euo pipefail
source "$1"
rc_release_checkout() { echo CHECKOUT-MUST-NOT-RUN; }
RC_WORK=work rc_images_publish`, "preflight", filepath.Join(root, ".gitlab/bin/release.sh"))
	command.Env = []string{"PATH=" + t.TempDir()}
	output, err := command.CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(output), "requires Syft in job-image")
	require.NotContains(t, string(output), "CHECKOUT-MUST-NOT-RUN")
}
