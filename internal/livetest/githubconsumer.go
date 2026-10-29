// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/crypto/nacl/box"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// The hosted GitHub consumer trial: a consumer's GitHub fixture workflows run
// in a disposable repository of a Forge Lab test organization, against the
// engine this checkout pushed to that organization. These are its pure parts;
// the live calls are in githubtrial.go.

// GitHubPrerelease is the rolling pre-release Self Runtime Container moves to
// the commit it built, and the binary ref a trial's consumer installs.
const GitHubPrerelease = "v0.90.0-pre"

// GitHubEngine is the engine under test in the organization: what the rendered
// consumer names in place of the published diggsweden/forge-ci.
type GitHubEngine struct {
	Owner      string // organization login
	Repository string // repository name, without the owner
	Commit     string // the full commit its reusable workflows are pinned to
	Namespace  string // optional full runtime image namespace; empty preserves owner-global coordinates
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
	githubWorkflowUse = regexp.MustCompile(`(uses:[ \t]*['"]?)diggsweden/forge-ci(/\.github/workflows/[^@\s'"]+)@[^\s'"#]+`)
	githubBinaryRef   = regexp.MustCompile(`(?m)^([ \t]*forge-ci-binary-ref:[ \t]*).*$`)
	githubRuntimeRef  = regexp.MustCompile(`ghcr\.io/diggsweden/(forge-ci-runtime[a-z0-9.-]*):[A-Za-z0-9._-]+`)
	githubLeftover    = regexp.MustCompile(`uses:[ \t]*['"]?([^\s'"#]+/\.github/workflows/[^\s'"#]+)`)
)

// RenderGitHubConsumer puts the engine where a fixture names the published
// one: its reusable workflows at the commit, the rolling pre-release binary,
// and the runtime images Self Runtime Container built for the commit.
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
	rendered = githubBinaryRef.ReplaceAllString(rendered, "${1}"+GitHubPrerelease)
	rendered = githubRuntimeRef.ReplaceAllString(rendered, engine.Registry()+"/${1}:"+engine.ImageTag())

	var applied []string

	for _, input := range sortedKeys(overrides) {
		line := regexp.MustCompile(`(?m)^([ \t]*` + regexp.QuoteMeta(input) + `:[ \t]*).*$`)
		if !line.MatchString(rendered) {
			continue
		}

		rendered = line.ReplaceAllString(rendered, "${1}"+strings.ReplaceAll(overrides[input], "$", "$$"))
		applied = append(applied, input)
	}

	prefix := engine.Owner + "/" + engine.Repository + "/.github/workflows/"
	for _, use := range githubLeftover.FindAllStringSubmatch(rendered, -1) {
		if !strings.HasPrefix(use[1], prefix) || !strings.HasSuffix(use[1], "@"+engine.Commit) {
			return "", nil, fmt.Errorf("%s still calls %s after rendering, which is not the engine under test at its commit: %w", name, use[1], errs.ErrValidation)
		}
	}

	return rendered, applied, nil
}

// requireOrchestratorImages refuses a fixture that calls an orchestrator
// without passing runtime-image. An orchestrator's image inputs default to the
// released images, which an unreleased engine has not built; only an image a
// fixture names can be pointed at the organization's build of it.
func requireOrchestratorImages(name, text string) error {
	if !githubOrchestratorUse.MatchString(text) || githubRuntimeImageInput.MatchString(text) {
		return nil
	}

	return fmt.Errorf("%s calls a forge-ci orchestrator without passing runtime-image; pass it, and the image of each "+
		"project type the workflow builds (runtime-image-java for maven), as ghcr.io/diggsweden/forge-ci-runtime-*:<tag>, "+
		"so the trial can point them at the engine under test: %w", name, errs.ErrValidation)
}

var (
	// githubOrchestratorUse is a call of one of the engine's orchestrators,
	// whose runtime-image inputs default to released images.
	githubOrchestratorUse = regexp.MustCompile(`uses:[ \t]*['"]?diggsweden/forge-ci/\.github/workflows/[a-z-]+-orchestrator\.yml@`)
	// githubRuntimeImageInput is the base runtime image passed as an input.
	githubRuntimeImageInput = regexp.MustCompile(`(?m)^[ \t]+runtime-image:[ \t]*\S`)
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
	Event     string            // TrialPullRequest or TrialRelease
	Workflow  string            // the fixture workflow whose run is judged
	Fixtures  string            // the consumer's fixture directory; "" finds it
	Version   string            // the release version, vX.Y.Z
	Image     string            // comma-separated released image names to verify, or ""
	Platforms string            // RELEASE_PLATFORMS of the engine and the consumer
	Overrides map[string]string // input: value lines set in the rendered fixtures
}

// githubDefaultPlatforms is what a trial builds unless RELEASE_PLATFORMS
// states otherwise, as in the lab trials: each
// stated platform runs natively on GitHub's runner of that architecture.
const githubDefaultPlatforms = "linux/amd64"

// TrialDevRelease exercises a branch-dispatched development build without a
// stable release request, signing secrets, or a mutation of the source branch.
const TrialDevRelease = "dev-release"

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

// ParseGitHubTrialSettings reads the caller's key=value words for an event.
func ParseGitHubTrialSettings(event string, words []string) (GitHubTrialSettings, error) {
	settings := GitHubTrialSettings{Event: event, Platforms: githubDefaultPlatforms, Overrides: map[string]string{}}

	switch event {
	case TrialPullRequest:
		settings.Workflow = trialPullRequestWorkflow
	case TrialRelease:
		settings.Workflow, settings.Version = trialReleaseWorkflow, "v0.1.0"
	case TrialDevRelease:
		settings.Workflow = "release-dev.yml"
	default:
		return GitHubTrialSettings{}, fmt.Errorf("event must be %s, %s or %s, got %q: %w", TrialPullRequest, TrialRelease, TrialDevRelease, event, errs.ErrValidation)
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
	if key == "image" && settings.Image != "" {
		return fmt.Errorf("image must be supplied once as image=<name[,name...]>; repeated settings would omit earlier verification targets: %w", errs.ErrValidation)
	}

	if !found || value == "" || (!settings.setPlatforms(key, value) && !settings.set(key, value)) {
		return fmt.Errorf("setting %q: want workflow=<file>, fixtures=<dir>, version=X.Y.Z or image=<name[,name...]> (release), "+
			"RELEASE_PLATFORMS=linux/amd64[,linux/arm64], or nanolinter-image=/megalinter-image=<reference>: %w", word, errs.ErrValidation)
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
	release := settings.Event == TrialRelease

	switch {
	case key == settingWorkflow:
		settings.Workflow = value

		return trialWorkflow.MatchString(value)
	case key == "fixtures":
		settings.Fixtures = value

		return !strings.HasPrefix(value, "/") && !strings.Contains("/"+value+"/", "/../")
	case key == settingVersion:
		version, valid := trialVersionValue(value)
		settings.Version = "v" + version

		return release && valid
	case key == "image":
		settings.Image = value

		return release && validTrialImageNames(value)
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
