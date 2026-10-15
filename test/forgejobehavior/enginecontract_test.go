// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// Every forge-ci command the Forgejo workflows, release scripts and actions,
// the GitHub workflows and the GitLab components and release scripts call
// exists in the engine, and its help declares every long option the call passes.
// The engine is the interface oracle, so a renamed verb or option fails here
// instead of inside a release or signing job.
func TestForgeCICLIContract(t *testing.T) {
	t.Parallel()
	engine := forgeCIEngine(t)
	helps := map[string]string{}
	help := func(verbs string) (string, bool) {
		if text, ok := helps[verbs]; ok {
			return text, text != "\x00unknown"
		}

		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()

		command := exec.CommandContext(ctx, engine, append(strings.Fields(verbs), "--help")...) //nolint:gosec // G204: the engine built from this tree, with verbs read from this repository's own files.
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "LANG=C", "LC_ALL=C"}

		out, err := command.CombinedOutput()
		if err != nil {
			helps[verbs] = "\x00unknown"

			return "", false
		}

		helps[verbs] = string(out)

		return string(out), true
	}

	for _, invocation := range forgeCIInvocations(t) {
		verbs := invocation.verbs
		text, known := help(verbs)

		// A word after a command that has no subcommands is its argument, as in
		// `container ledger merge ledgers`. A group lists COMMANDS, so a
		// renamed verb under one still fails.
		for cut := strings.LastIndex(verbs, " "); !known && cut > 0; cut = strings.LastIndex(verbs, " ") {
			verbs = verbs[:cut]
			text, known = help(verbs)
			known = known && !strings.Contains(text, "\nCOMMANDS:")
		}

		if !known {
			t.Errorf("%s calls forge-ci %s, which the engine does not know", invocation.file, invocation.verbs)

			continue
		}

		for _, flag := range invocation.flags {
			declared := regexp.MustCompile(`(?m)(^|[\s,])--?` + regexp.QuoteMeta(strings.TrimPrefix(flag, "--")) + `([\s=,]|$)`)
			if flag != "--help" && !declared.MatchString(text) {
				t.Errorf("%s passes %s to forge-ci %s, which its help does not declare", invocation.file, flag, invocation.verbs)
			}
		}
	}
}

