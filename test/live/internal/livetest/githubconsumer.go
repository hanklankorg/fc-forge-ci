// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/crypto/nacl/box"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// The hosted GitHub consumer trial: a consumer's GitHub fixture workflows run
// in a disposable repository of a Forge Lab test organization, against the
// engine this checkout pushed to that organization. These are its pure parts;
// the live calls are in githubtrial.go.

// GitHubPrerelease is the rolling pre-release of the engine's CLI. No consumer
// installs from it, and no rendered workflow may name it.
const GitHubPrerelease = "v0.90.0-pre"

const githubWorkflowDirectory = ".github/workflows"

// GitHubEngine is the engine under test in the organization: what the rendered
// consumer names in place of the published diggsweden/forge-ci.
type GitHubEngine struct {
	Owner      string // organization login
	Repository string // repository name, without the owner
	Commit     string // the full commit its reusable workflows are pinned to
	Namespace  string // optional full runtime image namespace; empty preserves owner-global coordinates
	// Digests are what the registry serves for ImageTag, by image name such as
	// forge-ci-runtime-base. Every image a fixture names renders by digest, as
	// every workflow admits only digest-pinned images; a fixture naming an
	// image the engine has no digest for is refused.
	Digests map[string]string
	admin   *workflowAdmin
}

// RuntimeImage is the digest-pinned reference of a runtime image the engine
// published, such as forge-ci-runtime-base, and false when it published none.
func (engine GitHubEngine) RuntimeImage(image string) (string, bool) {
	digest, ok := engine.Digests[image]
	if !ok {
		return "", false
	}

	return engine.Registry() + "/" + image + "@" + digest, true
}

// ImageTag is the runtime-image tag Self Runtime Container publishes for the
// commit.
func (engine GitHubEngine) ImageTag() string {
	if len(engine.Commit) < 7 {
		return ""
	}

	return "sha-" + engine.Commit[:7]
}

// Registry is the runtime image namespace, defaulting to the organization's
// GHCR namespace for callers that do not select a repository-specific one.
func (engine GitHubEngine) Registry() string {
	if engine.Namespace != "" {
		return engine.Namespace
	}

	return "ghcr.io/" + strings.ToLower(engine.Owner)
}

// KeylessIdentityRegexp is the certificate identity a keyless signature made
// by one of the engine's reusable workflows carries.
func (engine GitHubEngine) KeylessIdentityRegexp() string {
	return `^https://github\.com/` + regexp.QuoteMeta(engine.Owner+"/"+engine.Repository) + `/\.github/workflows/[^@]+@.+$`
}

var (
	githubWorkflowUse = regexp.MustCompile(`(uses:[ \t]*['"]?)[A-Za-z0-9_.-]+/forge-ci(/\.github/workflows/[^@\s'"]+)@[^\s'"#]+`)
	githubBinaryRef   = regexp.MustCompile(`(?m)^([ \t]*forge-ci-binary-ref:[ \t]*).*$`)
	githubRuntimeRef  = regexp.MustCompile(`ghcr\.io/[A-Za-z0-9_.-]+/(forge-ci-runtime[a-z0-9.-]*)(?::[A-Za-z0-9._-]+|@sha256:[0-9a-f]{64})`)
	githubLeftover    = regexp.MustCompile(`uses:[ \t]*['"]?([^\s'"#]+/\.github/workflows/[^\s'"#]+)`)
)

