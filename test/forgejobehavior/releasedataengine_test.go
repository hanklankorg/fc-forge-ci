// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// One release image of forgejo.invalid/owner/project at v1.2.3, as the engine
// admits it. No test reaches a registry with it.
const releaseImageLedger = `[{"ref":"forgejo.invalid/owner/project@sha256:` + releaseImageDigest + `",` +
	`"digest":"sha256:` + releaseImageDigest + `","final_tag":"forgejo.invalid/owner/project:v1.2.3"}]`

const releaseImageDigest = "0000000000000000000000000000000000000000000000000000000000000000"

// The three image steps of sign-and-publish-release.yml hand the ledger
// question to the engine. Their own shell runs here against the engine built
// from this tree: a release without a ledger
// succeeds with no token, a ledger the caller named must exist, a ledger
// without entries is refused by sign and promote and takes cleanup on to its
// login, and a release with images is refused when the named secret is empty,
// whatever other credential variable the job environment carries. No case
// reaches a registry: the engine's login only writes its private auth config.
func TestOptionalLedgerStepsThroughTheEngine(t *testing.T) {
	t.Parallel()

	engine := forgeCIEngine(t)
	lib := string(reporoot.ReadFile(t, ".forgejo/bin/lib-forge-ci.sh"))

	const (
		ledger   = "dist/release-images.json"
		skipped  = "no release-image ledger"
		loggedIn = "Logged in to "
		// Set under every name the engine falls back to from an empty token,
		// and under GITHUB_TOKEN, which it does not read for Forgejo and the
		// steps do not clear.
		ambient = "ambient-fixture-credential"
	)

	for _, step := range []struct{ job, name, verb string }{
		{"sign", "Sign and attest container images", "sign"},
		{"promote", "Promote signed images", "promote"},
		{"cleanup", "Cleanup release candidate tags", "cleanup"},
	} {
		for _, test := range []struct {
			name, only, named, content, registryToken string
			// refused names what stderr must carry; empty means the step succeeds.
			// cleanupRefused is what cleanup carries where it differs.
			refused, cleanupRefused string
			written, logsIn         bool
		}{
			{name: "skips an absent default ledger"},
			{
				// Cleanup needs no entries, so it goes on to its login.
				name: "refuses a ledger without entries", written: true, content: "[]",
				refused: "at least one entry", cleanupRefused: "registry password",
			},
			{name: "refuses a named ledger that is absent", named: ledger, refused: ledger},
			{
				name: "refuses images with an empty registry token", written: true, content: releaseImageLedger,
				refused: "registry password",
			},
			{
				name: "refuses images with an empty provider token", only: "cleanup", written: true, content: releaseImageLedger,
				registryToken: "fixture-registry-token", refused: "provider tag-deletion token", logsIn: true,
			},
		} {
			if test.only != "" && test.only != step.verb {
				continue
			}

			if step.verb == "cleanup" && test.cleanupRefused != "" {
				test.refused = test.cleanupRefused
			}

			t.Run(step.verb+"/"+test.name, func(t *testing.T) {
				t.Parallel()

				box := newSandbox(t, "sign-and-publish-release")
				// The real resolver runs the built engine as a non-strict
				// override.
				box.write(".forgejo/bin/lib-forge-ci.sh", lib, 0o600)
				box.write("dist/.keep", "", 0o600)

				if test.written {
					box.write(ledger, test.content, 0o600)
				}

				got := box.run(workflowStep(t, "sign-and-publish-release.yml", step.job, step.name).Run,
					"FORGE_CI_BIN="+engine, "HELPERS_DIR="+box.root, "CONSUMER_WORKSPACE="+box.root,
					"DIST_DIR=dist", "RELEASE_TAG=v1.2.3", "SIGN_AND_PUBLISH_STATE_DIR="+filepath.Join(box.root, "state"),
					"FORGEJO_SERVER_URL=https://forgejo.invalid", "FORGEJO_REPOSITORY=Owner/Project",
					"RELEASE_IMAGES_PATH="+test.named, "BASE_IMAGE_REPOSITORY=", "REGISTRY_USER=owner",
					"REGISTRY_TOKEN="+test.registryToken, "FORGE_CI_PACKAGE_API_TOKEN=",
					"REGISTRY_PASSWORD="+ambient, "FORGEJO_TOKEN="+ambient, "GITEA_TOKEN="+ambient, "GITHUB_TOKEN="+ambient,
					"COSIGN_KEY=fixture-key", "COSIGN_PASSWORD=fixture-password")

				want := test.refused
				if want == "" {
					want = skipped
				}

				if (got.status != 0) != (test.refused != "") || !strings.Contains(got.stderr, want) ||
					strings.Contains(got.stderr, loggedIn) != test.logsIn {
					t.Fatalf("status %d, want refusal %t with %q on stderr, logged in %t\nstdout %s\nstderr %s",
						got.status, test.refused != "", want, test.logsIn, got.stdout, got.stderr)
				}

				if test.refused != "" && strings.Contains(got.stderr, skipped) {
					t.Fatalf("a refused run reported a release without images:\n%s", got.stderr)
				}
			})
		}
	}
}

