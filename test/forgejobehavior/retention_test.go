// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRetentionPreviewNeedsNoEngineSourceInTheConsumerCheckout(t *testing.T) {
	t.Parallel()

	var workflow struct {
		Jobs map[string]struct {
			Steps []step `yaml:"steps"`
		} `yaml:"jobs"`
	}
	loadYAML(t, ".forgejo/workflows/package-retention.yml", &workflow)
	box := newSandbox(t, "retention-consumer")
	box.write("bin/forge-ci", fakeEngine, 0o700)

	for _, step := range workflow.Jobs["preview"].Steps {
		if step.Run == "" {
			continue // The pinned setup action supplies the published CLI.
		}

		result := box.run(step.Run,
			"PATH="+filepath.Join(box.root, "bin")+":"+os.Getenv("PATH"),
			"RUNNER_TEMP="+box.root,
			"EXPECTED_SHA256="+fmt.Sprintf("%x", sha256.Sum256([]byte(fakeEngine))),
			"RECENT_COUNT=10", "MAX_DELETE=100",
			"PACKAGES_JSON="+fmt.Sprintf(`{"include":[{"repository":"ghcr.io/acme/runtime","digest":"sha256:%s"}]}`, strings.Repeat("a", 64)),
			"REGISTRY_USER=owner",
			"GH_TOKEN=PRIVATE-retention-token", "GOPROXY=off", "GOSUMDB=off")
		if result.status != 0 {
			t.Fatalf("%s requires engine source in the consumer checkout: %s", step.Name, result.stderr)
		}
	}

	if args := box.argv(); len(args) < 2 || args[0] != "container" || args[1] != "package-retention" {
		t.Fatal("preview did not invoke the published retention CLI")
	}
}

func TestScheduledRetentionAlwaysPreviewsWithAllConsumerPins(t *testing.T) {
	t.Parallel()

	step := jobStep(t, ".forgejo/workflows/package-retention.yml", "preview", "Preview the reviewed retention policy")
	if step.Env["GH_TOKEN"] != "${{ secrets.RELEASE_REGISTRY_TOKEN }}" || step.Env["REGISTRY_USER"] != "${{ inputs['registry-user'] }}" {
		t.Fatal("retention must reuse the publisher's existing registry settings")
	}

	box := newSandbox(t, "retention")
	box.write("bin/forge-ci", fakeEngine, 0o700)

	inventory := fmt.Sprintf(`{"include":[{"repository":"ghcr.io/acme/runtime","digest":"sha256:%s"},{"repository":"ghcr.io/acme/runtime","digest":"sha256:%s"}]}`, strings.Repeat("a", 64), strings.Repeat("b", 64))
	result := box.run(step.Run, "PATH="+filepath.Join(box.root, "bin")+":"+os.Getenv("PATH"), "PACKAGES_JSON="+inventory,
		"REGISTRY_USER=owner", "RECENT_COUNT=10", "MAX_DELETE=100", "GH_TOKEN=PRIVATE-retention-token", "PACKAGE_RETENTION_DRY_RUN=false")

	want := []string{"container", "package-retention", "--packages-json", inventory,
		"--recent-count", "10", "--max-delete", "100", "--registry-username", "owner", "--dry-run=true"}
	if result.status != 0 || !slices.Equal(box.argv(), want) {
		t.Fatalf("unsafe retention invocation: %v %s", box.argv(), result.stderr)
	}
}

func TestScheduledRetentionUsesTheRealCLIFlags(t *testing.T) {
	t.Parallel()
	step := jobStep(t, ".forgejo/workflows/package-retention.yml", "preview", "Preview the reviewed retention policy")
	box := newSandbox(t, "retention-cli")
	box.write("bin/forge-ci", "#!/usr/bin/env bash\nexec '"+forgeCIEngine(t)+"' \"$@\"\n", 0o700)
	result := box.run(step.Run, "PATH="+filepath.Join(box.root, "bin")+":"+os.Getenv("PATH"),
		"PACKAGES_JSON="+fmt.Sprintf(`{"include":[{"repository":"ghcr.io/acme/runtime","digest":"sha256:%s"}]}`, strings.Repeat("a", 64)), "REGISTRY_USER=owner", "RECENT_COUNT=10", "MAX_DELETE=100")
	// No credential is provided: real flag parsing must finish and refuse the
	// missing token before any provider request. This caught --username drift.
	if result.status == 0 || !strings.Contains(result.stderr, "GitHub Packages token") {
		t.Fatalf("scheduled command did not reach credential admission: %s", result.stderr)
	}
}