// RenderGitHubConsumer puts the engine where a fixture names the published
// one: its reusable workflows at the commit, the binary (the full engine
// commit: every route runs the canonical CLI built from it, or the engine
// baked into its images), and the runtime images Self Runtime Container
// built for the commit, by digest where the engine knows one.
// Overrides are further `input: value` lines the caller sets, such as a
// mirrored nanolinter image. Each replaces the line wherever this workflow
// writes it; the inputs it replaced are returned, so the caller can refuse a
// setting that no workflow of the fixture passes. A rendered workflow that
// still calls another engine is refused, because it would run the published
// layer and not this checkout's.
func RenderGitHubConsumer(name, text string, engine GitHubEngine, overrides map[string]string) (string, []string, error) {
	if engine.Owner == "" || engine.Repository == "" || len(engine.Commit) != 40 {
		return "", nil, fmt.Errorf("the engine under test needs an owner, a repository and a full commit: %w", errs.ErrValidation)
	}

	if !container.ValidRepositoryName(engine.Registry()) {
		return "", nil, fmt.Errorf("runtime image namespace must be a canonical OCI repository: %w", errs.ErrValidation)
	}

	if err := requireOrchestratorImages(name, text); err != nil {
		return "", nil, err
	}

	rendered := githubWorkflowUse.ReplaceAllString(text, "${1}"+engine.Owner+"/"+engine.Repository+"${2}@"+engine.Commit)
	rendered = githubBinaryRef.ReplaceAllString(rendered, "${1}"+engine.Commit)

	var unresolved []string

	rendered = githubRuntimeRef.ReplaceAllStringFunc(rendered, func(reference string) string {
		image := githubRuntimeRef.FindStringSubmatch(reference)[1]

		pinned, ok := engine.RuntimeImage(image)
		if !ok {
			unresolved = append(unresolved, image)

			return reference
		}

		return pinned
	})

	if len(unresolved) > 0 {
		return "", nil, fmt.Errorf("%s names %s, which the engine under test published no digest for; every workflow admits "+
			"its images only by digest: %w", name, strings.Join(slices.Compact(slices.Sorted(slices.Values(unresolved))), ", "), errs.ErrValidation)
	}

	if err := requireAdmittedImages(name, rendered); err != nil {
		return "", nil, err
	}

	rendered, applied := githubInputOverrides(rendered, overrides)

	prefix := engine.Owner + "/" + engine.Repository + "/.github/workflows/"
	for _, use := range githubLeftover.FindAllStringSubmatch(rendered, -1) {
		// A workflow of the consumer's own repository, such as its tests,
		// is no engine.
		if strings.HasPrefix(use[1], "./") {
			continue
		}

		if !strings.HasPrefix(use[1], prefix) || !strings.HasSuffix(use[1], "@"+engine.Commit) {
			return "", nil, fmt.Errorf("%s still calls %s after rendering, which is not the engine under test at its commit: %w", name, use[1], errs.ErrValidation)
		}
	}

	rendered, err := engine.admin.render(rendered, map[string]string{
		engine.Owner + "/" + engine.Repository + "/.github/workflows/": githubWorkflowDirectory,
		engine.Owner + "/" + engine.Repository + "/.github/actions/":   ".github/actions",
	}, engine.Commit)
	if err != nil {
		return "", nil, err
	}

	return rendered, applied, nil
}

// githubInputOverrides is shared by precredential admission and rendering so
// admission checks the same effective input text that the installer will use.
func githubInputOverrides(text string, overrides map[string]string) (string, []string) {
	var applied []string

	for _, input := range sortedKeys(overrides) {
		line := regexp.MustCompile(`(?m)^([ \t]*` + regexp.QuoteMeta(input) + `:[ \t]*).*$`)
		if !line.MatchString(text) {
			continue
		}

		text = line.ReplaceAllString(text, "${1}"+strings.ReplaceAll(overrides[input], "$", "$$"))
		applied = append(applied, input)
	}

	return text, applied
}

