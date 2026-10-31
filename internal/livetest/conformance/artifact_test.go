// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

// PAR-ART-*: Actions-style artifact commands and native GitLab artifact handoff.
//
// GitHub and Forgejo expose an intra-run artifact store; GitLab does not, and
// that is a platform difference rather than a missing adapter — GitLab job
// artifacts are declared in .gitlab-ci.yml and uploaded by the runner, with no
// public API for a job to push one. The command refusal must be honest; the
// native GitLab job-to-job handoff is a separate positive delivery check.
//
// The positive path is not host-run either, for the same reason: uploading needs
// the run context a runner supplies, so a host-run scenario could only observe
// the runtime being absent. What a host CAN prove is the distinction that
// actually confuses adopters: "this forge will never do this" versus "you are not
// in a run". Those must not look alike, and PAR-ART-2 is the only place that is
// checked. The Actions round-trip is PAR-ART-3; PAR-ART-4 checks GitLab's
// runner-managed artifacts between two independent jobs.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
)

func claimsRunArtifacts(c provider.Capabilities) bool { return c.RunArtifacts }

// artifactUploadFlags point an upload at a real file, so the verb
// gets past its own input validation and reaches the capability gate.
func artifactUploadFlags(t *testing.T) []string {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.txt"), []byte("livetest\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	return []string{"--name", "livetest-par-art", "--dir", dir}
}

// PAR-ART-1: a forge without an artifact store refuses, and says what is
// missing.
//
// The failure this guards is not an error — it is a *silent success*. A verb
// that exits 0 having uploaded nothing leaves a pipeline believing its reports
// were published, and the next job looking for them is where anyone finds out.
func TestArtifact_WithoutAStore_RefusesAndNamesTheGap(t *testing.T) {
	livetest.RegisterLiveScenario(t)
	for _, forge := range forgesLacking(t, claimsRunArtifacts, "run artifacts") {
		t.Run(string(forge), func(t *testing.T) {
			t.Parallel()

			target := livetest.Accept(t, forge)
			repo := livetest.NewScratchRepo(t, target, "artifact-refuse")

			run := livetest.CLI(t, target, repo, append([]string{"artifact", "upload"}, artifactUploadFlags(t)...)...)

			if run.ExitCode == 0 {
				t.Fatalf("%s: uploading to a forge with no artifact store exited 0\nstdout: %s", forge, run.Stdout)
			}

			// A refusal has to be readable as one. Naming the platform is what
			// separates "this forge cannot" from a transient failure the reader
			// would otherwise sit and retry.
			lower := strings.ToLower(run.Stderr)
			if !strings.Contains(lower, "unsupported") && !strings.Contains(lower, "not supported") {
				t.Errorf("%s: refusal does not say the capability is unsupported\nstderr: %s", forge, run.Stderr)
			}

			if !strings.Contains(lower, string(forge)) {
				t.Errorf("%s: refusal does not name the platform, so a reader cannot tell why\nstderr: %s",
					forge, run.Stderr)
			}

			// The gap is permanent, so it must not exit with the code that
			// means "the external system is unavailable" — CI retries that, and
			// no number of retries gives GitLab an artifact store.
			if run.ExitCode == int(errs.ExitCodeUnavailable) {
				t.Errorf("%s: exits %d (unavailable) for a capability it will never have, so a retry loop keeps trying\nstderr: %s",
					forge, run.ExitCode, run.Stderr)
			}

			if run.ExitCode != int(errs.ExitCodeConfiguration) {
				t.Errorf("%s: exits %d for an unsupported capability, want %d (EX_CONFIG)\nstderr: %s",
					forge, run.ExitCode, errs.ExitCodeConfiguration, run.Stderr)
			}
		})
	}
}

// PAR-ART-2: a forge that HAS an artifact store must not refuse as though it
// did not.
//
// Off a runner the upload cannot succeed — there is no run to attach to — and
// that is fine. What must not happen is the two being reported the same way: an
// adopter who sees "unsupported on this platform" on a forge that supports it
// perfectly well concludes the feature is missing and works around a problem
// that does not exist.
func TestArtifact_WithAStore_FailsOnTheRuntimeNotTheCapability(t *testing.T) {
	livetest.RegisterLiveScenario(t)
	for _, forge := range forgesClaiming(t, claimsRunArtifacts, "run artifacts") {
		t.Run(string(forge), func(t *testing.T) {
			t.Parallel()

			target := livetest.Accept(t, forge)
			repo := livetest.NewScratchRepo(t, target, "artifact-runtime")

			run := livetest.CLI(t, target, repo, append([]string{"artifact", "upload"}, artifactUploadFlags(t)...)...)

			assertArtifactRuntimeRefusal(t, forge, run)
		})
	}
}

