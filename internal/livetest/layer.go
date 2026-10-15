// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// The Forgejo delivery layer is the workflows, actions and scripts under
// .forgejo/, and the checksum of the binary they run. A consumer reaches it by
// reference: a job names <owner>/forge-ci/.forgejo/... at a commit, and the
// runner fetches that commit from the forge. So a scenario about the layer
// needs the forge to serve this checkout's layer, and it does that the way a
// consumer's forge would: the tree is pushed there as a branch and the fixture
// pins its commit.
//
// The branch lives in the scenario's own scratch repository, beside the fixture
// workflow on the default branch. One repository means one teardown, and the
// reference a fixture writes is the same shape a consumer writes, only with
// another repository name.
//
// The binary is not in the tree. A job downloads it by the recorded checksum
// from the generic packages of the owner of the repository that serves the
// layer, which here is the scratch owner. So PushLayer publishes it there, as
// that owner's signer-image workflow does on a real forge.
// GitLab serves the same committed snapshot through templates/, with its binary
// in the scratch project's registry. Its engine-branch push skips self-tests;
// the consumer's own MR is the pipeline the trial judges.

const (
	// layerDir holds everything a job fetches from the served repository.
	layerDir = ".forgejo"

	// layerChecksum is the tracked line that names the binary the layer runs:
	// `<sha256>  bin/forge-ci`.
	layerChecksum = layerDir + "/bin/forge-ci.sha256"

	// The generic package a job downloads the binary from, under the sha256
	// as its version: the coordinate .forgejo/bin/lib-forge-ci.sh builds.
	layerPackage     = "forge-ci"
	layerPackageFile = "forge-ci-linux-amd64"

	// gitQuiet keeps git's progress out of a failure's message.
	gitQuiet    = "--quiet"
	gitRevParse = "rev-parse"

	// layerBranch is the branch of the scratch repository that serves the
	// layer. Its workflows start only when called, so pushing it runs nothing.
	layerBranch = "layer-under-test"

	// The placeholders the layer's own examples use for its coordinates, and
	// the ones a fixture uses for the facts of one run.
	layerActionBase   = "https://FORGE_HOST/OWNER/forge-ci/"
	layerWorkflowBase = "OWNER/forge-ci/"
	layerPin          = "PIN_FORGE_CI_SHA"
	layerBinaryDigest = "RENDER_FORGE_CI_SHA256"
	layerHelpers      = "RENDER_FORGE_CI_REPOSITORY"
	layerServerURL    = "RENDER_ACCEPTANCE_SERVER_URL"
)

// Layer is the layer under test as the forge serves it.
type Layer struct {
	// ServerURL is the forge's web root, without a trailing slash.
	ServerURL string
	// Repository is owner/name of the repository that serves the layer.
	Repository string
	// Commit is the pin: a parentless commit holding exactly HEAD's tree.
	Commit string
	// BinarySHA256 is the digest that tree records for the layer's binary,
	// and the digest of the build published for it.
	BinarySHA256 string
}