// requireOrchestratorImages refuses a fixture that calls an orchestrator
// without passing runtime-image and the canonical builder image. Only an
// image a fixture names can be pointed at the organization's build of it,
// and every orchestrator admits both as digests.
func requireOrchestratorImages(name, text string) error {
	if !githubOrchestratorUse.MatchString(text) {
		return nil
	}

	if !githubRuntimeImageInput.MatchString(text) {
		return fmt.Errorf("%s calls a forge-ci orchestrator without passing runtime-image; pass it, and the image of each "+
			"project type the workflow builds (runtime-image-java for maven), as ghcr.io/diggsweden/forge-ci-runtime-*:<tag> "+
			"or @sha256:<digest>, so the trial can point them at the engine under test: %w", name, errs.ErrValidation)
	}

	if !githubCanonicalBuilderInput.MatchString(text) {
		return fmt.Errorf("%s calls a forge-ci orchestrator without passing canonical-builder-image; pass it as "+
			"ghcr.io/diggsweden/forge-ci-runtime-go-1.27@sha256:<digest>, so the trial can point it at the engine under test: %w",
			name, errs.ErrValidation)
	}

	return nil
}

// requireAdmittedImages refuses a rendered call of an engine workflow that
// passes a runtime image other than by digest: the callee's first job would
// refuse it, after the push.
func requireAdmittedImages(name, rendered string) error {
	var workflow struct {
		Jobs map[string]struct {
			Uses string            `yaml:"uses"`
			With map[string]string `yaml:"with"`
		} `yaml:"jobs"`
	}

	if err := yaml.Unmarshal([]byte(rendered), &workflow); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	for _, job := range slices.Sorted(maps.Keys(workflow.Jobs)) {
		call := workflow.Jobs[job]
		workflowPath, _, _ := strings.Cut(call.Uses, "@")

		if !strings.Contains(workflowPath, "/.github/workflows/") {
			continue
		}

		for _, input := range slices.Sorted(maps.Keys(call.With)) {
			if !strings.HasPrefix(input, "runtime-image") && input != "canonical-builder-image" {
				continue
			}

			if value := call.With[input]; !pinnedImage.MatchString(value) {
				return fmt.Errorf("%s renders %s of job %s as %q, which the workflow admits only as image@sha256 digest; "+
					"the engine under test must resolve the digest of that image: %w", name, input, job, value, errs.ErrValidation)
			}
		}
	}

	return nil
}

var (
	// githubOrchestratorUse is a call of one of the engine's orchestrators,
	// whose runtime-image inputs default to released images.
	githubOrchestratorUse = regexp.MustCompile(`uses:[ \t]*['"]?[A-Za-z0-9_.-]+/forge-ci/\.github/workflows/[a-z-]+-orchestrator\.yml@`)
	// githubRuntimeImageInput is the base runtime image passed as an input.
	githubRuntimeImageInput = regexp.MustCompile(`(?m)^[ \t]+runtime-image:[ \t]*\S`)
	// githubCanonicalBuilderInput is the Go builder image an orchestrator
	// builds its canonical CLI in, passed as an input.
	githubCanonicalBuilderInput = regexp.MustCompile(`(?m)^[ \t]+canonical-builder-image:[ \t]*\S`)
)

// SealGitHubSecret encrypts a secret value for GitHub's Actions secrets API:
// a libsodium sealed box to the repository's public key, base64 encoded.
func SealGitHubSecret(publicKey, value string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil || len(raw) != 32 {
		return "", fmt.Errorf("the repository's Actions public key is not a 32-byte key: %w", errs.ErrValidation)
	}

	var key [32]byte

	copy(key[:], raw)

	sealed, err := box.SealAnonymous(nil, []byte(value), &key, rand.Reader)
	if err != nil {
		return "", fmt.Errorf("seal the secret: %w", err)
	}

	return base64.StdEncoding.EncodeToString(sealed), nil
}

// releaseVersionShape is the stable version a trial's release request names.
var releaseVersionShape = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// ReleaseRequestTag is the tag that asks the engine's orchestrator to release
// version, and the final tag the release creates.
func ReleaseRequestTag(version string) (string, string, error) {
	if !releaseVersionShape.MatchString(version) {
		return "", "", fmt.Errorf("release version %q is not a stable vX.Y.Z: %w", version, errs.ErrValidation)
	}

	return "release-request/" + version, version, nil
}

