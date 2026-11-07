// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package releasesetup_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/diggsweden/forge-ci/internal/app/releasesetup"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/sshkey"
)

func writeKeys(t *testing.T, dir string) (pubPath, privPath, pubLine string) {
	t.Helper()

	priv, signer := sshkey.New(t)
	pubLine = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	pubPath = filepath.Join(dir, "id.pub")
	privPath = filepath.Join(dir, "id")

	if err := os.WriteFile(pubPath, []byte(pubLine+" someone@laptop\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(privPath, []byte(priv), 0o600); err != nil {
		t.Fatal(err)
	}

	return pubPath, privPath, pubLine
}

func TestAddSignerCreatesAppendsAndIsIdempotent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	pub, priv, line := writeKeys(t, t.TempDir())

	first, err := releasesetup.AddSigner(releasesetup.SignerInput{Root: root, Principal: "you@example.org", PublicKeyPath: pub})
	if err != nil {
		t.Fatal(err)
	}

	if !first.Created || first.File != releasesetup.SignersFile || first.Line != "you@example.org "+line {
		t.Fatalf("first = %+v", first)
	}

	again, err := releasesetup.AddSigner(releasesetup.SignerInput{Root: root, Principal: "you@example.org", PublicKeyPath: pub})
	if err != nil || !again.Present {
		t.Fatalf("again = %+v, %v; want Present", again, err)
	}

	if _, err := releasesetup.AddSigner(releasesetup.SignerInput{Root: root, Principal: "bot@example.org", PublicKeyPath: pub}); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(filepath.Join(root, releasesetup.SignersFile))
	if err != nil {
		t.Fatal(err)
	}

	if strings.Count(string(body), line) != 2 || !strings.HasPrefix(string(body), "# ") {
		t.Errorf("file =\n%s", body)
	}

	if _, err := releasesetup.AddSigner(releasesetup.SignerInput{Root: root, Principal: "you@example.org", PublicKeyPath: priv}); !errors.Is(err, errs.ErrUsage) ||
		!strings.Contains(err.Error(), "private key") {
		t.Errorf("private key: err = %v, want a usage refusal naming it", err)
	}

	if _, err := releasesetup.AddSigner(releasesetup.SignerInput{Root: root, Principal: "two words", PublicKeyPath: pub}); !errors.Is(err, errs.ErrUsage) {
		t.Errorf("bad principal: err = %v", err)
	}
}

func TestAddSignerDryRunWritesNothingAndForgejoDefault(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".forgejo", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}

	pub, _, line := writeKeys(t, t.TempDir())

	got, err := releasesetup.AddSigner(releasesetup.SignerInput{Root: root, Principal: "you@example.org", PublicKeyPath: pub, DryRun: true})
	if err != nil || got.Line != "you@example.org "+line || got.File != releasesetup.ForgejoSignersFile {
		t.Fatalf("dry run = %+v, %v", got, err)
	}

	if _, err := os.Stat(filepath.Join(root, releasesetup.ForgejoSignersFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("dry run wrote the file: %v", err)
	}
}

type fakeGit struct {
	config map[string]string
	tags   map[string]bool
}

func (g fakeGit) Output(_ context.Context, args ...string) (string, error) {
	if len(args) == 3 && args[0] == "config" && args[1] == "--get" {
		if v, ok := g.config[args[2]]; ok {
			return v + "\n", nil
		}
	}

	return "", errors.New("exit status 1")
}

func (g fakeGit) TagExists(_ context.Context, tag string) (bool, error) { return g.tags[tag], nil }

