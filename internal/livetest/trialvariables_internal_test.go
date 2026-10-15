// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
