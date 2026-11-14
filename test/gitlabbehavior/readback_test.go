// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"strings"
	"testing"
)

// The publishing job reads the release back as its last engine call, from
// the manifest it published, with no release secret in the engine's
// environment: without GITLAB_TOKEN the engine uses the job token.
func TestPublishedReleaseIsReadBackWithoutTheReleaseToken(t *testing.T) {
	t.Parallel()

	body := `rc_release_checkout() { :; }; ` +
		`rc_read_handoff() { export DIST_DIR=dist RELEASE_FILES_MANIFEST=dist/release-files.json; }; ` +
		`rc_engine() { echo "engine token=[${GITLAB_TOKEN-unset}] secrets=[$(compgen -e | grep -E '^RELEASE_.*(TOKEN|KEY|PASSPHRASE)$|^(COSIGN|GPG|SSH)_' | tr '\n' ' ')]: $*"; }; ` +
		`rc_publish_release`

	output, err := runReleaseFunction(t, t.TempDir(), body, "CI_PROJECT_DIR=/project", "CI_PROJECT_PATH=group/app",
		"RELEASE_TAG=v1.0.0", "RELEASE_TOKEN=release-token", "RC_RELEASE_ASSET_RETENTION=3", "COSIGN_PASSWORD=stray")
	if err != nil {
		t.Fatalf("%v\n%s", err, output)
	}

	calls := strings.Split(strings.TrimSpace(output), "\n")

	const want = "engine token=[unset] secrets=[]: release validate-published --tag v1.0.0 --repository group/app --draft=false " +
		"--manifest dist/release-files.json --dist-dir dist"
	if len(calls) != 3 || calls[2] != want {
		t.Fatalf("the last of three engine calls must be the readback %q:\n%s", want, output)
	}

	for _, call := range calls[:2] {
		if !strings.HasPrefix(call, "engine token=[release-token] secrets=[COSIGN_PASSWORD RELEASE_TOKEN ]: release ") {
			t.Errorf("a publishing call lost the release token: %s", call)
		}
	}
}