// PushLayer makes the scratch repository serve this checkout's layer and
// returns its coordinates.
//
// What is served is HEAD's tree, so anything uncommitted under .forgejo/ is
// refused: a job would otherwise run a layer that differs from the one in
// front of the developer, and say nothing. The rest of the tree may be dirty,
// the scenario being written included, because no job reads it.
//
// The tree is pushed as one parentless commit, so the forge receives no
// history. The commit is written with a fixed identity and date, so one tree
// always yields the same pin.
//
// The binary published is the build `just test-live` made with
// scripts/ci/forgejo-binary.sh and handed over by path. It must be the one the
// tree records: a job accepts no other, and a tree whose checksum is stale
// would fail every job at its first engine call with a message about a
// download.
func PushLayer(tb TB, target Target, repo string) Layer {
	tb.Helper()
	requireAccepted(tb, target)

	if target.Forge != provider.ForgeForgejo && target.Forge != provider.ForgeGitLab {
		tb.Fatalf("livetest: no delivery layer is served for %s", target.Forge)
	}

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	gitBase := strings.TrimSuffix(target.endpoint.GitURL(), "/")
	git := layerGit{
		target: target,
		env:    ToolEnvironment(tb, target, layerGitEnvironment(gitBase, target.CredentialUsername, target.Token)),
	}

	root, err := git.run(ctx, "", gitRevParse, "--show-toplevel")
	if err != nil {
		tb.Fatalf("livetest: find the checkout that holds the layer: %v", err)
	}

	commit, err := git.snapshot(ctx, root)
	if err != nil {
		tb.Fatalf("livetest: snapshot the layer: %v", err)
	}

	if err = git.push(ctx, root, scratchRemote(target, repo), commit, layerBranch); err != nil {
		tb.Fatalf("livetest: push the layer to %s/%s: %v", target.Owner, repo, err)
	}

	// The forge is asked what it serves, so a push that landed elsewhere, or
	// not at all, is not found out by a job that cannot resolve its pin.
	if served := BranchCommitSHA(tb, target, repo, layerBranch); served != commit {
		tb.Fatalf("livetest: %s/%s serves %s as %s, want the pushed layer %s", target.Owner, repo, served, layerBranch, commit)
	}

	recorded, binary := readLayerBinary(tb, root)

	if err = publishLayerBinary(ctx, target, repo, recorded, binary); err != nil {
		tb.Fatalf("livetest: publish the layer's binary: %v", err)
	}

	// Forgejo scopes a package to the owner, so deleting the scratch
	// repository leaves it behind.
	if target.Forge == provider.ForgeForgejo {
		tb.Cleanup(func() { DeletePublishedPackage(tb, target, repo, "generic", layerPackage, recorded) })
	}

	return Layer{
		ServerURL:    strings.TrimSuffix(target.endpoint.WebBaseURL, "/"),
		Repository:   RepoSlug(target, repo),
		Commit:       commit,
		BinarySHA256: recorded,
	}
}

func readLayerBinary(tb TB, root string) (string, []byte) {
	tb.Helper()

	recorded, err := recordedLayerChecksum(root)
	if err != nil {
		tb.Fatalf("livetest: %v", err)
	}

	binary, err := os.ReadFile(LayerBinary(tb)) //nolint:gosec // The layer build the guarded launcher supplies.
	if err != nil {
		tb.Fatalf("livetest: read the layer build: %v", err)
	}

	if digest := sha256.Sum256(binary); hex.EncodeToString(digest[:]) != recorded {
		tb.Fatalf("livetest: %s records %s, and the build is %s; run just refresh-forgejo-binary and commit",
			layerChecksum, recorded, hex.EncodeToString(digest[:]))
	}

	return recorded, binary
}

// recordedLayerChecksum returns the digest the checkout records for the
// layer's binary.
func recordedLayerChecksum(root string) (string, error) {
	line, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(layerChecksum))) //nolint:gosec // The tracked checksum of this checkout.
	if err != nil {
		return "", fmt.Errorf("read the layer's recorded checksum: %w", err)
	}

	digest, name, found := strings.Cut(strings.TrimSuffix(string(line), "\n"), "  ")
	if decoded, decodeErr := hex.DecodeString(digest); !found || name != "bin/forge-ci" || decodeErr != nil || len(decoded) != sha256.Size {
		return "", fmt.Errorf("%s is not one `<sha256>  bin/forge-ci` line: %w", layerChecksum, errs.ErrValidation)
	}

	return digest, nil
}

// layerPackageURL is where a job of a layer served by owner downloads the
// binary with the digest.
func layerPackageURL(base, owner, digest string) string {
	return strings.TrimSuffix(base, "/") + "/api/packages/" + url.PathEscape(owner) + "/generic/" +
		layerPackage + "/" + digest + "/" + layerPackageFile
}

