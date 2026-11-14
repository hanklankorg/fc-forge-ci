// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"bufio"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"codefloe.com/itiquette/forge-lab-contract/githubclient"
	"codefloe.com/itiquette/forge-lab-contract/githubcontext"
	"github.com/google/go-github/v91/github"

	"github.com/diggsweden/forge-ci/internal/adapters/httpretry"
	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/diggsweden/forge-ci/internal/retry"
)

// GitHubContextEnv names the private context Forge Lab's hosted session
// writes: the organization, the actor, the resource prefix and the token.
const GitHubContextEnv = "FORGE_LAB_GITHUB_CONTEXT"

// GitHubRunTimeout bounds the wait for a consumer's workflow run.
const GitHubRunTimeout = 90 * time.Minute

const (
	githubPoll           = 20 * time.Second
	githubEngineTimeout  = 75 * time.Minute
	githubRunAppears     = 5 * time.Minute
	githubIndexTimeout   = 5 * time.Minute
	githubIssuer         = "https://token.actions.githubusercontent.com"
	githubEngineWorkflow = "self-runtime-container.yml"
	githubTrialAuthor    = "Forge Lab Trial"
	githubTrialEmail     = "forge-lab-trial@hosted.invalid"
	maxFailureLogBytes   = 32 << 20
	goTestFailAction     = "fail"
)

var (
	errGitHubJobLogRequest = errors.New("could not prepare the job log request")
	errGitHubJobLogFetch   = errors.New("could not fetch the job log")
)

// GitHubTrial is one hosted trial: the organization the context authorized,
// and the client, which refuses repositories outside the context's prefix.
type GitHubTrial struct {
	tb      TB
	client  *githubclient.Client
	api     *github.Client
	org     string
	prefix  string
	session string
	// actor is the login the context's token belongs to.
	actor string
	token string
	// history makes PushConsumer push the consumer's own history under the
	// rendered commit: WithHistory.
	history bool
	// files are trial-made files PushConsumer commits beside the rendered
	// workflows, by repository path: WithFile.
	files map[string]string
	// keyWorkflow is the fixture release workflow PushConsumer hands the
	// trial's Cosign key: WithTrialKeySigning.
	keyWorkflow string
	// secrets keeps scenario-owned values available for native log redaction.
	secrets map[string]map[string]string
	// registry is the image registry's base URL; empty is https://ghcr.io.
	registry string
	// mavenReadBack is set once the release oracle found a Maven package of
	// the trial's repository at the released version; the cleanup must then
	// remove one.
	mavenReadBack bool
}

// OpenGitHubTrial loads Forge Lab's hosted context and authorizes exactly the
// organization ID the caller named.
func OpenGitHubTrial(tb TB, organizationID int64) *GitHubTrial {
	tb.Helper()

	if _, err := readAdminInputs(os.Getenv); err != nil {
		tb.Fatalf("livetest: administrator policy: %v", err)
	}

	path := os.Getenv(GitHubContextEnv)
	if path == "" {
		tb.Skipf("no hosted GitHub context; run inside `just github run` of Forge Lab, which sets %s", GitHubContextEnv)
	}

	document, err := githubcontext.Load(path)
	if err != nil {
		tb.Fatalf("livetest: hosted GitHub context: %v", err)
	}

	client, err := githubclient.New(tb.Context(), document, organizationID)
	if err != nil {
		tb.Fatalf("livetest: hosted GitHub organization %d: %v", organizationID, err)
	}

	tb.Cleanup(client.Close)

	return &GitHubTrial{tb: tb, client: client, api: client.API(), org: document.Organization.Login, prefix: document.Prefix,
		session: document.Session[:8], actor: document.Actor.Login, token: document.Token}
}

// EnsureEngine puts this checkout's HEAD on main of <prefix>forge-ci, waits
// for Self Runtime Container to build its images and move the rolling
// pre-release to it, and returns the engine the consumer is rendered against.
//
// HEAD is pushed, not the working tree: commit the change under test, or
// check out a throwaway branch with it. main of the engine repository is
// overwritten; the repository is the trial's own, under the context prefix.
func (trial *GitHubTrial) EnsureEngine(root, platforms string) GitHubEngine {
	trial.tb.Helper()

	commit := strings.TrimSpace(trial.git(root, "rev-parse", "HEAD"))
	if len(commit) != 40 {
		trial.tb.Fatalf("livetest: the engine checkout must be SHA-1 for GitHub; HEAD is %q", commit)
	}

	admin, err := newWorkflowAdmin(trial.tb.Context(), root, commit, os.Getenv)
	if err != nil {
		trial.tb.Fatalf("livetest: administrator policy: %v", err)
	}

	ctx := trial.tb.Context()
	name := trial.prefix + "forge-ci"

	_, response, lookupErr := trial.api.Repositories.Get(ctx, trial.org, name)
	missing := response != nil && response.StatusCode == http.StatusNotFound

	switch {
	case lookupErr == nil:
	case !missing:
		trial.tb.Fatalf("livetest: look up the engine repository %s: %v", name, lookupErr)
	default:
		if _, createErr := trial.client.CreateRepository(ctx, name, false); createErr != nil {
			trial.tb.Fatalf("livetest: create the engine repository %s: %v", name, createErr)
		}
	}

	// The runtime images' platforms are this run's, set before the push that
	// builds them; the default comes back when the trial ends, so a selection
	// never outlives its run.
	trial.SetVariable(name, "RELEASE_PLATFORMS", platforms)

	if platforms != githubDefaultPlatforms {
		trial.tb.Cleanup(func() { trial.SetVariable(name, "RELEASE_PLATFORMS", githubDefaultPlatforms) })
	}

	namespace := "ghcr.io/" + strings.ToLower(trial.org+"/"+name)
	engine := GitHubEngine{Owner: trial.org, Repository: name, Commit: commit, Namespace: namespace, admin: admin}

	packageNames, err := engine.RuntimePackageNames()
	if err != nil {
		trial.tb.Fatalf("livetest: invalid runtime package coordinates: %v", err)
	}

	trial.tb.Logf("retained engine package coordinates (no deletion authority): %v", packageNames)
	trial.SetVariable(name, "FORGE_CI_IMAGE_NAMESPACE", namespace)
	trial.SetVariable(name, "FORGE_CI_REPOSITORY", trial.org+"/"+name)
	// Keyless signing writes to the public transparency log, permanently. A
	// trial writes nothing there (ErrPublicTransparency), so its engine's
	// images and CLI are built unsigned; every workflow admits them by digest.
	trial.SetVariable(name, "FORGE_CI_COSIGN_TRANSPARENCY", cosignTransparencyNone)
	trial.push(root, "--force", trial.remote(name), "HEAD:refs/heads/main")

	run := trial.engineRun(name, commit, true)

	if run.GetConclusion() != runSuccess {
		trial.ReportRun(name, run)
		trial.tb.Fatalf("livetest: Self Runtime Container for %s ended %s; the engine's images are not usable", commit, run.GetConclusion())
	}

	engine.Digests = trial.resolveEngineDigests(engine)
	trial.tb.Logf("engine under test: %s/%s at %s, images %s/forge-ci-runtime*:%s", trial.org, name, commit, engine.Registry(), engine.ImageTag())

	return engine
}

// ReuseEngine reads an engine prepared by EnsureEngine in this same hosted
// session. The caller must prepare all required image platforms before starting
// parallel consumers and keep the engine frozen until they all finish. Unlike
// EnsureEngine this does not push, change variables, or register engine cleanup.
func (trial *GitHubTrial) ReuseEngine(root string) GitHubEngine {
	trial.tb.Helper()

	commit := strings.TrimSpace(trial.git(root, "rev-parse", "HEAD"))
	if len(commit) != 40 {
		trial.tb.Fatalf("livetest: the engine checkout must be SHA-1 for GitHub; HEAD is %q", commit)
	}

	admin, err := newWorkflowAdmin(trial.tb.Context(), root, commit, os.Getenv)
	if err != nil {
		trial.tb.Fatalf("livetest: administrator inputs: %v", err)
	}

	name := trial.prefix + "forge-ci"

	ref, _, err := trial.api.Git.GetRef(trial.tb.Context(), trial.org, name, "heads/main")
	if err != nil || ref.GetObject().GetSHA() != commit {
		trial.tb.Fatalf("livetest: prepared engine %s main is not at %s: %v", name, commit, err)
	}

	run := trial.engineRun(name, commit, false)
	if run.GetConclusion() != runSuccess {
		trial.ReportRun(name, run)
		trial.tb.Fatalf("livetest: prepared engine build at %s ended %s", commit, run.GetConclusion())
	}

	trial.tb.Logf("reusing prepared engine without mutations: %s/%s at %s", trial.org, name, commit)

	engine := GitHubEngine{Owner: trial.org, Repository: name, Commit: commit, Namespace: "ghcr.io/" + strings.ToLower(trial.org+"/"+name), admin: admin}
	engine.Digests = trial.resolveEngineDigests(engine)

	return engine
}

// WithHistory returns the trial pushing the consumer's own history under the
// rendered commit, as a demo does. A trial pushes one parentless commit.
func (trial *GitHubTrial) WithHistory() *GitHubTrial {
	shown := *trial
	shown.history = true

	return &shown
}

// WithFile makes PushConsumer commit content at path in the consumer, in
// place of any file the fixture has there: material the trial itself makes,
// such as the allowed-signers line of a key it generated.
func (trial *GitHubTrial) WithFile(path, content string) *GitHubTrial {
	shown := *trial
	shown.files = maps.Clone(trial.files)

	if shown.files == nil {
		shown.files = map[string]string{}
	}

	shown.files[path] = content

	return &shown
}

// NewDemoRepository creates the public repository <prefix>demo-<name> for a demo and
// registers nothing to delete it. The client refuses a name that exists, so an
// earlier demo is never adopted or replaced; Forge Lab's session cleanup
// deletes only the repositories it created itself, by their IDs.
func (trial *GitHubTrial) NewDemoRepository(name string) string {
	trial.tb.Helper()

	if !validDemoName(name) {
		trial.tb.Fatalf("livetest: %q cannot name a demo (lowercase letters, digits and inner hyphens)", name)
	}

	repoName := trial.prefix + DemoRepo(name)

	if _, err := trial.client.CreateRepository(trial.tb.Context(), repoName, false); err != nil {
		trial.tb.Fatalf("livetest: create the demo repository %s (an existing one is never replaced): %v", repoName, err)
	}

	return repoName
}

