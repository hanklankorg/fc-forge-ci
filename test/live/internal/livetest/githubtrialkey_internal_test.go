// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// Every shipped hosted release that would write to the public log takes the
// trial's key: its workflow gains the key's two secrets and two inputs once
// each, and its artifacts config stops writing to the public log. One that
// declares GPG writes to no log and runs as declared.
func TestTrialKeySigningOfEveryShippedRelease(t *testing.T) {
	t.Parallel()

	releases, err := filepath.Glob(filepath.Join(reporoot.Path(t), "test/live/fixtures/consumers/*/consumer", githubDemoWorkflows, "release.yml"))
	if err != nil || len(releases) == 0 {
		t.Fatalf("shipped hosted releases: %v, %v", releases, err)
	}

	keyless := 0

	for _, release := range releases {
		consumer := strings.TrimSuffix(release, filepath.Join(githubDemoWorkflows, "release.yml"))

		artifacts, err := os.ReadFile(filepath.Join(consumer, ".forge-ci/artifacts.yml"))
		if err != nil {
			t.Fatal(err)
		}

		if !ReleaseWritesPublicLog(string(artifacts)) {
			continue
		}

		keyless++

		workflow, err := os.ReadFile(release)
		if err != nil {
			t.Fatal(err)
		}

		rekeyed, err := TrialKeyRelease(string(workflow))
		if err != nil {
			t.Errorf("%s: %v", release, err)

			continue
		}

		for _, line := range []string{
			"RELEASE_COSIGN_PRIVATE_KEY: ${{ secrets.RELEASE_COSIGN_PRIVATE_KEY }}",
			"RELEASE_COSIGN_PASSPHRASE: ${{ secrets.RELEASE_COSIGN_PASSPHRASE }}",
			"cosign-public-key: ${{ vars.COSIGN_PUBLIC_KEY }}",
			"cosign-public-key-sha256: " + unrunDigest,
		} {
			if strings.Count(rekeyed, line) != 1 {
				t.Errorf("%s: the re-keyed workflow has %q %d times, want once", release, line, strings.Count(rekeyed, line))
			}
		}

		signed, err := TrialKeyArtifacts(string(artifacts))
		if err != nil || ReleaseWritesPublicLog(signed) || strings.Count(signed, "\nsign:") != 1 || !strings.Contains(signed, "\nartifacts:") {
			t.Errorf("%s: re-keyed artifacts config still public, or lost a block: %v\n%s", consumer, err, signed)
		}
	}

	if keyless == 0 {
		t.Fatal("no shipped release takes the trial's key")
	}
}

func TestTrialKeySigningRefusesWhatItCannotRekey(t *testing.T) {
	t.Parallel()

	if _, err := TrialKeyRelease("jobs:\n  release:\n    with:\n      branch: main\n"); !errors.Is(err, ErrPublicTransparency) {
		t.Errorf("a workflow without the anchors: %v, want a refusal", err)
	}

	if _, err := TrialKeyArtifacts("sign:\n  method: sigstore\nsign:\n  method: sigstore\n"); !errors.Is(err, ErrPublicTransparency) {
		t.Errorf("two sign blocks: %v, want a refusal", err)
	}

	// The trial key replaces the Cosign method and keeps the files' own.
	rekeyed, err := TrialKeyArtifacts("sign:\n  method: sigstore\n  files-method: gpg # files\nartifacts: []\n")
	if want := trialKeySignBlock + "  files-method: gpg\nartifacts: []\n"; err != nil || rekeyed != want {
		t.Errorf("re-keyed files-method declaration = %q, %v; want %q", rekeyed, err, want)
	}

	for config, public := range map[string]bool{
		"artifacts: []\n": true,
		"sign:\n  method: sigstore\nartifacts: []\n":           true,
		"sign:\n  method: kms\n  key: env://K\n":               true,
		"sign:\n  method: gpg\n":                               false,
		"sign:\n  method: sigstore\n  files-method: gpg\n":     true,
		"sign:\n  method: kms\n  transparency: none\n":         false,
		"artifacts: []\nsign:\n\n  method: gpg\ncontainers:\n": false,
	} {
		if ReleaseWritesPublicLog(config) != public {
			t.Errorf("ReleaseWritesPublicLog(%q) = %v, want %v", config, !public, public)
		}
	}
}
