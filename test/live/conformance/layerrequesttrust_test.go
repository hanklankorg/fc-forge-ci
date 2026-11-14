// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	_ "embed"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

//go:embed testdata/layer/release-request-untrusted.yml
var layerRequestTrustFixture string

// The two ways a consumer's trust can fail to cover a correctly signed
// request, each with the refusal the engine gives for it.
// requestTrustRefusals are the ways the consumer's trust fails to cover the
// request: a file naming another key (anotherKey), or no file.
var requestTrustRefusals = []struct {
	name, refusal string
	anotherKey    bool
}{
	{"wrong-allowed-signer", "signature is valid but signer is not in allowed_signers", true},
	{"missing-allowed-signers", "verify-release-request: allowed signers must be a non-empty regular file without symlinks: " + requestAllowedSigners, false},
}

// requestAllowedSigners is where the layer reads a consumer's trust from by
// default, in the commit the request names.
const requestAllowedSigners = ".forgejo/release-allowed-signers"

// LAYER-6: prepare-release.yml refuses a signed request the consumer does not
// trust, before the bot's key is loaded.
//
// The registry smoke shows a trusted signed request released. Here the request
// is signed as well, by the same kind of key and pushed with git, and it is
// the consumer's allowed-signers file that does not cover it: the file names
// the requester's principal with a key that did not sign the tag, or there is
// no file. Everything else a release needs is real, the secrets, the bot's
// deploy key and the SSH route, so a request the layer admitted would become
// a release commit and a final tag.
//
// The check is a step of the job that signs: validate-request runs in
// "Prepare: release commit and tag", after the changelog is rendered and
// before the step that holds the key. So the run is expected to FAIL there
// and only there: every other job succeeded, that job failed on a first
// attempt, its log gives the refusal of the case and no account of a verified
// request, no log holds a key or the passphrase, main is still the requested
// commit, the request tag is the only tag, and there is no release and no
// package. The rendered changelog is the one artifact; it is uploaded before
// the refusal and holds nothing signed.
func TestForgejoLayer_PrepareReleaseRefusesAnUntrustedRequest(t *testing.T) {
	livetest.RegisterLiveScenario(t)

	for _, visibility := range livetest.ConsumerVisibilities(t) {
		t.Run(visibility.Name, func(t *testing.T) {
			for _, trust := range requestTrustRefusals {
				t.Run(trust.name, func(t *testing.T) { runLayerRequestTrust(t, visibility.Private, trust.refusal, trust.anotherKey) })
			}
		})
	}
}

