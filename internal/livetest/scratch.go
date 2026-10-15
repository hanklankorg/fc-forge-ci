// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"code.gitea.io/sdk/gitea"
	"codefloe.com/itiquette/forge-lab-contract/labclient"
	gitlabapi "gitlab.com/gitlab-org/api/client-go"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// This suite creates and destroys its own fixtures through the forge's public
// API. That is the environment's rule and it is the right one: the lab supplies
// what needs lab infrastructure — the instances, the trust, the credentials,
// the runners — and a consumer that can hold a token has no business asking it
// for a repository it could make itself.
//
// The lifecycle itself is the contract module's repository lifecycle: create inside the
// prefix, delete before create, sweep, and the teardown order each forge
// requires (owner-scoped packages on Forgejo; registry tags, then deletion,
// then permanent removal on GitLab). What stays here is the projection — an
// accepted Target, this suite's own lab-only transport, and the
// wrappers the scenarios call. None of it goes through the adapters under
// test: a broken adapter would otherwise produce a broken fixture and the
// scenario would agree with it.

// defaultBranch is the branch every scratch repository is created with, on both
// forges, so a scenario never has to ask which one it got.
const defaultBranch = "main"

const fieldPrivate = "private"

// labClients caches one module client per accepted target. Construction makes
// no request, but it builds a transport, and each lifecycle step of each
// scenario would otherwise open a connection pool of its own.
var labClients sync.Map //nolint:gochecknoglobals // Process cache keyed by forge, host and owner.

// ScratchRepo returns the name of a scratch repository for a scenario, always
// inside the namespace this suite declared it owns.
func ScratchRepo(scenario string) string { return ResourcePrefix + scenario }

// NewScratchRepo brings an empty repository into existence at a known state and
// registers its teardown.
//
// It deletes first. A previous run that died between create and cleanup would
// otherwise leave a repository whose contents no scenario planned, and "start
// from empty" would be a lie told once and believed forever after.
func NewScratchRepo(tb TB, target Target, scenario string) string {
	tb.Helper()
	requireAccepted(tb, target)

	repos, err := scratchRepos(target)
	if err != nil {
		tb.Fatalf("livetest: %v", err)
	}

	repo := ScratchRepo(scenario)
	keepOrDeleteAfter(tb, target, repo)

	ctx, cancel := context.WithTimeout(Context(tb), timeoutScratch)
	defer cancel()

	if err := repos.Ensure(ctx, repo); err != nil {
		tb.Fatalf("livetest: reset and create %s/%s: %v", target.Owner, repo, err)
	}

	return repo
}

// keepOrDeleteAfter registers a scratch repository's teardown.
func keepOrDeleteAfter(tb TB, target Target, repo string) {
	tb.Helper()

	tb.Cleanup(func() {
		// A failing in-runner scenario reports only a conclusion, and the job log
		// that would explain it lives in a repository this cleanup is about to
		// delete. Forgejo's actions API does not serve those logs by any stable
		// route, so the only way to read one is to still have the repository —
		// hence an explicit opt-out, used while diagnosing and never in a normal
		// run. Deliberately not a flag: it must be awkward enough that nobody
		// leaves it on. The next run starts by deleting the repository anyway, so
		// what is kept is one generation, not a growing pile.
		if os.Getenv("RC_LIVE_KEEP_SCRATCH") == "1" {
			retainScratchForInspection(tb, target, repo)

			return
		}

		cleanupCtx, cleanupCancel := cleanupContext(timeoutScratch)
		defer cleanupCancel()

		// Errorf, not Fatalf: cleanup runs after the scenario, and a leaked
		// repository must be visible in the result rather than swallowed.
		if err := DeleteScratchRepo(cleanupCtx, target, repo); err != nil {
			tb.Errorf("livetest: cleanup %s/%s: %v", target.Owner, repo, err)
		}
	})
}

