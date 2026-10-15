// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The token comes from a file only its owner reads, with a named user; a
// token in a readable file or without a user stops the trial.
func TestReadExternalRegistry(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	private, shared := filepath.Join(dir, "private"), filepath.Join(dir, "shared")

	for path, mode := range map[string]os.FileMode{private: 0o600, shared: 0o644} {
		if err := os.WriteFile(path, []byte("ghp_fixture\n"), mode); err != nil {
			t.Fatal(err)
		}

		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}

	got, err := readExternalRegistry(private, "hanklankorg")
	if err != nil || got != (ExternalRegistry{User: "hanklankorg", Token: "ghp_fixture"}) {
		t.Fatalf("readExternalRegistry = %+v, %v", got, err)
	}

	for name, tc := range map[string]struct{ path, user string }{
		"readable by others": {path: shared, user: "hanklankorg"},
		"no user":            {path: private, user: " "},
		"no file":            {path: filepath.Join(dir, "absent"), user: "u"},
	} {
		if _, err := readExternalRegistry(tc.path, tc.user); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// image= names an external repository a release trial verifies: a full
// repository without tag or digest, once, and only for a release.
func TestParseTrialSettings_Image(t *testing.T) {
	t.Parallel()

	settings, err := ParseTrialSettings(TrialRelease, []string{"image=ghcr.io/hanklankorg/nanolinter", "IMAGE_REPOSITORY=ghcr.io/hanklankorg"})
	if err != nil || !slices.Equal(settings.Images, []string{"ghcr.io/hanklankorg/nanolinter"}) || settings.Variables["IMAGE_REPOSITORY"] != "ghcr.io/hanklankorg" {
		t.Fatalf("settings = %+v, %v", settings, err)
	}

	for image, want := range map[string]string{
		"ghcr.io/hanklankorg/nanolinter":           "ghcr.io/hanklankorg/nanolinter:v0.0.1",
		"ghcr.io/hanklankorg/nanolinter:v0.0.1-go": "ghcr.io/hanklankorg/nanolinter:v0.0.1-go",
		"registry.lab:5000/owner/app":              "registry.lab:5000/owner/app:v0.0.1",
	} {
		if _, err := ParseTrialSettings(TrialRelease, []string{"image=" + image}); err != nil {
			t.Errorf("image=%s refused: %v", image, err)
		}

		if got := TrialImageReference(image, "0.0.1"); got != want {
			t.Errorf("TrialImageReference(%s) = %s, want %s", image, got, want)
		}
	}

	for _, words := range [][]string{
		{"image=ghcr.io/hanklankorg/nanolinter@sha256:abc"},
		{"image=ghcr.io/hanklankorg/nanolinter", "image=ghcr.io/hanklankorg/nanolinter"},
		{"image=nanolinter"},
	} {
		if _, err := ParseTrialSettings(TrialRelease, words); err == nil {
			t.Errorf("%q accepted", words)
		}
	}

	if _, err := ParseTrialSettings(TrialPullRequest, []string{"image=ghcr.io/hanklankorg/nanolinter"}); err == nil {
		t.Error("image= accepted for a pull request")
	}
}

// The cold GHCR release passed the tagged reference it was given and the
// login step refused it as a repository name; the host is the same for a
// repository, a tag and a digest.
func TestExternalImageHostAcceptsAnyReference(t *testing.T) {
	t.Parallel()

	for image, want := range map[string]string{
		"ghcr.io/hanklankorg/nl-sim":                      "ghcr.io",
		"ghcr.io/hanklankorg/nl-sim:v0.0.1-go":            "ghcr.io",
		"registry.lab:5000/owner/app:v1":                  "registry.lab:5000",
		"ghcr.io/o/app@sha256:" + strings.Repeat("a", 64): "ghcr.io",
	} {
		if got, err := externalImageHost(image); err != nil || got != want {
			t.Errorf("externalImageHost(%s) = %q, %v; want %q", image, got, err, want)
		}
	}
}
