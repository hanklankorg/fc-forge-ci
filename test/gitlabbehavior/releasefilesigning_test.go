// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package gitlabbehavior

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/testutil/gpgkey"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// realEngine replaces runReleaseFunction's recording engine with the CLI
// built from this tree.
const realEngine = `rc_engine() { "${FORGE_CI_BIN:?}" "$@"; }; `

// The sign jobs read the release file signing method through the engine
// built from this tree: gpg only for a declaration that says sign.method
// gpg or sign.files-method gpg, cosign for one without a sign block, for a
// Cosign key and without a declaration. Any other answer, and none, stops
// the job.
func TestReleaseFileSigningMethodThroughTheEngine(t *testing.T) {
	t.Parallel()

	engine := treeEngine(t)

	for _, test := range []struct {
		name, declaration, engine, want string
		absent                          bool
	}{
		{name: "method gpg", declaration: "sign:\n  method: gpg\n", want: "gpg"},
		{name: "files-method gpg", declaration: "sign:\n  method: kms\n  key: env://COSIGN_KEY\n  files-method: gpg\n", want: "gpg"},
		{name: "no sign block", declaration: "", want: "cosign"},
		{name: "cosign key", declaration: "sign:\n  method: kms\n  key: env://COSIGN_KEY\n", want: "cosign"},
		{name: "no declaration", absent: true, want: "cosign"},
		{name: "another answer", engine: `rc_engine() { echo RELEASE_FILE_SIGNING=sigstore >"${CI_OUTPUT}"; }; `},
		{name: "no answer", engine: `rc_engine() { :; }; `},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			work := t.TempDir()
			config := ""

			if !test.absent {
				config = ".forge-ci/release.yml"
				writeFixture(t, filepath.Join(work, config), test.declaration+"artifacts:\n  - name: app\n    project-type: maven\n    working-directory: .\n")
			}

			fake := test.engine
			if fake == "" {
				fake = realEngine
			}

			output, err := runReleaseFunction(t, work, fake+`rc_release_file_signing; echo "signing=${rc_file_signing}"`,
				"FORGE_CI_BIN="+engine, "RC_WORK="+work, "RC_ARTIFACTS_CONFIG="+config, "GITLAB_CI=true", "FORGE_CI_PROVIDER=gitlab")
			if (err == nil) != (test.want != "") {
				t.Fatalf("succeeded=%t, want %t:\n%s", err == nil, test.want != "", output)
			}

			if test.want != "" && !strings.Contains(output, "signing="+test.want+"\n") {
				t.Errorf("rc_file_signing is not %s:\n%s", test.want, output)
			}
		})
	}
}

// The signing preflight, which "Check: signing access" runs before the
// release commit, requires the GPG key, its passphrase and its fingerprint
// for a declaration that says sign.method or sign.files-method gpg, also
// without distro packages.
func TestSigningPreflightRequiresTheGPGKeyForGPGReleaseFiles(t *testing.T) {
	t.Parallel()

	gpg := []string{"RELEASE_GPG_PRIVATE_KEY=key", "RELEASE_GPG_PASSPHRASE=passphrase", "RELEASE_GPG_FINGERPRINT=fingerprint"}

	for _, test := range []struct {
		name, distro, files, refused string
		gpg                          bool
	}{
		{name: "cosign files", distro: "false", files: "cosign"},
		{name: "gpg files with the key", distro: "false", files: "gpg", gpg: true},
		{name: "gpg files without the key", distro: "false", files: "gpg", refused: "artifacts.yml says sign.method or sign.files-method gpg"},
		{name: "distro packages without the key", distro: "true", files: "cosign", refused: "sign-distro-packages is true"},
		{name: "another method", distro: "false", files: "sigstore", refused: "not 'sigstore'"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			work := t.TempDir()
			// The signer's toolchain check is the image's; stubbed here.
			writeFixture(t, filepath.Join(work, "helpers/.forgejo/scripts/verify-signer-toolchain.sh"), "exit 0\n")

			env := []string{"RC_WORK=" + work, "RC_HELPERS_DIR=" + filepath.Join(work, "helpers"),
				"COSIGN_PUBLIC_KEY=public", "RC_PUBLIC_KEY_SHA256=digest",
				"RELEASE_COSIGN_PRIVATE_KEY=cosign", "RELEASE_COSIGN_PASSPHRASE=cosign"}
			if test.gpg {
				env = append(env, gpg...)
			}

			output, err := runReleaseFunction(t, work, "rc_signing_preflight "+test.distro+" "+test.files, env...)
			if (err != nil) != (test.refused != "") || !strings.Contains(output, test.refused) {
				t.Fatalf("refused=%t, want refusal %t naming %q:\n%s", err != nil, test.refused != "", test.refused, output)
			}
		})
	}
}