// NewBareScratchRepo is NewScratchRepo for a repository that receives its
// history by a push: no first commit, and the object format of what will be
// pushed, because a SHA-256 history cannot enter a SHA-1 repository. Forgejo
// Actions are switched off until EnableActions. GitLab consumers supply their
// own MR-only rules and require successful, non-skipped pipelines for merging.
//
// The module's lifecycle creates an initialised SHA-1 repository and has no
// option for either, so the one creating request is made here, after the
// module's own delete and inside its prefix.
func NewBareScratchRepo(tb TB, target Target, scenario, objectFormat string, private bool) string {
	tb.Helper()
	requireAccepted(tb, target)

	if target.Forge != provider.ForgeForgejo && target.Forge != provider.ForgeGitLab {
		tb.Fatalf("livetest: bare scratch repositories are unsupported on %s", target.Forge)
	}

	repo := ScratchRepo(scenario)
	keepOrDeleteAfter(tb, target, repo)

	ctx, cancel := context.WithTimeout(Context(tb), timeoutScratch)
	defer cancel()

	if err := DeleteScratchRepo(ctx, target, repo); err != nil {
		tb.Fatalf("livetest: reset %s/%s: %v", target.Owner, repo, err)
	}

	if target.Forge == provider.ForgeGitLab {
		body := map[string]any{
			"name": repo, "path": repo, "visibility": visibilityName(private), "default_branch": defaultBranch,
			"initialize_with_readme": false, "repository_object_format": objectFormat,
			"auto_devops_enabled": false, "only_allow_merge_if_pipeline_succeeds": true,
			"allow_merge_on_skipped_pipeline": false,
		}

		var created struct {
			ObjectFormat string `json:"repository_object_format"`
		}
		if _, err := decode(ctx, target, http.MethodPost, target.BaseURL()+"/api/v4/projects", body, &created, http.StatusCreated); err != nil {
			tb.Fatalf("livetest: create %s/%s with object format %s: %v", target.Owner, repo, objectFormat, err)
		}

		if created.ObjectFormat != objectFormat {
			tb.Fatalf("livetest: GitLab created object format %q, requested %q; SHA-256 creation requires support_sha256_repositories for the lab account", created.ObjectFormat, objectFormat)
		}

		requireScratchVisibility(ctx, tb, target, repo, private)

		return repo
	}

	body := map[string]any{
		"name": repo, "default_branch": defaultBranch, fieldPrivate: private, "auto_init": false, "object_format_name": objectFormat,
	}
	if err := discard(ctx, target, http.MethodPost, target.BaseURL()+"/api/v1/user/repos", body, http.StatusCreated); err != nil {
		tb.Fatalf("livetest: create %s/%s with object format %s: %v", target.Owner, repo, objectFormat, err)
	}

	setActions(ctx, tb, target, repo, false)
	requireScratchVisibility(ctx, tb, target, repo, private)

	return repo
}

// Verify the provider's persisted visibility before any consumer source is
// pushed. A requested private repository must also refuse an anonymous API read.
func requireScratchVisibility(ctx context.Context, tb TB, target Target, repo string, private bool) {
	tb.Helper()

	endpoint := actionsURL(target, repo, "")
	if target.Forge == provider.ForgeGitLab {
		endpoint = target.BaseURL() + "/api/v4/projects/" + url.PathEscape(RepoSlug(target, repo))
	}

	var observed repositoryVisibilityState
	if _, err := decode(ctx, target, http.MethodGet, endpoint, nil, &observed, http.StatusOK); err != nil {
		tb.Fatalf("livetest: read consumer visibility: %v", err)
	}

	visibility := visibilityName(private)
	if !observed.matches(target.Forge, private) {
		tb.Fatalf("livetest: %s/%s did not confirm requested visibility %q; no consumer content was pushed", target.Owner, repo, visibility)
	}

	if private {
		anonymous := target

		anonymous.Token = ""
		if err := discard(ctx, anonymous, http.MethodGet, endpoint, nil, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound); err != nil {
			tb.Fatalf("livetest: private consumer accepted an anonymous read or its refusal could not be verified: %v", err)
		}
	}

	tb.Logf("consumer repository %s/%s id=%d visibility=%s verified before source publication", target.Owner, repo, observed.ID, visibility)
}