// The consumer kit, pinned to one forge-ci commit, passes the release
// isolation gate a consumer pull request runs: the check-l3-isolation step
// runs the engine over the kit's job names with the action's default signing
// secrets. With this checkout's sign-and-publish workflow placed as the pinned
// sibling, the engine also checks that its secrets-contract header lists
// every enforced signing secret. The subject is the repository the kit's
// `uses:` lines name, as the runner reports it for a fetched action, and the
// kit's forge-ci-sha inputs are the pin the action names to the engine.
// The kit is served here from a repository that is not called forge-ci,
// as a renamed fork or a lab snapshot is.
func TestConsumerTemplatesPassReleaseIsolation(t *testing.T) {
	t.Parallel()
	action := loadAction(t, "check-l3-isolation")
	s := newSandbox(t, "check-l3-isolation")
	s.write(".forgejo/bin/lib-forge-ci.sh", string(reporoot.ReadFile(t, ".forgejo/bin/lib-forge-ci.sh")), 0o600)
	// A fetched action comes with its layer checkout. The gate reads its actual
	// declarations rather than a consumer-maintained copy of the input lists.
	for _, pattern := range []string{".forgejo/actions/*/action.yml", ".forgejo/workflows/*.yml"} {
		files, err := filepath.Glob(filepath.Join(reporoot.Path(t), pattern))
		if err != nil {
			t.Fatal(err)
		}

		for _, file := range files {
			relative, err := filepath.Rel(reporoot.Path(t), file)
			if err != nil {
				t.Fatal(err)
			}

			s.write(filepath.ToSlash(relative), string(reporoot.ReadFile(t, relative)), 0o600)
		}
	}

	const pin = "0123456789abcdef0123456789abcdef01234567"

	// The placeholders that precede the two digests are left as they are in
	// the first pass below: only a filled-in kit passes.
	coordinates := strings.NewReplacer("PIN_FORGE_CI_SHA", pin, "OWNER/forge-ci", "fork/delivery-0a1b", "FORGE_HOST", "forge.example")
	instantiate := strings.NewReplacer("PIN_FORGE_CI_SHA", pin, "OWNER/forge-ci", "fork/delivery-0a1b", "FORGE_HOST", "forge.example",
		"PIN_SIGNER_IMAGE_DIGEST", strings.Repeat("a", 64), "PIN_COSIGN_PUBLIC_KEY_SHA256", strings.Repeat("b", 64))

	for _, template := range consumerTemplates() {
		s.write("consumer/.forgejo/workflows/"+filepath.Base(template), instantiate.Replace(string(reporoot.ReadFile(t, template))), 0o600)
	}
	// The engine resolves the sign job's pinned workflow to this sibling path.
	s.write("delivery-0a1b/.forgejo/workflows/sign-and-publish-release.yml",
		string(reporoot.ReadFile(t, ".forgejo/workflows/sign-and-publish-release.yml")), 0o600)

	// The resolver runs the built engine as a non-strict override.
	script := action.script(t, action.Runs.Steps[0].Name)

	got := s.run(script,
		"FORGE_CI_BIN="+forgeCIEngine(t), "GITHUB_ACTION_REPOSITORY=fork/delivery-0a1b",
		"ISOLATION_WORKFLOW=consumer/.forgejo/workflows/release.yml",
		"ISOLATION_BUILD_JOB=build", "ISOLATION_SIGN_JOB=sign-and-publish", "ISOLATION_PREPARE_JOB=prepare",
		"ISOLATION_DIST_DIGEST_OUTPUT=dist-digest", "ISOLATION_SIGNING_SECRETS="+fmt.Sprint(action.Inputs["signing-secrets"].Default))
	if got.status != 0 {
		t.Fatalf("status %d\nstdout %s\nstderr %s", got.status, got.stdout, got.stderr)
	}
	// The engine's only report that it read the sibling workflow.
	if !strings.Contains(got.stdout+got.stderr, "Local sibling header/input-reference hints checked") {
		t.Fatalf("the engine did not check the sibling sign-and-publish workflow:\n%s%s", got.stdout, got.stderr)
	}

	// The helper pin is held to the same commit although the serving
	// repository's name says nothing about the input: a kit whose second pin
	// names another commit is refused.
	drifted := strings.Replace(instantiate.Replace(string(reporoot.ReadFile(t, consumerTemplates()[0]))),
		"forge-ci-sha: "+pin, "forge-ci-sha: "+strings.Repeat("f", 40), 1)
	s.write("consumer/.forgejo/workflows/release.yml", drifted, 0o600)

	refused := s.run(script,
		"FORGE_CI_BIN="+forgeCIEngine(t), "GITHUB_ACTION_REPOSITORY=fork/delivery-0a1b",
		"ISOLATION_WORKFLOW=consumer/.forgejo/workflows/release.yml",
		"ISOLATION_BUILD_JOB=build", "ISOLATION_SIGN_JOB=sign-and-publish", "ISOLATION_PREPARE_JOB=prepare",
		"ISOLATION_DIST_DIGEST_OUTPUT=dist-digest", "ISOLATION_SIGNING_SECRETS="+fmt.Sprint(action.Inputs["signing-secrets"].Default))
	if refused.status == 0 || !strings.Contains(refused.stdout+refused.stderr, "mixed fork/delivery-0a1b pins") {
		t.Fatalf("a helper pin on another commit was not refused: status %d\n%s%s", refused.status, refused.stdout, refused.stderr)
	}

	// The signer image and the key digest are the trust root, so the gate
	// holds them to literals of their shape: the kit as copied, with the two
	// placeholders still in it, is refused, and so is a digest that would come
	// from a variable.
	release := string(reporoot.ReadFile(t, consumerTemplates()[0]))
	for name, kit := range map[string]struct{ workflow, want string }{
		"unfilled placeholders": {coordinates.Replace(release), "passes signer-image to fork/delivery-0a1b as a value that is not an image reference pinned by digest"},
		"digest from a variable": {
			strings.Replace(instantiate.Replace(release), "cosign-public-key-sha256: "+strings.Repeat("b", 64), "cosign-public-key-sha256: ${{ vars.COSIGN_PUBLIC_KEY_SHA256 }}", 1),
			"passes cosign-public-key-sha256 to fork/delivery-0a1b as a value that is an expression",
		},
		"signer image from a variable": {
			strings.Replace(instantiate.Replace(release), "signer-image: forge.example/fork/delivery-0a1b-signer@sha256:"+strings.Repeat("a", 64), "signer-image: ${{ vars.SIGNER_IMAGE }}", 1),
			"passes signer-image to fork/delivery-0a1b as a value that is an expression",
		},
	} {
		s.write("consumer/.forgejo/workflows/release.yml", kit.workflow, 0o600)

		refused := s.run(script,
			"FORGE_CI_BIN="+forgeCIEngine(t), "GITHUB_ACTION_REPOSITORY=fork/delivery-0a1b",
			"ISOLATION_WORKFLOW=consumer/.forgejo/workflows/release.yml",
			"ISOLATION_BUILD_JOB=build", "ISOLATION_SIGN_JOB=sign-and-publish", "ISOLATION_PREPARE_JOB=prepare",
			"ISOLATION_DIST_DIGEST_OUTPUT=dist-digest", "ISOLATION_SIGNING_SECRETS="+fmt.Sprint(action.Inputs["signing-secrets"].Default))
		if refused.status == 0 || !strings.Contains(refused.stdout+refused.stderr, kit.want) {
			t.Fatalf("%s: not refused with %q: status %d\n%s%s", name, kit.want, refused.status, refused.stdout, refused.stderr)
		}
	}
}

