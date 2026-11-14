// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// An armored signature of the release's key over a file verifies as that
// key's; the same signature checked against another fingerprint, which gpg
// --verify alone would pass, and one over other bytes are refused.
func TestVerifyOpenPGPSignature(t *testing.T) {
	t.Parallel()

	const passphrase = "openpgp-signature-test"

	runtime := runtimeEnvironment(t)
	environment := make([]string, 0, len(runtime))

	for key, value := range runtime {
		environment = append(environment, key+"="+value)
	}

	release := t.TempDir()
	_, fingerprint := GPGKey(t, release, passphrase)
	home := filepath.Join(release, "gnupg")

	file := filepath.Join(t.TempDir(), "checksums.txt")
	if err := os.WriteFile(file, []byte("checksums\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(t.Context(), "gpg", "--homedir", home, "--batch",
		"--pinentry-mode", "loopback", "--passphrase-fd", "0", "--armor", "--detach-sign", "--output", file+".asc", file)
	command.Stdin = strings.NewReader(passphrase + "\n")
	command.Env = environment

	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("gpg --detach-sign: %v\n%s", err, output)
	}

	signature, err := os.ReadFile(file + ".asc")
	if err != nil {
		t.Fatal(err)
	}

	if err := VerifyOpenPGPSignature(t.Context(), home, environment, fingerprint, []byte("checksums\n"), signature); err != nil {
		t.Fatalf("the release key's signature: %v", err)
	}

	for name, test := range map[string]struct {
		fingerprint, body, finding string
	}{
		"another fingerprint":   {"0123456789ABCDEF0123456789ABCDEF01234567", "checksums\n", "is not the key 0123456789ABCDEF0123456789ABCDEF01234567's"},
		"changed after signing": {fingerprint, "other checksums\n", "does not verify"},
	} {
		if err := VerifyOpenPGPSignature(t.Context(), home, environment, test.fingerprint, []byte(test.body), signature); err == nil || !strings.Contains(err.Error(), test.finding) {
			t.Errorf("%s: %v, want a finding %q", name, err, test.finding)
		}
	}
}

// The Go service demo staged with its GPG fixtures is its ordinary release
// from a declaration that says sign.method gpg: the workflows are the ones
// it had, the plan a trial makes of it renders, and the host, reading the
// staged consumer, judges it a release whose files GPG signs. Without the
// fixtures the demo's release signs them with the Cosign key.
func TestForgejoFixturesStageTheDemosGPGFilesRelease(t *testing.T) {
	t.Parallel()

	_, demoRoot, source, git := stageTestDemo(t, "hello-go-service")

	ordinary, err := source.workflows(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	// The consumer is written out from its private index, which staging
	// changes: the ordinary demo first.
	roots := map[bool]string{false: t.TempDir(), true: t.TempDir()}
	if err = source.export(t.Context(), roots[false]); err != nil {
		t.Fatal(err)
	}

	staged, err := source.withFixtures(t.Context(), ForgejoGPGFilesFixtures)
	if err != nil {
		t.Fatal(err)
	}

	workflows, err := staged.workflows(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if !maps.Equal(workflows, ordinary) {
		t.Error("the GPG fixtures changed a workflow; they replace the declaration alone")
	}

	if _, err = sampleLayer().PlanConsumer(workflows, releaseFacts()); err != nil {
		t.Errorf("the staged demo does not render: %v", err)
	}

	if err = staged.export(t.Context(), roots[true]); err != nil {
		t.Fatal(err)
	}

	for want, root := range roots {
		gpg, signErr := ForgejoSignsReleaseFilesWithGPG(root, trialReleaseWorkflow)
		if signErr != nil || gpg != want {
			t.Errorf("the demo staged with the GPG fixtures %t signs its release files with GPG %t, %v", want, gpg, signErr)
		}
	}

	if err = git.requireClean(t.Context(), demoRoot, "."); err != nil {
		t.Errorf("writing the staged consumer changed its checkout: %v", err)
	}
}

// The Go CLI demo staged with its GitLab GPG fixtures is a release whose
// files GPG signs, read from the default declaration the GitLab release
// reads; without them the demo signs them with the Cosign key.
func TestGitLabFixturesStageTheDemosGPGFilesRelease(t *testing.T) {
	t.Parallel()

	_, demoRoot, source, git := stageTestDemo(t, "hello-go-cli")

	roots := map[bool]string{false: t.TempDir(), true: t.TempDir()}
	if err := source.export(t.Context(), roots[false]); err != nil {
		t.Fatal(err)
	}

	staged, err := source.withFixtures(t.Context(), GitLabGPGFilesFixtures)
	if err != nil {
		t.Fatal(err)
	}

	if err = staged.export(t.Context(), roots[true]); err != nil {
		t.Fatal(err)
	}

	for want, root := range roots {
		gpg, signErr := SignsReleaseFilesWithGPG(root, "")
		if signErr != nil || gpg != want {
			t.Errorf("the demo staged with the GPG fixtures %t signs its release files with GPG %t, %v", want, gpg, signErr)
		}
	}

	if err = git.requireClean(t.Context(), demoRoot, "."); err != nil {
		t.Errorf("writing the staged consumer changed its checkout: %v", err)
	}
}