// publishLayerBinary uploads the binary as the owner's generic package. A
// version cannot be replaced, so one that is already there is left: a job
// verifies what it downloads against the same digest.
func publishLayerBinary(ctx context.Context, target Target, repo, digest string, binary []byte) error {
	endpoint := layerPackageURL(target.BaseURL(), target.Owner, digest)
	if target.Forge == provider.ForgeGitLab {
		endpoint = target.BaseURL() + "/api/v4/projects/" + url.PathEscape(RepoSlug(target, repo)) +
			"/packages/generic/" + layerPackage + "/" + digest + "/" + layerPackageFile
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(binary))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	authorize(req, target)

	client, err := targetHTTPClient(target, timeoutLayerPush)
	if err != nil {
		return fmt.Errorf("configure target HTTP trust: %w", err)
	}

	resp, err := client.Do(req) //nolint:gosec // G704: the lab transport reaches lab hosts only, and the address is built from the accepted target.
	if err != nil {
		return fmt.Errorf("PUT %s: %w", redact(endpoint), err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusConflict {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))

		return fmt.Errorf("PUT %s: HTTP %d: %s: %w", redact(endpoint), resp.StatusCode, detail, errs.ErrValidation)
	}

	return nil
}

// Render fills a fixture workflow's placeholders with the served layer's
// coordinates, and with the facts of one scenario given as old, new pairs.
//
// A fixture names the layer exactly as the Forgejo examples do, so it reads as
// a consumer's workflow and a renamed placeholder cannot leave the two apart.
// A fixture that pins nothing is refused, because it would not run the layer,
// and so is one that keeps a placeholder, because the forge would reject it
// with a message about YAML. Where one fact's placeholder begins another's,
// the longer comes first.
func (layer Layer) Render(fixture string, facts ...string) (string, error) {
	if !strings.Contains(fixture, layerPin) {
		return "", fmt.Errorf("the fixture does not pin the layer with %s: %w", layerPin, errs.ErrValidation)
	}

	if len(facts)%2 != 0 {
		return "", fmt.Errorf("scenario facts must be old, new pairs: %w", errs.ErrValidation)
	}

	pairs := append(slices.Clone(facts),
		layerActionBase, layer.actionBase(),
		layerWorkflowBase, layer.workflowBase(),
		layerPin, layer.Commit,
		layerBinaryDigest, layer.BinarySHA256,
		layerHelpers, layer.Repository,
		layerServerURL, layer.ServerURL,
	)

	rendered := strings.NewReplacer(pairs...).Replace(fixture)

	for _, left := range []string{"FORGE_HOST", "OWNER/", "PIN_", "RENDER_"} {
		if strings.Contains(rendered, left) {
			return "", fmt.Errorf("the rendered fixture still holds a %q placeholder: %w", left, errs.ErrValidation)
		}
	}

	return rendered, nil
}

// unrunDigest fills a digest of a workflow that is checked and never run: well
// formed, and naming no image and no key.
const unrunDigest = "0000000000000000000000000000000000000000000000000000000000000000"

// RenderReleaseExample fills examples/go-cli/forgejo/release.yml as a consumer of
// the served layer would, for a scenario that has the isolation gate read it.
//
// The scenario uses the example itself and no copy, so the gate is held to
// the workflow this repository tells consumers to start from. The workflow
// is never run there, so the signer image and the key digest are well-formed
// literals that name nothing.
func (layer Layer) RenderReleaseExample(example string) (string, error) {
	owner, _, _ := strings.Cut(layer.Repository, "/")

	return layer.Render(example,
		"OWNER/forge-ci-signer", layer.Repository+"-signer",
		"OWNER/forge-ci", layer.Repository,
		"PIN_SIGNER_IMAGE_DIGEST", unrunDigest,
		"PIN_COSIGN_PUBLIC_KEY_SHA256", unrunDigest,
		"FORGE_HOST", strings.TrimPrefix(layer.ServerURL, "https://"),
		"OWNER", owner,
	)
}

// actionBase is what an action's uses: line starts with: the served
// repository's web address.
func (layer Layer) actionBase() string { return layer.ServerURL + "/" + layer.Repository + "/" }

// workflowBase is what a called workflow's uses: line starts with: the served
// repository on the instance that runs the caller.
func (layer Layer) workflowBase() string { return layer.Repository + "/" }

