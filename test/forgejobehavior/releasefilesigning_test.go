// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/testutil/gpgkey"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// Both release workflows read the release file signing method through the
// engine built from this tree: gpg only for a declaration that says
// sign.method gpg, cosign for one without a sign block, for a Cosign key and
// without a declaration.
func TestReleaseFileSigningMethodThroughTheEngine(t *testing.T) {
	t.Parallel()

	engine := forgeCIEngine(t)
	lib := string(reporoot.ReadFile(t, ".engine/bin/lib-forge-ci.sh"))

	// Each job finds the engine's resolver where its checkout puts it.
	for _, site := range []struct{ file, job, lib string }{
		{file: "sign-and-publish-release.yml", job: "sign", lib: ".engine/bin/lib-forge-ci.sh"},
		{file: "prepare-release.yml", job: "plan", lib: ".forge-ci-helpers/.engine/bin/lib-forge-ci.sh"},
	} {
		run := workflowStep(t, site.file, site.job, "Read the release file signing method").Run

		for _, test := range []struct {
			name, declaration, want string
		}{
			{name: "method gpg", declaration: "sign:\n  method: gpg\n", want: "gpg"},
			{name: "no sign block", declaration: "", want: "cosign"},
			{name: "cosign key", declaration: "sign:\n  method: kms\n  key: env://COSIGN_KEY\n", want: "cosign"},
			{name: "no declaration", want: "cosign"},
		} {
			t.Run(site.file+"/"+test.name, func(t *testing.T) {
				t.Parallel()

				box := newSandbox(t, "release-file-signing")
				box.write(site.lib, lib, 0o600)

				config := ""
				if test.name != "no declaration" {
					config = ".forge-ci/artifacts.yml"
					box.write(config, test.declaration+"artifacts:\n  - name: app\n    project-type: maven\n    working-directory: .\n", 0o600)
				}

				got := box.run(run, "FORGE_CI_BIN="+engine, "HELPERS_DIR="+box.root, "CONSUMER_WORKSPACE="+box.root,
					"ARTIFACTS_CONFIG="+config, "FORGEJO_ACTIONS=true", "GITHUB_ACTIONS=true")
				if got.status != 0 {
					t.Fatalf("status %d\nstdout %s\nstderr %s", got.status, got.stdout, got.stderr)
				}

				if output := box.output(); !strings.Contains(output, "release-file-signing="+test.want+"\n") {
					t.Errorf("the step output is %q, want release-file-signing=%s\nstderr %s", output, test.want, got.stderr)
				}
			})
		}
	}
}