// GitHubTrialSettings is what the caller of a hosted GitHub trial chose.
type GitHubTrialSettings struct {
	Event     string            // TrialPullRequest, TrialDevRelease, TrialRelease or TrialLifecycle
	Workflow  string            // the fixture workflow whose run is judged
	Fixtures  string            // the consumer's fixture directory; "" finds it
	Version   string            // the release version, vX.Y.Z
	Image     string            // comma-separated released image names to verify, or ""
	Platforms string            // RELEASE_PLATFORMS of the engine and the consumer
	Fork      bool              // head=fork: the pull request comes from a fork of the consumer
	Overrides map[string]string // input: value lines set in the rendered fixtures
}

// githubDefaultPlatforms is what a trial builds unless RELEASE_PLATFORMS
// states otherwise, as in the lab trials: each
// stated platform runs natively on GitHub's runner of that architecture.
const githubDefaultPlatforms = "linux/amd64"

// TrialDevRelease exercises a branch-dispatched development build without a
// stable release request, signing secrets, or a mutation of the source branch.
const TrialDevRelease = "dev-release"

// GitHubDevelopmentWorkflow is the fixture workflow a development build
// dispatches: the consumer's own call of the snapshot orchestrator. A
// lifecycle's development phase dispatches the same workflow.
const GitHubDevelopmentWorkflow = "release-dev.yml"

// githubPlatforms reports whether value is a duplicate-free list of the
// platforms GitHub's hosted runners build natively.
func githubPlatforms(value string) bool {
	seen := map[string]bool{}

	for platform := range strings.SplitSeq(value, ",") {
		if (platform != "linux/amd64" && platform != "linux/arm64") || seen[platform] {
			return false
		}

		seen[platform] = true
	}

	return true
}

// GitHubEnginePlatforms are the runtime image platforms the engine builds for
// a trial: always linux/amd64, which the orchestrators' jobs run on, plus
// linux/arm64 when the trial states it. The consumer gets exactly what was
// stated, so an arm64-only consumer still has the images its jobs need.
func GitHubEnginePlatforms(stated string) string {
	if slices.Contains(strings.Split(stated, ","), "linux/arm64") {
		return "linux/amd64,linux/arm64"
	}

	return githubDefaultPlatforms
}