// DeleteSecretsAfter removes, whatever the outcome, the repository secrets
// named, so a repository that is kept holds no credential once the run is over.
func (trial *GitHubTrial) DeleteSecretsAfter(repo string, names []string) {
	trial.tb.Helper()

	trial.tb.Cleanup(func() {
		ctx, cancel := cleanupContext(timeoutAPI)
		defer cancel()

		for _, name := range names {
			response, err := trial.api.Actions.DeleteRepoSecret(ctx, trial.org, repo, name)
			if err != nil && (response == nil || response.StatusCode != http.StatusNotFound) {
				trial.tb.Errorf("livetest: delete secret %s of %s: %v", name, repo, err)
			}
		}
	})
}

// NewConsumerRepository creates <prefix><name>-<session> and removes it when
// the test ends. The container packages it published stay: a fine-grained
// token cannot reach GitHub's packages API, so they are left to the
// organization's sweep. The session in the name keeps a trial clear of them:
// a package outlives its repository, and a new repository of the same name
// is denied a push to the package the old one created.
func (trial *GitHubTrial) NewConsumerRepository(name string, private bool) string {
	trial.tb.Helper()

	// demo- names belong to demos, which a trial must never be mistaken for.
	if strings.HasPrefix(name, demoRepoPrefix) {
		trial.tb.Fatalf("livetest: trial repository names may not start %s, which marks demos", demoRepoPrefix)
	}

	repoName := trial.prefix + name + "-" + trial.session

	repository, err := trial.client.CreateRepository(trial.tb.Context(), repoName, private)
	if repository != nil && repository.GetID() > 0 {
		id := repository.GetID()

		trial.tb.Cleanup(func() { trial.removeConsumer(repoName, id) })
	}

	if err != nil {
		trial.tb.Fatalf("livetest: create %s: %v", repoName, err)
	}

	return repoName
}

// GitHubConsumerTree is the trial's working copy of the rendered consumer.
type GitHubConsumerTree struct {
	Dir  string
	Main string
}

// PushConsumer pushes HEAD of the consumer checkout to main of repo, with the
// fixture workflows of fixtures/workflows rendered into .github/workflows in
// place of the consumer's own. GitHub has no SHA-256 repositories, so the tree
// is committed afresh in a SHA-1 repository; the checkout is not written.
func (trial *GitHubTrial) PushConsumer(repo, root, fixtures string, engine GitHubEngine, overrides map[string]string) GitHubConsumerTree {
	trial.tb.Helper()

	dir, workflows := trial.stageConsumer(root, fixtures, engine, overrides)
	trial.push(dir, trial.remote(repo), "HEAD:refs/heads/main")
	trial.waitWorkflows(repo, workflows)

	return GitHubConsumerTree{Dir: dir, Main: strings.TrimSpace(trial.git(dir, "rev-parse", "HEAD"))}
}

func (trial *GitHubTrial) stageConsumer(root, fixtures string, engine GitHubEngine, overrides map[string]string) (string, string) { //nolint:funcorder // keep the staging helper beside PushConsumer.
	trial.tb.Helper()

	dir := trial.tempDir("consumer")
	git := githubStageGit()

	source, staged, err := githubStage(trial.tb.Context(), git, root, trial.tempDir("objects"), dir, trial.history)
	if err != nil {
		trial.tb.Fatalf("livetest: stage consumer: %v", err)
	}

	// Bind native draft policy inputs in the imported SHA-1 tree, never the
	// source checkout. These coordinates identify the frozen engine for source
	// assertions; GitHub executes the fixture workflows, not Forgejo actions.
	staged.git.env = append(slices.Clone(staged.git.env),
		"GIT_AUTHOR_NAME="+consumerAuthor, "GIT_AUTHOR_EMAIL="+requestSigner,
		"GIT_COMMITTER_NAME="+consumerAuthor, "GIT_COMMITTER_EMAIL="+requestSigner,
		"GIT_AUTHOR_DATE=2000-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z")

	staged.Commit, err = staged.commit(trial.tb.Context(), consumerBaseMessage)
	if err != nil {
		trial.tb.Fatalf("livetest: snapshot imported consumer: %v", err)
	}

	nativeLayer, err := githubNativePolicyLayer(trial.tb.Context(), staged, engine)
	if err != nil {
		trial.tb.Fatalf("livetest: native policy checksum: %v", err)
	}

	staged = staged.WithDraftLayer(trial.tb, nativeLayer)

	prefix := filepath.ToSlash(filepath.Join(fixtures, "workflows")) + "/"

	fixtureWorkflows, err := githubFixtureWorkflows(trial.tb.Context(), source, prefix)
	if err != nil {
		trial.tb.Fatalf("livetest: list fixture blobs: %v", err)
	}

	if err = validateGitHubGPGFixtureSource(trial.tb.Context(), source, fixtures, fixtureWorkflows, overrides); err != nil {
		trial.tb.Fatalf("livetest: fixture profile: %v", err)
	}

	applied := trial.stageFixtureWorkflows(staged, fixtureWorkflows, engine, overrides)

	for _, input := range sortedKeys(overrides) {
		if !applied[input] {
			trial.tb.Fatalf("livetest: no fixture passes %s", input)
		}
	}

	for _, path := range sortedKeys(trial.files) {
		if err = staged.stageFile(trial.tb.Context(), "100644", path, trial.files[path]); err != nil {
			trial.tb.Fatalf("livetest: %v", err)
		}
	}

	if err = staged.renderNPMScope(trial.tb.Context(), "@"+strings.ToLower(trial.org)+"/"); err != nil {
		trial.tb.Fatalf("livetest: render the npm scope: %v", err)
	}

	message := consumerBaseMessage
	if trial.history {
		message = demoBaseMessage
	}

	trial.commit(dir, message, "")
	// Only Git's checkout writes the imported tree; no source links are read.
	trial.git(dir, "checkout-index", "--all", "--force")
	workflows := filepath.Join(dir, ".github", "workflows")

	return dir, workflows
}

// githubNativePolicyLayer selects checksum data for native source assertions.
// A pinned engine reader supplies its recorded CLI digest, not publication
// authentication. Without that reader, preserve the native policy's checksum;
// it is NOT evidence that the selected engine's binary matches it. GitHub runs
// only the separately rendered fixture workflows, never these native calls.
func githubNativePolicyLayer(ctx context.Context, consumer Consumer, engine GitHubEngine) (Layer, error) {
	layer := Layer{ServerURL: "https://github.com", Repository: engine.Owner + "/" + engine.Repository,
		Commit: engine.Commit, admin: engine.admin}

	listed, err := consumer.git.output(ctx, consumer.stage, nil, "ls-tree", "-z", "--name-only", consumer.Commit, consumerWorkflows+"/")
	if err != nil {
		return Layer{}, err
	}

	hasWorkflow := false

	for path := range strings.SplitSeq(string(listed), "\x00") {
		if isWorkflowFile(path) {
			hasWorkflow = true

			break
		}
	}

	if !hasWorkflow {
		return layer, nil
	}

	workflows, err := consumer.workflows(ctx)
	if err != nil {
		return Layer{}, err
	}

	draft := false
	for _, workflow := range workflows {
		draft = draft || strings.Contains(workflow, "@PIN_FORGE_CI_SHA")
	}

	if !draft {
		return layer, nil
	}

	pin, err := ReadConsumerPin(workflows)
	if err != nil {
		return Layer{}, err
	}

	if pin.Commit != "PIN_FORGE_CI_SHA" {
		return layer, nil
	}
	// Do not give PlanConsumer an empty replacement for a native digest. The
	// preserved value is source policy data, not a selected-engine trust fact.
	layer.BinarySHA256 = pin.BinarySHA256
	if pin.BinarySHA256 == "" || engine.admin == nil {
		return layer, nil
	}

	digest, err := frozenEngineChecksum(engine)
	if err != nil {
		return Layer{}, err
	}

	layer.BinarySHA256 = digest

	return layer, nil
}

// frozenEngineChecksum reads the recorded CLI digest of the frozen engine
// commit through its pinned reader.
func frozenEngineChecksum(engine GitHubEngine) (string, error) {
	if engine.admin.commit != engine.Commit || engine.admin.read == nil {
		return "", fmt.Errorf("checksum reader does not identify the frozen engine commit: %w", errs.ErrValidation)
	}

	line, err := engine.admin.read(layerChecksum)
	if err != nil {
		return "", fmt.Errorf("read frozen engine checksum: %w", err)
	}

	digest, name, found := strings.Cut(strings.TrimSuffix(string(line), "\n"), "  ")
	if decoded, decodeErr := hex.DecodeString(digest); !found || name != "bin/forge-ci" || decodeErr != nil || len(decoded) != sha256.Size {
		return "", fmt.Errorf("%s is not one `<sha256>  bin/forge-ci` line: %w", layerChecksum, errs.ErrValidation)
	}

	return digest, nil
}

// stageFixtureWorkflows renders every workflow from the admitted fixture directory
// into the staged consumer and returns the overridden inputs they pass.
func (trial *GitHubTrial) stageFixtureWorkflows(staged Consumer, workflows map[string]string, engine GitHubEngine, overrides map[string]string) map[string]bool { //nolint:funcorder // keep the staging helper beside stageConsumer.
	trial.tb.Helper()

	applied := map[string]bool{}

	for _, name := range sortedKeys(workflows) {
		text := workflows[name]

		if name == trial.keyWorkflow {
			rekeyed, err := TrialKeyRelease(text)
			if err != nil {
				trial.tb.Fatalf("livetest: %s: %v", name, err)
			}

			text = rekeyed
		}

		rendered, inputs, renderErr := RenderGitHubConsumer(name, text, engine, overrides)
		if renderErr != nil {
			trial.tb.Fatalf("livetest: %v", renderErr)
		}

		if err := staged.stageFile(trial.tb.Context(), "100644", ".github/workflows/"+name, rendered); err != nil {
			trial.tb.Fatalf("livetest: %v", err)
		}

		for _, input := range inputs {
			applied[input] = true
		}
	}

	return applied
}