// The sign job's first look at the producer's files runs here, its own shell
// against the engine built from this tree. An unsigned
// hand-off passes; a signature or a bundle next to a declared file, or a
// provenance statement, is refused before any signing step could publish it as
// the signer's.
func TestUnsignedHandOffStepThroughTheEngine(t *testing.T) {
	t.Parallel()

	engine := forgeCIEngine(t)
	lib := string(reporoot.ReadFile(t, ".forgejo/bin/lib-forge-ci.sh"))

	const manifest = `{"version":1,` +
		`"assets":[{"path":"dist/app.tar.gz","name":"app.tar.gz","source":"producer"},` +
		`{"path":"dist/checksums.txt","name":"checksums.txt","source":"producer"}],` +
		`"checksums":[{"path":"dist/checksums.txt","name":"checksums.txt","source":"producer"}],` +
		`"sboms":[],"evidence":[],"provenance":[]}`

	for _, test := range []struct{ name, planted string }{
		{name: "admits an unsigned hand-off"},
		{name: "refuses a planted package signature", planted: "dist/app.tar.gz.sig"},
		{name: "refuses a planted bundle", planted: "dist/checksums.txt.bundle"},
		{name: "refuses a planted provenance statement", planted: "dist/slsa-provenance.intoto.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			box := newSandbox(t, "sign-and-publish-release")
			box.write(".forgejo/bin/lib-forge-ci.sh", lib, 0o600)
			box.write("dist/app.tar.gz", "archive\n", 0o600)
			box.write("dist/checksums.txt", "not read by this step\n", 0o600)
			box.write("dist/release-files.json", manifest, 0o600)

			if test.planted != "" {
				box.write(test.planted, "planted by the producer\n", 0o600)
			}

			got := box.run(workflowStep(t, "sign-and-publish-release.yml", "sign", "Verify unsigned release file hand-off").Run,
				"FORGE_CI_BIN="+engine, "HELPERS_DIR="+box.root, "CONSUMER_WORKSPACE="+box.root,
				"DIST_DIR=dist", "RELEASE_FILES_MANIFEST=dist/release-files.json")

			if (got.status != 0) != (test.planted != "") || !strings.Contains(got.stderr, test.planted) {
				t.Fatalf("status %d, want refusal %t naming %q\nstdout %s\nstderr %s",
					got.status, test.planted != "", test.planted, got.stdout, got.stderr)
			}
		})
	}
}

// passthroughEngine stands in for the resolved engine: it records the arguments
// and the environment of every call, answers `artifact download` itself, since
// that one reads a forge, and hands every other call to the real engine.
//
//nolint:gosec // G101: a fixture script, no credential.
const passthroughEngine = `#!/usr/bin/env bash
set -euo pipefail
{ printf 'argv:'; printf ' %q' "$@"; printf '\n'; env; } >>"${ENGINE_CALL_LOG:?}"
if [[ "$1 $2" == "artifact download" ]]; then exit 0; fi
exec "${REAL_FORGE_CI:?}" "$@"
`

// forgejoRunnerEnv is what a Forgejo runner says about one job of
// Owner/Project. The engine takes the source ref and the builder and
// invocation identity of a provenance statement from these, and refuses a run
// without a ref name, so a test passes them to the step or the engine call
// alone.
func forgejoRunnerEnv() []string {
	return []string{
		"GITHUB_ACTIONS=true", "FORGEJO_ACTIONS=true", "FORGEJO_SERVER_URL=https://forgejo.invalid",
		"FORGEJO_REPOSITORY=Owner/Project", "FORGEJO_SHA=" + releaseImageDigest[:40], "FORGEJO_RUN_ID=1",
		"FORGEJO_REF_NAME=release-request/v1.2.3",
	}
}