// EnableActions switches Actions on in a repository NewBareScratchRepo made,
// once the forge has taken in what was pushed to it.
//
// Forgejo handles a push after git has returned. Until it has, the
// repository still counts as empty, and an empty repository starts no
// workflow: a tag created in that moment is not seen, and the push itself is
// announced later, with whatever Actions setting is in force by then. So this
// waits for the repository to stop being empty. The forge records that just
// before it announces the push and gives no sign of the announcement, so one
// poll interval more passes before the switch.
func EnableActions(tb TB, target Target, repo string) {
	tb.Helper()
	requireForgejoActions(tb, target)

	ctx, cancel := context.WithTimeout(Context(tb), timeoutAPI)
	defer cancel()

	for {
		var state struct {
			Empty *bool `json:"empty"`
		}

		if _, err := decodeQuiet(ctx, target, actionsURL(target, repo, ""), &state); err == nil && state.Empty != nil && !*state.Empty {
			break
		}

		select {
		case <-ctx.Done():
			tb.Fatalf("livetest: %s/%s still counts as empty %s after the push to it", target.Owner, repo, timeoutAPI)

			return
		case <-time.After(workflowPollInterval / 4):
		}
	}

	select {
	case <-ctx.Done():
	case <-time.After(workflowPollInterval):
	}

	setActions(ctx, tb, target, repo, true)
}

func setActions(ctx context.Context, tb TB, target Target, repo string, enabled bool) {
	tb.Helper()

	if err := discard(ctx, target, http.MethodPatch, actionsURL(target, repo, ""), map[string]any{"has_actions": enabled}, http.StatusOK); err != nil {
		tb.Fatalf("livetest: set Actions of %s/%s to %t: %v", target.Owner, repo, enabled, err)
	}
}

// NewScratchRepoUnique is NewScratchRepo for scenarios whose identity is bound
// to the repository path.
//
// The ordinary scratch repository is reused by name: deleted and recreated at a
// fixed path, so a scenario always starts from a known state. For OIDC that is
// exactly wrong, and GitLab enforces it. Deleting a project records its path in
// `burned_project_routes`, and any project later created at a burned path is
// refused an ID token -- `id_token_burned_project_path`, which fails the job
// before the script runs, so it reads as a workflow fault rather than a naming
// one. The rule exists for a good reason: without it, recreating a project at a
// deleted project's path would inherit its identity with any relying party that
// trusts the path claim.
//
// So the path must be new every time, not merely unlikely to collide. It is
// derived from the clock rather than from the run id, which sounds equivalent
// and is not: the run id belongs to the CONTRACT, so every scenario run against
// one contract would share a path, and the second run would find what the first
// one burned. That is precisely how this was discovered.
//
// The cost is accepted deliberately: there is no delete-first step to lean on,
// so a run that dies between create and cleanup leaves a repository behind, and
// every run adds a burned route on the forge. Both stay inside the namespace
// this suite owns, and a lab is disposable -- which is the trade being made.
func NewScratchRepoUnique(tb TB, target Target, scenario string) string {
	tb.Helper()
	requireAccepted(tb, target)

	// Lowercase alphanumerics only. This name reaches the URLs the SDKs build
	// and the prefix check deciding what the suite may delete, so it should
	// not depend on validation happening somewhere else.
	unique := strconv.FormatInt(time.Now().UnixNano(), 36)

	// Reclaim earlier generations first. A fixed-name scratch repository is
	// reclaimed by the delete-before-create in NewScratchRepo; a unique one has
	// nothing to reclaim it, so without this every run of an OIDC scenario would
	// leave its repository behind forever. Same discipline, adapted: what cannot
	// be reused can at least be swept.
	sweepScratchRepos(tb, target, ScratchRepo(scenario)+"-")

	return NewScratchRepo(tb, target, scenario+"-"+unique)
}

