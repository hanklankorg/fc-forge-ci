// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gitlabapi "gitlab.com/gitlab-org/api/client-go"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/livetest"
	"github.com/diggsweden/forge-ci/internal/retry"
)

func TestGitLabRelease_BaseImagesColdAndWarm(t *testing.T) {
	for _, visibility := range livetest.ConsumerVisibilities(t) {
		t.Run(visibility.Name, func(t *testing.T) {
			runGitLabReleaseScenario(t, "", true, visibility.Private)
		})
	}
}

func requireGitLabBaseReuse(t *testing.T, client *gitlabapi.Client, target livetest.Target, repo, publication string, keys releaseKeys, requestKeyDir string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(livetest.Context(t), 45*time.Minute)
	defer cancel()

	baseTag := strings.Repeat("b", 64) + "-go"
	digest, found := livetest.ImageDigest(t, target, publication+"/bases", baseTag)
	if !found {
		t.Fatal("cold base publication did not create its immutable final tag")
	}
	registry, err := livetest.RegistryHost(target)
	if err != nil {
		t.Fatal(err)
	}
	ref := registry + "/" + livetest.RepoSlug(target, publication) + "/bases@" + digest
	auth := livetest.RegistryAuthConfigDir(t, target, t.TempDir())
	verifyCosignSignature(t, target, ref, keys.cosignPublicPath, auth)
	verifyCosignAttestations(t, target, ref, keys.cosignPublicPath, auth, digest)

	const requestTag, finalTag = "release-request/v0.0.2", "v0.0.2"
	request := mergeGitLabWarmCandidate(t, client, target, repo)
	livetest.SignGitLabRequest(t, target, repo, request, requestTag, filepath.Join(requestKeyDir, "lab-ssh"))
	run := waitGitLabTagPipeline(t, ctx, client, livetest.RepoSlug(target, repo), requestTag, request)
	child := requireGitLabReleaseChild(t, ctx, client, livetest.RepoSlug(target, repo), run)
	requireReleaseGit(t, target, repo, keys, request, requestTag, finalTag)
	verified := requireGitLabReleaseBundles(t, target, repo, keys, finalTag)
	if actual, exists := livetest.ImageDigest(t, target, publication+"/bases", baseTag); !exists || actual != digest {
		t.Fatal("warm release changed the immutable base digest")
	}
	for _, tc := range []struct {
		tag   string
		found bool
	}{{releaseFinalTag, false}, {finalTag, true}} {
		body := livetest.Download(t, target, livetest.ReleaseAssetURL(t, target, repo, tc.tag, "base-verification.json"), maxReleaseFile)
		var evidence struct {
			AllFound bool `json:"all_found"`
		}
		if err := json.Unmarshal(body, &evidence); err != nil || evidence.AllFound != tc.found {
			t.Fatalf("base reuse evidence for %s: %v", tc.tag, err)
		}
	}
	t.Logf("base cold publication and verified warm reuse: %s; warm parent %d child %d, %d bundles", ref, run.ID, child, verified)
}