// The signing-key binding accepts the caller's trust root only as a whole: the
// engine checks the key the step wrote against the caller's digest, and the
// signing key must still derive that key. The helper checkout holds no key,
// so half a pair and no pair at all are refused, not replaced. A stub cosign
// derives the fixture public key from any signing key.
func TestCallerCosignTrustRootThroughTheEngine(t *testing.T) {
	t.Parallel()

	// A P-256 public key generated for this test; its private half was never
	// kept, and nothing signs with it.
	const key = "-----BEGIN PUBLIC KEY-----\n" +
		"MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAERMtaO8d1uC54af1/sOk5mldX9s5j\n" +
		"Tn+jHR0RAsfCFF4OCNnzck4neIbQPzuQfXmvROOdLKt9xqqkakLf7rH5aQ==\n" +
		"-----END PUBLIC KEY-----\n"

	keyDigest := sha256.Sum256([]byte(key))
	digest := hex.EncodeToString(keyDigest[:])
	other := "-----BEGIN PUBLIC KEY-----\nb3RoZXI=\n-----END PUBLIC KEY-----\n"
	otherDigest := sha256.Sum256([]byte(other))
	selected := workflowStep(t, "sign-and-publish-release.yml", "sign", "Bind Cosign signing key to trusted public key")

	for _, test := range []struct {
		name, key, digest string
		accepted          bool
	}{
		{"the matching key and its digest", strings.TrimSuffix(key, "\n"), digest, true},
		{"a digest of another file", key, hex.EncodeToString(otherDigest[:]), false},
		{"a key without a digest", key, "", false},
		{"a digest without a key", "", digest, false},
		{"another key with its own digest", other, hex.EncodeToString(otherDigest[:]), false},
		{"no key anywhere", "", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			s := newSandbox(t, "trust-root")
			s.write(".forgejo/bin/lib-forge-ci.sh", string(reporoot.ReadFile(t, ".forgejo/bin/lib-forge-ci.sh")), 0o600)
			s.write("tools/cosign", "#!/usr/bin/env bash\ncat -- '"+s.write("derived.pub", key, 0o600)+"'\n", 0o700)
			private := filepath.Join(s.root, "private")

			if err := os.Mkdir(private, 0o700); err != nil {
				t.Fatal(err)
			}

			got := s.run(selected.Run,
				"FORGE_CI_BIN="+forgeCIEngine(t), "PATH="+filepath.Join(s.root, "tools")+":"+os.Getenv("PATH"),
				"HELPERS_DIR="+s.root, "SIGNER_WORKSPACE="+private,
				"TRUSTED_KEY_INPUT="+test.key, "TRUSTED_KEY_SHA256_INPUT="+test.digest,
				"COSIGN_KEY=fixture-key", "COSIGN_PASSWORD=fixture-password")
			if (got.status == 0) != test.accepted {
				t.Fatalf("status %d, accepted want %t\nstdout %s\nstderr %s", got.status, test.accepted, got.stdout, got.stderr)
			}
		})
	}
}