func TestPlanRequestChecksSigningAndTags(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	pub, priv, _ := writeKeys(t, t.TempDir())

	git := fakeGit{config: map[string]string{"gpg.format": "ssh", "user.signingkey": priv}, tags: map[string]bool{}}

	plan, err := releasesetup.PlanRequest(context.Background(), releasesetup.RequestInput{Root: root, Version: "1.2.3", Git: git})
	if err != nil {
		t.Fatal(err)
	}

	if plan.Tag != "release-request/v1.2.3" || plan.Ready() {
		t.Fatalf("before the signer is listed: plan = %+v", plan)
	}

	if plan.Commands[0] != `git tag -s release-request/v1.2.3 -m "Release v1.2.3"` || plan.Commands[1] != "git push origin release-request/v1.2.3" {
		t.Errorf("commands = %q", plan.Commands)
	}

	if _, err := releasesetup.AddSigner(releasesetup.SignerInput{Root: root, Principal: "you@example.org", PublicKeyPath: pub}); err != nil {
		t.Fatal(err)
	}

	plan, err = releasesetup.PlanRequest(context.Background(), releasesetup.RequestInput{Root: root, Version: "v1.2.3", Git: git})
	if err != nil || !plan.Ready() {
		t.Fatalf("with the signer listed: plan = %+v, %v", plan, err)
	}

	git.tags["v1.2.3"] = true

	plan, _ = releasesetup.PlanRequest(context.Background(), releasesetup.RequestInput{Root: root, Version: "v1.2.3", Git: git})
	if plan.Ready() {
		t.Error("an existing release tag must block the request")
	}

	if _, err := releasesetup.PlanRequest(context.Background(), releasesetup.RequestInput{Root: root, Version: "1.2", Git: git}); !errors.Is(err, errs.ErrUsage) {
		t.Errorf("bad version: err = %v", err)
	}
}

func TestPlanRequestWithoutSSHSigning(t *testing.T) {
	t.Parallel()

	plan, err := releasesetup.PlanRequest(context.Background(), releasesetup.RequestInput{
		Root: t.TempDir(), Version: "v0.1.0", Git: fakeGit{config: map[string]string{}, tags: map[string]bool{}},
	})
	if err != nil {
		t.Fatal(err)
	}

	fixes := ""

	for _, c := range plan.Checks {
		if !c.OK {
			fixes += c.Fix + "\n"
		}
	}

	if !strings.Contains(fixes, "git config gpg.format ssh") || !strings.Contains(fixes, "git config user.signingkey") {
		t.Errorf("fixes = %q", fixes)
	}
}