// The GPG release file signing runs here, release.sh against the engine
// built from this tree, with a throwaway key: it writes an armored signature
// of the checksums file and of the SBOM that the key verifies, dated at the
// release commit's time, and no Cosign bundle. With a secret missing it stops
// before the engine runs, and with a key that is not the pinned one, or
// another passphrase, the engine refuses it; neither signs anything. The throwaway key sets the
// test's environment, so it runs serially.
func TestGPGReleaseFileSigningThroughTheEngine(t *testing.T) {
	engine := treeEngine(t)

	// The key is protected, so the signature shows that the job hands
	// RELEASE_GPG_PASSPHRASE to the engine as the passphrase that unlocks it.
	const (
		passphrase = "fixture-passphrase"
		other      = "0123456789ABCDEF0123456789ABCDEF01234567"
	)

	key := gpgkey.NewProtected(t, passphrase)
	armored := key.ArmoredPrivateKey()

	// The release commit's time, which the signatures carry. A key cannot
	// sign before it was made, so it is after the key: an hour ahead, which
	// a signature dated at the moment it is made never is.
	committed := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)

	for _, test := range []struct {
		name, key, fingerprint, password, refused string
	}{
		{name: "signs the checksums and the SBOM", key: armored, fingerprint: key.Fingerprint, password: passphrase},
		{name: "refuses a missing key", fingerprint: key.Fingerprint, password: passphrase, refused: "RELEASE_GPG_PRIVATE_KEY"},
		{name: "refuses a missing fingerprint", key: armored, password: passphrase, refused: "RELEASE_GPG_FINGERPRINT"},
		{name: "refuses a missing passphrase", key: armored, fingerprint: key.Fingerprint, refused: "RELEASE_GPG_PASSPHRASE"},
		{name: "refuses another passphrase", key: armored, fingerprint: key.Fingerprint, password: "another-passphrase", refused: "decrypt private key"},
		{name: "refuses a key that is not the pinned one", key: armored, fingerprint: other, password: passphrase, refused: "not the pinned --fingerprint " + other},
	} {
		t.Run(test.name, func(t *testing.T) {
			work := t.TempDir()
			writeFixture(t, filepath.Join(work, "dist/app.jar"), "application\n")
			writeFixture(t, filepath.Join(work, "dist/app.cdx.json"), "{}\n")
			writeFixture(t, filepath.Join(work, "dist/checksums.sha256"),
				sha256Hex(t, filepath.Join(work, "dist/app.jar"))+"  app.jar\n"+sha256Hex(t, filepath.Join(work, "dist/app.cdx.json"))+"  app.cdx.json\n")
			writeFixture(t, filepath.Join(work, "dist/release-files.json"), `{"version":1,`+
				`"assets":[{"path":"dist/app.jar","name":"app.jar","source":"producer"},`+
				`{"path":"dist/app.cdx.json","name":"app.cdx.json","source":"producer"},`+
				`{"path":"dist/checksums.sha256","name":"checksums.sha256","source":"producer"}],`+
				`"checksums":[{"path":"dist/checksums.sha256","name":"checksums.sha256","source":"checksum"}],`+
				`"sboms":[{"path":"dist/app.cdx.json","name":"app.cdx.json","source":"sbom"}],"evidence":[],"provenance":[]}`)
			// The runner's swap state, injected: the engine refuses to load a
			// GPG key while swap is on, and this host's is not the test's.
			swaps := filepath.Join(work, "swaps")
			writeFixture(t, swaps, "Filename\tType\tSize\tUsed\tPriority\n")
			release := gpgkey.CommitAt(t, work, committed)

			output, err := runReleaseFunction(t, work, realEngine+"rc_sign_release_files gpg",
				"FORGE_CI_BIN="+engine, "DIST_DIR=dist", "RELEASE_FILES_MANIFEST=dist/release-files.json",
				"FORGE_CI_PROC_SWAPS="+swaps, "RELEASE_SHA="+release,
				"RELEASE_GPG_PRIVATE_KEY="+test.key, "RELEASE_GPG_FINGERPRINT="+test.fingerprint, "RELEASE_GPG_PASSPHRASE="+test.password,
				"RELEASE_COSIGN_PRIVATE_KEY=fixture-cosign-key")
			if (err != nil) != (test.refused != "") || !strings.Contains(output, test.refused) {
				t.Fatalf("refused=%t, want refusal %t naming %q:\n%s", err != nil, test.refused != "", test.refused, output)
			}

			for _, signed := range []string{"dist/checksums.sha256", "dist/app.cdx.json"} {
				_, err := os.Stat(filepath.Join(work, signed+".asc"))

				switch {
				case test.refused != "" && err == nil:
					t.Errorf("a refused signing wrote %s.asc", signed)
				case test.refused == "" && err != nil:
					t.Errorf("the signing wrote no %s.asc: %v", signed, err)
				case test.refused == "":
					key.VerifyDetachedAt(t, filepath.Join(work, signed), committed)
				}

				if _, err := os.Stat(filepath.Join(work, signed+".bundle")); err == nil {
					t.Errorf("the GPG signing wrote %s.bundle", signed)
				}
			}
		})
	}
}

