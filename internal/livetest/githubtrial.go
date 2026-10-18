// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"codefloe.com/itiquette/forge-lab-contract/githubclient"
	"codefloe.com/itiquette/forge-lab-contract/githubcontext"
	"github.com/google/go-github/v91/github"

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
	token   string
	// history makes PushConsumer push the consumer's own history under the
	// rendered commit: WithHistory.
	history bool
}

// OpenGitHubTrial loads Forge Lab's hosted context and authorizes exactly the
// organization ID the caller named.
func OpenGitHubTrial(tb TB, organizationID int64) *GitHubTrial {
	tb.Helper()

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
		session: document.Session[:8], token: document.Token}
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

	commit := strings.TrimSpace(trial.git(root, "rev-parse", "HEAD"))
	if len(commit) != 40 {
		trial.tb.Fatalf("livetest: the engine checkout must be SHA-1 for GitHub; HEAD is %q", commit)
	}

	// The runtime images' platforms are this run's, set before the push that
	// builds them; the default comes back when the trial ends, so a selection
	// never outlives its run.
	trial.SetVariable(name, "RELEASE_PLATFORMS", platforms)
	if platforms != githubDefaultPlatforms {
		trial.tb.Cleanup(func() { trial.SetVariable(name, "RELEASE_PLATFORMS", githubDefaultPlatforms) })
	}
	trial.push(root, "--force", trial.remote(name), "HEAD:refs/heads/main")
	trial.SetVariable(name, "FORGE_CI_REPOSITORY", trial.org+"/"+name)
	trial.ensurePrerelease(name)

	engine := GitHubEngine{Owner: trial.org, Repository: name, Commit: commit}
	run := trial.WaitRun(name, githubEngineWorkflow, "push", commit, githubEngineTimeout)

	if run.GetConclusion() != "success" {
		trial.ReportRun(name, run)
		trial.tb.Fatalf("livetest: Self Runtime Container for %s ended %s; the engine's images are not usable", commit, run.GetConclusion())
	}

	trial.waitPrerelease(name, commit)
	trial.tb.Logf("engine under test: %s/%s at %s, images %s/forge-ci-runtime*:%s", trial.org, name, commit, engine.Registry(), engine.ImageTag())

	return engine
}

// WithHistory returns the trial pushing the consumer's own history under the
// rendered commit, as a demo does. A trial pushes one parentless commit.
func (trial *GitHubTrial) WithHistory() *GitHubTrial {
	shown := *trial
	shown.history = true

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

	dir := trial.tempDir("consumer")
	message := consumerBaseMessage

	if trial.history {
		// The consumer's own history, with its rendered workflows one commit on
		// top. GitHub has no SHA-256 repositories, so only a SHA-1 one can come.
		trial.git(dir, "init", gitQuiet, "--object-format=sha1", "--initial-branch=main", ".")
		trial.git(dir, "fetch", gitQuiet, root, "HEAD")
		trial.git(dir, "checkout", gitQuiet, "-B", "main", "FETCH_HEAD")

		message = demoBaseMessage
	} else {
		archive := filepath.Join(trial.tempDir("archive"), "head.tar")

		trial.git(root, "archive", "--format=tar", "-o", archive, "HEAD")
		trial.run(dir, nil, "tar", "-xf", archive, "-C", dir)
		trial.git(dir, "init", gitQuiet, "--object-format=sha1", "--initial-branch=main", ".")
	}

	workflows := filepath.Join(dir, ".github", "workflows")
	if err := os.RemoveAll(workflows); err != nil {
		trial.tb.Fatalf("livetest: clear the consumer's own workflows: %v", err)
	}

	if err := os.MkdirAll(workflows, 0o755); err != nil { //nolint:gosec // a workflow directory is world-readable by convention.
		trial.tb.Fatalf("livetest: %v", err)
	}

	trial.renderFixtures(filepath.Join(root, fixtures, "workflows"), workflows, engine, overrides)

	trial.git(dir, "add", "--all")
	trial.commit(dir, message, "")
	trial.push(dir, trial.remote(repo), "HEAD:refs/heads/main")
	trial.waitWorkflows(repo, workflows)

	return GitHubConsumerTree{Dir: dir, Main: strings.TrimSpace(trial.git(dir, "rev-parse", "HEAD"))}
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
	trial.git(tree.Dir, "checkout", gitQuiet, "main")

	return head
}