// PushChange pushes one commit on branch, from main, for a pull request: the
// change the local trials make. It adds one file, has a Conventional subject
// and a sign-off, and is signed with an SSH key made for it, because the
// consumer lints the commits it reviews and may require a signature on each.
func (trial *GitHubTrial) PushChange(repo string, tree GitHubConsumerTree, branch string) string {
	trial.tb.Helper()

	trial.git(tree.Dir, "checkout", gitQuiet, "-b", branch, tree.Main)

	if err := os.WriteFile(filepath.Join(tree.Dir, consumerCanary), []byte(consumerCanaryText), 0o644); err != nil { //nolint:gosec // a committed file is world-readable by convention.
		trial.tb.Fatalf("livetest: write the change under review: %v", err)
	}

	keyDir := trial.tempDir("signer")
	SSHKey(trial.tb, keyDir)

	trial.git(tree.Dir, "add", "--", consumerCanary)
	trial.commit(tree.Dir, consumerChangeMessage, filepath.Join(keyDir, sshKeyFile))
	trial.push(tree.Dir, trial.remote(repo), "HEAD:refs/heads/"+branch)

	head := strings.TrimSpace(trial.git(tree.Dir, "rev-parse", "HEAD"))
	trial.git(tree.Dir, "checkout", gitQuiet, defaultBranch)

	return head
}

// OpenPullRequest opens branch against main and returns its number.
func (trial *GitHubTrial) OpenPullRequest(repo, branch string) int {
	trial.tb.Helper()

	pull, _, err := trial.api.PullRequests.Create(trial.tb.Context(), trial.org, repo, github.CreatePullRequest{
		Title: new("Hosted trial: pull-request gate"), Head: branch, Base: defaultBranch,
		Body: new("Forge Lab hosted consumer trial. Disposable."),
	})
	if err != nil {
		trial.tb.Fatalf("livetest: open the pull request from %s: %v", branch, err)
	}

	return pull.GetNumber()
}

// MergePullRequest merges the pull request natively with a merge commit, the
// forge told which head commit is meant, and returns the merge commit.
func (trial *GitHubTrial) MergePullRequest(repo string, number int, head string) string {
	trial.tb.Helper()

	result, _, err := trial.api.PullRequests.Merge(trial.tb.Context(), trial.org, repo, number, "Merge the change under review",
		&github.PullRequestOptions{SHA: head, MergeMethod: "merge"})
	if err != nil || !result.GetMerged() || len(result.GetSHA()) != 40 {
		trial.tb.Fatalf("livetest: merge pull request #%d of %s at %s: %v", number, repo, head, err)
	}

	return result.GetSHA()
}

// CheckMerge checks what the native merge did, as RequireMergeOf does for the
// lab forges: main is at merge, branch still at head, and the working copy,
// fetched to main, shows merge to be a merge commit of base and head with
// head's tree.
func (trial *GitHubTrial) CheckMerge(repo string, tree GitHubConsumerTree, branch, merge, base, head string) {
	trial.tb.Helper()

	ctx := trial.tb.Context()

	for name, want := range map[string]string{defaultBranch: merge, branch: head} {
		ref, _, err := trial.api.Repositories.GetBranch(ctx, trial.org, repo, name, 0)
		if err != nil || ref.GetCommit().GetSHA() != want {
			trial.tb.Fatalf("livetest: %s of %s is %s, want %s: %v", name, repo, ref.GetCommit().GetSHA(), want, err)
		}
	}

	if tree.Main != merge {
		trial.tb.Fatalf("livetest: the working copy's main is %s, want the merge %s", tree.Main, merge)
	}

	git := func(ctx context.Context, args ...string) (string, error) {
		output, err := trial.outputContext(ctx, tree.Dir, trial.gitEnv(), "git", append([]string{"-C", tree.Dir}, args...)...)

		return strings.TrimSpace(output), err
	}

	if err := gitMergeOf(ctx, git, merge, base, head); err != nil {
		trial.tb.Fatalf("livetest: the merge %s %v", merge, err)
	}
}

// FetchMain brings the trial's working copy to main of repo as the forge has
// it, after a merge the forge made, and returns the tree at it.
func (trial *GitHubTrial) FetchMain(repo string, tree GitHubConsumerTree) GitHubConsumerTree {
	trial.tb.Helper()

	trial.gitRemote(tree.Dir, "fetch", gitQuiet, trial.remote(repo), "refs/heads/"+defaultBranch)
	trial.git(tree.Dir, "checkout", gitQuiet, "-B", defaultBranch, "FETCH_HEAD")

	return GitHubConsumerTree{Dir: tree.Dir, Main: strings.TrimSpace(trial.git(tree.Dir, "rev-parse", "HEAD"))}
}

// GitHubInventory counts what a repository holds.
type GitHubInventory struct {
	Runs, Tags, Releases int
}

// RepositoryInventory counts the workflow runs, tags and releases of repo.
func (trial *GitHubTrial) RepositoryInventory(repo string) GitHubInventory {
	trial.tb.Helper()

	ctx := trial.tb.Context()
	page := github.ListOptions{PerPage: 100}

	runs, _, err := trial.api.Actions.ListRepositoryWorkflowRuns(ctx, trial.org, repo, &github.ListWorkflowRunsOptions{ListOptions: page})
	if err != nil {
		trial.tb.Fatalf("livetest: list the runs of %s: %v", repo, err)
	}

	tags, _, err := trial.api.Repositories.ListTags(ctx, trial.org, repo, &page)
	if err != nil {
		trial.tb.Fatalf("livetest: list the tags of %s: %v", repo, err)
	}

	releases, _, err := trial.api.Repositories.ListReleases(ctx, trial.org, repo, &page)
	if err != nil {
		trial.tb.Fatalf("livetest: list the releases of %s: %v", repo, err)
	}

	return GitHubInventory{Runs: runs.GetTotalCount(), Tags: len(tags), Releases: len(releases)}
}

// SetVariable creates or updates a repository Actions variable.
func (trial *GitHubTrial) SetVariable(repo, name, value string) {
	trial.tb.Helper()

	ctx := trial.tb.Context()

	response, err := trial.api.Actions.CreateRepoVariable(ctx, trial.org, repo, github.ActionsCreateVariableRequest{Name: name, Value: value})
	if err != nil && response != nil && response.StatusCode == http.StatusConflict {
		_, err = trial.api.Actions.UpdateRepoVariable(ctx, trial.org, repo, name, github.ActionsUpdateVariableRequest{Value: new(value)})
	}

	if err != nil {
		trial.tb.Fatalf("livetest: set variable %s on %s: %v", name, repo, err)
	}
}

// SetSecret seals value to the repository's Actions key and stores it.
func (trial *GitHubTrial) SetSecret(repo, name, value string) {
	trial.tb.Helper()

	ctx := trial.tb.Context()

	key, _, err := trial.api.Actions.GetRepoPublicKey(ctx, trial.org, repo)
	if err != nil {
		trial.tb.Fatalf("livetest: Actions public key of %s: %v", repo, err)
	}

	sealed, err := SealGitHubSecret(key.GetKey(), value)
	if err != nil {
		trial.tb.Fatalf("livetest: %v", err)
	}

	_, err = trial.api.Actions.CreateOrUpdateRepoSecret(ctx, trial.org, repo, name, github.SecretRequest{KeyID: key.GetKeyID(), EncryptedValue: sealed})
	if err != nil {
		trial.tb.Fatalf("livetest: set secret %s on %s: %s", name, repo, strings.ReplaceAll(err.Error(), value, "[redacted]"))
	}

	if trial.secrets == nil {
		trial.secrets = map[string]map[string]string{}
	}

	if trial.secrets[repo] == nil {
		trial.secrets[repo] = map[string]string{}
	}

	trial.secrets[repo][name] = value
}

// Token is the context's management/Packages token, not a release credential.
func (trial *GitHubTrial) Token() string { return trial.token }

// WaitRun waits for the newest run of workflow for event at headSHA to finish
// and returns it, including a cancelled terminal run. A cancelled one-dispatch
// consumer will not be replaced, so ignoring it stalls cleanup until timeout.
func (trial *GitHubTrial) WaitRun(repo, workflow, event, headSHA string, timeout time.Duration) *github.WorkflowRun {
	trial.tb.Helper()

	started := time.Now()
	deadline := started.Add(timeout)
	appeared := false

	for {
		runs, _, err := trial.api.Actions.ListWorkflowRunsByFileName(trial.tb.Context(), trial.org, repo, workflow,
			&github.ListWorkflowRunsOptions{HeadSHA: headSHA, Event: event})
		if err == nil {
			for _, run := range runs.WorkflowRuns {
				appeared = true

				if run.GetStatus() == "completed" {
					return run
				}

				break
			}
		}

		// GitHub creates a run within seconds of its event, or never.
		if !appeared && time.Since(started) > githubRunAppears {
			trial.tb.Fatalf("livetest: GitHub created no %s run of %s for %s at %s within %s: it starts no run for an event it dropped",
				event, workflow, repo, headSHA, githubRunAppears)
		}

		if time.Now().After(deadline) {
			trial.tb.Fatalf("livetest: no finished %s run of %s for %s at %s within %s", event, workflow, repo, headSHA, timeout)
		}

		trial.sleep()
	}
}

// ReportRun prints each job's conclusion (or observed status when absent) and
// bounded log diagnostics for non-successful jobs.
func (trial *GitHubTrial) ReportRun(repo string, run *github.WorkflowRun) {
	trial.tb.Helper()

	ctx := trial.tb.Context()

	jobs, err := trial.listRunJobs(ctx, repo, run)
	if err != nil {
		trial.tb.Errorf("livetest: list jobs of run %d: %v", run.GetID(), err)

		return
	}

	for _, job := range jobs.Jobs {
		status := job.GetConclusion()
		if status == "" {
			status = job.GetStatus()
		}

		trial.tb.Logf("  %-9s %s", status, job.GetName())

		switch job.GetConclusion() {
		case runSuccess:
			// A passing job is evidence too, and cleanup deletes the
			// repository that holds its log, so it is retained whenever an
			// evidence directory is set.
			if snapshotEvidenceRoot() != "" {
				trial.retainPassingJobLog(ctx, repo, run, job)
			}

			continue
		case runSkipped, "neutral":
			continue
		}

		trial.reportFailedJob(ctx, repo, run, job)
	}

	if len(jobs.Jobs) == 0 {
		trial.reportRefChecks(ctx, repo, run.GetHeadSHA())
	}
}

