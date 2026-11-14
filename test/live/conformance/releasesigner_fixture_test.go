// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package conformance_test

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/diggsweden/forge-ci/internal/testutil/evidence"
)

// releaseSignerTrust is the allowed-signers evidence one release is judged
// against: a file whose only entry is the expected key, and a control file
// whose only entry is a key that signed nothing.
type releaseSignerTrust struct {
	allowed, foreign string
	publicKey        string
}

// releaseSignerFindings asks git which key signed a release commit and its tag
// and returns every way the answer differs from the expected key.
//
// An SSH signature carries a key and no name: the principal git prints is the
// one the allowed-signers file gives that key, so it is the file's claim and
// is not compared. The key is. The commit's signature must be good and made
// with the expected key's fingerprint, the tag must verify against the file
// that holds that key alone, and both must fail against the control file: a
// check that passes against a key that signed nothing has verified nothing.
func releaseSignerFindings(git func(args ...string) (string, error), trust releaseSignerTrust, commit, tag string) []string {
	// A public key line is its type, the key in base64 and a comment.
	fields := strings.Fields(trust.publicKey)
	if len(fields) < 2 {
		return []string{"the expected signing key is not an SSH public key line"}
	}

	wire, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return []string{"the expected signing key is not an SSH public key: " + err.Error()}
	}

	key, err := ssh.ParsePublicKey(wire)
	if err != nil {
		return []string{"the expected signing key is not an SSH public key: " + err.Error()}
	}

	with := func(signers string, args ...string) (string, error) {
		return git(append([]string{"-c", "gpg.format=ssh", "-c", "gpg.ssh.allowedSignersFile=" + signers}, args...)...)
	}
	want := "G|" + ssh.FingerprintSHA256(key)

	var findings []string

	if got, err := with(trust.allowed, "log", "-1", "--format=%G?|%GK", commit); err != nil || got != want {
		findings = append(findings, "the release commit's signature is "+got+", want "+want)
	}

	if _, err := with(trust.allowed, "verify-tag", "refs/tags/"+tag); err != nil {
		findings = append(findings, tag+" is not a tag signed by the expected key: "+err.Error())
	}

	if _, err := with(trust.foreign, "verify-commit", commit); err == nil {
		findings = append(findings, "the release commit verifies against a key that did not sign it")
	}

	if _, err := with(trust.foreign, "verify-tag", "refs/tags/"+tag); err == nil {
		findings = append(findings, tag+" verifies against a key that did not sign it")
	}

	return findings
}

// The oracle against real git and real SSH signatures: it accepts the signer
// it was told to expect and names each way another one differs.
func TestReleaseSignerFindingsNameTheWrongSigner(t *testing.T) {
	t.Parallel()

	gitBinary := evidence.RequireTool(t, "git")
	sshKeygen := evidence.RequireTool(t, "ssh-keygen")

	dir := t.TempDir()
	run := func(binary string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()

		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = dir
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@invalid.example",
			"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@invalid.example",
		}

		out, err := cmd.CombinedOutput()

		return strings.TrimSpace(string(out)), err
	}
	must := func(binary string, args ...string) string {
		out, err := run(binary, args...)
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", binary, args, err, out)
		}

		return out
	}
	key := func(name string) (string, string) {
		path := filepath.Join(dir, name)
		must(sshKeygen, "-q", "-t", "ed25519", "-N", "", "-C", name, "-f", path)

		public, err := os.ReadFile(path + ".pub")
		if err != nil {
			t.Fatal(err)
		}

		return path, strings.TrimSpace(string(public))
	}
	signers := func(name, principal, public string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(principal+" "+public+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		return path
	}

	const (
		bot = "bot@invalid.example"
		tag = "v0.0.1"
	)

	botKey, botPublic := key("bot")
	_, otherPublic := key("other")

	must(gitBinary, "init", "-q", "-b", "main")
	must(gitBinary, "-c", "gpg.format=ssh", "-c", "user.signingkey="+botKey, "commit", "-q", "--allow-empty", "-S", "-m", "release")
	must(gitBinary, "-c", "gpg.format=ssh", "-c", "user.signingkey="+botKey, "tag", "-s", "-m", "release", tag)

	git := func(args ...string) (string, error) { return run(gitBinary, args...) }
	allowed, foreign := signers("allowed", bot, botPublic), signers("foreign", bot, otherPublic)

	if findings := releaseSignerFindings(git, releaseSignerTrust{allowed: allowed, foreign: foreign, publicKey: botPublic}, "HEAD", tag); len(findings) != 0 {
		t.Errorf("the bot's own signatures are refused: %v", findings)
	}

	for name, wrong := range map[string]releaseSignerTrust{
		"another key is trusted and expected":      {allowed: foreign, foreign: foreign, publicKey: otherPublic},
		"the control trusts the signing key":       {allowed: allowed, foreign: allowed, publicKey: botPublic},
		"the signing key is trusted, not expected": {allowed: allowed, foreign: foreign, publicKey: otherPublic},
	} {
		findings := releaseSignerFindings(git, wrong, "HEAD", tag)
		if want := map[string]int{"another key is trusted and expected": 2, "the control trusts the signing key": 2, "the signing key is trusted, not expected": 1}[name]; len(findings) != want {
			t.Errorf("%s: findings are %v, want %d", name, findings, want)
		}
	}

	if findings := releaseSignerFindings(git, releaseSignerTrust{publicKey: "not a key"}, "HEAD", tag); len(findings) != 1 {
		t.Errorf("an unreadable expected key gives %v, want one finding", findings)
	}
}