// DeleteScratchRepo removes a scratch repository and what the forge keeps
// outside it. Absent is success, so it is safe as both a pre-run reset and a
// teardown. A name outside the namespace this suite owns is refused with
// labcontract.ErrOutsidePrefix before anything is sent.
func DeleteScratchRepo(ctx context.Context, target Target, repo string) error {
	repos, err := scratchRepos(target)
	if err != nil {
		return err
	}

	return repos.Delete(ctx, repo)
}

// SweepPackagesAfter removes, when the test is over, the owner's packages
// whose name starts with a scratch repository's name.
//
// Deleting a repository takes the packages named exactly as it is. A release
// that builds images also publishes beside that name, a base image as
// <repository>-base, and those would otherwise stay on the account.
func SweepPackagesAfter(tb TB, target Target, repo string) {
	tb.Helper()
	requireAccepted(tb, target)

	tb.Cleanup(func() {
		if os.Getenv("RC_LIVE_KEEP_SCRATCH") == "1" {
			return
		}

		repos, err := scratchRepos(target)
		if err != nil {
			tb.Errorf("livetest: %v", err)

			return
		}

		ctx, cancel := cleanupContext(timeoutScratch)
		defer cancel()

		if _, err := repos.SweepPackages(ctx, repo); err != nil {
			tb.Errorf("livetest: sweep the packages named %s*: %v", repo, err)
		}
	})
}

// PrepareTag gives a scratch repository a commit and a tag to release from.
//
// Neither adapter sends a target commitish: CreateRelease passes only tag_name
// (GitLab) / TagName (Forgejo), so both assume the tag already exists. That is
// correct for how releases are actually cut — CI tags a commit, then releases
// it — but it means an empty scratch repo cannot be released from, and a
// scenario that skipped this would fail for a fixture reason and read like a
// product bug.
func PrepareTag(tb TB, target Target, repo, tag string) {
	tb.Helper()
	requireAccepted(tb, target)

	ctx, cancel := context.WithTimeout(Context(tb), timeoutScratch)
	defer cancel()

	if err := prepareTag(ctx, target, repo, tag); err != nil {
		tb.Fatalf("livetest: prepare %s/%s@%s: %v", target.Owner, repo, tag, err)
	}
}

// TagCommitSHA asks the forge which commit a tag points at.
//
// The oracle for "did the checkout land on the right ref". Asking the resulting
// clone instead would be asking git about what git just did, and it is fragile
// besides: a clone made at a branch does not necessarily carry the tag object,
// so the comparison fails to run rather than failing to match.
func TagCommitSHA(tb TB, target Target, repo, tag string) string {
	tb.Helper()
	requireAccepted(tb, target)

	ctx, cancel := context.WithTimeout(Context(tb), timeoutAPI)
	defer cancel()

	sha, err := tagCommitSHA(ctx, target, repo, tag)
	if err != nil {
		tb.Fatalf("livetest: read tag %s: %v", tag, err)
	}

	return sha
}

func prepareTag(ctx context.Context, target Target, repo, tag string) error {
	client, err := scratchClient(target)
	if err != nil {
		return err
	}

	switch target.Forge {
	case provider.ForgeForgejo:
		// The module created the repository initialised, so main already has
		// a commit and only the tag is missing.
		if _, _, err = client.Gitea().CreateTag(target.Owner, repo, gitea.CreateTagOption{TagName: tag, Target: defaultBranch}); err != nil {
			return fmt.Errorf("tag %s/%s: %w", target.Owner, repo, err)
		}

		return nil

	case provider.ForgeGitLab:
		// The module created the project bare on purpose: initialize_with_readme
		// is an async worker, so a tag issued right after creation can race it.
		// The first API commit to an empty repo creates the default branch
		// synchronously, which is the deterministic path.
		if err = commitFile(ctx, target, repo, "README.md",
			"livetest: seed a commit to tag", "livetest scratch fixture\n"); err != nil {
			return err
		}

		options := &gitlabapi.CreateTagOptions{TagName: new(tag), Ref: new(defaultBranch)}
		if _, _, err = client.GitLab().Tags.CreateTag(RepoSlug(target, repo), options, gitlabapi.WithContext(ctx)); err != nil {
			return fmt.Errorf("tag %s/%s: %w", target.Owner, repo, err)
		}

		return nil

	case provider.ForgeGitHub, provider.ForgeLocal:
	}

	return fmt.Errorf("no tag preparation for platform %q: %w", target.Forge, errs.ErrUnsupported)
}