// PAR-ART-3: an artifact uploaded from a job can be read back, byte-identical.
//
// This is the claim the capability matrix makes and nothing was backing.
// RunArtifacts is derived from Uploader AND Downloader both being satisfied, so
// the matrix promises a store that round-trips — while every tier below this one
// proves only that the methods exist and that the fake we wrote agrees with the
// adapter we wrote.
//
// The scenario is deliberately hostile to a false pass, because "download
// produced a file" is trivially satisfiable three different ways:
//
//   - the payload carries a per-run nonce, so an artifact left by an earlier run
//     of this suite cannot satisfy the comparison;
//   - the source directory is deleted between upload and download, so a download
//     that silently does nothing leaves the file genuinely absent rather than
//     rediscovering the copy the job just wrote;
//   - the content is compared, not merely the file's existence, so a truncated or
//     re-encoded transfer fails.
//
// What is asserted is the round-trip, not the destination layout: the probe finds
// the payload anywhere under the download directory. Layout is a real contract,
// but a different one, and pinning it here would make this scenario fail for a
// reason unrelated to whether the store works.
func TestInRunner_ArtifactRoundTripsThroughTheStore(t *testing.T) {
	livetest.RegisterLiveScenario(t)
	const tag = "v0.0.5-artifact"

	for _, forge := range livetest.ForgesMeeting(t, forgesClaiming(t, claimsRunArtifacts, "run artifacts"), livetest.NeedsInRunner) {
		t.Run(string(forge), func(t *testing.T) {
			t.Parallel()

			target := livetest.Accept(t, forge)
			repo := livetest.NewScratchRepo(t, target, "artifact-roundtrip")

			livetest.PrepareTag(t, target, repo, tag)
			livetest.PublishBinaryAsset(t, target, repo, tag, t.TempDir())

			assetURL := livetest.ReleaseAssetURL(t, target, repo, tag, "forge-ci")

			conclusion := livetest.RunWorkflow(t, target, repo, "artifact-roundtrip",
				artifactRoundTripProbe(target, assetURL))
			if conclusion != "success" {
				t.Errorf("%s: the artifact round-trip concluded %q inside a real job — this forge reports RunArtifacts, but a file uploaded from a job did not come back intact",
					forge, conclusion)
			}
		})
	}
}

// artifactRoundTripProbe uploads a nonce, destroys the source, downloads it back
// and compares. Written in the Actions dialect only; GitLab's native handoff
// uses artifacts/needs in gitlabNativeArtifactProbe instead.
func artifactRoundTripProbe(target livetest.Target, assetURL string) string {
	return `on: [push]
jobs:
  roundtrip:
    runs-on: ubuntu-latest
    steps:
      - name: upload an artifact and read it back
        run: |
          ` + indent(livetest.ProbePrelude(target, assetURL), 10) + `

          # Unique to this run, so a leftover artifact cannot pass for a fresh one.
          nonce="par-art-3-${GITHUB_RUN_ID:-norun}-${GITHUB_SHA:-nosha}"

          mkdir -p artifact-src
          printf '%s\n' "$nonce" > artifact-src/payload.txt

          run_product artifact upload --name livetest-roundtrip --dir artifact-src

          # The load-bearing line: with the source gone, only a real download can
          # produce the payload, so a no-op download fails instead of passing.
          rm -rf artifact-src

          mkdir -p artifact-dst
          run_product artifact download --name livetest-roundtrip --dir artifact-dst

          found="$(find artifact-dst -name payload.txt -type f | head -n 1)"
          if [ -z "$found" ]; then
            echo "FAIL: the artifact did not come back; nothing named payload.txt under artifact-dst"
            find artifact-dst -type f || true
            exit 1
          fi

          if [ "$(cat "$found")" != "$nonce" ]; then
            echo "FAIL: the artifact came back with different content"
            echo "want: $nonce"
            echo "got:  $(cat "$found")"
            exit 1
          fi

          echo "artifact round-tripped intact: $found"
`
}

