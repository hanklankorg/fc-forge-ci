// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// The shipped release fixtures of the Go CLI demo are the variants the hosted
// trial runs; each must read as the profile its name promises, so the oracle
// judges the variant and not the ordinary release.
func TestGitHubReleaseProfile_ShippedGoCLIVariants(t *testing.T) {
	consumer := filepath.Join(reporoot.Path(t), "test/live/fixtures/consumers/hello-go-cli/consumer")

	for _, tc := range []struct {
		fixtures, workflow string
		want               githubReleaseProfile
	}{
		{"test/live/github", "release.yml", githubReleaseProfile{GitSigning: config.GitSignSSH, Changelog: true, Prepares: true, SBOMBundle: sbomBundleOffered}},
		{"test/live/github-gpg", "release-gpg.yml", githubReleaseProfile{GitSigning: config.GitSignGPG, Changelog: true, Prepares: true, SBOMBundle: sbomBundleOffered}},
		{"test/live/github-no-sboms", "release-no-sboms.yml", githubReleaseProfile{GitSigning: config.GitSignGPG, Changelog: true, Prepares: true, SBOMBundle: sbomBundleWithheld}},
		{"test/live/github-no-changelog", "release-no-changelog.yml", githubReleaseProfile{GitSigning: config.GitSignGPG, Prepares: false, SBOMBundle: sbomBundleOffered}},
	} {
		t.Run(tc.workflow, func(t *testing.T) {
			dir := t.TempDir()

			for from, to := range map[string]string{
				filepath.Join(tc.fixtures, "workflows", tc.workflow): filepath.Join(".github/workflows", tc.workflow),
				".forge-ci/artifacts.yml":                            ".forge-ci/artifacts.yml",
				".forge-ci/artifacts-gpg.yml":                        ".forge-ci/artifacts-gpg.yml",
			} {
				body, err := os.ReadFile(filepath.Join(consumer, from))
				if err != nil {
					t.Fatal(err)
				}

				writeProfileFile(t, dir, to, string(body))
			}

			got, err := githubReleaseProfileOf(dir, tc.workflow)
			if err != nil || got != tc.want {
				t.Fatalf("profile = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

// A meta release offers no file only while it attaches none: the meta demo
// with one attachment is judged as any release that offers files.
func TestGitHubReleaseProfile_AMetaReleaseThatAttachesOffersFiles(t *testing.T) {
	consumer := filepath.Join(reporoot.Path(t), "test/live/fixtures/consumers/hello-meta/consumer")
	dir := t.TempDir()

	workflow, err := os.ReadFile(filepath.Join(consumer, "test/live/github/workflows/release.yml"))
	if err != nil {
		t.Fatal(err)
	}

	declaration, err := os.ReadFile(filepath.Join(consumer, ".forge-ci/artifacts.yml"))
	if err != nil {
		t.Fatal(err)
	}

	writeProfileFile(t, dir, ".forge-ci/artifacts.yml", string(declaration))

	attached := strings.Replace(string(workflow), "      release.draft: false\n", "      release.draft: false\n      release.attachartifacts: install.sh\n", 1)
	writeProfileFile(t, dir, ".github/workflows/release.yml", attached)

	if got, err := githubReleaseProfileOf(dir, "release.yml"); err != nil || got.NoFiles {
		t.Fatalf("a meta release that attaches a file: %+v, %v", got, err)
	}
}

func TestGitHubReleaseProfile_Inputs(t *testing.T) {
	const declared = "artifacts:\n  - name: app\n    project-type: go\n    working-directory: .\n"

	workflow := func(inputs string) string {
		return "jobs:\n  release:\n    uses: example/forge-ci/.github/workflows/release-orchestrator.yml@0000000000000000000000000000000000000000\n    with:\n      branch: main\n" + inputs
	}

	for name, tc := range map[string]struct {
		workflow, config string
		want             githubReleaseProfile
	}{
		"orchestrator defaults":   {workflow(""), declared, githubReleaseProfile{GitSigning: config.GitSignGPG, Changelog: true, Prepares: true, SBOMBundle: sbomBundleOffered}},
		"no changelog creator":    {workflow("      changelog-creator: \"\"\n"), declared, githubReleaseProfile{GitSigning: config.GitSignGPG, SBOMBundle: sbomBundleOffered}},
		"null changelog creator":  {workflow("      changelog-creator:\n"), declared, githubReleaseProfile{GitSigning: config.GitSignGPG, SBOMBundle: sbomBundleOffered}},
		"version bump skipped":    {workflow("      changelog.skipversionbump: true\n"), declared, githubReleaseProfile{GitSigning: config.GitSignGPG, Changelog: true, SBOMBundle: sbomBundleOffered}},
		"version bump kept":       {workflow("      changelog.skipversionbump: 'false'\n"), declared, githubReleaseProfile{GitSigning: config.GitSignGPG, Changelog: true, Prepares: true, SBOMBundle: sbomBundleOffered}},
		"release sboms none":      {workflow("      release.sboms: none\n"), declared, githubReleaseProfile{GitSigning: config.GitSignGPG, Changelog: true, Prepares: true, SBOMBundle: sbomBundleWithheld}},
		"artifact sboms none":     {workflow(""), declared + "    sboms: none\n", githubReleaseProfile{GitSigning: config.GitSignGPG, Changelog: true, Prepares: true, SBOMBundle: sbomBundleWithheld}},
		"disjoint layers":         {workflow("      release.sboms: analyzed-artifact\n"), declared + "    sboms: build\n", githubReleaseProfile{GitSigning: config.GitSignGPG, Changelog: true, Prepares: true, SBOMBundle: sbomBundleWithheld}},
		"shared build layer":      {workflow("      release.sboms: build\n"), declared, githubReleaseProfile{GitSigning: config.GitSignGPG, Changelog: true, Prepares: true, SBOMBundle: sbomBundleOffered}},
		"container layer only":    {workflow("      release.sboms: analyzed-container\n"), declared, githubReleaseProfile{GitSigning: config.GitSignGPG, Changelog: true, Prepares: true, SBOMBundle: sbomBundleUnjudged}},
		"ssh git signing":         {workflow(""), declared + "git-signing:\n  method: ssh\n", githubReleaseProfile{GitSigning: config.GitSignSSH, Changelog: true, Prepares: true, SBOMBundle: sbomBundleOffered}},
		"derived, no declaration": {workflow(""), "", githubReleaseProfile{GitSigning: config.GitSignGPG, Changelog: true, Prepares: true, SBOMBundle: sbomBundleUnjudged}},
		"derived, sboms none":     {workflow("      release.sboms: none\n"), "", githubReleaseProfile{GitSigning: config.GitSignGPG, Changelog: true, Prepares: true, SBOMBundle: sbomBundleWithheld}},
		"no orchestrator call":    {"jobs:\n  release:\n    runs-on: ubuntu-26.04\n    steps:\n      - run: true\n", declared, githubReleaseProfile{GitSigning: config.GitSignGPG, Changelog: true, Prepares: true, SBOMBundle: sbomBundleOffered}},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeProfileFile(t, dir, ".github/workflows/release.yml", tc.workflow)

			if tc.config != "" {
				writeProfileFile(t, dir, ".forge-ci/artifacts.yml", tc.config)
			}

			got, err := githubReleaseProfileOf(dir, "release.yml")
			if err != nil || got != tc.want {
				t.Fatalf("profile = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}

	for name, body := range map[string]string{
		"expression":      workflow("      release.sboms: ${{ vars.SBOMS }}\n"),
		"skip expression": workflow("      changelog.skipversionbump: ${{ inputs.skip }}\n"),
		"skip typo":       workflow("      changelog.skipversionbump: yes\n"),
		"skip empty":      workflow("      changelog.skipversionbump: ''\n"),
		"skip null":       workflow("      changelog.skipversionbump:\n"),
		"creator typo":    workflow("      changelog-creator: git-clif\n"),
		"creator other":   workflow("      changelog-creator: git-chglog\n"),
		"creator blank":   workflow("      changelog-creator: ' '\n"),
		"unknown layer":   workflow("      release.sboms: everything\n"),
		"two orchestrate": workflow("") + "  again:\n    uses: example/forge-ci/.github/workflows/release-orchestrator.yml@0000000000000000000000000000000000000000\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeProfileFile(t, dir, ".github/workflows/release.yml", body)
			writeProfileFile(t, dir, ".forge-ci/artifacts.yml", declared)

			if got, err := githubReleaseProfileOf(dir, "release.yml"); !errors.Is(err, errs.ErrValidation) {
				t.Fatalf("an input the oracle cannot judge is read as %+v, %v", got, err)
			}
		})
	}
}

func TestReleaseSBOMBundleGap(t *testing.T) {
	with := map[string]int64{"my-cli-linux-amd64": 1, "app-0.1.0-sboms.zip": 2, "app-0.1.0-sboms.zip.bundle": 3}
	without := map[string]int64{"my-cli-linux-amd64": 1, "checksums.sha256": 2}

	for name, tc := range map[string]struct {
		expect sbomBundle
		assets map[string]int64
		gap    bool
	}{
		"offered and present":  {sbomBundleOffered, with, false},
		"offered but absent":   {sbomBundleOffered, without, true},
		"offered twice":        {sbomBundleOffered, map[string]int64{"a-1-sboms.zip": 1, "b-1-sboms.zip": 2}, true},
		"withheld and absent":  {sbomBundleWithheld, without, false},
		"withheld but present": {sbomBundleWithheld, with, true},
		"unjudged, present":    {sbomBundleUnjudged, with, false},
		"unjudged, absent":     {sbomBundleUnjudged, without, false},
	} {
		if err := releaseSBOMBundleGap(tc.expect, tc.assets); (err != nil) != tc.gap || (err != nil && !errors.Is(err, errReleaseEvidence)) {
			t.Errorf("%s: gap = %v, want one: %t", name, err, tc.gap)
		}
	}
}

func TestReleaseCommitGap(t *testing.T) {
	const pushed, release, signature = "aaaa", "bbbb", "-----BEGIN SSH SIGNATURE-----"

	for name, tc := range map[string]struct {
		prepares  bool
		head      string
		parents   []string
		signature string
		gap       bool
	}{
		"prepared commit":            {true, release, []string{pushed}, signature, false},
		"prepared, main unmoved":     {true, pushed, nil, signature, true},
		"prepared, two commits":      {true, release, []string{"cccc"}, signature, true},
		"prepared, merge":            {true, release, []string{pushed, "cccc"}, signature, true},
		"prepared, unsigned":         {true, release, []string{pushed}, "", true},
		"no changelog, unmoved":      {false, pushed, nil, "", false},
		"no changelog, main moved":   {false, release, []string{pushed}, signature, true},
		"no changelog, other commit": {false, "cccc", nil, "", true},
	} {
		if gap := releaseCommitGap(tc.prepares, pushed, tc.head, tc.parents, tc.signature); (gap != "") != tc.gap {
			t.Errorf("%s: gap = %q, want one: %t", name, gap, tc.gap)
		}
	}
}

// The final tag names main's head; a release that prepares no commit, the
// skipped version bump among them, tags the very commit the trial pushed.
func TestReleaseTagGap(t *testing.T) {
	const pushed, release = "aaaa", "bbbb"

	for name, tc := range map[string]struct {
		prepares     bool
		head, tagged string
		gap          bool
	}{
		"prepared, tag at the release commit": {true, release, release, false},
		"prepared, tag left at the request":   {true, release, pushed, true},
		"skipped, tag at the pushed commit":   {false, pushed, pushed, false},
		"skipped, tag elsewhere":              {false, pushed, release, true},
		"skipped, main and tag both moved":    {false, release, release, true},
	} {
		if gap := releaseTagGap(tc.prepares, pushed, tc.head, tc.tagged); (gap != "") != tc.gap {
			t.Errorf("%s: gap = %q, want one: %t", name, gap, tc.gap)
		}
	}
}

func TestReleaseNotesGap(t *testing.T) {
	for name, tc := range map[string]struct {
		changelog bool
		notes     string
		gap       bool
	}{
		"changelog rendered":  {true, "## 0.2.0\n\n- Add the script library\n", false},
		"changelog missing":   {true, "", true},
		"changelog blank":     {true, " \n", true},
		"no creator, empty":   {false, "", false},
		"no creator, written": {false, "Release notes", false},
	} {
		if gap := releaseNotesGap(tc.changelog, tc.notes); (gap != "") != tc.gap {
			t.Errorf("%s: gap = %q, want one: %t", name, gap, tc.gap)
		}
	}
}

func TestGitSignatureKind(t *testing.T) {
	for signature, want := range map[string]config.GitSignMethod{
		"gpgsig -----BEGIN SSH SIGNATURE-----\n": config.GitSignSSH,
		"gpgsig -----BEGIN PGP SIGNATURE-----\n": config.GitSignGPG,
		"tree 0000\nauthor a\n\nunsigned\n":      "",
	} {
		if got := gitSignatureKind(signature); got != want {
			t.Errorf("kind of %q = %q, want %q", signature, got, want)
		}
	}
}

// The Git signature oracle runs real git, ssh-keygen and gpg against commits
// and tags it signs itself: the declared method's own key passes, and another
// key, the other method and an unsigned object are each refused.
func TestGitSignatureGap(t *testing.T) {
	for _, tool := range []string{"git", "ssh-keygen", "gpg"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH", tool)
		}
	}

	trial := &GitHubTrial{tb: t}
	dir := t.TempDir()

	trial.git(dir, "init", gitQuiet, "-b", defaultBranch)

	object := func(kind, name string, signing ...string) string {
		t.Helper()

		args := append([]string{"-c", "user.name=" + githubTrialAuthor, "-c", "user.email=" + githubTrialEmail}, signing...)
		if kind == "tag" {
			trial.git(dir, append(args, "tag", "-a", "-m", name, name, "HEAD")...)

			return name
		}

		trial.git(dir, append(args, gitCommitCommand, gitQuiet, "--allow-empty", "-m", name)...)

		return strings.TrimSpace(trial.git(dir, "rev-parse", "HEAD"))
	}

	sshSigner := func() (string, string) {
		t.Helper()

		keys := t.TempDir()
		_, public := SSHKey(t, keys)
		allowed := filepath.Join(keys, "allowed_signers")

		if err := os.WriteFile(allowed, []byte("bot@lab.invalid "+public+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		return filepath.Join(keys, sshKeyFile), allowed
	}

	sshKey, allowed := sshSigner()
	_, otherAllowed := sshSigner()
	ssh := []string{"-c", "gpg.format=ssh", "-c", "user.signingkey=" + sshKey, "-c", "commit.gpgsign=true", "-c", "tag.gpgsign=true"}

	gpgKey, otherGPGKey := trial.NewTrialGPGKey(), trial.NewTrialGPGKey()
	program := filepath.Join(gpgKey.Home, "sign")
	script := "#!/bin/sh\nexec gpg --homedir '" + gpgKey.Home + "' --batch --pinentry-mode loopback --passphrase '" + gpgKey.Passphrase + "' \"$@\"\n"

	if err := os.WriteFile(program, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	gpg := []string{"-c", "gpg.format=openpgp", "-c", "gpg.program=" + program, "-c", "user.signingkey=" + gpgKey.Fingerprint, "-c", "commit.gpgsign=true", "-c", "tag.gpgsign=true"}

	unsigned := object("commit", "unsigned", "-c", "commit.gpgsign=false")
	sshCommit, sshTag := object("commit", "ssh-commit", ssh...), object("tag", "ssh-tag", ssh...)
	gpgCommit, gpgTag := object("commit", "gpg-commit", gpg...), object("tag", "gpg-tag", gpg...)

	for name, tc := range map[string]struct {
		kind, object string
		method       config.GitSignMethod
		key          TrialGPGKey
		allowed      string
		gap          bool
	}{
		"ssh commit by the allowed signer": {"commit", sshCommit, config.GitSignSSH, gpgKey, allowed, false},
		"ssh tag by the allowed signer":    {"tag", sshTag, config.GitSignSSH, gpgKey, allowed, false},
		"ssh commit by another signer":     {"commit", sshCommit, config.GitSignSSH, gpgKey, otherAllowed, true},
		"ssh tag by another signer":        {"tag", sshTag, config.GitSignSSH, gpgKey, otherAllowed, true},
		"ssh commit declared gpg":          {"commit", sshCommit, config.GitSignGPG, gpgKey, allowed, true},
		"gpg commit by the trial key":      {"commit", gpgCommit, config.GitSignGPG, gpgKey, "", false},
		"gpg tag by the trial key":         {"tag", gpgTag, config.GitSignGPG, gpgKey, "", false},
		"gpg commit by another key":        {"commit", gpgCommit, config.GitSignGPG, otherGPGKey, "", true},
		"gpg tag by another key":           {"tag", gpgTag, config.GitSignGPG, otherGPGKey, "", true},
		"gpg commit declared ssh":          {"commit", gpgCommit, config.GitSignSSH, gpgKey, allowed, true},
		"unsigned commit, gpg":             {"commit", unsigned, config.GitSignGPG, gpgKey, "", true},
		"unsigned commit, ssh":             {"commit", unsigned, config.GitSignSSH, gpgKey, allowed, true},
		"lightweight tag":                  {"tag", unsigned, config.GitSignSSH, gpgKey, allowed, true},
	} {
		t.Run(name, func(t *testing.T) {
			err := trial.gitSignatureGap(dir, tc.kind, tc.object, tc.method, tc.key, tc.allowed)
			if (err != nil) != tc.gap {
				t.Fatalf("gap = %v, want one: %t", err, tc.gap)
			}
		})
	}
}

// Each variant lives in its own fixture root, so installing it never adds a
// second workflow to an ordinary trial's event. It must render against an
// engine under test and pass the fixture admission the trial applies.
func TestGitHubVariantFixturesRenderAndAdmit(t *testing.T) {
	consumers := filepath.Join(reporoot.Path(t), "test/live/fixtures/consumers")

	for _, tc := range []struct{ consumer, fixtures, workflow, promise string }{
		{"hello-go-cli", "test/live/github-no-sboms", "release-no-sboms.yml", "release.sboms: none"},
		{"hello-go-cli", "test/live/github-no-changelog", "release-no-changelog.yml", `changelog-creator: ""`},
		{"hello-go-service", "test/live/github-gpg-files", "release-gpg-files.yml", "artifacts-config: .forge-ci/artifacts-gpg-files.yml"},
		{"hello-npm-package", "test/live/github-fork", "pullrequest.yml", "private-dependencies: true"},
	} {
		t.Run(tc.fixtures, func(t *testing.T) {
			dir := filepath.Join(consumers, tc.consumer, "consumer", tc.fixtures, "workflows")

			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 || entries[0].Name() != tc.workflow {
				t.Fatalf("%s must hold exactly %s: %v, %v", dir, tc.workflow, entries, err)
			}

			body, err := os.ReadFile(filepath.Join(dir, tc.workflow))
			if err != nil {
				t.Fatal(err)
			}

			rendered, _, err := RenderGitHubConsumer(tc.workflow, string(body), trialEngine, nil)
			if err != nil {
				t.Fatalf("render: %v", err)
			}

			if !strings.Contains(rendered, tc.promise) {
				t.Fatalf("the rendered variant lost %q", tc.promise)
			}

			if _, err := validateGitHubGPGFixtureWorkflows(tc.fixtures, map[string]string{tc.workflow: string(body)}, nil); err != nil {
				t.Fatalf("fixture admission: %v", err)
			}
		})
	}
}

func writeProfileFile(t *testing.T, dir, name, body string) {
	t.Helper()

	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