// OpenPullRequest opens branch against main.
func (trial *GitHubTrial) OpenPullRequest(repo, branch string) {
	trial.tb.Helper()

	_, _, err := trial.api.PullRequests.Create(trial.tb.Context(), trial.org, repo, github.CreatePullRequest{
		Title: new("Hosted trial: pull-request gate"), Head: branch, Base: "main",
		Body: new("Forge Lab hosted consumer trial. Disposable."),
	})
	if err != nil {
		trial.tb.Fatalf("livetest: open the pull request from %s: %v", branch, err)
	}
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
		trial.tb.Fatalf("livetest: set secret %s on %s: %v", name, repo, err)
	}
}

// Token is the context's token, for a consumer's RELEASE_TOKEN.
func (trial *GitHubTrial) Token() string { return trial.token }

// WaitRun waits for the newest run of workflow for event at headSHA to finish
// and returns it. A run cancelled by a newer one of the same group is skipped.
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

				if run.GetConclusion() == "cancelled" {
					continue
				}

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

// ReportRun prints every job of the run with its conclusion, and the end of
// the log of each that failed.
func (trial *GitHubTrial) ReportRun(repo string, run *github.WorkflowRun) {
	trial.tb.Helper()

	ctx := trial.tb.Context()

	jobs, _, err := trial.api.Actions.ListWorkflowJobs(ctx, trial.org, repo, run.GetID(), &github.ListWorkflowJobsOptions{Filter: "latest", ListOptions: github.ListOptions{PerPage: 100}})
	if err != nil {
		trial.tb.Errorf("livetest: list jobs of run %d: %v", run.GetID(), err)

		return
	}

	for _, job := range jobs.Jobs {
		trial.tb.Logf("  %-9s %s", job.GetConclusion(), job.GetName())

		if job.GetConclusion() != "failure" {
			continue
		}

		location, _, logErr := trial.api.Actions.GetWorkflowJobLogs(ctx, trial.org, repo, job.GetID(), 0)
		if logErr != nil {
			trial.tb.Logf("    (no log: %v)", logErr)

			continue
		}

		trial.tb.Logf("    log at the failure:\n%s", failureOf(ctx, location.String(), 40))
	}
}

// failureOf fetches a pre-signed log URL without the token and returns the
// lines that explain the failure.
func failureOf(ctx context.Context, location string, lines int) string {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return err.Error()
	}

	response, err := (&http.Client{Timeout: time.Minute}).Do(request)
	if err != nil {
		return err.Error()
	}

	defer func() { _ = response.Body.Close() }()

	var all []string

	scanner := bufio.NewScanner(io.LimitReader(response.Body, 32<<20))
	for scanner.Scan() {
		all = append(all, scanner.Text())
	}

	return strings.Join(FailureExcerpt(all, lines), "\n")
}