// The signing step of sign-container-images.yml runs here, its own shell
// against the engine built from this tree. Every ledger
// names an SBOM outside premade-sboms/, which the engine refuses after its
// local registry login and before it resolves or signs anything, so no case
// reaches a registry. A case that logs in was admitted: the ledger sat inside
// the base repository the step's flags name or derive, and the token arrived
// on stdin. The registry token appears in no argument and in no child
// environment. On the forge's registry a base-repository input chooses the
// package name only: the engine refuses one under another owner, before the
// ledger and the login, so a caller cannot have the release key attest a path
// the calling repository's owner does not hold. A repository on another
// registry is the step's declaration of an external one, taken as declared and
// logged in to there. The command tests cover the rules; the cases here show
// the step's flags reach them.
func TestBaseImageSigningThroughTheEngine(t *testing.T) {
	t.Parallel()

	engine := forgeCIEngine(t)

	const (
		derived  = "forgejo.invalid/owner/project"
		explicit = "forgejo.invalid/owner/persistent-bases"
		foreign  = "forgejo.invalid/other/persistent-bases"
		offForge = "registry.invalid:5000/owner/persistent-bases"
		external = "registry.invalid:5000/another-org/persistent-bases"
		token    = "fixture-registry-token"
		loggedIn = "Logged in to forgejo.invalid as owner"
	)

	for _, test := range []struct {
		name, image, suffix, repository string
		admitted                        bool
		// login is the login an admitted case shows; empty is the forge's.
		login, refusal string
	}{
		{name: "derives the repository with a suffix", image: derived + "-base", suffix: "-base", admitted: true},
		{name: "takes another package name under the owner", image: explicit, suffix: "-base", repository: explicit, admitted: true},
		{name: "refuses a ledger outside the derived repository", image: derived, suffix: "-base", refusal: "must be under"},
		{
			name: "refuses a repository under another owner", image: foreign, repository: foreign,
			refusal: "must be below the source repository's owner",
		},
		{
			name: "takes a repository on another registry as declared", image: offForge, repository: offForge,
			admitted: true, login: "Logged in to registry.invalid:5000 as owner",
		},
		{
			name: "takes an external repository under another owner", image: external, repository: external,
			admitted: true, login: "Logged in to registry.invalid:5000 as owner",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			ledger := `[{"role":"base","image_kind":"base","flavor":"core","ref":"` + test.image + `@sha256:` + releaseImageDigest +
				`","digest":"sha256:` + releaseImageDigest + `","final_tag":"` + test.image + `:` + releaseImageDigest +
				`-core","base_input_id":"` + releaseImageDigest + `","sbom":"dist/outside.cyclonedx.json","sbom_sha256":"` +
				releaseImageDigest + `"}]`

			box := newSandbox(t, "sign-container-images")
			box.write(".forgejo/bin/forge-ci", passthroughEngine, 0o700)
			calls := filepath.Join(box.root, "calls")

			got := box.run(workflowStep(t, "sign-container-images.yml", "sign-container-images", "Sign and attest images").Run,
				append(forgejoRunnerEnv(),
					"REAL_FORGE_CI="+engine, "ENGINE_CALL_LOG="+calls, "HELPERS_DIR="+box.root,
					"SIGNER_WORK_DIR="+box.root, "FORGEJO_SERVER=https://forgejo.invalid", "FORGEJO_REPO=Owner/Project",
					"LEDGER_JSON="+ledger, "REPOSITORY_SUFFIX="+test.suffix, "BASE_IMAGE_REPOSITORY="+test.repository,
					"SOURCE_SHA="+releaseImageDigest[:40], "CALLER_WORKFLOW=release.yml", "SBOM_ARTIFACT=base-sboms",
					"COSIGN_KEY=fixture-key", "COSIGN_PASSWORD=fixture-password", "REGISTRY_TOKEN="+token, "REGISTRY_USER=owner")...)

			// Every case ends in a refusal; the login tells an admitted ledger
			// from one refused at admission.
			login := loggedIn
			if test.login != "" {
				login = test.login
			}

			if got.status == 0 || strings.Contains(got.stderr, login) != test.admitted ||
				!strings.Contains(got.stderr, test.refusal) {
				t.Fatalf("status %d, want a refusal naming %q, logged in %t\nstdout %s\nstderr %s",
					got.status, test.refusal, test.admitted, got.stdout, got.stderr)
			}

			recorded, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}

			if strings.Contains(string(recorded)+got.stdout+got.stderr, token) {
				t.Fatal("the registry token reached an argument, a child environment or the step output")
			}

			if keys := strings.Count(string(recorded), "COSIGN_KEY=fixture-key"); keys != 1 {
				t.Fatalf("the signing key is in the environment of %d engine calls, want only the signing call", keys)
			}
		})
	}
}