// A run GitHub refused before any job (startup_failure) has no job to ask.
// What GitHub says of it, if anything, is on the checks of the run's commit:
// print each one's conclusion, title and summary, and its annotations.
func (trial *GitHubTrial) reportRefChecks(ctx context.Context, repo, sha string) { //nolint:funcorder // keep failure diagnostics beside their caller.
	checks, _, err := trial.api.Checks.ListCheckRunsForRef(ctx, trial.org, repo, sha, &github.ListCheckRunsOptions{PerPage: 20})
	if err != nil {
		trial.tb.Logf("  (no job ran; the checks of %s are unavailable: %v)", sha, err)

		return
	}

	trial.tb.Logf("  no job ran; %d checks on %s", checks.GetTotal(), sha)

	for _, check := range checks.CheckRuns {
		summary := check.GetOutput().GetSummary()
		if len(summary) > 2048 {
			summary = strings.ToValidUTF8(summary[:2048], "\uFFFD") + " [truncated]"
		}

		trial.tb.Logf("  check %s: %s %q %s", check.GetName(), cmp.Or(check.GetConclusion(), check.GetStatus()), check.GetOutput().GetTitle(), summary)

		annotations, _, err := trial.api.Checks.ListCheckRunAnnotations(ctx, trial.org, repo, check.GetID(), &github.ListOptions{PerPage: 10})
		if err != nil {
			trial.tb.Logf("    check annotations unavailable: %v", err)

			continue
		}

		for _, annotation := range annotations {
			message := annotation.GetMessage()
			if len(message) > 2048 {
				message = strings.ToValidUTF8(message[:2048], "\uFFFD") + " [truncated]"
			}

			trial.tb.Logf("    check annotation: %s", message)
		}
	}
}

// Jobs rejected before a runner starts may have no log archive. GitHub records
// the cause in check annotations; query that exact check through the SDK rather
// than following a response-provided URL with credentials.
func (trial *GitHubTrial) reportJobAnnotations(ctx context.Context, repo string, job *github.WorkflowJob) { //nolint:funcorder // keep failure diagnostics beside their caller.
	prefix := "https://api.github.com/repos/" + trial.org + "/" + repo + "/check-runs/"

	value, found := strings.CutPrefix(job.GetCheckRunURL(), prefix)
	if !found {
		return
	}

	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return
	}

	annotations, _, err := trial.api.Checks.ListCheckRunAnnotations(ctx, trial.org, repo, id, &github.ListOptions{PerPage: 10})
	if err != nil {
		trial.reportAnnotationFailure(repo, job, err)

		return
	}

	for _, annotation := range annotations {
		message := annotation.GetMessage()
		if len(message) > 2048 {
			message = strings.ToValidUTF8(message[:2048], "�") + " [truncated]"
		}

		trial.tb.Logf("    check annotation: %s", message)

		if strings.Contains(message, "The job was not started because recent account payments have failed or your spending limit needs to be increased") {
			trial.tb.Logf("    GitHub billing blocked job execution: check failed payments and Actions spending limits in Billing & plans before retrying; consumer code did not run in this job")
		}
	}
}

func (trial *GitHubTrial) reportAnnotationFailure(repo string, job *github.WorkflowJob, err error) { //nolint:funcorder // keep failure diagnostics beside their caller.
	trial.tb.Logf("    check annotations unavailable: %v", err)

	var responseError *github.ErrorResponse
	if errors.As(err, &responseError) && responseError.Response != nil && responseError.Response.StatusCode == http.StatusForbidden {
		trial.tb.Logf("    fine-grained PATs cannot read the Checks API; adding PAT permissions will not enable annotations")
	}

	trial.tb.Logf("    inspect Summary > Annotations at https://github.com/%s/%s/actions/runs/%d (disposable trial repositories are removed during cleanup)", trial.org, repo, job.GetRunID())

	if job.GetRunnerID() == 0 && len(job.Steps) == 0 {
		trial.tb.Logf("    GitHub reported no runner or steps: the job may have been rejected before execution. Check account payments and Actions spending limits in Billing & plans; billing is a possible cause, not confirmed by missing logs")
	}
}

// failureOf fetches a pre-signed log URL without the token and returns the
// lines that explain the failure.
func failureOf(ctx context.Context, location string, lines int) string {
	reader, err := openGitHubJobLog(ctx, location)
	if err != nil {
		return err.Error()
	}
	defer func() { _ = reader.Close() }()

	return readFailureExcerpt(reader, lines)
}

func openGitHubJobLog(ctx context.Context, location string) (io.ReadCloser, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil) //nolint:gosec // G704: pre-signed URL comes from the authenticated GitHub job-log API, not repository content.
	if err != nil {
		return nil, errGitHubJobLogRequest
	}

	response, err := (&http.Client{Timeout: time.Minute}).Do(request) //nolint:gosec // G704: fetch the GitHub-issued pre-signed log URL without an API token.
	if err != nil {
		// The URL is pre-signed; transport errors can include its credentials.
		return nil, fmt.Errorf("%w: class=%s", errGitHubJobLogFetch, githubJobLogErrorClass(err))
	}

	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()

		return nil, fmt.Errorf("%w: HTTP %d class=http-status", errGitHubJobLogFetch, response.StatusCode)
	}

	return response.Body, nil
}

// Classify without formatting transport errors, which may contain signed URLs.
func githubJobLogErrorClass(err error) string {
	var networkError net.Error

	switch {
	case errors.Is(err, context.Canceled):
		return "context-canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "unexpected-eof"
	case errors.As(err, &networkError) && networkError.Timeout():
		return "timeout"
	default:
		return "io-or-transport"
	}
}

func readFailureExcerpt(reader io.Reader, lines int) string {
	var all []string

	limited := &io.LimitedReader{R: reader, N: maxFailureLogBytes}
	scanner := bufio.NewScanner(limited)
	// Progress output can put well over Scanner's default 64 KiB on one line.
	// Keep reading to the actual error, within the existing total log budget.
	// Leave one byte of scanner space to confirm EOF after an exact-budget
	// final record without a newline; the reader still bounds content at 32 MiB.
	scanner.Buffer(make([]byte, 64<<10), maxFailureLogBytes+1)

	for scanner.Scan() {
		all = append(all, scanner.Text())
	}

	excerpt := FailureExcerpt(all, lines)
	for index, line := range excerpt {
		if len(line) > 2048 {
			excerpt[index] = strings.ToValidUTF8(line[:512], "�") +
				" ... [line shortened; see full job log] ... " + strings.ToValidUTF8(line[len(line)-1536:], "�")
		}
	}

	if scanner.Err() != nil {
		excerpt = append(excerpt, "[job log incomplete: could not read the remaining data]")
	} else if limited.N == 0 {
		// A full budget is not proof of truncation. Probe one byte without
		// including it in the excerpt or exposing an underlying read error.
		var probe [1]byte

		if count, err := io.ReadFull(reader, probe[:]); count != 0 {
			excerpt = append(excerpt, "[job log may be truncated at the 32 MiB read limit]")
		} else if !errors.Is(err, io.EOF) {
			excerpt = append(excerpt, "[job log incomplete: could not read the remaining data]")
		}
	}

	return strings.Join(excerpt, "\n")
}

// FailureExcerpt picks the lines up to the first native Go test failure or
// ##[error] annotation. A job's last lines are
// its post-run cleanup, Harden Runner's agent log among them, which once hid
// the error of a failed publication entirely. Without an annotation it falls
// back to the end of the log.
func FailureExcerpt(log []string, lines int) []string {
	if lines <= 0 {
		return nil
	}

	end := len(log)

	for index, line := range log {
		if strings.Contains(line, "##[error]") || goTestFailureLine(line) {
			end = index + 1

			break
		}
	}

	return log[max(0, end-lines):end]
}

// Native go test JSON is often followed by many successful packages before the
// runner emits its final annotation. Keep the first failing test's context.
func goTestFailureLine(line string) bool {
	start := strings.IndexByte(line, '{')
	if start < 0 || !strings.Contains(line[start:], `"fail"`) {
		return false
	}

	var event struct {
		Action  string `json:"Action"`
		Package string `json:"Package"`
	}

	return json.Unmarshal([]byte(line[start:]), &event) == nil && event.Action == goTestFailAction && event.Package != ""
}

// CosignTrust is what a trial's Cosign signatures verify against: the engine's
// keyless workflow identity, or the public key of a trial key pair.
type CosignTrust struct {
	identity string
	key      string
}

// KeylessTrust trusts signatures the engine's reusable workflows made keyless.
func KeylessTrust(engine GitHubEngine) CosignTrust {
	return CosignTrust{identity: engine.KeylessIdentityRegexp()}
}

func (trust CosignTrust) String() string {
	if trust.key != "" {
		return "the trial's Cosign key"
	}

	return trust.identity
}

// verifyArgs are the cosign verify flags of the trust. A key-signed release
// keeps its signatures out of the public log (transparency: none), so there is
// no log entry to require.
func (trust CosignTrust) verifyArgs() []string {
	if trust.key != "" {
		return []string{"--key", trust.key, "--insecure-ignore-tlog=true"}
	}

	return []string{"--certificate-identity-regexp", trust.identity, "--certificate-oidc-issuer", githubIssuer}
}

// TrialCosignKey is a passphrase-protected Cosign key pair made for one trial:
// the consumer signs its release with it, and the trial verifies against it.
type TrialCosignKey struct {
	Passphrase string
	Private    string
	Public     string
	// SHA256 is the public key's digest as `sha256sum cosign.pub` prints it,
	// the value a workflow writes beside the key it reads from a variable.
	SHA256 string
	path   string
}

// NewTrialCosignKey makes the key pair with cosign itself, so the private key
// has the encrypted form a real release secret has.
func (trial *GitHubTrial) NewTrialCosignKey() TrialCosignKey {
	trial.tb.Helper()

	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		trial.tb.Fatalf("livetest: %v", err)
	}

	dir := trial.tempDir("cosign")
	key := TrialCosignKey{Passphrase: hex.EncodeToString(raw), path: filepath.Join(dir, "cosign.pub")}
	trial.run(dir, []string{"COSIGN_PASSWORD=" + key.Passphrase}, HostTool(trial.tb, "cosign"), "generate-key-pair")

	private, err := os.ReadFile(filepath.Join(dir, "cosign.key")) //nolint:gosec // the key pair cosign just wrote to the trial's own directory.
	if err != nil {
		trial.tb.Fatalf("livetest: read the trial Cosign key: %v", err)
	}

	public, err := os.ReadFile(key.path)
	if err != nil {
		trial.tb.Fatalf("livetest: read the trial Cosign public key: %v", err)
	}

	digest := sha256.Sum256(public)
	key.Private, key.Public, key.SHA256 = string(private), string(public), hex.EncodeToString(digest[:])

	return key
}

