// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"strings"
	"testing"
)

// The build job of the release example, from its build step to the hand-off,
// against the CLI of this tree: the build action leaves the binaries and the
// Build SBOM, and the example's own next step stages the SBOM, writes the
// release body and declares the files. The result passes the signer's first
// check, `release files validate --unsigned`, and its checksums file covers
// every public file.
func TestReleaseExampleBuildJobHandsOffWhatTheSignerAccepts(t *testing.T) {
	t.Parallel()

	const commit = "0123456789abcdef0123456789abcdef01234567"

	box, env := goBuildSandbox(t)

	got := box.run(actionStep(t, "build-go", "Build Go module"), append(env, goBuildInputs(commit)...)...)
	if got.status != 0 {
		t.Fatalf("status %d\nstdout %s\nstderr %s", got.status, got.stdout, got.stderr)
	}

	declare := jobStep(t, "examples/go-cli/forgejo/release.yml", "build", "Declare the release files")
	if declare.Env["RELEASE_TAG"] != "${{ needs.prepare.outputs.release-tag }}" || declare.Env["RELEASE_SHA"] != "${{ needs.prepare.outputs.release-sha }}" {
		t.Fatalf("the declaring step reads %q, not the prepared release", declare.Env)
	}

	got = box.run(declare.Run+"\nforge-ci release files validate --unsigned\nforge-ci release files validate-checksums\n",
		append(env, "ARTIFACT_NAME=app", "RELEASE_TAG=v1.2.3", "RELEASE_SHA="+commit)...)
	if got.status != 0 {
		t.Fatalf("status %d\nstdout %s\nstderr %s", got.status, got.stdout, got.stderr)
	}

	manifest := readSandboxFile(t, box, "dist/release-files.json")
	for _, declared := range []string{
		"dist/linux-amd64/app-linux-amd64", "dist/linux-arm64/app-linux-arm64", "dist/app.cdx.json", "dist/checksums.txt",
	} {
		if !strings.Contains(manifest, `"path": "`+declared+`"`) {
			t.Errorf("the hand-off does not declare %s:\n%s", declared, manifest)
		}
	}

	// The release body is handed off and is not a release file.
	if notes := readSandboxFile(t, box, "dist/release-notes.md"); !strings.Contains(notes, "v1.2.3") || strings.Contains(manifest, "release-notes.md") {
		t.Errorf("release notes %q, manifest %s", notes, manifest)
	}
}
