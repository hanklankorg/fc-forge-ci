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

// The prune step is one engine call: it forwards its inputs, keeps the token
// in the environment and off the argument list, and stops before the engine
// without the release images or the key that decide what is kept. It deletes
// nothing unless the caller turns the dry run off.
func TestPruneBaseImages(t *testing.T) {
	t.Parallel()

	action := loadAction(t, "prune-base-images")
	action.requireSteps(t, 1)
	script := action.Runs.Steps[0].Run

	if value := fmt.Sprint(action.Inputs["dry-run"].Default); value != "true" {
		t.Fatalf("dry-run defaults to %q, want true", value)
	}

	env := func(overrides ...string) []string {
		return append([]string{
			"INPUT_RELEASE_IMAGES=forge.example/o/app@sha256:" + strings.Repeat("a", 64),
			"INPUT_COSIGN_PUBLIC_KEY_PATH=keys/cosign.pub", "INPUT_REPOSITORY_SUFFIX=-base",
			"INPUT_EXPECTED_REPOSITORY=", "INPUT_DRY_RUN=true", "INPUT_MAX_DELETE=20", "INPUT_LOCAL_REGISTRY=false",
			"INPUT_TRANSPARENCY_LOG=none",
			"FORGEJO_TOKEN=token-DO-NOT-PASS-AS-ARGUMENT",
		}, overrides...)
	}

	t.Run("forwards the inputs and keeps the token off argv", func(t *testing.T) {
		t.Parallel()

		box := newSandbox(t, "prune-base-images")
		got := box.run(script, env()...)

		want := []string{
			"container", "base-images", "prune",
			"--release-images", "forge.example/o/app@sha256:" + strings.Repeat("a", 64),
			"--cosign-public-key-path", "keys/cosign.pub", "--repository-suffix", "-base",
			"--expected-repository", "", "--max-delete", "20", "--dry-run=true", "--local-registry=false",
		}
		if got.status != 0 || !slices.Equal(box.argv(), want) {
			t.Fatalf("status %d argv %q, want %q; stderr %s", got.status, box.argv(), want, got.stderr)
		}
	})

	t.Run("propagates the engine status", func(t *testing.T) {
		t.Parallel()

		box := newSandbox(t, "prune-base-images")
		if got := box.run(script, env("FAKE_ENGINE_STATUS=7")...); got.status != 7 {
			t.Fatalf("status %d, want 7; stderr %s", got.status, got.stderr)
		}
	})

	// The attestations are verified the way the release was signed, and the
	// default is the layer's: no public transparency log. A value the CLI
	// would read as "unset", and so as public, never reaches it.
	if value := fmt.Sprint(action.Inputs["transparency-log"].Default); value != "none" {
		t.Fatalf("transparency-log defaults to %q, want none", value)
	}

	for _, value := range []string{"none", "public"} {
		t.Run("verifies with the transparency log "+value, func(t *testing.T) {
			t.Parallel()

			box := newSandbox(t, "prune-base-images")
			box.write(".forgejo/bin/forge-ci",
				"#!/usr/bin/env bash\nprintf 'seen=%s\\n' \"${FORGE_CI_COSIGN_TRANSPARENCY-unset}\" >>\"${FORGEJO_OUTPUT:?}\"\n", 0o700)

			if got := box.run(script, env("INPUT_TRANSPARENCY_LOG="+value)...); got.status != 0 || box.output() != "seen="+value+"\n" {
				t.Fatalf("status %d, engine saw %q, want %s; stderr %s", got.status, box.output(), value, got.stderr)
			}
		})
	}

	for _, value := range []string{"", "Public", "off", "none "} {
		t.Run(fmt.Sprintf("stops before the engine with the transparency log %q", value), func(t *testing.T) {
			t.Parallel()

			box := newSandbox(t, "prune-base-images")

			got := box.run(script, env("INPUT_TRANSPARENCY_LOG="+value)...)
			if got.status == 0 || box.argv() != nil || !strings.Contains(got.stderr, "transparency-log must be none or public") {
				t.Fatalf("status %d argv %q; stderr %s", got.status, box.argv(), got.stderr)
			}
		})
	}

	for name, override := range map[string]string{
		"release images": "INPUT_RELEASE_IMAGES=",
		"public key":     "INPUT_COSIGN_PUBLIC_KEY_PATH=",
	} {
		t.Run("stops before the engine without the "+name, func(t *testing.T) {
			t.Parallel()

			box := newSandbox(t, "prune-base-images")
			if got := box.run(script, env(override)...); got.status == 0 || box.argv() != nil {
				t.Fatalf("status %d argv %q; stderr %s", got.status, box.argv(), got.stderr)
			}
		})
	}
}