// Trust trusts signatures made with this key pair.
func (key TrialCosignKey) Trust() CosignTrust { return CosignTrust{key: key.path} }

// TrialGPGKey is a passphrase-protected key made for one trial, in its own
// GNUPGHOME: it signs the release request and is the consumer's release key.
type TrialGPGKey struct {
	Home        string
	Fingerprint string
	Passphrase  string
	Private     string
	Public      string
}

// NewTrialGPGKey makes the key. A protected key is the realistic case: the
// passphrase preset is what release signing depends on.
func (trial *GitHubTrial) NewTrialGPGKey() TrialGPGKey {
	trial.tb.Helper()

	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		trial.tb.Fatalf("livetest: %v", err)
	}

	key := TrialGPGKey{Home: trial.tempDir("gnupg"), Passphrase: hex.EncodeToString(raw)}
	loopback := []string{"--homedir", key.Home, "--batch", "--pinentry-mode", "loopback", "--passphrase", key.Passphrase}

	trial.run(key.Home, nil, "gpg", append(loopback, "--quick-generate-key", githubTrialAuthor+" <"+githubTrialEmail+">", "ed25519", "sign", "1d")...)

	for line := range strings.SplitSeq(trial.run(key.Home, nil, "gpg", "--homedir", key.Home, "--batch", "--with-colons", "--list-secret-keys"), "\n") {
		if fields := strings.Split(line, ":"); len(fields) > 9 && fields[0] == "fpr" && key.Fingerprint == "" {
			key.Fingerprint = fields[9]
		}
	}

	if key.Fingerprint == "" {
		trial.tb.Fatalf("livetest: the trial GPG key has no fingerprint")
	}

	key.Private = trial.run(key.Home, nil, "gpg", append(loopback, "--armor", "--export-secret-keys", key.Fingerprint)...)
	key.Public = trial.run(key.Home, nil, "gpg", "--homedir", key.Home, "--batch", "--armor", "--export", key.Fingerprint)

	return key
}

// PushReleaseRequest signs the annotated request tag at main with the trial
// key and pushes it, which starts the consumer's release.
func (trial *GitHubTrial) PushReleaseRequest(repo string, tree GitHubConsumerTree, key TrialGPGKey, request, version string) {
	trial.tb.Helper()

	passphrase := filepath.Join(key.Home, "passphrase")
	program := filepath.Join(key.Home, "gpg-loopback")

	if err := os.WriteFile(passphrase, []byte(key.Passphrase), 0o600); err != nil {
		trial.tb.Fatalf("livetest: %v", err)
	}

	script := "#!/bin/sh\nexec gpg --homedir " + strconv.Quote(key.Home) + " --batch --pinentry-mode loopback --passphrase-file " + strconv.Quote(passphrase) + " \"$@\"\n"
	if err := os.WriteFile(program, []byte(script), 0o700); err != nil { //nolint:gosec // the trial's own signing wrapper, owner-only.
		trial.tb.Fatalf("livetest: %v", err)
	}

	// The tagger is the identity the trial key was made for, so the request's
	// tagger and its signature name the same person.
	trial.git(tree.Dir, "-c", "user.name="+githubTrialAuthor, "-c", "user.email="+githubTrialEmail,
		"-c", "gpg.format=openpgp", "-c", "gpg.program="+program, "-c", "user.signingkey="+key.Fingerprint,
		"tag", "-s", request, "-m", "Release "+version, tree.Main)
	trial.push(tree.Dir, trial.remote(repo), "refs/tags/"+request)
}

// CheckRelease checks what the engine promises of a GitHub release beyond a
// green run: main advanced by one signed commit, the final tag exists, each
// npm package its declaration publishes to the forge's registry is there at
// the released version, the release offers assets (none when it declares no
// file, as a meta release), and its signatures verify: each Cosign bundle
// against trust and each GPG signature (.asc) against the trial's GPG key.
// Every subject carries a verified signature of the method the release
// workflow's artifacts configuration declares (gpg when it declares none).
func (trial *GitHubTrial) CheckRelease(repo string, tree GitHubConsumerTree, workflow, final string, trust CosignTrust, key TrialGPGKey) map[string]int64 {
	trial.tb.Helper()

	ctx := trial.tb.Context()

	method, err := githubReleaseSignMethod(tree.Dir, workflow)
	if err != nil {
		trial.tb.Fatalf("livetest: %v", err)
	}

	profile, err := githubReleaseProfileOf(tree.Dir, workflow)
	if err != nil {
		trial.tb.Fatalf("livetest: %v", err)
	}

	head := trial.checkReleaseCommit(repo, tree, profile.Prepares)

	if _, _, err := trial.api.Git.GetRef(ctx, trial.org, repo, "tags/"+final); err != nil {
		trial.tb.Errorf("the final tag %s is missing: %v", final, err)
	} else {
		trial.checkReleaseGitSignatures(repo, tree, final, head, profile, key)
	}

	release, _, err := trial.api.Repositories.GetReleaseByTag(ctx, trial.org, repo, final)
	if err != nil {
		trial.tb.Fatalf("livetest: the GitHub release %s: %v", final, err)
	}

	if gap := releaseNotesGap(profile.Changelog, release.GetBody()); gap != "" {
		trial.tb.Errorf("the release %s %s", final, gap)
	}

	trial.checkReleasedPackages(repo, tree, workflow, final)

	assets := map[string]int64{}

	names := make([]string, 0, len(release.Assets))
	for _, asset := range release.Assets {
		assets[asset.GetName()] = asset.GetID()
		names = append(names, asset.GetName())
	}

	if profile.NoFiles {
		if len(assets) != 0 {
			trial.tb.Errorf("the release %s declares no file and offers %v", final, names)
		}

		trial.tb.Logf("release %s: git-signing %s, renders a changelog %t, prepares a commit %t; no file declared and none offered",
			final, profile.GitSigning, profile.Changelog, profile.Prepares)

		return assets
	}

	trial.checkReleaseFiles(repo, tree, final, method, profile, assets, names, trust, key)

	return assets
}

// CheckImage verifies the consumer's promoted image at the release version
// against trust. A public consumer's image is read as the host reads it, with
// no credential of the trial's: it must be pullable by anyone. A private
// consumer's is read with the trial's own registry credential
// (privateRegistryEnv), which only the verifying cosign gets.
func (trial *GitHubTrial) CheckImage(repo, image, final string, engine GitHubEngine, trust CosignTrust, private bool) string {
	trial.tb.Helper()

	reference := "ghcr.io/" + strings.ToLower(engine.Owner+"/"+repo) + "/" + image
	cosign := HostTool(trial.tb, "cosign")

	var env []string
	if private {
		env = trial.privateRegistryEnv()
	}

	for _, tag := range []string{final, strings.TrimPrefix(final, "v")} {
		output, err := trial.outputEnv(trial.tempDir("verify"), env, cosign, append(append([]string{"verify"}, trust.verifyArgs()...), reference+":"+tag)...)
		if err == nil {
			trial.tb.Logf("image %s:%s verifies against %s", reference, tag, trust)

			// A released image carries SLSA provenance by default, signed as
			// the image is.
			attested, err := trial.outputEnv(trial.tempDir("verify-provenance"), env, cosign,
				append(append([]string{"verify-attestation", "--type", "slsaprovenance1"}, trust.verifyArgs()...), reference+":"+tag)...)
			if err != nil {
				trial.tb.Errorf("image %s:%s has no SLSA provenance that verifies against %s: %v\n%s", reference, tag, trust, err, attested)
			}

			return reference + ":" + tag
		}

		trial.tb.Logf("image %s:%s does not verify: %v\n%s", reference, tag, err, output)
	}

	trial.tb.Errorf("no promoted tag of %s for %s verifies", reference, final)

	return ""
}

// engineRun waits for Self Runtime Container of commit and returns its
// finished run. Preparing the engine is the request to make its images usable,
// so with rerunFailed a run that ended in failure or was cancelled has its
// failed jobs rerun, once, and the new attempt is the one returned: a dropped
// download in one image build is otherwise final for the commit, since a
// second push of the same commit starts no run. Reusing an engine only reads.
func (trial *GitHubTrial) engineRun(repo, commit string, rerunFailed bool) *github.WorkflowRun {
	trial.tb.Helper()

	run := trial.WaitRun(repo, githubEngineWorkflow, "push", commit, githubEngineTimeout)
	if !rerunFailed || (run.GetConclusion() != runFailure && run.GetConclusion() != runCancelled) {
		return run
	}

	ctx := trial.tb.Context()

	if _, err := trial.api.Actions.RerunFailedJobsByID(ctx, trial.org, repo, run.GetID()); err != nil {
		trial.tb.Logf("engine build run %d ended %s and GitHub did not rerun its failed jobs (the token needs Actions: write): %v",
			run.GetID(), run.GetConclusion(), err)

		return run
	}

	trial.tb.Logf("engine build run %d ended %s at attempt %d; asked GitHub once to rerun its failed jobs",
		run.GetID(), run.GetConclusion(), run.GetRunAttempt())

	deadline := time.Now().Add(githubEngineTimeout)

	for {
		// The run keeps its ID; until GitHub has started the new attempt it
		// still answers with the finished one.
		again, _, err := trial.api.Actions.GetWorkflowRunByID(ctx, trial.org, repo, run.GetID())
		if err == nil && again.GetRunAttempt() > run.GetRunAttempt() && again.GetStatus() == "completed" {
			trial.tb.Logf("engine build run %d attempt %d ended %s", again.GetID(), again.GetRunAttempt(), again.GetConclusion())

			return again
		}

		if time.Now().After(deadline) {
			trial.tb.Fatalf("livetest: the rerun of engine build run %d for %s did not finish within %s", run.GetID(), commit, githubEngineTimeout)
		}

		trial.sleep()
	}
}