// layerGitEnvironment is what git needs beyond the closed tool environment:
// no configuration but this, the credential as a header scoped to the lab's
// Git root, and a fixed identity and date for the snapshot commit.
//
// The credential travels in the environment and not in the remote URL or an
// argument, so it reaches neither a process listing nor git's own messages.
func layerGitEnvironment(gitBase, username, token string) map[string]string {
	const (
		identity = "livetest"
		email    = "livetest@localhost"
		date     = "2000-01-01T00:00:00Z"
	)

	basic := base64.StdEncoding.EncodeToString([]byte(username + ":" + token))

	return map[string]string{
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_TERMINAL_PROMPT": "0",
		"GIT_CONFIG_COUNT":    "1",
		"GIT_CONFIG_KEY_0":    "http." + gitBase + "/.extraHeader",
		"GIT_CONFIG_VALUE_0":  "Authorization: Basic " + basic,
		"GIT_AUTHOR_NAME":     identity,
		"GIT_AUTHOR_EMAIL":    email,
		"GIT_AUTHOR_DATE":     date,
		"GIT_COMMITTER_NAME":  identity,
		"GIT_COMMITTER_EMAIL": email,
		"GIT_COMMITTER_DATE":  date,
	}
}

// scratchRemote is the HTTPS address git pushes a scratch repository by.
func scratchRemote(target Target, repo string) string {
	return strings.TrimSuffix(target.endpoint.GitURL(), "/") + "/" + target.Owner + "/" + repo + ".git"
}

// layerGit runs git for PushLayer, and for a consumer trial, in one closed
// environment.
type layerGit struct {
	target Target
	env    []string
}

// snapshot returns a parentless commit that holds exactly HEAD's tree. It
// refuses a checkout whose layer differs from HEAD, naming the files.
func (git layerGit) snapshot(ctx context.Context, root string) (string, error) {
	if err := git.requireClean(ctx, root, layerDir); err != nil {
		return "", err
	}

	if git.target.Forge == provider.ForgeGitLab {
		for _, directory := range []string{"templates", ".gitlab"} {
			if err := git.requireClean(ctx, root, directory); err != nil {
				return "", err
			}
		}
	}

	return git.run(ctx, root, "commit-tree", "-m", "livetest: the layer under test", "HEAD^{tree}")
}

// requireClean refuses a checkout in which what, a path or "." for all of
// it, differs from HEAD, and names the files. What a forge is given is
// HEAD's, so a difference would be tested by nobody and reported by nothing.
func (git layerGit) requireClean(ctx context.Context, root, what string) error {
	dirty, err := git.run(ctx, root, "status", "--porcelain", "--untracked-files=all", "--", what)
	if err != nil {
		return err
	}

	if dirty != "" {
		return fmt.Errorf("%s has uncommitted changes, and the forge would be given HEAD without them; commit or stash them first:\n%s: %w",
			filepath.Join(root, what), dirty, errs.ErrValidation)
	}

	return nil
}

// push makes commit the head of a branch on remote.
func (git layerGit) push(ctx context.Context, root, remote, commit, branch string) error {
	args := []string{eventPush, gitQuiet}
	if git.target.Forge == provider.ForgeGitLab && branch == layerBranch {
		// Serving the engine's tree must not launch its own self-test pipeline.
		args = append(args, "--push-option=ci.skip")
	}

	args = append(args, remote, commit+":refs/heads/"+branch)
	_, err := git.run(ctx, root, args...)

	return err
}

// run returns git's trimmed standard output. A failure carries what git wrote
// to standard error, with the target's credential redacted.
func (git layerGit) run(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := git.output(ctx, dir, nil, args...)

	return strings.TrimSpace(string(out)), err
}

// output is run for the commands whose bytes matter: it hands git a standard
// input and returns standard output as it is.
func (git layerGit) output(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // G204: kit-authored arguments.
	cmd.Dir = dir
	cmd.Env = git.env
	cmd.Stdin = bytes.NewReader(stdin)

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, redactTargetSecrets(git.target, strings.TrimSpace(stderr.String())))
	}

	return stdout.Bytes(), nil
}
