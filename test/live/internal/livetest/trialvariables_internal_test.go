// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestTrialVariablesPreserveLiteralValuesAndRejectAmbiguity(t *testing.T) {
	t.Parallel()

	got, err := ParseTrialVariables("RELEASE_PLATFORMS=linux/amd64,linux/arm64\nMESSAGE=a b=c\nEMPTY=\n")
	want := map[string]string{"RELEASE_PLATFORMS": "linux/amd64,linux/arm64", "MESSAGE": "a b=c", "EMPTY": ""}

	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("variables = %v, %v", got, err)
	}

	for _, input := range []string{"A=one\nA=two", "workflow=release.yml", "token=NAME", "NOT_AN_ASSIGNMENT", "9NAME=value"} {
		if _, err := ParseTrialVariables(input); err == nil {
			t.Fatalf("accepted invalid variables %q", input)
		}
	}

	if defaults, err := ParseTrialVariables(""); err != nil || len(defaults) != 0 {
		t.Fatalf("default imposed consumer variables: %v, %v", defaults, err)
	}
}

func TestGitLabConsumerBuildVariablesStayLiteralAndLocal(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".gitlab"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, ".gitlab", "build-release.sh"), []byte("printf '%s' \"$MESSAGE\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	value := "literal 'quotes' $(exit 77); $HOME\nsecond line"

	command, err := GitLabConsumerBuildCommand(map[string]string{"MESSAGE": value})
	if err != nil {
		t.Fatal(err)
	}

	child := exec.CommandContext(t.Context(), "bash")
	child.Stdin = strings.NewReader(command + "; printf ':%s' \"${MESSAGE-unset}\"")
	child.Dir = root
	child.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root}

	output, err := child.CombinedOutput()
	if err != nil || string(output) != value+":unset" {
		t.Fatalf("consumer setting was interpreted or leaked: %q, %v", output, err)
	}

	if command, err := GitLabConsumerBuildCommand(nil); err != nil || command != "bash .gitlab/build-release.sh" {
		t.Fatalf("default command changed: %q, %v", command, err)
	}
}

// version and job-user are the trial's own settings, written into the request
// and the policy; every other word stays a consumer variable for the build.
// The declaration decides the bump, so version-bump is no setting.
func TestParseGitLabReleaseSettings_SeparatesItsSettingsFromVariables(t *testing.T) {
	got, err := ParseGitLabReleaseSettings("version=0.2.0\njob-user=65532:65532\nRELEASE_PLATFORMS=linux/amd64")
	if err != nil || got.Version != "0.2.0" || got.JobUser != "65532:65532" ||
		got.Variables["RELEASE_PLATFORMS"] != "linux/amd64" || len(got.Variables) != 1 {
		t.Fatalf("ParseGitLabReleaseSettings() = %+v, %v", got, err)
	}

	if got, err := ParseGitLabReleaseSettings("version=v0.2.0"); err != nil || got.Version != "0.2.0" {
		t.Errorf("a leading v: ParseGitLabReleaseSettings() = %+v, %v; want version 0.2.0", got, err)
	}

	for _, input := range []string{
		"version-bump=maven",
		"version=vv0.2.0", "version=0.2", "version=1.0.0\nversion=1.0.1",
		"job-user=", "job-user=nonroot", "job-user=65532\njob-user=65532", "job-user=65532:",
		"job-user=065532", "job-user=65532:065532", "job-user=00",
		// The policy selects the builder and the job user on GitLab, not the
		// build's environment.
		"CI_IMAGE_BUILDER=kaniko", "CI_IMAGE_BUILDER_JOB_IMAGE=registry.invalid/kaniko@sha256:" + strings.Repeat("a", 64),
		"RC_JOB_USER=0",
	} {
		if _, err := ParseGitLabReleaseSettings(input); err == nil {
			t.Errorf("ParseGitLabReleaseSettings(%q) accepted it", input)
		}
	}
}

func TestParseGitLabLifecycleSettings_TakesAReleaseSettingsButASecondCase(t *testing.T) {
	t.Parallel()

	got, err := ParseGitLabLifecycleSettings("version=0.2.0\njob-user=65532:65532\nRELEASE_PLATFORMS=linux/amd64")
	if err != nil || got.Version != "0.2.0" || got.JobUser != "65532:65532" || got.Variables["RELEASE_PLATFORMS"] != "linux/amd64" {
		t.Fatalf("ParseGitLabLifecycleSettings() = %+v, %v", got, err)
	}

	for _, input := range []string{"scenario=TestGitLabRelease_ConsumerTrial", "version=0.2.0\nnext-version=0.3.0"} {
		if _, err := ParseGitLabLifecycleSettings(input); !errors.Is(err, errs.ErrValidation) {
			t.Errorf("ParseGitLabLifecycleSettings(%q) = %v, want a validation error", input, err)
		}
	}
}