func tagCommitSHA(ctx context.Context, target Target, repo, tag string) (string, error) {
	client, err := scratchClient(target)
	if err != nil {
		return "", err
	}

	switch target.Forge {
	case provider.ForgeGitLab:
		found, _, err := client.GitLab().Tags.GetTag(RepoSlug(target, repo), tag, gitlabapi.WithContext(ctx))
		if err != nil {
			return "", err
		}

		if found.Commit == nil {
			return "", fmt.Errorf("tag %s carries no commit: %w", tag, errs.ErrValidation)
		}

		return found.Commit.ID, nil

	case provider.ForgeForgejo:
		found, _, err := client.Gitea().GetTag(target.Owner, repo, tag)
		if err != nil {
			return "", err
		}

		if found.Commit == nil {
			return "", fmt.Errorf("tag %s carries no commit: %w", tag, errs.ErrValidation)
		}

		return found.Commit.SHA, nil

	case provider.ForgeGitHub, provider.ForgeLocal:
	}

	return "", fmt.Errorf("no tag oracle for platform %q: %w", target.Forge, errs.ErrUnsupported)
}

// sweepScratchRepos deletes one scenario's generations, the repositories whose
// name starts with prefix, through the module's prefix-bounded Sweep, which
// refuses a prefix outside the namespace this suite owns.
//
// Failures are reported and not fatal: a leftover repository is untidy, and
// failing the run that noticed it would be worse than the untidiness.
func sweepScratchRepos(tb TB, target Target, prefix string) {
	tb.Helper()

	repos, err := scratchRepos(target)
	if err != nil {
		tb.Fatalf("livetest: %v", err)
	}

	ctx, cancel := context.WithTimeout(Context(tb), timeoutScratch)
	defer cancel()

	if _, err := repos.Sweep(ctx, prefix); err != nil {
		tb.Logf("livetest: could not sweep %s/%s*: %v", target.Owner, prefix, err)
	}
}

// scratchRepos is the prefix-bounded lifecycle for an accepted Target. Every
// name it creates, deletes or lists sits under ResourcePrefix; the module
// refuses anything else with labcontract.ErrOutsidePrefix.
func scratchRepos(target Target) (*labclient.Repositories, error) {
	if !target.accepted {
		return nil, fmt.Errorf("refusing scratch lifecycle through an unaccepted target: %w", errs.ErrValidation)
	}

	client, err := scratchClient(target)
	if err != nil {
		return nil, err
	}

	return client.Repositories(ResourcePrefix)
}

// scratchClient is the module client for an accepted Target: the contract
// endpoint it was projected from, the SDK for its kind, and this suite's own
// lab-only transport with the frozen CA in place of the module's, so a
// credential reaches exactly the hosts every other call of the kit may reach.
func scratchClient(target Target) (*labclient.Client, error) {
	key := string(target.Forge) + "@" + target.Host + "/" + target.Owner

	if cached, ok := labClients.Load(key); ok {
		if client, isClient := cached.(*labclient.Client); isClient {
			return client, nil
		}
	}

	httpClient, err := targetHTTPClient(target, timeoutAPI)
	if err != nil {
		return nil, fmt.Errorf("configure target HTTP trust: %w", err)
	}

	acceptedAt := func() time.Time { return target.acceptedAt }

	client, err := labclient.New(target.contract, target.endpoint,
		labclient.WithHTTPClient(httpClient), labclient.WithClock(acceptedAt))
	if err != nil {
		return nil, fmt.Errorf("configure %s lab client: %w", target.Forge, err)
	}

	if cached, loaded := labClients.LoadOrStore(key, client); loaded {
		if existing, isClient := cached.(*labclient.Client); isClient {
			return existing, nil
		}
	}

	return client, nil
}