// Prepared release commits carry [skip ci]. The next request must name a new
// candidate, admitted through normal MR merge authority rather than unprotecting
// main or changing the release bot's generated message.
func mergeGitLabWarmCandidate(t *testing.T, client *gitlabapi.Client, target livetest.Target, repo string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(livetest.Context(t), 5*time.Minute)
	defer cancel()
	project := livetest.RepoSlug(target, repo)
	const branch = "next-base-release"
	commit, _, err := client.Commits.CreateCommit(project, &gitlabapi.CreateCommitOptions{
		Branch: gitlabapi.Ptr(branch), StartBranch: gitlabapi.Ptr("main"), CommitMessage: gitlabapi.Ptr("test: request warm base reuse"),
		Actions: []*gitlabapi.CommitActionOptions{{Action: gitlabapi.Ptr(gitlabapi.FileCreate), FilePath: gitlabapi.Ptr("warm-request.txt"), Content: gitlabapi.Ptr("base inputs remain unchanged\n")}},
	}, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	mr, _, err := client.MergeRequests.CreateMergeRequest(project, &gitlabapi.CreateMergeRequestOptions{
		SourceBranch: gitlabapi.Ptr(branch), TargetBranch: gitlabapi.Ptr("main"), Title: gitlabapi.Ptr("Request verified warm base reuse"),
	}, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	if err = retry.Run(ctx, nil, 90, 2*time.Second, func() error {
		current, _, readErr := client.MergeRequests.GetMergeRequest(project, mr.IID, nil, gitlabapi.WithContext(ctx))
		if readErr != nil {
			return retry.Permanent(readErr)
		}
		if current.DetailedMergeStatus != "mergeable" {
			return fmt.Errorf("merge request is %s: %w", current.DetailedMergeStatus, errs.ErrDependencyUnavailable)
		}

		return nil
	}, retry.WithBackoff(retry.Constant)); err != nil {
		t.Fatal(err)
	}
	merged, _, err := client.MergeRequests.AcceptMergeRequest(project, mr.IID, &gitlabapi.AcceptMergeRequestOptions{
		SHA: gitlabapi.Ptr(commit.ID), MergeCommitMessage: gitlabapi.Ptr("test: merge warm base candidate"), ShouldRemoveSourceBranch: gitlabapi.Ptr(true),
	}, gitlabapi.WithContext(ctx))
	if err != nil || merged.MergeCommitSHA == "" {
		t.Fatalf("merge warm candidate: %v", err)
	}

	return merged.MergeCommitSHA
}

// One tiny amd64 scratch image, no consumer toolchain or release matrix. The
// second request must find verified evidence and never enter the build branch.
const gitlabBaseBuild = `# Verification uses an isolated subprocess, including on a warm-only build.
# A tool-manager shim needing additional environment is not a usable verifier.
env -i HOME="${HOME}" PATH="${PATH}" cosign version
mkdir dist
printf '%s\n' "${COSIGN_PUBLIC_KEY%$'\n'}" > "${RC_WORK}/base-key.pub"
printf '%s\n' "${RC_PUBLIC_KEY_SHA256}" > "${RC_WORK}/base-key.sha256"
forge-ci platform stage-public-key --source "${RC_WORK}/base-key.pub" \
  --digest-file "${RC_WORK}/base-key.sha256" --workspace "$PWD" --destination .base-trust.pub
printf 'go\n' > .base-flavors
base_id=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
forge-ci --format json container base-images validate --expected-repository "${RC_PUBLICATION_IMAGE_REPOSITORY}/bases" \
  --expected-source "${CI_PROJECT_URL}" --expected-workflow gitlab/release.yml \
  --cosign-public-key-path .base-trust.pub --cosign-public-key-sha256 "${RC_PUBLIC_KEY_SHA256}" \
  --flavors-file .base-flavors --base-input-id "${base_id}" > dist/base-verification.json
if jq -e '.all_found == true' dist/base-verification.json >/dev/null; then
  test "${RELEASE_TAG}" = v0.0.2
  printf 'reused\n' > dist/base-use.txt
else
  test "${RELEASE_TAG}" = v0.0.1
  mkdir -m 0700 "${RC_WORK}/buildah"
  export CONTAINERS_STORAGE_CONF="${RC_WORK}/buildah/storage.conf" TMPDIR="${RC_WORK}/buildah/tmp"
  forge-ci container setup-buildah --install-packages=false --temp-dir "${RC_WORK}/buildah" \
    --storage-conf "${CONTAINERS_STORAGE_CONF}" --storage-root "${RC_WORK}/buildah/storage" \
    --tmp-dir "${TMPDIR}" --env-file "${RC_WORK}/buildah.env"
  mkdir "${RC_WORK}/base-context"
  printf 'one immutable base fixture\n' > "${RC_WORK}/base-context/base.txt"
  printf 'FROM scratch\nCOPY base.txt /base.txt\n' > "${RC_WORK}/base-context/Containerfile"
  buildah build --isolation chroot --network host --pull=never --platform linux/amd64 \
    --timestamp 946684800 --manifest localhost/rc-base-fixture "${RC_WORK}/base-context"
  auth="${RC_WORK}/registry-auth.json"
  printf '%s' "${CI_REGISTRY_PASSWORD}" | forge-ci container login --registry "${CI_REGISTRY}" \
    --registry-username "${CI_REGISTRY_USER}" --registry-password-file - --auth-file "${auth}"
  digest=$(forge-ci container manifest push --local-manifest localhost/rc-base-fixture \
    --destination "${CI_REGISTRY_IMAGE}/bases:staging-${base_id}-go" --auth-file "${auth}")
  forge-ci container ledger add --ledger dist/base-images.json --image-kind base --role base --flavor go \
    --base-input-id "${base_id}" --image-name "${CI_REGISTRY_IMAGE}/bases" --digest "${digest}" \
    --final-tag-name "${base_id}-go" --derive-candidate-tag --default-sbom
  printf 'built\n' > dist/base-use.txt
fi
printf '{"bomFormat":"CycloneDX","specVersion":"1.6","version":1,"metadata":{"component":{"type":"file","name":"base-use.txt"}}}\n' > dist/fixture-sbom.json
forge-ci release notes --changelog-file CHANGELOG.md --release-version "$RELEASE_TAG" --target-file dist/release-notes.md
forge-ci release files declare --dist-dir dist --asset dist/base-use.txt --sbom dist/fixture-sbom.json \
  --checksums-file dist/checksums.txt --evidence-file dist/base-verification.json --evidence-file dist/release-notes.md
`
