// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestCheckPreparedSource(t *testing.T) {
	t.Parallel()

	const commit = "0123456789abcdef0123456789abcdef01234567"

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, PreparedEnvFile), []byte("PREPARED_COMMIT="+commit+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := CheckPreparedSource(dir, commit, ""); err != nil {
		t.Fatalf("the prepared commit, clean, is refused: %v", err)
	}

	for name, tc := range map[string]struct{ dir, head, changes string }{
		"another commit":    {dir, "89abcdef0123456789abcdef0123456789abcdef", ""},
		"a changed file":    {dir, commit, " M scripts/ci/run-live-tests.sh\n"},
		"an untracked file": {dir, commit, "?? notes.txt\n"},
		"a relative dir":    {"prepared", commit, ""},
		"nothing prepared":  {t.TempDir(), commit, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if err := CheckPreparedSource(tc.dir, tc.head, tc.changes); err == nil {
				t.Fatal("accepted")
			} else if name != "nothing prepared" && !errors.Is(err, errs.ErrValidation) {
				t.Fatalf("not a validation error: %v", err)
			}
		})
	}
}

// writeFile writes body to path, making its directory.
func writeFile(t *testing.T, path, body string) string {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// A prepare builds the runtime images the GitLab cases of its lanes run
// in: each case's own, the scenarios' go image, the further job images of
// the demos' gates and the producer families of the snapshots. A forge names
// every lane of it. A name that is no forge or lane, a target images.json
// does not build and a nanolinter image of a flavor the prepare does not
// publish are refused.
func TestPreparedRuntimeTargets_ComeFromTheLanesCasesAndTheirDemosGates(t *testing.T) {
	t.Parallel()

	repo := reporoot.Path(t)
	flavors := []string{"go", "java", "javascript-typescript", "rust"}

	for _, tc := range []struct{ names, flavors, want []string }{
		{[]string{"k3s-private-gitlab"}, flavors, []string{"go", "java", "node", "rust"}},
		{[]string{"lifecycle-gitlab"}, flavors, []string{"java", "node", "rust"}},
		// A snapshot lints in nothing, and the monorepo's producers build in
		// their families' images.
		{[]string{"snapshot-gitlab"}, nil, []string{"go", "java", "node", "rust"}},
		// The scenarios' go image alone.
		{[]string{"build-capability-gitlab"}, nil, []string{"go"}},
		// And the images of the scenarios' consumers, the Go CLI and the
		// Maven service.
		{[]string{"harness-inputs-gitlab"}, []string{"go", "java"}, []string{"go", "java"}},
		// A Forgejo lane runs no GitLab gate and no GitLab scenario.
		{[]string{"k3s-private-forgejo", "forgejo"}, flavors, nil},
		{[]string{"k3s-private-forgejo", "lifecycle-gitlab"}, flavors, []string{"java", "node", "rust"}},
		{[]string{"gitlab"}, flavors, []string{"android", "go", "java", "node", "rust"}},
		// The Android gates build in the Android image and lint in the Java flavor.
		{[]string{"k3s-private-android-gitlab"}, []string{"java"}, []string{"android"}},
	} {
		if targets, err := PreparedRuntimeTargets(repo, tc.names, tc.flavors); err != nil || !slices.Equal(targets, tc.want) {
			t.Errorf("%q: targets %q, %v; want %q", tc.names, targets, err, tc.want)
		}
	}

	// A demo whose gate checks in a further job image, in a repository whose
	// roster builds it, and in one whose roster does not.
	gate := "include:\n  - component: $CI_SERVER_FQDN/team/engine/check@" + strings.Repeat("a", 40) + "\n    inputs:\n      job-image: $CI_JOB_IMAGE_NODE\n"
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "containers", "runtime", "images.json"), `[{"target":"android"},{"target":"node"}]`)
	writeFile(t, filepath.Join(demoConsumerDir(root, "hello-android"), filepath.FromSlash(GitLabGateFile)), gate)

	if targets, err := PreparedRuntimeTargets(root, []string{"k3s-private-android-gitlab"}, []string{"java"}); err != nil || !slices.Equal(targets, []string{"android", "node"}) {
		t.Errorf("a gate's further job image: targets %q, %v", targets, err)
	}

	unbuilt := t.TempDir()
	writeFile(t, filepath.Join(unbuilt, "containers", "runtime", "images.json"), `[{"target":"android"}]`)
	writeFile(t, filepath.Join(demoConsumerDir(unbuilt, "hello-android"), filepath.FromSlash(GitLabGateFile)), gate)

	for _, tc := range []struct {
		root           string
		names, flavors []string
		want           string
	}{
		{unbuilt, []string{"k3s-private-android-gitlab"}, []string{"java"}, `"node"`},
		{repo, []string{"k3s-private-android-gitlab"}, nil, "add job-image=java:<local image>"},
		{repo, []string{"lifecycle-gitlab"}, []string{"go"}, "add job-image=java:<local image>"},
		{repo, []string{"k3s-private-forgejo"}, nil, "add job-image=go:<local image>"},
		{repo, []string{"harness-inputs-gitlab"}, []string{"go"}, "add job-image=java:<local image>"},
		{repo, []string{"lifecycle-gitlab"}, []string{"Java"}, "a job image flavor"},
		{repo, nil, flavors, "name what to prepare for"},
		{repo, []string{"github"}, flavors, "want forgejo, gitlab or a lane"},
		{repo, []string{"gitlab=lifecycle-gitlab"}, flavors, "want forgejo, gitlab or a lane"},
	} {
		if _, err := PreparedRuntimeTargets(tc.root, tc.names, tc.flavors); !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q with %q: got %v, want a validation error naming %q", tc.names, tc.flavors, err, tc.want)
		}
	}
}

// The Android runtime image is large, so only its own lane prepares it.
func TestAndroidPreparedRuntimeIsExplicitlyOptedIn(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)
	base, android := "k3s-private-gitlab", "k3s-private-android-gitlab"
	flavors := []string{"go", "java", "javascript-typescript", "rust"}
	ordinary, err := PreparedRuntimeTargets(root, []string{base}, flavors)

	if err != nil || slices.Contains(ordinary, "android") {
		t.Fatalf("the ordinary lane unexpectedly prepares Android: %q, %v", ordinary, err)
	}

	targets, err := PreparedRuntimeTargets(root, []string{base, android}, flavors)
	if want := []string{"android", "go", "java", "node", "rust"}; err != nil || !slices.Equal(targets, want) {
		t.Fatalf("opt-in runtime targets: got %q, %v; want %q", targets, err, want)
	}
}