// listRunJobs lists the jobs of the run's latest attempt, again after an
// answer of a gateway that could not reach GitHub.
func (trial *GitHubTrial) listRunJobs(ctx context.Context, repo string, run *github.WorkflowRun) (*github.Jobs, error) {
	var jobs *github.Jobs

	err := retry.Run(ctx, nil, 3, time.Second, func() error {
		var (
			response *github.Response
			err      error
		)

		jobs, response, err = trial.api.Actions.ListWorkflowJobs(ctx, trial.org, repo, run.GetID(), &github.ListWorkflowJobsOptions{Filter: "latest", PerPage: 100})
		if err == nil {
			return nil
		}

		if response != nil {
			switch response.StatusCode {
			case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
				return err
			}
		}

		return retry.Permanent(err)
	}, retry.WithBackoff(retry.Constant))

	return jobs, err
}

// reportFailedJob logs the job's log at its failure, or why there is none,
// and its annotations where the log does not show the failure.
func (trial *GitHubTrial) reportFailedJob(ctx context.Context, repo string, run *github.WorkflowRun, job *github.WorkflowJob) {
	location, logResponse, logErr := trial.api.Actions.GetWorkflowJobLogs(ctx, trial.org, repo, job.GetID(), 0)
	if logErr != nil {
		status, class := 0, githubJobLogErrorClass(logErr)
		if logResponse != nil {
			status, class = logResponse.StatusCode, "http-status"
			if status == http.StatusFound {
				class = "redirect-location"
			}
		}

		if snapshotEvidenceRoot() != "" {
			trial.tb.Errorf("livetest: job %d log URL unavailable; no full evidence retained (status=%d class=%s observed_bytes=0)", job.GetID(), status, class)
		}

		// SDK redirect-location parse errors may contain the entire signed URL.
		trial.tb.Logf("    (no log: status=%d class=%s)", status, class)
		trial.reportJobAnnotations(ctx, repo, job)

		return
	}

	excerpt := trial.failureEvidence(ctx, repo, run, job, location.String())
	trial.tb.Logf("    log at the failure:\n%s", excerpt)

	if strings.HasPrefix(excerpt, "could not fetch the job log") || strings.Contains(excerpt, "[job log evidence incomplete:") {
		trial.reportJobAnnotations(ctx, repo, job)
	}
}

// checkReleaseFiles holds the files the release final offers to their
// signatures: each verified against trust or key as method signs, and the
// SBOM bundle the profile expects.
func (trial *GitHubTrial) checkReleaseFiles(repo string, tree GitHubConsumerTree, final string, method domainrelease.SignMethod, profile githubReleaseProfile, assets map[string]int64, names []string, trust CosignTrust, key TrialGPGKey) {
	trial.tb.Helper()

	if len(assets) == 0 {
		trial.tb.Fatalf("livetest: the release %s offers no assets", final)
	}

	evidence, err := NewReleaseEvidence(Target{Forge: "github", Host: "github.com", Owner: trial.org, Token: trial.token}, repo, final, tree.Main, names,
		map[string]string{"gpg": key.Private, "passphrase": key.Passphrase})
	if err != nil {
		trial.tb.Fatalf("retain GitHub release inventory: %v", err)
	}

	bundles := trial.verifyBundles(repo, assets, trust, evidence)

	signatures := trial.verifyGPGSignatures(repo, assets, key, evidence)
	if evidence.Enabled() {
		dir := trial.tempDir("release-readback")

		for name, id := range assets {
			if !evidence.retained(name) {
				trial.download(repo, id, filepath.Join(dir, name), evidence)
			}
		}
	}

	if err := releaseSignatureGaps(method, assets, bundles, signatures); err != nil {
		trial.tb.Errorf("the release %s: %v", final, err)
	}

	if err := releaseSBOMBundleGap(profile.SBOMBundle, assets); err != nil {
		trial.tb.Errorf("the release %s: %v", final, err)
	}

	trial.tb.Logf("release %s: sign method %s, git-signing %s, renders a changelog %t, prepares a commit %t, SBOM bundle expectation %d; %d assets, %d Cosign bundles verified against %s, %d GPG signatures verified against %s",
		final, method, profile.GitSigning, profile.Changelog, profile.Prepares, profile.SBOMBundle, len(assets), len(bundles), trust, len(signatures), key.Fingerprint)
}

// resolveEngineDigests asks the registry what each runtime image serves for
// the commit's tag, so fixtures render the digests every
// workflow admits. The base and Go builder images must exist; a fixture that
// names another image the engine did not publish is refused when it renders.
func (trial *GitHubTrial) resolveEngineDigests(engine GitHubEngine) map[string]string {
	trial.tb.Helper()

	packages, err := engine.RuntimePackageNames()
	if err != nil {
		trial.tb.Fatalf("livetest: runtime package coordinates: %v", err)
	}

	digests := map[string]string{}

	for _, name := range packages {
		image := name[strings.LastIndex(name, "/")+1:]
		if !strings.HasPrefix(image, "forge-ci-runtime") {
			continue
		}

		if digest, ok := trial.registryDigest(strings.ToLower(trial.org)+"/"+name, engine.ImageTag()); ok {
			digests[image] = digest
		}
	}

	for _, image := range []string{"forge-ci-runtime-base", "forge-ci-runtime-go-1.27"} {
		if digests[image] == "" {
			trial.tb.Fatalf("livetest: %s/%s:%s has no published digest; every workflow admits only digest-pinned images", engine.Registry(), image, engine.ImageTag())
		}
	}

	return digests
}

// hostedReads is the client of the trial's own reads beside the API: the
// registry's digests before anything is made, and a run's artifacts after
// it. One lookup of a host that fails must not end the cell, so the reads
// are asked again as the engine's own are.
var hostedReads = httpretry.NewClient(5*time.Minute, httpretry.NewTransport(httpretry.Config{}))

// registryDigest asks the registry which manifest tag names in repository,
// anonymously, as a runner pulls a public engine image. The Packages API is
// not used: a fine-grained token, as the hosted session holds, cannot read
// it. A tag the registry does not serve anonymously has no digest.
func (trial *GitHubTrial) registryDigest(repository, tag string) (string, bool) {
	trial.tb.Helper()

	registry := trial.registry
	if registry == "" {
		registry = "https://ghcr.io"
	}

	ctx := trial.tb.Context()

	tokenRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, registry+"/token?scope=repository:"+repository+":pull", nil) //nolint:gosec // G704: the host is ghcr.io, or a test's own server (trial.registry); the path names the engine's own image.
	if err != nil {
		trial.tb.Fatalf("livetest: registry token request for %s: %v", repository, err)
	}

	var grant struct {
		Token string `json:"token"`
	}

	if status := trial.registryCall(tokenRequest, &grant); status != http.StatusOK || grant.Token == "" {
		return "", false
	}

	manifestRequest, err := http.NewRequestWithContext(ctx, http.MethodHead, registry+"/v2/"+repository+"/manifests/"+tag, nil) //nolint:gosec // G704: as the token request above, with the engine's own tag.
	if err != nil {
		trial.tb.Fatalf("livetest: registry manifest request for %s: %v", repository, err)
	}

	manifestRequest.Header.Set("Authorization", "Bearer "+grant.Token)
	manifestRequest.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))

	response, err := hostedReads.Do(manifestRequest) //nolint:gosec // G704: the manifest request built above.
	if err != nil {
		trial.tb.Fatalf("livetest: read the manifest of %s:%s: %v", repository, tag, err)
	}

	_ = response.Body.Close()

	digest := response.Header.Get("Docker-Content-Digest")

	switch {
	case response.StatusCode == http.StatusOK && pinnedImage.MatchString("x@"+digest):
		return digest, true
	case response.StatusCode == http.StatusOK:
		trial.tb.Fatalf("livetest: %s:%s has no admissible digest: %q", repository, tag, digest)
	case response.StatusCode >= http.StatusInternalServerError:
		trial.tb.Fatalf("livetest: read the manifest of %s:%s: HTTP %d", repository, tag, response.StatusCode)
	}

	return "", false
}

// registryCall sends request, decodes a successful JSON answer into the
// value given, and returns the status.
func (trial *GitHubTrial) registryCall(request *http.Request, into any) int {
	trial.tb.Helper()

	response, err := hostedReads.Do(request) //nolint:gosec // G704: registryDigest's token request, to the trial's registry.
	if err != nil {
		trial.tb.Fatalf("livetest: registry %s: %v", request.URL.Path, err)
	}

	defer func() { _ = response.Body.Close() }()

	if response.StatusCode == http.StatusOK {
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(into); err != nil {
			trial.tb.Fatalf("livetest: registry %s: %v", request.URL.Path, err)
		}
	}

	return response.StatusCode
}

// retainPassingJobLog saves a successful job's full redacted log in the
// evidence directory, by the same bounded, redacting path as a failing job's.
func (trial *GitHubTrial) retainPassingJobLog(ctx context.Context, repo string, run *github.WorkflowRun, job *github.WorkflowJob) {
	location, _, err := trial.api.Actions.GetWorkflowJobLogs(ctx, trial.org, repo, job.GetID(), 0)
	if err != nil {
		// The SDK's error may hold the signed URL; name its class only.
		trial.tb.Errorf("livetest: job %d log URL unavailable; no full evidence retained (class=%s observed_bytes=0)", job.GetID(), githubJobLogErrorClass(err))

		return
	}

	_ = trial.failureEvidence(ctx, repo, run, job, location.String())
}