// The GPG release file signing step runs here, its own shell against the
// engine built from this tree, with a throwaway key: it writes an armored
// signature of the checksums file and of the SBOM that the key verifies,
// dated at the release commit's time, and no Cosign bundle. With a secret
// missing it stops before the engine runs, and with a key that is not the
// pinned one the engine refuses it; neither signs anything. The throwaway keys set the test's
// environment, so it runs serially.
func TestGPGReleaseFileSigningThroughTheEngine(t *testing.T) {
	engine := forgeCIEngine(t)
	lib := string(reporoot.ReadFile(t, ".engine/bin/lib-forge-ci.sh"))
	run := workflowStep(t, "sign-and-publish-release.yml", "sign", "GPG-sign checksums and SBOMs").Run

	// The fixture exports only an unprotected key; the engine signs with one
	// whatever passphrase the step passes.
	const (
		passphrase = "fixture-passphrase"
		other      = "0123456789ABCDEF0123456789ABCDEF01234567"
	)

	key := gpgkey.New(t)
	armored := key.ArmoredPrivateKey()

	// The release commit's time, which the signatures carry. A key cannot
	// sign before it was made, so it is after the key: an hour ahead, which
	// a signature dated at the moment it is made never is.
	committed := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)

	for _, test := range []struct {
		name, key, fingerprint, password, refused string
	}{
		{name: "signs the checksums and the SBOM", key: armored, fingerprint: key.Fingerprint, password: passphrase},
		{name: "refuses a missing key", fingerprint: key.Fingerprint, password: passphrase, refused: "GPG_SIGNING_KEY"},
		{name: "refuses a missing fingerprint", key: armored, password: passphrase, refused: "GPG_SIGNING_FINGERPRINT"},
		{name: "refuses a missing passphrase", key: armored, fingerprint: key.Fingerprint, refused: "GPG_SIGNING_PASSWORD"},
		{name: "refuses a key that is not the pinned one", key: armored, fingerprint: other, password: passphrase, refused: "not the pinned --fingerprint " + other},
	} {
		t.Run(test.name, func(t *testing.T) {
			box := newSandbox(t, "sign-and-publish-release")
			box.write(".engine/bin/lib-forge-ci.sh", lib, 0o600)
			box.write("dist/app.jar", "application\n", 0o600)
			box.write("dist/app.cdx.json", "{}\n", 0o600)
			box.write("dist/checksums.sha256",
				fileSHA256(t, filepath.Join(box.root, "dist/app.jar"))+"  app.jar\n"+fileSHA256(t, filepath.Join(box.root, "dist/app.cdx.json"))+"  app.cdx.json\n", 0o600)
			box.write("dist/release-files.json", `{"version":1,`+
				`"assets":[{"path":"dist/app.jar","name":"app.jar","source":"producer"},`+
				`{"path":"dist/app.cdx.json","name":"app.cdx.json","source":"producer"},`+
				`{"path":"dist/checksums.sha256","name":"checksums.sha256","source":"producer"}],`+
				`"checksums":[{"path":"dist/checksums.sha256","name":"checksums.sha256","source":"checksum"}],`+
				`"sboms":[{"path":"dist/app.cdx.json","name":"app.cdx.json","source":"sbom"}],"evidence":[],"provenance":[]}`, 0o600)
			// The runner's swap state, injected: the engine refuses to load a
			// GPG key while swap is on, and this host's is not the test's.
			swaps := box.write("swaps", "Filename\tType\tSize\tUsed\tPriority\n", 0o600)
			release := gpgkey.CommitAt(t, box.root, committed)

			got := box.run(run,
				"FORGE_CI_BIN="+engine, "HELPERS_DIR="+box.root, "CONSUMER_WORKSPACE="+box.root,
				"DIST_DIR=dist", "RELEASE_FILES_MANIFEST=dist/release-files.json", "FORGE_CI_PROC_SWAPS="+swaps, "RELEASE_SHA="+release,
				"GPG_SIGNING_KEY="+test.key, "GPG_SIGNING_FINGERPRINT="+test.fingerprint, "GPG_SIGNING_PASSWORD="+test.password,
				"COSIGN_KEY=fixture-cosign-key", "REGISTRY_TOKEN=fixture-registry-token")

			if (got.status != 0) != (test.refused != "") || !strings.Contains(got.stderr, test.refused) {
				t.Fatalf("status %d, want refusal %t naming %q\nstdout %s\nstderr %s",
					got.status, test.refused != "", test.refused, got.stdout, got.stderr)
			}

			for _, signed := range []string{"dist/checksums.sha256", "dist/app.cdx.json"} {
				_, err := os.Stat(filepath.Join(box.root, signed+".asc"))

				switch {
				case test.refused != "" && err == nil:
					t.Errorf("a refused step wrote %s.asc", signed)
				case test.refused == "" && err != nil:
					t.Errorf("the step wrote no %s.asc: %v", signed, err)
				case test.refused == "":
					key.VerifyDetachedAt(t, filepath.Join(box.root, signed), committed)
				}

				if _, err := os.Stat(filepath.Join(box.root, signed+".bundle")); err == nil {
					t.Errorf("the GPG step wrote %s.bundle", signed)
				}
			}
		})
	}
}

// The sign job's read step stops on any answer but gpg or cosign, and on
// none, so neither signing step is skipped without a word.
func TestReleaseFileSigningReadStepRefusesAnotherAnswer(t *testing.T) {
	t.Parallel()

	run := workflowStep(t, "sign-and-publish-release.yml", "sign", "Read the release file signing method").Run

	for answer, signs := range map[string]bool{
		"release-file-signing=gpg":      true,
		"release-file-signing=cosign":   true,
		"release-file-signing=sigstore": false,
		"release-file-signing=":         false,
		"":                              false,
	} {
		box := newSandbox(t, "release-file-signing")

		got := box.run(run, "HELPERS_DIR="+box.root, "CONSUMER_WORKSPACE="+box.root, "ARTIFACTS_CONFIG=", "FAKE_ENGINE_OUTPUT="+answer)
		if (got.status == 0) != signs {
			t.Errorf("answer %q: status %d, want success %t\nstderr %s", answer, got.status, signs, got.stderr)
		}

		if argv := box.argv(); len(argv) < 2 || argv[0] != "plan" || argv[1] != "release-file-signing" {
			t.Errorf("answer %q: the step ran the engine with %q", answer, argv)
		}
	}
}
