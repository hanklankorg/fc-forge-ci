// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"os"
	"path/filepath"
	"testing"
)

// trustRootEngine stands in for the engine at the four steps that hand it a
// Cosign trust root. It keeps the key file and the digest it was given, by
// flag for the signing-key binding and by environment for the base-image verbs.
const trustRootEngine = `#!/usr/bin/env bash
set -euo pipefail
key=${COSIGN_PUBLIC_KEY_PATH:-} digest=${COSIGN_PUBLIC_KEY_SHA256:-}
while (($#)); do
  case "$1" in
    --trusted-public-key) key=$2 ;;
    --trusted-public-key-digest) read -r digest _ <"$2" ;;
  esac
  shift
done
cp -- "${key}" "${SEEN_TRUST_ROOT:?}/key"
printf '%s' "${digest}" >"${SEEN_TRUST_ROOT}/digest"
`

// trustRootSites are the four steps that hand the engine a Cosign trust root.
func trustRootSites() []struct{ file, job, step string } {
	return []struct{ file, job, step string }{
		{"sign-and-publish-release.yml", "sign", "Bind Cosign signing key to trusted public key"},
		{"sign-container-images.yml", "sign-container-images", "Bind Cosign signing key to trusted public key"},
		{"promote-base-container-images.yml", "promote-base-container-images", "Verify signatures and promote base tags"},
		{"verify-base-container-images.yml", "verify-base-container-images", "Verify existing base images"},
	}
}

// Each step that trusts a Cosign public key reads it from the workflow's two
// inputs, which are required and have no default: the layer ships no key, so
// there is nothing a default could name.
func TestCosignTrustRootInputsAreRequired(t *testing.T) {
	t.Parallel()

	for _, site := range trustRootSites() {
		selected := workflowStep(t, site.file, site.job, site.step)

		var document struct {
			On struct {
				Call struct {
					Inputs map[string]actionInput `yaml:"inputs"`
				} `yaml:"workflow_call"`
			} `yaml:"on"`
		}
		loadYAML(t, ".forgejo/workflows/"+site.file, &document)

		for variable, name := range map[string]string{
			"TRUSTED_KEY_INPUT": "cosign-public-key", "TRUSTED_KEY_SHA256_INPUT": "cosign-public-key-sha256",
		} {
			if selected.Env[variable] != "${{ inputs['"+name+"'] }}" {
				t.Errorf("%s: %s is %q, not the %s input", site.file, variable, selected.Env[variable], name)
			}

			if input, exists := document.On.Call.Inputs[name]; !exists || !input.Required || input.Default != nil {
				t.Errorf("%s: %s must be required and have no default", site.file, name)
			}
		}
	}
}

// Each of those steps hands the engine the caller's key and digest and nothing
// else: there is no file in the helper checkout to fall back to. The key
// reaches the engine as a file ending in one newline, which is the form its
// digest is taken over. Half a pair, or none, reaches the engine as it is, and
// the engine refuses the empty half
// (TestCallerCosignTrustRootThroughTheEngine).
func TestCosignTrustRootIsTheCallersInputs(t *testing.T) {
	t.Parallel()

	const callerKey, callerDigest = "-----BEGIN PUBLIC KEY-----\ncaller\n-----END PUBLIC KEY-----", "caller-digest"

	for _, site := range trustRootSites() {
		run := workflowStep(t, site.file, site.job, site.step).Run

		for _, test := range []struct {
			name, key, digest, wantKey, wantDigest string
		}{
			{"the caller's key", callerKey, callerDigest, callerKey + "\n", callerDigest},
			{"the caller's key given with its newline", callerKey + "\n", callerDigest, callerKey + "\n", callerDigest},
			{"a key without a digest", callerKey, "", callerKey + "\n", ""},
			{"a digest without a key", "", callerDigest, "\n", callerDigest},
			{"neither", "", "", "\n", ""},
		} {
			t.Run(site.file+"/"+test.name, func(t *testing.T) {
				t.Parallel()

				key, digest := seenTrustRoot(t, run, test.key, test.digest)
				if key != test.wantKey || digest != test.wantDigest {
					t.Errorf("engine was given key %q and digest %q, want %q and %q", key, digest, test.wantKey, test.wantDigest)
				}
			})
		}
	}
}

// seenTrustRoot runs a trust-root step with the given inputs and returns the
// key file content and the digest the engine received.
func seenTrustRoot(t *testing.T, run, keyInput, digestInput string) (string, string) {
	t.Helper()

	box := newSandbox(t, "trust-root")
	box.write(".forgejo/bin/forge-ci", trustRootEngine, 0o700)
	// A key in the helper checkout, where Release CI kept one, must not be
	// read.
	box.write("keys/cosign.pub", "helper key\n", 0o600)
	box.write("keys/cosign.pub.sha256", "helper-digest\n", 0o600)

	seen := filepath.Join(box.root, "seen")
	for _, directory := range []string{seen, filepath.Join(box.root, "private"), filepath.Join(box.root, "consumer")} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	got := box.run(run,
		"HELPERS_DIR="+box.root, "SIGNER_WORKSPACE="+filepath.Join(box.root, "private"),
		"CONSUMER_WORKSPACE="+filepath.Join(box.root, "consumer"), "SEEN_TRUST_ROOT="+seen,
		"TRUSTED_KEY_INPUT="+keyInput, "TRUSTED_KEY_SHA256_INPUT="+digestInput,
		"COSIGN_KEY=fixture-key", "COSIGN_PASSWORD=fixture-password",
		"REGISTRY_TOKEN=fixture-token", "REGISTRY_USER=owner", "BASE_IMAGE_REPOSITORY=")
	if got.status != 0 {
		t.Fatalf("workflow shell failed: %s", got.stderr)
	}

	key, err := os.ReadFile(filepath.Join(seen, "key"))
	if err != nil {
		t.Fatal(err)
	}

	digest, err := os.ReadFile(filepath.Join(seen, "digest"))
	if err != nil {
		t.Fatal(err)
	}

	return string(key), string(digest)
}