// Retain before ReportRun returns to the verdict and disposable cleanup. Reuse
// the snapshot destination, bounds and redactor rather than a second archive.
func (trial *GitHubTrial) failureEvidence(ctx context.Context, repo string, run *github.WorkflowRun, job *github.WorkflowJob, location string) string {
	if snapshotEvidenceRoot() == "" {
		return failureOf(ctx, location, 40)
	}

	reader, err := openGitHubJobLog(ctx, location)
	if err != nil {
		trial.tb.Errorf("livetest: job %d log unavailable: %s observed_bytes=0 retention_limit_bytes=%d diagnostic_limit_bytes=%d; no full evidence retained",
			job.GetID(), err.Error(), maxJobLog, maxFailureLogBytes)

		return err.Error()
	}
	defer func() { _ = reader.Close() }()

	// The existing diagnostic budget is independent of the smaller raw-log
	// retention budget. One extra byte distinguishes exact-bound EOF from
	// truncation; it is counted but never included in the diagnostic content.
	body, err := io.ReadAll(io.LimitReader(reader, maxFailureLogBytes+1))
	observed := len(body)
	overDiagnosticLimit := observed > maxFailureLogBytes
	body = body[:min(observed, maxFailureLogBytes)]
	target := Target{Forge: "github", Host: "github.com", Owner: trial.org, Token: trial.token, CredentialUsername: "x-access-token"}
	log := redactJobLog(target, string(body), trial.secrets[repo])

	if err != nil || len(body) > maxJobLog {
		class := "retention-limit"
		if err != nil {
			class = githubJobLogErrorClass(err)
		} else if overDiagnosticLimit {
			class = "diagnostic-limit"
		}

		trial.tb.Errorf("livetest: job %d log HTTP 200 class=%s observed_bytes=%d retention_limit_bytes=%d diagnostic_limit_bytes=%d; no full evidence retained",
			job.GetID(), class, observed, maxJobLog, maxFailureLogBytes)

		if err != nil || overDiagnosticLimit {
			// Never print a final partial line: it may end halfway through a
			// credential, which cannot be matched by the whole-value redactor.
			log = log[:strings.LastIndexByte(log, '\n')+1]
		}

		return readFailureExcerpt(strings.NewReader(log), 40) +
			fmt.Sprintf("\n[job log evidence incomplete: class=%s observed_bytes=%d diagnostic_limit_bytes=%d; no full evidence retained]", class, observed, maxFailureLogBytes)
	}

	retainJobLogText(trial.tb, target, trial.org+"/"+repo, RunJob{RunID: run.GetID(), ID: job.GetID(), Attempt: int64(max(1, run.GetRunAttempt()))}, log, trial.secrets[repo])

	return readFailureExcerpt(strings.NewReader(log), 40)
}

// waitWorkflows waits until GitHub lists every workflow just pushed to a new
// repository. GitHub indexes them after the push, and an event that comes
// before, such as the release-request tag pushed seconds later, starts no run
// and is not replayed.
func (trial *GitHubTrial) waitWorkflows(repo, workflows string) {
	trial.tb.Helper()

	entries, err := os.ReadDir(workflows)
	if err != nil {
		trial.tb.Fatalf("livetest: list the rendered workflows: %v", err)
	}

	deadline := time.Now().Add(githubIndexTimeout)

	for {
		listed, _, err := trial.api.Actions.ListWorkflows(trial.tb.Context(), trial.org, repo, &github.ListOptions{PerPage: 100})
		if err == nil && githubListsAll(listed, entries) {
			return
		}

		if time.Now().After(deadline) {
			trial.tb.Fatalf("livetest: GitHub did not list the workflows of %s within %s (last error: %v)", repo, githubIndexTimeout, err)
		}

		trial.sleep()
	}
}

// githubListsAll reports whether GitHub lists an active workflow for each
// file in the workflow directory.
func githubListsAll(listed *github.Workflows, entries []os.DirEntry) bool {
	active := map[string]bool{}

	for _, workflow := range listed.Workflows {
		active[workflow.GetPath()] = workflow.GetState() == "active"
	}

	for _, entry := range entries {
		if !active[".github/workflows/"+entry.Name()] {
			return false
		}
	}

	return true
}

// checkReleaseCommit wants main one signed commit ahead of the trial's main,
// or, for a release that prepares no commit (no changelog creator, or the
// version bump skipped), exactly where the trial left it: no changelog and no
// project file was touched. It returns main's head.
func (trial *GitHubTrial) checkReleaseCommit(repo string, tree GitHubConsumerTree, prepares bool) string {
	trial.tb.Helper()

	branch, _, err := trial.api.Repositories.GetBranch(trial.tb.Context(), trial.org, repo, defaultBranch, 0)
	if err != nil {
		trial.tb.Fatalf("livetest: main of %s: %v", repo, err)
	}

	head := branch.GetCommit()

	if gap := releaseCommitGap(prepares, tree.Main, head.GetSHA(), parentSHAs(head), head.GetCommit().GetVerification().GetSignature()); gap != "" {
		trial.tb.Errorf("main of %s: %s", repo, gap)
	}

	return head.GetSHA()
}

func parentSHAs(commit *github.RepositoryCommit) []string {
	parents := make([]string, 0, len(commit.Parents))
	for _, parent := range commit.Parents {
		parents = append(parents, parent.GetSHA())
	}

	return parents
}

// releaseCommitGap judges main after a release against the commit the trial
// pushed.
func releaseCommitGap(prepares bool, pushed, head string, parents []string, signature string) string {
	switch {
	case !prepares && head != pushed:
		return fmt.Sprintf("is %s; the release prepares no commit, so it must stay at %s", head, pushed)
	case !prepares:
		return ""
	case head == pushed || len(parents) != 1 || parents[0] != pushed:
		return fmt.Sprintf("is %s; want one release commit on top of %s", head, pushed)
	case signature == "":
		return fmt.Sprintf("the release commit %s is not signed", head)
	}

	return ""
}

// releaseTagGap judges the commit the final tag names: main's head, and for a
// release that prepares no commit the very commit the trial pushed.
func releaseTagGap(prepares bool, pushed, head, tagged string) string {
	switch {
	case tagged != head:
		return fmt.Sprintf("names %s; want main's head %s", tagged, head)
	case !prepares && tagged != pushed:
		return fmt.Sprintf("names %s; the release prepares no commit, so it must name %s", tagged, pushed)
	}

	return ""
}

// releaseNotesGap judges the release's notes: a release that renders a
// changelog publishes one, also when it skips the version bump.
func releaseNotesGap(changelog bool, notes string) string {
	if changelog && strings.TrimSpace(notes) == "" {
		return "has no notes, and its workflow renders a changelog"
	}

	return ""
}

// checkReleaseGitSignatures fetches what the release wrote and verifies it
// locally: the final tag names main's head, and the tag, and the release
// commit when the release prepared one, carry a signature of the declared
// git-signing method by the release's own key: the trial's GPG key, or a
// principal of the allowed-signers file the trial pushed.
func (trial *GitHubTrial) checkReleaseGitSignatures(repo string, tree GitHubConsumerTree, final, head string, profile githubReleaseProfile, key TrialGPGKey) {
	trial.tb.Helper()

	trial.gitRemote(tree.Dir, "fetch", gitQuiet, trial.remote(repo), "refs/heads/"+defaultBranch, "+refs/tags/"+final+":refs/tags/"+final)

	if gap := releaseTagGap(profile.Prepares, tree.Main, head, strings.TrimSpace(trial.git(tree.Dir, "rev-parse", "refs/tags/"+final+"^{commit}"))); gap != "" {
		trial.tb.Errorf("the final tag %s %s", final, gap)
	}

	var allowedSigners string

	if profile.GitSigning == config.GitSignSSH {
		allowedSigners = filepath.Join(trial.tempDir("allowed-signers"), "allowed_signers")
		if err := os.WriteFile(allowedSigners, []byte(trial.git(tree.Dir, "show", tree.Main+":.forge-ci/allowed_signers")), 0o600); err != nil {
			trial.tb.Fatalf("livetest: %v", err)
		}
	}

	if err := trial.gitSignatureGap(tree.Dir, "tag", final, profile.GitSigning, key, allowedSigners); err != nil {
		trial.tb.Errorf("the release %s: %v", final, err)
	}

	if !profile.Prepares {
		return
	}

	if err := trial.gitSignatureGap(tree.Dir, "commit", head, profile.GitSigning, key, allowedSigners); err != nil {
		trial.tb.Errorf("the release %s: %v", final, err)
	}
}

// verifyBundles verifies each <subject>.bundle asset against its subject with
// trust. An orphan bundle now fails acceptance instead of being silently skipped.
func (trial *GitHubTrial) verifyBundles(repo string, assets map[string]int64, trust CosignTrust, evidence ...*ReleaseEvidence) map[string]bool {
	trial.tb.Helper()

	cosign := HostTool(trial.tb, "cosign")
	dir := trial.tempDir("release")

	verified, err := VerifyReleaseBundles(slices.Collect(maps.Keys(assets)), func(subject, bundle string) error {
		trial.download(repo, assets[bundle], filepath.Join(dir, bundle), evidence...)
		trial.download(repo, assets[subject], filepath.Join(dir, subject), evidence...)
		args := append(append([]string{"verify-blob", "--bundle", filepath.Join(dir, bundle)}, trust.verifyArgs()...), filepath.Join(dir, subject))
		trial.run(dir, nil, cosign, args...)

		return nil
	})
	if err != nil {
		trial.tb.Errorf("livetest: %v", err)
	}

	return verified
}

// verifyGPGSignatures verifies each <subject>.asc beside its subject and
// requires the signature to be the trial key's own: gpg --verify alone also
// accepts any other key in the keyring.
func (trial *GitHubTrial) verifyGPGSignatures(repo string, assets map[string]int64, key TrialGPGKey, evidence ...*ReleaseEvidence) map[string]bool {
	trial.tb.Helper()

	dir := trial.tempDir("release-gpg")
	verified := map[string]bool{}

	for name, id := range assets {
		subject, isSignature := strings.CutSuffix(name, ".asc")
		subjectID, hasSubject := assets[subject]

		if !isSignature || !hasSubject {
			continue
		}

		trial.download(repo, id, filepath.Join(dir, name), evidence...)
		trial.download(repo, subjectID, filepath.Join(dir, subject), evidence...)
		status := trial.run(dir, nil, "gpg", "--homedir", key.Home, "--batch", "--status-fd", "1",
			"--verify", filepath.Join(dir, name), filepath.Join(dir, subject))

		if !signedBy(status, key.Fingerprint) {
			trial.tb.Errorf("the GPG signature %s is not the trial key %s's", name, key.Fingerprint)

			continue
		}

		verified[subject] = true
	}

	return verified
}

// releaseSignatureSuffixes name the signature files of a release, which
// sign a subject and are not subjects themselves.
var releaseSignatureSuffixes = []string{".bundle", ".asc", ".sig", ".pem"}

// githubReleaseSignMethod is the method the release workflow's artifacts
// configuration declares for its release files: its sign: block, or gpg, the
// default, when it has none or the file is absent and the release derives its
// plan.
func githubReleaseSignMethod(dir, workflow string) (domainrelease.SignMethod, error) {
	body, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", filepath.Base(workflow))) //nolint:gosec // the staged consumer's own workflow, named by the trial.
	if err != nil {
		return "", fmt.Errorf("release workflow: %w", err)
	}

	parsed, err := githubReleaseConfig(dir, workflow, string(body))
	if err != nil {
		return "", err
	}

	if parsed == nil {
		return domainrelease.DefaultSignMethod, nil
	}

	return parsed.Sign.EffectiveFilesMethod(), nil
}