func TestChecklistFollowsTheRepository(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	steps, err := releasesetup.Checklist(t.Context(), root, "diggsweden/forge-ci", "")
	if err != nil {
		t.Fatal(err)
	}

	if len(steps) != 8 || steps[0].State != releasesetup.StepTodo || !strings.Contains(steps[0].Command, "forge-ci init") {
		t.Fatalf("empty repository: steps = %+v", steps)
	}

	write := func(rel, body string) {
		t.Helper()

		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(".forge-ci/artifacts.yml", "artifacts:\n  - name: widget\n    project-type: meta\n")
	write(".github/workflows/release.yml", "on: push\njobs: {}\n")

	steps, err = releasesetup.Checklist(t.Context(), root, "diggsweden/forge-ci", "")
	if err != nil {
		t.Fatal(err)
	}

	for i, want := range []releasesetup.StepState{releasesetup.StepDone, releasesetup.StepDone, releasesetup.StepTodo, releasesetup.StepManual, releasesetup.StepDone} {
		if steps[i].State != want {
			t.Errorf("step %d %q = %s, want %s", i+1, steps[i].Title, steps[i].State, want)
		}
	}

	// What blocks a merge is the person's decision, stated before the first
	// pull request rather than found out after a red test merged green.
	if s := steps[5]; s.State != releasesetup.StepManual || !strings.Contains(s.Detail, "blocks") || !strings.Contains(s.Command, "admin-policy") {
		t.Errorf("enforcement step = %+v", s)
	}
}

// A principal is a pattern list in allowed_signers: "*" would trust the key
// for every signer, and a leading "#" would make the line a comment.
func TestAddSignerRefusesPatternPrincipals(t *testing.T) {
	t.Parallel()

	pub, _, _ := writeKeys(t, t.TempDir())

	for _, principal := range []string{"*", "*@example.org", "a?@example.org", "!me@example.org", "#me", "a,b", "me @x"} {
		_, err := releasesetup.AddSigner(releasesetup.SignerInput{Root: t.TempDir(), Principal: principal, PublicKeyPath: pub})
		if !errors.Is(err, errs.ErrUsage) {
			t.Errorf("principal %q: err = %v, want a usage error", principal, err)
		}
	}
}

// The key is matched as a parsed key, not a substring: a line whose comment
// quotes the key does not list it, and a tab separator still does.
func TestAddSignerMatchesKeysExactly(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	pub, _, line := writeKeys(t, t.TempDir())
	_, _, other := writeKeys(t, t.TempDir())
	file := filepath.Join(root, ".forge-ci", "allowed_signers")

	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(file, []byte("me@example.org "+other+" was "+line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := releasesetup.AddSigner(releasesetup.SignerInput{Root: root, Principal: "me@example.org", PublicKeyPath: pub})
	if err != nil || result.Present {
		t.Fatalf("a key quoted in another line's comment counted as listed: %+v, %v", result, err)
	}

	if err := os.WriteFile(file, []byte("me@example.org\t"+line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if result, err := releasesetup.AddSigner(releasesetup.SignerInput{Root: root, Principal: "me@example.org", PublicKeyPath: pub}); err != nil || !result.Present {
		t.Fatalf("a tab-separated entry was not found: %+v, %v", result, err)
	}
}

// A malformed allowlist is left as it is: appending to it would change the
// file and leave it broken.
func TestAddSignerLeavesAMalformedFileAlone(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	pub, _, _ := writeKeys(t, t.TempDir())
	file := filepath.Join(root, ".forge-ci", "allowed_signers")
	broken := "me@example.org not-a-key\n"

	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(file, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := releasesetup.AddSigner(releasesetup.SignerInput{Root: root, Principal: "you@example.org", PublicKeyPath: pub}); err == nil {
		t.Fatal("a malformed allowlist was appended to")
	}

	if body, _ := os.ReadFile(file); string(body) != broken {
		t.Errorf("file changed to %q", body)
	}
}

// The version rule is the release's own: a prerelease is a valid request
// (its release-policy decides), build metadata and a missing patch are not.
func TestPlanRequestUsesTheReleaseTagRule(t *testing.T) {
	t.Parallel()

	git := fakeGit{config: map[string]string{}, tags: map[string]bool{}}

	if plan, err := releasesetup.PlanRequest(t.Context(), releasesetup.RequestInput{Root: t.TempDir(), Version: "1.2.3-rc.1", Git: git}); err != nil || plan.Tag != "release-request/v1.2.3-rc.1" {
		t.Errorf("prerelease: %+v, %v", plan, err)
	}

	for _, v := range []string{"v1.2", "v1.2.3+build", "v1.2.3; rm -rf /"} {
		if _, err := releasesetup.PlanRequest(t.Context(), releasesetup.RequestInput{Root: t.TempDir(), Version: v, Git: git}); err == nil {
			t.Errorf("version %q was accepted", v)
		}
	}
}

// A policy the gates refuse is a step to do, pointing at doctor, not an
// error that hides the rest of the checklist.
func TestChecklistShowsARefusedPolicyAsAStep(t *testing.T) {
	t.Setenv("CI_ENFORCE_LINT", "true") // not parallel: changes the process environment

	steps, err := releasesetup.Checklist(t.Context(), t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}

	if s := steps[5]; s.State != releasesetup.StepTodo || !strings.Contains(s.Detail, "CI_ENFORCE_LINT") || s.Command != "forge-ci doctor" {
		t.Errorf("enforcement step = %+v", s)
	}
}