// FailureExcerpt picks the lines of a job log that explain its failure: those
// up to and including the first ##[error] annotation. A job's last lines are
// its post-run cleanup, Harden Runner's agent log among them, which once hid
// the error of a failed publication entirely. Without an annotation it falls
// back to the end of the log.
func FailureExcerpt(log []string, lines int) []string {
	end := len(log)

	for index, line := range log {
		if strings.Contains(line, "##[error]") {
			end = index + 1

			break
		}
	}

	return log[max(0, end-lines):end]
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
	trial.run(dir, []string{"COSIGN_PASSWORD=" + key.Passphrase}, "cosign", "generate-key-pair")

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

	for _, line := range strings.Split(trial.run(key.Home, nil, "gpg", "--homedir", key.Home, "--batch", "--with-colons", "--list-secret-keys"), "\n") {
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
// green run: main advanced by one signed commit, the final tag exists, the
// release offers assets, and each Cosign bundle verifies against trust.
func (trial *GitHubTrial) CheckRelease(repo string, tree GitHubConsumerTree, final string, trust CosignTrust) {
	trial.tb.Helper()

	ctx := trial.tb.Context()

	trial.checkReleaseCommit(repo, tree)

	if _, _, err := trial.api.Git.GetRef(ctx, trial.org, repo, "tags/"+final); err != nil {
		trial.tb.Errorf("the final tag %s is missing: %v", final, err)
	}

	release, _, err := trial.api.Repositories.GetReleaseByTag(ctx, trial.org, repo, final)
	if err != nil {
		trial.tb.Fatalf("livetest: the GitHub release %s: %v", final, err)
	}

	assets := map[string]int64{}
	for _, asset := range release.Assets {
		assets[asset.GetName()] = asset.GetID()
	}

	if len(assets) == 0 {
		trial.tb.Fatalf("livetest: the release %s offers no assets", final)
	}

	verified := trial.verifyBundles(repo, assets, trust)
	if verified == 0 {
		trial.tb.Errorf("the release %s offers no Cosign bundle beside its subject", final)
	}

	trial.tb.Logf("release %s: %d assets, %d Cosign bundles verified against %s", final, len(assets), verified, trust)
}

// CheckImage verifies the consumer's promoted image at the release version
// against trust. The registry must let the caller pull: a public consumer's
// packages, or Docker credentials for a private one.
func (trial *GitHubTrial) CheckImage(repo, image, final string, engine GitHubEngine, trust CosignTrust) {
	trial.tb.Helper()

	reference := engine.Registry() + "/" + strings.ToLower(repo) + "/" + image
	for _, tag := range []string{final, strings.TrimPrefix(final, "v")} {
		output, err := trial.output(trial.tempDir("verify"), "cosign", append(append([]string{"verify"}, trust.verifyArgs()...), reference+":"+tag)...)
		if err == nil {
			trial.tb.Logf("image %s:%s verifies against %s", reference, tag, trust)

			return
		}

		trial.tb.Logf("image %s:%s does not verify: %v\n%s", reference, tag, err, output)
	}

	trial.tb.Errorf("no promoted tag of %s for %s verifies", reference, final)
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

// renderFixtures writes each fixture workflow of source into workflows,
// rendered for the engine under test.
func (trial *GitHubTrial) renderFixtures(source, workflows string, engine GitHubEngine, overrides map[string]string) {
	trial.tb.Helper()

	entries, err := os.ReadDir(source)
	if err != nil {
		trial.tb.Fatalf("livetest: read the consumer's GitHub fixtures in %s: %v", source, err)
	}

	applied := map[string]bool{}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yml" {
			continue
		}

		for _, input := range trial.renderFixture(source, workflows, entry.Name(), engine, overrides) {
			applied[input] = true
		}
	}

	// A setting no workflow passes would be silently ignored, and the trial
	// would run the published image instead of the one the caller named.
	for _, input := range sortedKeys(overrides) {
		if !applied[input] {
			trial.tb.Fatalf("livetest: no workflow in %s passes %s, so the setting cannot override it", source, input)
		}
	}
}

// renderFixture writes one rendered fixture workflow and returns the
// override inputs it passes.
func (trial *GitHubTrial) renderFixture(source, workflows, name string, engine GitHubEngine, overrides map[string]string) []string {
	trial.tb.Helper()

	text, err := os.ReadFile(filepath.Join(source, name)) //nolint:gosec // a fixture of the consumer checkout under test.
	if err != nil {
		trial.tb.Fatalf("livetest: %v", err)
	}

	rendered, inputs, err := RenderGitHubConsumer(name, string(text), engine, overrides)
	if err != nil {
		trial.tb.Fatalf("livetest: %v", err)
	}

	if err = os.WriteFile(filepath.Join(workflows, name), []byte(rendered), 0o644); err != nil { //nolint:gosec // a workflow file is world-readable by convention.
		trial.tb.Fatalf("livetest: %v", err)
	}

	return inputs
}

// checkReleaseCommit wants main one signed commit ahead of the trial's main.
func (trial *GitHubTrial) checkReleaseCommit(repo string, tree GitHubConsumerTree) {
	trial.tb.Helper()

	branch, _, err := trial.api.Repositories.GetBranch(trial.tb.Context(), trial.org, repo, "main", 0)
	if err != nil {
		trial.tb.Fatalf("livetest: main of %s: %v", repo, err)
	}

	head := branch.GetCommit()
	if parents := head.Parents; head.GetSHA() == tree.Main || len(parents) != 1 || parents[0].GetSHA() != tree.Main {
		trial.tb.Errorf("main of %s is %s; want one release commit on top of %s", repo, head.GetSHA(), tree.Main)
	}

	if head.GetCommit().GetVerification().GetSignature() == "" {
		trial.tb.Errorf("the release commit %s is not signed", head.GetSHA())
	}
}

// verifyBundles verifies each <subject>.bundle asset against its subject with
// trust, and counts them.
func (trial *GitHubTrial) verifyBundles(repo string, assets map[string]int64, trust CosignTrust) int {
	trial.tb.Helper()

	if _, err := exec.LookPath("cosign"); err != nil {
		trial.tb.Fatalf("livetest: cosign is required to verify the release and is not on PATH: %v", err)
	}

	dir := trial.tempDir("release")
	verified := 0

	for name, id := range assets {
		subject, isBundle := strings.CutSuffix(name, ".bundle")
		subjectID, hasSubject := assets[subject]

		if !isBundle || !hasSubject {
			continue
		}

		trial.download(repo, id, filepath.Join(dir, name))
		trial.download(repo, subjectID, filepath.Join(dir, subject))
		args := append(append([]string{"verify-blob", "--bundle", filepath.Join(dir, name)}, trust.verifyArgs()...), filepath.Join(dir, subject))
		trial.run(dir, nil, "cosign", args...)

		verified++
	}

	return verified
}

func (trial *GitHubTrial) ensurePrerelease(repo string) {
	ctx := trial.tb.Context()

	_, response, err := trial.api.Repositories.GetReleaseByTag(ctx, trial.org, repo, GitHubPrerelease)
	if err == nil {
		return
	}

	if response == nil || response.StatusCode != http.StatusNotFound {
		trial.tb.Fatalf("livetest: look up %s: %v", GitHubPrerelease, err)
	}

	// Self Runtime Container replaces the assets of an existing pre-release
	// and refuses when there is none (docs/github.md#forks).
	_, _, err = trial.api.Repositories.CreateRelease(ctx, trial.org, repo, github.CreateReleaseRequest{
		TagName: GitHubPrerelease, TargetCommitish: new("main"), Name: new("forge-ci 0.90.0-pre (rolling pre-release)"),
		Prerelease: new(true), MakeLatest: new("false"), Body: new(""),
	})
	if err != nil {
		trial.tb.Fatalf("livetest: create %s: %v", GitHubPrerelease, err)
	}
}

func (trial *GitHubTrial) waitPrerelease(repo, commit string) {
	deadline := time.Now().Add(10 * time.Minute)

	for {
		ref, _, err := trial.api.Git.GetRef(trial.tb.Context(), trial.org, repo, "tags/"+GitHubPrerelease)
		if err == nil && ref.GetObject().GetSHA() == commit {
			return
		}

		if time.Now().After(deadline) {
			trial.tb.Fatalf("livetest: %s did not move to %s after the engine build", GitHubPrerelease, commit)
		}

		trial.sleep()
	}
}

// removeConsumer deletes the repository by the ID its creation returned.
func (trial *GitHubTrial) removeConsumer(repo string, id int64) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(trial.tb.Context()), 5*time.Minute)
	defer cancel()

	if err := trial.client.DeleteRepository(ctx, repo, id); err != nil {
		trial.tb.Errorf("livetest: delete %s (id %d): %v", repo, id, err)
	}
}