// PAR-ART-4: the runner transports a binary payload to a distinct job, whose
// clean workspace cannot satisfy the assertion using the producer's files.
func TestInRunner_GitLabNativeArtifactsReachTheNextJob(t *testing.T) {
	livetest.RegisterLiveScenario(t)
	if !livetest.Selected(t, provider.ForgeGitLab) {
		livetest.NotCoveredf(t, "the contract selects no GitLab endpoint for native job artifacts")
	}

	for _, forge := range livetest.ForgesMeeting(t, []provider.ForgeAPI{provider.ForgeGitLab}, livetest.NeedsInRunner) {
		t.Run(string(forge), func(t *testing.T) {
			target := livetest.Accept(t, forge)

			repo := livetest.NewScratchRepo(t, target, "native-artifact-handoff")
			if conclusion := livetest.RunWorkflow(t, target, repo, "native-artifact-handoff", gitlabNativeArtifactProbe()); conclusion != "success" {
				t.Fatalf("GitLab native artifact handoff concluded %q; the downstream job did not verify the payload", conclusion)
			}
		})
	}
}

// Exercise the actual build-to-sign digest verifier after native job transport.
// Bundle mutation here tests byte integrity, not signature verification.
func TestInRunner_GitLabReleaseHandoffRejectsTamperedBytes(t *testing.T) {
	livetest.RegisterLiveScenario(t)
	if !livetest.Requires(t, provider.ForgeGitLab, livetest.NeedsInRunner) {
		livetest.NotCoveredf(t, "release handoff rejection requires a GitLab runner")
	}

	target := livetest.Accept(t, provider.ForgeGitLab)
	repo := livetest.NewScratchRepo(t, target, "release-handoff-rejection")

	const tag = "v0.0.9-probe"
	livetest.PrepareTag(t, target, repo, tag)
	livetest.PublishBinaryAsset(t, target, repo, tag, t.TempDir())
	prelude := livetest.ProbePrelude(target, livetest.ReleaseAssetURL(t, target, repo, tag, "forge-ci"))

	body, err := yaml.Marshal(map[string]any{
		"stages":    []string{"produce", "verify"},
		"variables": map[string]string{"GIT_STRATEGY": "empty"},
		"produce": map[string]any{
			"stage": "produce", "script": []string{prelude, `set -eu
mkdir dist stale
printf 'release artifact:%s:%s\000\377\n' "$CI_PIPELINE_ID" "$CI_COMMIT_SHA" > dist/payload.bin
printf 'inert bundle bytes\n' > dist/payload.bin.bundle
./forge-ci release dist-digest --dist-dir dist > current.digest
printf 'previous build bytes\n' > stale/payload.bin
./forge-ci release dist-digest --dist-dir stale --manifest-root dist > stale.digest`},
			"artifacts": map[string]any{"paths": []string{"dist/", "current.digest", "stale.digest"}, "expire_in": "1 hour"},
		},
		"verify": map[string]any{
			"stage": "verify", "needs": []map[string]any{{"job": "produce", "artifacts": true}},
			"script": []string{prelude, `set -eu
expected=$(cat current.digest)
./forge-ci release validate-dist --dist-dir dist --expected-digest "$expected"
reject_digest() {
  if ./forge-ci release validate-dist --dist-dir dist --expected-digest "$1" > refusal.log 2>&1; then
    printf 'modified or stale handoff was accepted\n' >&2
    exit 1
  fi
  grep -F 'digest mismatch across job boundary' refusal.log
}
reject_digest "$(cat stale.digest)"
cp dist/payload.bin original.bin
printf x >> dist/payload.bin
reject_digest "$expected"
cp original.bin dist/payload.bin
./forge-ci release validate-dist --dist-dir dist --expected-digest "$expected"
printf x >> dist/payload.bin.bundle
reject_digest "$expected"`},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if conclusion := livetest.RunWorkflow(t, target, repo, "release-handoff-rejection", string(body)); conclusion != "success" {
		t.Fatalf("native release handoff rejection concluded %q", conclusion)
	}
}