func runLayerRequestTrust(t *testing.T, private bool, refusal string, anotherKey bool) {
	t.Helper()

	const (
		workflow   = "layer-request-trust"
		name       = "layer-request-trust"
		caller     = "prepare"
		signingJob = "Prepare: release commit and tag"
	)

	signer, ok := livetest.SignerImage(t)
	if !ok {
		t.Skip("no signer image, so the prepare jobs cannot reach the request check")
	}

	gitURL, fingerprint, ok := livetest.SSHRoute(t, livetest.Accept(t, provider.ForgeForgejo), livetest.ScratchRepo(name))
	if !ok {
		t.Skip("no SSH route, so an admitted request could not be pushed and the refusal would show nothing")
	}

	scenario := newLayerScenario(t, name, private, "sha1")
	target, repo := scenario.target, scenario.repo

	keys := newReleaseKeys(t)
	requester := livetest.NewRequestSigner(t, t.TempDir())
	livetest.AddDeployKey(t, target, repo, "livetest release bot", keys.sshPublic)

	secrets := map[string]string{
		"RELEASE_SSH_SIGNING_KEY":    keys.sshPrivate,
		"RELEASE_COSIGN_PRIVATE_KEY": keys.cosignPrivate,
		"RELEASE_COSIGN_PASSPHRASE":  keys.passphrase,
		"RELEASE_TOKEN":              target.Token,
	}
	for secret, value := range secrets {
		livetest.SetRepoSecret(t, target, repo, secret, value)
	}

	files := map[string]string{
		".chglog/config-keepachangelog.yml":    chglogKeepConfig,
		".chglog/CHANGELOG-keepachangelog.tpl": chglogKeepTemplate,
		".chglog/config-minimal.yml":           chglogMinimalConfig,
		".chglog/CHANGELOG-minimal.tpl":        chglogMinimalTemplate,
		"CHANGELOG.md":                         releaseCanaryChangelog,
		".forgejo/workflows/" + workflow + ".yml": scenario.render(t, layerRequestTrustFixture,
			"RENDER_SIGNER_IMAGE", signer,
			"RENDER_RELEASE_GIT_URL", gitURL,
			"RENDER_RELEASE_GIT_HOST_KEY_FINGERPRINT", fingerprint,
		),
	}

	// Another signer's line is the requester's principal with a key that did
	// not sign the request. The other case commits no file at all.
	if anotherKey {
		files[requestAllowedSigners] = livetest.NewRequestSigner(t, t.TempDir()).AllowedSigners
	}

	request := livetest.CommitFiles(t, target, repo, livetest.FileCommit{
		Branch: layerMain, Message: "feat: add the untrusted release request canary", Files: files,
	})

	packages := livetest.OwnerPackages(t, target)

	requester.PushTag(t, target, repo, releaseRequestTag, layerMain, "Request release v0.0.1")

	run := livetest.WaitForLayerRun(t, target, repo, workflow, "push")
	jobs := scenario.requireStatus(t, run, jobFailure, secrets)

	// Every job of the called workflow ran on the runner; the calling job has
	// none and ends blocked or failed with the workflow it called.
	onRunner, wrappers := requireJobsOnRunner(t, jobs, run.ID, scenario.label)
	if len(onRunner) < 3 || len(wrappers) != 1 || wrappers[0] != caller {
		t.Fatalf("the run's jobs are %v on the runner and %v without one, want the prepare jobs under one caller", onRunner, wrappers)
	}

	refused := false

	for _, job := range jobs {
		switch job.Name {
		case caller:
			if job.Status != "blocked" && job.Status != jobFailure {
				t.Errorf("the calling job ended %q, want blocked or failed with its workflow", job.Status)
			}

			continue
		case signingJob:
			refused = job.Status == jobFailure
		default:
			if job.Status != jobSuccess {
				t.Errorf("job %q is %s, want only the signing job to refuse the request", job.Name, job.Status)
			}
		}

		log := livetest.JobLog(t, target, repo, job)
		requireNoReleaseKey(t, job.Name, log, keys)

		if job.Name != signingJob {
			continue
		}

		if !strings.Contains(log, refusal) {
			t.Errorf("the signing job's log does not give the refusal %q", refusal)
		}

		for _, admitted := range []string{"Release request signature verified", "Release request validated WITHOUT signature verification"} {
			if strings.Contains(log, admitted) {
				t.Errorf("the signing job's log says %q of a request the consumer does not trust", admitted)
			}
		}
	}

	if !refused {
		t.Errorf("job %q did not fail, so the request was not refused where the key is loaded", signingJob)
	}

	// The request tag is the requester's and stays; the final tag would be
	// the bot's.
	scenario.requireBranches(t, map[string]string{layerMain: request})
	scenario.requireNone(t, "/releases")
	scenario.requireSamePackages(t, packages)

	if tags := livetest.RepoCount(t, target, repo, "/tags"); tags != 1 {
		t.Errorf("the repository has %d tags, want only the request tag", tags)
	}

	artifacts := livetest.RunArtifacts(t, target, repo, run.ID)
	if len(artifacts) != 1 || artifacts[0].Name != "release-changelog" {
		t.Errorf("the refused run left artifacts %+v, want only the rendered changelog", artifacts)
	}

	if keysOnRepo := livetest.RepoCount(t, target, repo, "/keys"); keysOnRepo != 1 {
		t.Errorf("the repository has %d deploy keys, want only the bot's", keysOnRepo)
	}
}