func (trial *GitHubTrial) download(repo string, id int64, path string) {
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

	if _, err = io.Copy(file, io.LimitReader(body, 512<<20)); err != nil {
		trial.tb.Fatalf("livetest: write %s: %v", path, err)
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

// push runs `git push` with args, retrying a broken connection as the lab's
// pushes do (runPush): pushing an exact ref again lands it or finds it there,
// and the forge's own refusal is not retried.
func (trial *GitHubTrial) push(dir string, args ...string) {
	trial.tb.Helper()

	var output string

	err := retry.Run(trial.tb.Context(), nil, 4, 5*time.Second, func() error {
		out, err := trial.outputEnv(dir, trial.gitEnv(), "git", append([]string{"-C", dir, eventPush}, args...)...)
		output = out

		if err != nil && !brokenConnection(out) {
			return retry.Permanent(err)
		}

		return err
	})
	if err != nil {
		trial.tb.Fatalf("livetest: git push: %v\n%s", err, output)
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

func (trial *GitHubTrial) output(dir, name string, args ...string) (string, error) {
	return trial.outputEnv(dir, nil, name, args...)
}

func (trial *GitHubTrial) outputEnv(dir string, env []string, name string, args ...string) (string, error) {
	// nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command
	command := exec.CommandContext(trial.tb.Context(), name, args...) //nolint:gosec // fixed tools of the trial with its own arguments.
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