// githubReleaseConfig is the one artifacts configuration the release workflow
// selects, parsed, or nil when the file is absent and the release derives its
// plan.
func githubReleaseConfig(dir, workflow, body string) (*config.Config, error) {
	configs, err := githubFixtureConfigs(workflow, body)
	if err != nil {
		return nil, err
	}

	slices.Sort(configs)

	switch configs = slices.Compact(configs); len(configs) {
	case 0:
		configs = []string{DefaultArtifactsConfig}
	case 1:
	default:
		return nil, fmt.Errorf("release workflow %s selects %d artifacts configurations, %v; the oracle needs one: %w", workflow, len(configs), configs, errs.ErrValidation)
	}

	declared, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(configs[0]))) //nolint:gosec // a canonical repo-relative path githubFixtureConfigs admitted.
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil //nolint:nilnil // no declaration is a state of its own: the release derives its plan.
	}

	if err != nil {
		return nil, err
	}

	parsed, err := config.Parse(declared)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", configs[0], err)
	}

	return parsed, nil
}

// releaseSignatureGaps judges a release's verified signatures against its
// declared method: every subject needs a verified signature of that method,
// a Cosign bundle for sigstore and kms and a GPG .asc for gpg. One signed by
// the other method alone is named as such. A gpg release offers no Cosign
// bundle at all: with files-method gpg, Cosign signs the images alone.
func releaseSignatureGaps(method domainrelease.SignMethod, assets map[string]int64, bundles, signatures map[string]bool) error {
	declared, other, otherName := signatures, bundles, "a Cosign bundle"
	if method != domainrelease.SignMethodGPG {
		declared, other, otherName = bundles, signatures, "a GPG signature"
	}

	unsigned := unsignedSubjects(assets, declared, nil)
	if len(unsigned) == 0 {
		if len(declared) == 0 {
			return fmt.Errorf("offers no subject to sign: %w", errReleaseEvidence)
		}

		if offered := offeredBundles(assets); method == domainrelease.SignMethodGPG && len(offered) != 0 {
			return fmt.Errorf("signs its files with gpg, but offers Cosign bundles: %v: %w", offered, errReleaseEvidence)
		}

		return nil
	}

	var wrong []string

	for _, subject := range unsigned {
		if other[subject] {
			wrong = append(wrong, subject)
		}
	}

	if len(wrong) != 0 {
		return fmt.Errorf("declares sign method %s, but %d assets carry only %s: %v (%d without a %s signature: %v): %w",
			method, len(wrong), otherName, wrong, len(unsigned), method, unsigned, errReleaseEvidence)
	}

	return fmt.Errorf("offers %d assets without a verified %s signature: %v: %w", len(unsigned), method, unsigned, errReleaseEvidence)
}

// offeredBundles lists, sorted, the Cosign bundles among the assets.
func offeredBundles(assets map[string]int64) []string {
	var offered []string

	for name := range assets {
		if strings.HasSuffix(name, ".bundle") {
			offered = append(offered, name)
		}
	}

	slices.Sort(offered)

	return offered
}

// unsignedSubjects lists, sorted, the assets that are not signatures and have
// no verified Cosign bundle or GPG signature.
func unsignedSubjects(assets map[string]int64, bundles, signatures map[string]bool) []string {
	var unsigned []string

	for name := range assets {
		if slices.ContainsFunc(releaseSignatureSuffixes, func(suffix string) bool { return strings.HasSuffix(name, suffix) }) {
			continue
		}

		if !bundles[name] && !signatures[name] {
			unsigned = append(unsigned, name)
		}
	}

	slices.Sort(unsigned)

	return unsigned
}

// signedBy reports whether gpg's machine-readable status names a valid
// signature by the primary key with this fingerprint.
func signedBy(status, fingerprint string) bool {
	for line := range strings.SplitSeq(status, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "[GNUPG:]" && fields[1] == "VALIDSIG" &&
			(strings.EqualFold(fields[2], fingerprint) || strings.EqualFold(fields[len(fields)-1], fingerprint)) {
			return true
		}
	}

	return false
}

// removeConsumer deletes the repository by the ID its creation returned.
func (trial *GitHubTrial) removeConsumer(repo string, id int64) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(trial.tb.Context()), 5*time.Minute)
	defer cancel()

	if err := trial.client.DeleteRepository(ctx, repo, id); err != nil {
		trial.tb.Errorf("livetest: delete %s (id %d): %v", repo, id, err)
	}
}

func (trial *GitHubTrial) download(repo string, id int64, path string, evidence ...*ReleaseEvidence) {
	body, location, err := trial.api.Repositories.DownloadReleaseAsset(trial.tb.Context(), trial.org, repo, id, &http.Client{Timeout: 5 * time.Minute})
	if err != nil {
		trial.tb.Fatalf("livetest: download release asset %d: %v", id, err)
	}

	if body == nil {
		trial.tb.Fatalf("livetest: release asset %d answered with %q instead of its content", id, location)
	}

	defer func() { _ = body.Close() }()

	file, err := os.Create(path) //nolint:gosec // the trial's own temporary directory.
	if err != nil {
		trial.tb.Fatalf("livetest: %v", err)
	}

	defer func() { _ = file.Close() }()

	size, err := io.Copy(file, io.LimitReader(body, (512<<20)+1))
	if err != nil {
		trial.tb.Fatalf("livetest: write %s: %v", path, err)
	}

	if size > 512<<20 {
		trial.tb.Fatalf("livetest: release asset exceeds 512 MiB download limit")
	}

	if err := file.Close(); err != nil {
		trial.tb.Fatalf("livetest: close release asset: %v", err)
	}

	if len(evidence) > 0 && evidence[0].Enabled() && !evidence[0].retained(filepath.Base(path)) {
		payload, err := os.ReadFile(path) //nolint:gosec // own bounded download, closed above.
		if err != nil {
			trial.tb.Fatalf("livetest: read release evidence: %v", err)
		}

		if err := evidence[0].Retain(filepath.Base(path), payload); err != nil {
			trial.tb.Fatalf("livetest: retain release evidence: %v", err)
		}
	}
}

// remote is the HTTPS remote; the token travels in the environment only.
func (trial *GitHubTrial) remote(repo string) string {
	return "https://github.com/" + trial.org + "/" + repo + ".git"
}

// gitEnv authenticates to github.com through an extra header in the
// environment, never argv or a config file, and keeps the operator's own Git
// configuration (signing, hooks) out of the trial's commits.
func (trial *GitHubTrial) gitEnv() []string {
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + trial.token))

	return []string{
		"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader", "GIT_CONFIG_VALUE_0=AUTHORIZATION: basic " + basic,
	}
}

func (trial *GitHubTrial) git(dir string, args ...string) string {
	trial.tb.Helper()

	return trial.run(dir, trial.gitEnv(), "git", append([]string{"-C", dir}, args...)...)
}

// push runs `git push` with args: pushing an exact ref again lands it or
// finds it there.
func (trial *GitHubTrial) push(dir string, args ...string) {
	trial.tb.Helper()
	trial.gitRemote(dir, append([]string{eventPush}, args...)...)
}

// gitRemote runs a git command that talks to the forge and may be run again,
// a fetch or a push of exact refs, retrying a broken connection as the lab's
// pushes do (runPush). The forge's own refusal is not retried.
func (trial *GitHubTrial) gitRemote(dir string, args ...string) {
	trial.tb.Helper()

	var output string

	err := retry.Run(trial.tb.Context(), nil, 4, 5*time.Second, func() error {
		out, err := trial.outputEnv(dir, trial.gitEnv(), "git", append([]string{"-C", dir}, args...)...)
		output = out

		if err != nil && !brokenConnection(out) {
			return retry.Permanent(err)
		}

		return err
	})
	if err != nil {
		trial.tb.Fatalf("livetest: git %s: %v\n%s", firstWord(args), err, output)
	}
}

// commit commits the index as the identity the trial's sign-off names, signed
// with the SSH key at sshKey, or unsigned when it is empty.
func (trial *GitHubTrial) commit(dir, message, sshKey string) {
	trial.tb.Helper()

	args := []string{"-c", "user.name=" + consumerAuthor, "-c", "user.email=" + requestSigner, "-c", "commit.gpgsign=false"}
	if sshKey != "" {
		args = append(args, "-c", "gpg.format=ssh", "-c", "user.signingkey="+sshKey)
	}

	args = append(args, gitCommitCommand, gitQuiet, "-m", message)
	if sshKey != "" {
		args = append(args, "-S")
	}

	trial.git(dir, args...)
}

func (trial *GitHubTrial) run(dir string, env []string, name string, args ...string) string {
	trial.tb.Helper()

	output, err := trial.outputEnv(dir, env, name, args...)
	if err != nil {
		trial.tb.Fatalf("livetest: %s %s: %v\n%s", name, firstWord(args), err, output)
	}

	return output
}

func (trial *GitHubTrial) outputEnv(dir string, env []string, name string, args ...string) (string, error) {
	return trial.outputContext(trial.tb.Context(), dir, env, name, args...)
}

func (trial *GitHubTrial) outputContext(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	// nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command
	command := exec.CommandContext(ctx, name, args...) //nolint:gosec // fixed tools of the trial with its own arguments.
	command.Dir = dir

	command.Env = append(os.Environ(), env...)

	output, err := command.CombinedOutput()

	return string(output), err
}

func (trial *GitHubTrial) tempDir(name string) string {
	dir, err := os.MkdirTemp("", "forge-ci-github-trial-"+name+"-")
	if err != nil {
		trial.tb.Fatalf("livetest: %v", err)
	}

	trial.tb.Cleanup(func() { _ = os.RemoveAll(dir) })

	return dir
}

func (trial *GitHubTrial) sleep() {
	select {
	case <-trial.tb.Context().Done():
		trial.tb.Fatalf("livetest: the trial was stopped while waiting: %v", trial.tb.Context().Err())
	case <-time.After(githubPoll):
	}
}

func firstWord(args []string) string {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
	}

	return ""
}