// treeEngine builds the CLI from this tree into the test's directory, so a
// change to a verb and to release.sh, which calls it, are tested together.
func treeEngine(t *testing.T) string {
	t.Helper()

	engine := filepath.Join(t.TempDir(), "forge-ci")

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()

	command := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", engine, "./cmd/forge-ci")
	command.Dir = reporoot.Path(t)

	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build the engine from this tree: %v\n%s", err, out)
	}

	return engine
}

// writeFixture writes body to path, making its directory.
func writeFixture(t *testing.T, path, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sha256Hex(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

// "Check: signing access" takes the release file signing method from the
// request's declaration through the engine built from this tree, before the
// release commit: a gpg declaration without the GPG key is refused there,
// with the reason, also without distro packages.
func TestCheckSigningAccessRequiresTheGPGKeyTheDeclarationNames(t *testing.T) {
	t.Parallel()

	engine := treeEngine(t)
	gpg := []string{"RELEASE_GPG_PRIVATE_KEY=key", "RELEASE_GPG_PASSPHRASE=passphrase", "RELEASE_GPG_FINGERPRINT=fingerprint"}

	for _, test := range []struct {
		name, declaration, refused string
		gpg                        bool
	}{
		{name: "method gpg without the key", declaration: "sign:\n  method: gpg\n", refused: "artifacts.yml says sign.method or sign.files-method gpg"},
		{name: "files-method gpg without the key", declaration: "sign:\n  method: kms\n  key: env://COSIGN_KEY\n  files-method: gpg\n", refused: "artifacts.yml says sign.method or sign.files-method gpg"},
		{name: "method gpg with the key", declaration: "sign:\n  method: gpg\n", gpg: true},
		{name: "the cosign key", declaration: "sign:\n  method: kms\n  key: env://COSIGN_KEY\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			work := t.TempDir()
			config := ".forge-ci/release.yml"
			writeFixture(t, filepath.Join(work, config), test.declaration+"artifacts:\n  - name: app\n    project-type: maven\n    working-directory: .\n")
			// The signer's toolchain check is the image's; stubbed here.
			writeFixture(t, filepath.Join(work, "helpers/.forgejo/scripts/verify-signer-toolchain.sh"), "exit 0\n")

			env := []string{"FORGE_CI_BIN=" + engine, "RC_WORK=" + work, "RC_ARTIFACTS_CONFIG=" + config,
				"GITLAB_CI=true", "FORGE_CI_PROVIDER=gitlab", "RC_SIGN_DISTRO_PACKAGES=false",
				"RC_HELPERS_DIR=" + filepath.Join(work, "helpers"), "COSIGN_PUBLIC_KEY=public", "RC_PUBLIC_KEY_SHA256=digest",
				"RELEASE_COSIGN_PRIVATE_KEY=cosign", "RELEASE_COSIGN_PASSPHRASE=cosign"}
			if test.gpg {
				env = append(env, gpg...)
			}

			// The request checkout is the declaration in place; the plan verb is
			// the tree's engine, and the key checks the recording one.
			body := `rc_request_checkout() { :; }; ` +
				`rc_engine() { if [ "$1" = plan ]; then "${FORGE_CI_BIN:?}" "$@"; else echo "engine: $*"; fi; }; ` +
				`rc_check_signing_access`

			output, err := runReleaseFunction(t, work, body, env...)
			if (err != nil) != (test.refused != "") || !strings.Contains(output, test.refused) {
				t.Fatalf("refused=%t, want refusal %t naming %q:\n%s", err != nil, test.refused != "", test.refused, output)
			}
		})
	}
}

// The sign job signs the release files the way the declaration's answer
// says, not a way of its own.
func TestSigningSignsTheReleaseFilesTheWayTheDeclarationSays(t *testing.T) {
	t.Parallel()

	for _, signing := range []string{"gpg", "cosign"} {
		t.Run(signing, func(t *testing.T) {
			t.Parallel()

			// The checks before the file signing are stubbed, and the run
			// ends at the file signing.
			body := `rc_signing_preflight() { :; }; rc_release_checkout() { :; }; ` +
				`rc_release_file_signing() { rc_file_signing=` + signing + `; }; ` +
				`rc_read_handoff() { mkdir dist; export DIST_DIR=dist RELEASE_FILES_MANIFEST=dist/release-files.json; }; ` +
				`rc_sign_release_files() { echo "files signed with $1"; exit 0; }; rc_sign_release false`

			output, err := runReleaseFunction(t, t.TempDir(), body, "CI_PROJECT_DIR=/project", "RC_SIGN_DISTRO_PACKAGES=false")
			if err != nil || !strings.Contains(output, "files signed with "+signing+"\n") {
				t.Fatalf("the release files were not signed with %s: %v\n%s", signing, err, output)
			}
		})
	}
}

// The engine that signs the release files with GPG holds the GPG key and its
// passphrase, and no other signing or publishing secret the job has.
func TestGPGReleaseFileSigningHoldsNoOtherSecret(t *testing.T) {
	t.Parallel()

	work := t.TempDir()
	release := gpgkey.CommitAt(t, work, "1700000000")

	secrets := []string{"RELEASE_SSH_SIGNING_KEY", "RELEASE_COSIGN_PRIVATE_KEY", "RELEASE_COSIGN_PASSPHRASE",
		"RELEASE_TOKEN", "RELEASE_REGISTRY_USER", "RELEASE_REGISTRY_TOKEN", "RELEASE_PACKAGE_TOKEN",
		"SSH_SIGNING_KEY", "COSIGN_KEY", "COSIGN_PASSWORD", "GPG_SIGNING_KEY", "GPG_SIGNING_PASSWORD",
		"GITLAB_TOKEN", "REGISTRY_USER", "REGISTRY_TOKEN", "CI_JOB_TOKEN", "CI_REGISTRY_PASSWORD",
		"CI_DEPENDENCY_PROXY_PASSWORD", "CI_DEPLOY_PASSWORD"}

	env := make([]string, 0, 6+len(secrets))
	env = append(env, "RELEASE_SHA="+release, "DIST_DIR=dist", "RELEASE_FILES_MANIFEST=dist/release-files.json",
		"RELEASE_GPG_PRIVATE_KEY=gpg-key", "RELEASE_GPG_PASSPHRASE=gpg-passphrase", "RELEASE_GPG_FINGERPRINT=fingerprint")

	for _, secret := range secrets {
		env = append(env, secret+"=fixture-secret")
	}

	body := `rc_engine() { echo "engine sees: $(compgen -e | sort | tr '\n' ' ')"; ` +
		`echo "key=${GPG_PRIVATE_KEY} passphrase=${GPG_PASSPHRASE} args=$*"; }; rc_sign_release_files gpg`

	output, err := runReleaseFunction(t, work, body, env...)
	if err != nil {
		t.Fatalf("the GPG signing failed: %v\n%s", err, output)
	}

	if !strings.Contains(output, "key=gpg-key passphrase=gpg-passphrase args=release sign --method gpg --fingerprint fingerprint ") {
		t.Fatalf("the engine was not handed the GPG key, its passphrase and its fingerprint:\n%s", output)
	}

	_, seen, _ := strings.Cut(output, "engine sees: ")
	seen, _, _ = strings.Cut(seen, "\n")

	for _, name := range append(secrets, "RELEASE_GPG_PRIVATE_KEY", "RELEASE_GPG_PASSPHRASE") {
		if slices.Contains(strings.Fields(seen), name) {
			t.Errorf("the GPG signing engine holds %s: %s", name, seen)
		}
	}
}