// pinnedImage is an image reference pinned by a full digest, the form the
// fixtures pin theirs in: a shortened digest would reach the job and fail
// there, as an unpullable image, instead of here.
var pinnedImage = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]*@sha256:[0-9a-f]{64}$`)

// githubOverridable are the fixture inputs a setting may replace: the lab's
// mirrors of images a published fixture names.
var githubOverridable = map[string]bool{"nanolinter-image": true, "megalinter-image": true}

// githubPlaceholderLintImage is a lint image input still pinned by the all-zero
// digest the examples carry until their consumer pins a real one.
var githubPlaceholderLintImage = regexp.MustCompile(`(?m)^[ \t]*(nanolinter-image|megalinter-image):[ \t]*\S+@sha256:0{64}\b`)

// PullRequestWorkflow is the fixture workflow the event runs on a pull
// request: the judged one of a pull request, the fixed one of a lifecycle,
// and none for a release or a development build.
func (settings *GitHubTrialSettings) PullRequestWorkflow() string {
	switch settings.Event {
	case TrialPullRequest:
		return settings.Workflow
	case TrialLifecycle:
		return trialPullRequestWorkflow
	default:
		return ""
	}
}

// RequireGitHubLintImage refuses a pull-request workflow that, with the
// caller's overrides, still hands its lint engine the placeholder digest. No
// runner can pull it, so the lint job would fail after the repository and the
// run exist; the refusal names the setting before either does.
func RequireGitHubLintImage(name, text string, overrides map[string]string) error {
	effective, _ := githubInputOverrides(text, overrides)

	if placeholder := githubPlaceholderLintImage.FindStringSubmatch(effective); placeholder != nil {
		return fmt.Errorf("%s pins %s by the placeholder digest, which no runner can pull; pass %s=<repository>@sha256:<digest> "+
			"of an image GitHub's runners can pull: %w", name, placeholder[1], placeholder[1], errs.ErrValidation)
	}

	return nil
}

// ParseGitHubTrialSettings reads the caller's key=value words for an event.
func ParseGitHubTrialSettings(event string, words []string) (GitHubTrialSettings, error) {
	settings := GitHubTrialSettings{Event: event, Platforms: githubDefaultPlatforms, Overrides: map[string]string{}}

	switch event {
	case TrialPullRequest:
		settings.Workflow = trialPullRequestWorkflow
	case TrialRelease:
		settings.Workflow, settings.Version = trialReleaseWorkflow, "v0.1.0"
	case TrialDevRelease:
		settings.Workflow = GitHubDevelopmentWorkflow
	case TrialLifecycle:
		// The release phase's workflow and version; the pull request's and the
		// development build's workflows are the fixed fixture names.
		settings.Workflow, settings.Version = trialReleaseWorkflow, "v0.1.0"
	default:
		return GitHubTrialSettings{}, fmt.Errorf("event must be %s, %s, %s or %s, got %q: %w", TrialPullRequest, TrialDevRelease, TrialRelease, TrialLifecycle, event, errs.ErrValidation)
	}

	for _, word := range words {
		if err := settings.parseWord(word); err != nil {
			return GitHubTrialSettings{}, err
		}
	}

	return settings, nil
}

func (settings *GitHubTrialSettings) parseWord(word string) error {
	key, value, found := strings.Cut(word, "=")
	if key == settingImage && settings.Image != "" {
		return fmt.Errorf("image must be supplied once as image=<name[,name...]>; repeated settings would omit earlier verification targets: %w", errs.ErrValidation)
	}

	if !found || value == "" || (!settings.setPlatforms(key, value) && !settings.set(key, value)) {
		return fmt.Errorf("setting %q: want workflow=<file> (not lifecycle), fixtures=<dir>, version=X.Y.Z or image=<name[,name...]> (release or lifecycle), "+
			"head=fork (pull-request), RELEASE_PLATFORMS=linux/amd64[,linux/arm64], or nanolinter-image=/megalinter-image=<reference>: %w", word, errs.ErrValidation)
	}

	return nil
}

// setPlatforms takes RELEASE_PLATFORMS and reports whether it was that setting
// with a valid value; any other key is left to set.
func (settings *GitHubTrialSettings) setPlatforms(key, value string) bool {
	if key != releasePlatformsVariable {
		return false
	}

	settings.Platforms = value

	return githubPlatforms(value)
}

func (settings *GitHubTrialSettings) set(key, value string) bool {
	release := settings.Event == TrialRelease || settings.Event == TrialLifecycle

	switch {
	case key == settingWorkflow:
		settings.Workflow = value

		// A lifecycle runs the three fixed fixture workflows.
		return settings.Event != TrialLifecycle && trialWorkflow.MatchString(value)
	case key == "fixtures":
		settings.Fixtures = value

		return !strings.HasPrefix(value, "/") && !strings.Contains("/"+value+"/", "/../")
	case key == settingVersion:
		version, valid := trialVersionValue(value)
		settings.Version = "v" + version

		return release && valid
	case key == settingImage:
		settings.Image = value

		return release && validTrialImageNames(value)
	case key == "head":
		// The one head a pull request has besides the consumer's own branch.
		settings.Fork = true

		return settings.Event == TrialPullRequest && value == "fork"
	case githubOverridable[key]:
		settings.Overrides[key] = value

		return pinnedImage.MatchString(value)
	default:
		return false
	}
}

func validTrialImageNames(value string) bool {
	seen := map[string]bool{}
	validName := regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*$`)

	for image := range strings.SplitSeq(value, ",") {
		if !validName.MatchString(image) || seen[image] || strings.Contains("/"+image+"/", "/../") {
			return false
		}

		seen[image] = true
	}

	return true
}
