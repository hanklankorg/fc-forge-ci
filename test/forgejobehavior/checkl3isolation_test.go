// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// The isolation step forwards its inputs, names the layer's helper-pin input
// and the inputs that must be literals, and turns the whitespace-separated signing-secret list into repeated
// --signing-secret flags. Isolation analysis belongs to the engine.
func TestCheckL3Isolation(t *testing.T) {
	t.Parallel()
	action := loadAction(t, "check-l3-isolation")
	action.requireSteps(t, 1)
	script := action.Runs.Steps[0].Run
	base := []string{
		"validate", "isolation", "--workflow", "workflow-v", "--build-job", "build-v", "--sign-job", "sign-v",
		"--prepare-job", "prepare-v", "--dist-digest-output", "digest-v", "--single-pin-subject", "served/layer",
		// The layer's own name for the second pin, never one derived from
		// the serving repository.
		"--single-pin-input", "forge-ci-sha",
		// The trust inputs a caller writes out, each in its shape.
		"--literal-input", "signer-image=image-digest",
		"--literal-input", "cosign-public-key-sha256=sha256",
		// The transparency log: a public entry cannot be withdrawn.
		"--literal-input", "transparency-log=enum:none/public",
	}

	for _, test := range []struct {
		name, secrets, status string
		want                  int
		flags                 []string
	}{
		{
			"forwards every default signing secret", fmt.Sprint(action.Inputs["signing-secrets"].Default), "0", 0,
			[]string{
				"--signing-secret", "RELEASE_COSIGN_PRIVATE_KEY", "--signing-secret", "RELEASE_COSIGN_PASSPHRASE",
				"--signing-secret", "RELEASE_GPG_PRIVATE_KEY", "--signing-secret", "RELEASE_GPG_PASSPHRASE",
				"--signing-secret", "RELEASE_GPG_FINGERPRINT",
			},
		},
		{"splits on runs of whitespace", " ALPHA \t BETA\n", "0", 0, []string{"--signing-secret", "ALPHA", "--signing-secret", "BETA"}},
		{"forwards no secret flag for an empty list", "", "0", 0, nil},
		{"propagates the engine status", "ALPHA", "3", 3, []string{"--signing-secret", "ALPHA"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			box := newSandbox(t, "check-l3-isolation")

			got := box.run(script, append(isolationEnv(), "GITHUB_ACTION_REPOSITORY=served/layer",
				"ISOLATION_SIGNING_SECRETS="+test.secrets, "FAKE_ENGINE_STATUS="+test.status)...)
			want := slices.Concat(base, []string{"--contract-root", box.actionDir + "/../../.."}, test.flags)

			if got.status != test.want || !slices.Equal(box.argv(), want) {
				t.Fatalf("status %d argv %q, want %d %q; stderr %s", got.status, box.argv(), test.want, want, got.stderr)
			}
		})
	}
}

// The single-pin subject is the input when the caller names one, and otherwise
// the repository the runner fetched the action from. With neither, the step
// stops before the engine: an empty subject switches the pin check off.
func TestCheckL3IsolationSinglePinSubject(t *testing.T) {
	t.Parallel()
	action := loadAction(t, "check-l3-isolation")
	script := action.Runs.Steps[0].Run

	if value := fmt.Sprint(action.Inputs["single-pin-subject"].Default); value != "" {
		t.Fatalf("single-pin-subject defaults to %q, want the empty string that follows the serving repository", value)
	}

	for _, test := range []struct {
		name, input, served, want string
	}{
		{"follows the serving repository", "", "fork/layer", "fork/layer"},
		{"prefers the named subject", "named/subject", "fork/layer", "named/subject"},
		{"takes a named subject for a local-path action", "named/subject", "", "named/subject"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			box := newSandbox(t, "check-l3-isolation")
			got := box.run(script, append(isolationEnv(), "ISOLATION_SIGNING_SECRETS=",
				"ISOLATION_SINGLE_PIN_SUBJECT="+test.input, "GITHUB_ACTION_REPOSITORY="+test.served)...)

			argv := box.argv()
			if at := slices.Index(argv, "--single-pin-subject"); got.status != 0 || at < 0 || argv[at+1] != test.want {
				t.Fatalf("status %d argv %q, want subject %q; stderr %s", got.status, argv, test.want, got.stderr)
			}

			if checked := slices.Contains(argv, "--contract-root"); checked != (test.want == test.served) {
				t.Errorf("input declarations must come from the subject's own serving checkout: %q", argv)
			}
		})
	}

	t.Run("refuses to run without a subject", func(t *testing.T) {
		t.Parallel()

		box := newSandbox(t, "check-l3-isolation")

		got := box.run(script, append(isolationEnv(), "ISOLATION_SIGNING_SECRETS=")...)
		if got.status == 0 || box.argv() != nil || !strings.Contains(got.stderr, "single-pin-subject is empty") {
			t.Fatalf("status %d argv %q stderr %q", got.status, box.argv(), got.stderr)
		}
	})
}

func isolationEnv() []string {
	return []string{
		"ISOLATION_WORKFLOW=workflow-v", "ISOLATION_BUILD_JOB=build-v", "ISOLATION_SIGN_JOB=sign-v",
		"ISOLATION_PREPARE_JOB=prepare-v", "ISOLATION_DIST_DIGEST_OUTPUT=digest-v",
	}
}
