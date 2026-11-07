// SPDX-FileCopyrightText: The Forge CI Authors
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
	// The CLI obtains provider-owned source-release URLs, not shell routes or
	// the independent image-publication project's coordinates.
	require.NotContains(t, publish, "--sbom-url-prefix")
	require.NotContains(t, publish, "install_syft")
	require.Less(t, strings.Index(publish, "command -v syft"), strings.Index(publish, "rc_release_checkout"))
	require.Less(t, strings.Index(publish, "rc_collect_release_images"), strings.Index(publish, "rc_register_release_image_evidence"))
	require.Less(t, strings.Index(publish, "rc_register_release_image_evidence"), strings.Index(publish, "release dist-digest"))
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
