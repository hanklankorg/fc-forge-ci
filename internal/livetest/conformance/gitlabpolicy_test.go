// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	gitlabapi "gitlab.com/gitlab-org/api/client-go"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
)

// The policy must be outside the consumer's editable tree. This is a native
// GitLab configuration/variable/artifact proof with an inert canary, not a
// cryptographic signing test or a claim about privileged runner containment.
func TestGitLabPolicy_ConsumerCannotReplaceTheSigningPipeline(t *testing.T) {
	if !livetest.Requires(t, provider.ForgeGitLab, livetest.NeedsInRunner) {
		livetest.NotCoveredf(t, "external release policy requires a GitLab runner")
	}

	target := livetest.Accept(t, provider.ForgeGitLab)
	policyRepo := livetest.NewScratchRepo(t, target, "release-policy")
	consumerRepo := livetest.NewScratchRepo(t, target, "policy-consumer")
	project := livetest.RepoSlug(target, consumerRepo)

	client, err := gitlabapi.NewClient(target.Token, gitlabapi.WithBaseURL(target.BaseURL()+"/api/v4/"),
		gitlabapi.WithHTTPClient(livetest.HTTPClient(t, target, time.Minute)))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(livetest.Context(t), 8*time.Minute)
	defer cancel()

	policyProject := livetest.RepoSlug(target, policyRepo)
	policy := gitlabPolicyCommit(t, ctx, client, policyProject, "policy.yml", gitlabTrustedPolicy, gitlabapi.FileCreate)
	coordinate := "policy.yml@" + policyProject + ":" + policy.ID

	settings, _, err := client.Projects.EditProject(project, &gitlabapi.EditProjectOptions{
		CIConfigPath:                           gitlabapi.Ptr(coordinate),
		CIPipelineVariablesMinimumOverrideRole: gitlabapi.Ptr(gitlabapi.CIPipelineVariablesMinimumOverrideRoleValue("no_one_allowed")),
		CIPushRepositoryForJobTokenAllowed:     gitlabapi.Ptr(false),
	}, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	if settings.CIConfigPath != coordinate || settings.CIPipelineVariablesMinimumOverrideRole != "no_one_allowed" || settings.CIPushRepositoryForJobTokenAllowed {
		t.Fatal("GitLab did not retain the external pinned policy and variable/token restrictions")
	}

	livetest.RestrictGitLabJobToken(t, target, consumerRepo)

	// Moving the policy project's default branch must not move the consumer's
	// authenticated policy revision. Consumer YAML must not merge into it.
	gitlabPolicyCommit(t, ctx, client, policyProject, "policy.yml", "wrong-policy:\n  script: exit 97\n", gitlabapi.FileUpdate)
	gitlabPolicyCommit(t, ctx, client, project, "build.sh", gitlabUntrustedBuild, gitlabapi.FileCreate)

	consumer := gitlabPolicyCommit(t, ctx, client, project, ".gitlab-ci.yml", gitlabUntrustedPolicy, gitlabapi.FileCreate)
	if _, _, seedErr := client.Releases.CreateRelease(project, &gitlabapi.CreateReleaseOptions{
		TagName: gitlabapi.Ptr("v0.0.0-sentinel"), Ref: gitlabapi.Ptr(consumer.ID), Description: gitlabapi.Ptr("untouched publication sentinel"),
	}, gitlabapi.WithContext(ctx)); seedErr != nil {
		t.Fatal(seedErr)
	}

	if _, _, setErr := client.ProjectVariables.CreateVariable(project, &gitlabapi.CreateProjectVariableOptions{
		Key: gitlabapi.Ptr("RELEASE_COSIGN_PRIVATE_KEY"), Value: gitlabapi.Ptr("inert-signing-policy-canary"),
		Protected: gitlabapi.Ptr(true), Masked: gitlabapi.Ptr(true), EnvironmentScope: gitlabapi.Ptr("release-signing"),
	}, gitlabapi.WithContext(ctx)); setErr != nil {
		t.Fatal(setErr)
	}

	if _, _, protectErr := client.ProtectedTags.ProtectRepositoryTags(project, &gitlabapi.ProtectRepositoryTagsOptions{
		Name: gitlabapi.Ptr("release-request/*"), CreateAccessLevel: gitlabapi.Ptr(gitlabapi.MaintainerPermissions),
	}, gitlabapi.WithContext(ctx)); protectErr != nil {
		t.Fatal(protectErr)
	}

	const tag = "release-request/v0.0.1"
	if _, _, tagErr := client.Tags.CreateTag(project, &gitlabapi.CreateTagOptions{
		TagName: gitlabapi.Ptr(tag), Ref: gitlabapi.Ptr(consumer.ID), Message: gitlabapi.Ptr("Request the policy fixture release"),
	}, gitlabapi.WithContext(ctx)); tagErr != nil {
		t.Fatal(tagErr)
	}

	run := waitGitLabTagPipeline(t, ctx, client, project, tag, consumer.ID)
	if run.Status != "success" {
		logGitLabTrace(t, gitlabTrialTrace(t, ctx, client, project, run.ID))
		t.Fatalf("the external policy pipeline ended %s", run.Status)
	}

	jobs, _, err := client.Jobs.ListPipelineJobs(project, run.ID, nil, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	names := make([]string, 0, len(jobs))
	for _, job := range jobs {
		if job.Status != "success" || job.AllowFailure {
			t.Fatalf("required policy job %s ended %s", job.Name, job.Status)
		}

		names = append(names, job.Name)
	}

	slices.Sort(names)

	if !slices.Equal(names, []string{"build-from-policy", "sign-from-policy"}) {
		t.Fatalf("the consumer replaced or extended the policy jobs: %v", names)
	}

	releases, _, err := client.Releases.ListReleases(project, nil, gitlabapi.WithContext(ctx))
	if err != nil || len(releases) != 1 || releases[0].TagName != "v0.0.0-sentinel" || releases[0].Description != "untouched publication sentinel" {
		t.Fatal("the source job changed the publication sentinel")
	}

	packages, _, err := client.Packages.ListProjectPackages(project, nil, gitlabapi.WithContext(ctx))
	if err != nil || len(packages) != 0 {
		t.Fatal("the source job published a package despite its read-only self-scope")
	}

	_, response, overrideErr := client.Pipelines.CreatePipeline(project, &gitlabapi.CreatePipelineOptions{
		Ref: gitlabapi.Ptr(tag), Variables: gitlabapi.Ptr([]*gitlabapi.PipelineVariableOptions{{Key: gitlabapi.Ptr("CI_CONFIG_PATH"), Value: gitlabapi.Ptr(".gitlab-ci.yml")}}),
	}, gitlabapi.WithContext(ctx))
	if overrideErr == nil || response == nil || (response.StatusCode != http.StatusBadRequest && response.StatusCode != http.StatusForbidden) ||
		!strings.Contains(overrideErr.Error(), "pipeline variables") {
		t.Fatal("GitLab did not refuse caller-supplied pipeline variable overrides")
	}

	t.Logf("policy %s stayed pinned; pipeline %d used only trusted jobs, kept the canary out of source execution, and rejected variable overrides", policy.ID, run.ID)
}

func gitlabPolicyCommit(t *testing.T, ctx context.Context, client *gitlabapi.Client, project, path, body string, operation gitlabapi.FileActionValue) *gitlabapi.Commit {
	t.Helper()

	commit, _, err := client.Commits.CreateCommit(project, &gitlabapi.CreateCommitOptions{
		Branch: gitlabapi.Ptr("main"), CommitMessage: gitlabapi.Ptr("livetest: stage policy fixture"),
		Actions: []*gitlabapi.CommitActionOptions{{Action: gitlabapi.Ptr(operation), FilePath: gitlabapi.Ptr(path), Content: gitlabapi.Ptr(body)}},
	}, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	return commit
}

// Registry authorization is separate from the REST fine-grained permissions.
// Even an Owner-triggered build must not mutate the publication project.
func TestGitLabPolicy_JobTokenCannotWriteThePublicationRegistry(t *testing.T) {
	if !livetest.Requires(t, provider.ForgeGitLab, livetest.NeedsInRunner) {
		livetest.NotCoveredf(t, "registry publication policy requires a GitLab runner")
	}

	target := livetest.Accept(t, provider.ForgeGitLab)
	publication := livetest.NewScratchRepo(t, target, "publication-registry")
	build := livetest.NewScratchRepo(t, target, "publication-producer")
	livetest.RestrictGitLabJobToken(t, target, build)
	sentinel := livetest.PushImage(t, target, publication, "v0.0.0-sentinel")

	const fixtureTag = "v0.0.1-probe"
	livetest.PrepareTag(t, target, publication, fixtureTag)
	assets := livetest.PublishRegistryAuthAssets(t, target, publication, fixtureTag, t.TempDir())
	probe := assets.Asset(t, "probe-json", livetest.ReleaseAssetURL(t, target, publication, fixtureTag, "probe-json"))
	publicationImage := strings.TrimSuffix(sentinel.Ref, ":v0.0.0-sentinel")

	workflow := strings.NewReplacer(
		"PROBE_IMAGE", livetest.ProbeImage,
		"PROBE_URL", probe.URL,
		"PROBE_SHA256", probe.SHA256,
		"PUBLICATION_IMAGE", publicationImage,
		"SENTINEL_DIGEST", sentinel.Digest,
	).Replace(gitlabRegistryBoundaryWorkflow)
	if outcome := livetest.RunWorkflow(t, target, build, "registry-boundary", workflow); outcome != "success" {
		t.Fatalf("job-token publication boundary ended %s", outcome)
	}

	if actual, present := livetest.ImageDigest(t, target, publication, "v0.0.0-sentinel"); !present || actual != sentinel.Digest {
		t.Fatal("the source job changed the publication sentinel")
	}

	if _, present := livetest.ImageDigest(t, target, build, "staging-ci-boundary"); !present {
		t.Fatal("the positive-control staging write did not arrive")
	}
}

func waitGitLabTagPipeline(t *testing.T, ctx context.Context, client *gitlabapi.Client, project, tag, sha string) *gitlabapi.Pipeline {
	t.Helper()

	for {
		runs, _, err := client.Pipelines.ListProjectPipelines(project, &gitlabapi.ListProjectPipelinesOptions{Ref: gitlabapi.Ptr(tag)}, gitlabapi.WithContext(ctx))
		if err != nil {
			t.Fatal(err)
		}

		if len(runs) > 1 {
			t.Fatal("the release request unexpectedly created more than one pipeline")
		}

		if len(runs) == 1 {
			run, _, readErr := client.Pipelines.GetPipeline(project, runs[0].ID, gitlabapi.WithContext(ctx))
			if readErr != nil {
				t.Fatal(readErr)
			}

			if run.SHA != sha || run.Source != "push" {
				t.Fatal("the release pipeline is not bound to its tag event and exact source")
			}

			switch run.Status {
			case "success", "failed", "canceled", "skipped", "manual":
				return run
			}
		}

		select {
		case <-ctx.Done():
			t.Fatal("the release policy pipeline did not finish within its bounded wait")
		case <-time.After(2 * time.Second):
		}
	}
}

const gitlabTrustedPolicy = `workflow:
  rules:
    - if: '$CI_COMMIT_TAG && $CI_COMMIT_REF_PROTECTED == "true"'
stages: [build, sign]
default:
  image: ` + livetest.ProbeImage + `
  cache: []
  before_script: []
variables:
  GIT_STRATEGY: empty
build-from-policy:
  stage: build
  variables:
    GIT_STRATEGY: clone
  script:
    - bash build.sh
  artifacts:
    paths: [out/]
sign-from-policy:
  stage: sign
  environment:
    name: release-signing
    action: prepare
  needs:
    - job: build-from-policy
      artifacts: true
  script:
    - 'test -n "${RELEASE_COSIGN_PRIVATE_KEY:-}"'
    - 'test -z "${BUILD_ENV_POISON:-}"'
    - 'test -z "${BASH_ENV:-}"'
    - 'test "$(cat out/data.txt)" = build-data'
`

const gitlabUntrustedPolicy = `steal-signing-key:
  environment: release-signing
  script:
    - 'test -n "${RELEASE_COSIGN_PRIVATE_KEY:-}"'
    - exit 99
`

const gitlabUntrustedBuild = `set -eu
test -z "${RELEASE_COSIGN_PRIVATE_KEY:-}"
mkdir out
printf 'BUILD_ENV_POISON=untrusted\nBASH_ENV=out/startup.sh\n' > out/poison.env
printf 'exit 98\n' > out/startup.sh
printf 'build-data\n' > out/data.txt
request() {
  method=$1
  path=$2
  shift 2
  printf 'header = "JOB-TOKEN: %s"\n' "$CI_JOB_TOKEN" |
    curl --config - --silent --show-error --max-time 30 --cacert "$CI_SERVER_TLS_CA_FILE" \
      --output /dev/null --write-out '%{http_code}' --request "$method" "$CI_API_V4_URL/$path" "$@"
}
test "$(request GET job)" = 200
deny() {
  code=$(request "$@")
  case "$code" in 403|404) ;; *) echo "write was not denied: HTTP $code" >&2; exit 1 ;; esac
}
deny POST "projects/$CI_PROJECT_ID/releases" --header 'Content-Type: application/json' \
  --data '{"tag_name":"release-request/v0.0.1","name":"unauthorized release"}'
deny PUT "projects/$CI_PROJECT_ID/releases/v0.0.0-sentinel" --header 'Content-Type: application/json' \
  --data '{"description":"overwritten by source job"}'
deny DELETE "projects/$CI_PROJECT_ID/releases/v0.0.0-sentinel"
deny PUT "projects/$CI_PROJECT_ID/packages/generic/policy-probe/1/data.txt" --data-binary @out/data.txt
`

const gitlabRegistryBoundaryWorkflow = `registry-boundary:
  image: PROBE_IMAGE
  variables:
    GIT_STRATEGY: empty
  script:
    - |
      set -eu
      curl --fail --silent --show-error --cacert "$CI_SERVER_TLS_CA_FILE" -o probe-json 'PROBE_URL'
      printf '%s  probe-json\n' 'PROBE_SHA256' | sha256sum --check --strict
      chmod 0500 probe-json
      export SSL_CERT_FILE="$CI_SERVER_TLS_CA_FILE"
      auth=$(printf '%s:%s' "$CI_REGISTRY_USER" "$CI_JOB_TOKEN" | base64 -w0)
      printf '{"auths":{"%s":{"auth":"%s"}}}\n' "$CI_REGISTRY" "$auth" |
        ./probe-json registry-boundary "$CI_REGISTRY_IMAGE" 'PUBLICATION_IMAGE' 'SENTINEL_DIGEST'
      unset auth
`
