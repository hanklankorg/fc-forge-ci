// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"codefloe.com/itiquette/forge-lab-contract/labcontract"
	"golang.org/x/mod/semver"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// A consumer trial runs a real repository's own workflow on the lab against
// the layer under test: "does my repository still pass with this engine
// checkout?", asked before the engine commit is published anywhere.
//
// The consumer is HEAD of a checkout the caller names. It reaches the lab as
// one parentless commit, as the layer does, with one difference: wherever its
// files name the layer it pins today, they name the layer under test. What
// that is, is read from the consumer's own workflows and from nothing else,
// so the engine holds no table of consumers.
//
// The checkout is only read. The commits are written in a temporary
// repository that borrows the checkout's objects, so not even an unreachable
// object is left behind in it.

const (
	// consumerWorkflows is where Forgejo reads a repository's workflows.
	consumerWorkflows = layerDir + "/workflows"

	// consumerCanary is the file a trial's pull request adds. Its header is
	// for the consumer's own licence check, which reads every file. Each tag
	// is written in two parts, so that a licence scanner reading this file
	// does not take the canary's header for this file's own.
	consumerCanary     = ".forge-ci-consumer-trial.txt"
	consumerCanaryText = "# SPDX-FileCopyrightText" + ": 2026 Digg - Agency for Digital Government\n#\n" +
		"# SPDX-License-Identifier" + ": CC0-1.0\n\nA consumer trial of the forge-ci live tier adds this file.\n"

	// The messages of the two commits a trial writes. A consumer lints its
	// pull request's commits with its own rules, so the one under review has a
	// Conventional subject, a sign-off by its author and no line over 72
	// characters.
	consumerAuthor        = "livetest"
	consumerSignOff       = "Signed-off-by: " + consumerAuthor + " <" + requestSigner + ">"
	consumerBaseMessage   = "test: snapshot the consumer for a trial\n\n" + consumerSignOff
	consumerChangeMessage = "test: add the consumer trial canary\n\n" + consumerSignOff
)

// The events a trial can start a consumer's workflows with: one workflow
// each, or lifecycle, the linked flow of a pull request merged natively, a
// development run on the merged main and a signed release request from it,
// or snapshot, that development run alone over every image the planner plans
// from the consumer's declaration.
const (
	TrialPullRequest = "pull-request"
	TrialRelease     = "release"
	TrialLifecycle   = "lifecycle"
	TrialSnapshot    = "snapshot"
)

// ConsumerTrial is the scenario name of a Forgejo consumer trial's scratch
// repositories: the consumer's copy, and LayerScenario of it for the layer
// under test that its workflows call. ConsumerLifecycle names a lifecycle's
// consumer copy, on either forge. On Forgejo a lifecycle serves the layer
// from the trial's layer repository: a run admits a kaniko job image only
// for the repository its source label names, and one image labelled for
// that repository (ConsumerTrialLayer) serves every event of a consumer
// trial, a snapshot's too.
// SnapshotScenario names a snapshot's consumer copy, on either forge.
const (
	ConsumerTrial     = "consumer-trial"
	ConsumerLifecycle = "consumer-lifecycle"
	SnapshotScenario  = "snapshot"
)

// LayerScenario is the scenario name of the scratch repository a scenario
// pushes the layer under test to, beside its own.
func LayerScenario(scenario string) string { return scenario + "-layer" }

// ConsumerTrialLayer is the address, ServerURL and Repository alone, of the
// repository a Forgejo consumer trial pushes the layer under test to,
// whichever its event: a run admits a kaniko job image only when its source
// label names the repository the layer's workflows run from.
func ConsumerTrialLayer() (Layer, error) {
	lab, err := currentLabContract()
	if err != nil {
		return Layer{}, err
	}

	return consumerTrialLayer(lab)
}

// ConsumerTrialLayerURL is ConsumerTrialLayer's web URL, for the publisher of
// the lab's kaniko job image.
func ConsumerTrialLayerURL() (string, error) {
	layer, err := ConsumerTrialLayer()
	if err != nil {
		return "", err
	}

	return layer.ServerURL + "/" + layer.Repository, nil
}

// consumerTrialLayer is the layer's address as PushLayer serves it: the
// endpoint's web root, the credential's owner and the scratch repository.
func consumerTrialLayer(lab *labcontract.Contract) (Layer, error) {
	endpoint, err := lab.Endpoint(labcontract.Kind(provider.ForgeForgejo))
	if err != nil {
		return Layer{}, err
	}

	return Layer{ServerURL: strings.TrimSuffix(endpoint.WebBaseURL, "/"), Repository: endpoint.Owner() + "/" + ScratchRepo(LayerScenario(ConsumerTrial))}, nil
}

// The values a repository variable of a trial may refer to, because the
// caller cannot know them before the run.
const (
	trialLayerCommit   = "@layer-commit"
	trialRequestCommit = "@request-commit"
)

// TrialSettings is what the caller of a trial decides.
type TrialSettings struct {
	Event string
	// Workflow is the file under .forgejo/workflows whose run is judged: for
	// a lifecycle the release workflow, the last of its three.
	Workflow string
	// DemoPullRequest is the workflow a demo's pull request is judged by,
	// after its release.
	DemoPullRequest string
	// Version is what a release trial requests, as release-request/v<Version>.
	Version string
	// NextVersion requests a second release in the same repository.
	NextVersion string
	// Variables are repository variables to set, by name.
	Variables map[string]string
	// TokenSecrets are repository secrets that receive the lab account's token.
	TokenSecrets []string
	// FileSecrets are repository secrets read from the caller's own token
	// files, by secret name: a credential for a service outside the lab, such
	// as a read-only GitHub token for mise.
	FileSecrets map[string]string
	// Images are images on an external registry a release trial verifies
	// against the trial's key: a repository, whose v<version> tag is meant, or
	// a tagged reference, such as a flavour's v<version>-go.
	Images []string
	// InterruptAfter names a job of a release trial: once a job whose name
	// starts with it succeeds, the run is cancelled, as a release interrupted
	// after that step would be. The trial then checks that nothing was
	// released; requesting the same version again shows what the rerun does
	// with what the cancelled run left.
	InterruptAfter string
	// ReleaseImagesConfig opts into local-registry output readback. The
	// declaration is read from the verified prepared commit, not the host tree.
	ReleaseImagesConfig string
	// SnapshotDeclaration is the declaration a snapshot trial is taken from
	// in place of ServiceSnapshotSource's: a variant of the consumer's own,
	// for a policy its release declaration does not have.
	SnapshotDeclaration string
	// ReleaseOption is the release option the trial gives the release, as
	// the repository variable the consumer's release workflow reads
	// (ForgejoReleaseOptionVariables), and holds it to: OptionDraft,
	// OptionPrerelease or OptionRetention. Empty gives and judges none.
	ReleaseOption string
	// Fixtures is a directory of the consumer whose files a release trial
	// stages over the consumer's root (WithForgejoFixtures): an alternative
	// of its release that the consumer keeps beside the one it runs, since
	// two release workflows would both run on one release-request tag.
	Fixtures string
}

// The release options a trial gives a release and judges the effect of.
const (
	// OptionDraft: the release stays a draft.
	OptionDraft = "draft"
	// OptionPrerelease: the release policy admits X.Y.Z-<prerelease>, which
	// the trial then requests, and the forge records a prerelease.
	OptionPrerelease = "prerelease"
	// OptionRetention: after a second release, the first keeps its checksums,
	// signatures and SBOMs and loses its other files.
	OptionRetention = "retention"
)

var (
	trialName     = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	trialImage    = regexp.MustCompile(`^[a-z0-9.-]+(:[0-9]+)?(/[a-z0-9._-]+)+(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?$`)
	trialWorkflow = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.ya?ml$`)
	trialVersion  = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	// trialPrerelease is the version OptionPrerelease requests: X.Y.Z and
	// dot-separated prerelease identifiers, without build metadata.
	trialPrerelease = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*$`)
)

// The trial's own settings, and the workflow each event judges by default.
const (
	settingWorkflow            = "workflow"
	settingDemoPullRequest     = "pull-request-workflow"
	settingVersion             = "version"
	settingNextVersion         = "next-version"
	settingToken               = "token"
	settingSecretFile          = "secret-file"
	settingImage               = "image"
	settingInterrupt           = "interrupt-after"
	settingReleaseImagesConfig = "release-images-config"
	settingSnapshotDeclaration = "snapshot-declaration"
	settingReleaseOption       = "release-option"
	settingFixtures            = "fixtures"

	trialPullRequestWorkflow = "pullrequest.yml"
	trialReleaseWorkflow     = "release.yml"
)

// ParseTrialSettings reads the words after the checkout and the event. Each
// is key=value. A lowercase key is one of the trial's own settings, workflow,
// version, next-version, token, image, interrupt-after, release-option and
// fixtures; any other key is the name of a repository variable, in capitals
// as Forgejo stores it.
//
// A lifecycle's three workflows are fixed, so it takes no workflow=; it
// requests one release, so it takes version= and image= but no next-version=
// or interrupt-after=. A snapshot's one workflow is the harness's and it
// releases nothing, so it takes none of these; it alone takes
// snapshot-declaration=, the declaration it is taken from.
//
// No word carries a secret. A caller can ask for the lab account's token, by
// naming the repository secret that receives it, and for a token from one of
// its own files, by secret-file=<SECRET_NAME>:<absolute path> (owner-only file,
// one token on one line). An external registry's token comes from a file
// too (see ExternalRegistryCredential).
func ParseTrialSettings(event string, words []string) (TrialSettings, error) {
	settings := TrialSettings{Event: event, DemoPullRequest: trialPullRequestWorkflow, Variables: map[string]string{}, FileSecrets: map[string]string{}}

	switch event {
	case TrialPullRequest:
		settings.Workflow = trialPullRequestWorkflow
	case TrialRelease, TrialLifecycle:
		settings.Workflow, settings.Version = trialReleaseWorkflow, "0.0.1"
	case TrialSnapshot:
	default:
		return TrialSettings{}, fmt.Errorf("the event is %q, want %s, %s, %s or %s: %w", event, TrialPullRequest, TrialRelease, TrialLifecycle, TrialSnapshot, errs.ErrValidation)
	}

	for _, word := range words {
		if key, value, _ := strings.Cut(word, "="); value == "" || !settings.set(key, value) {
			return TrialSettings{}, fmt.Errorf("setting %q is none of %s=<file>.yml (not lifecycle or snapshot), "+
				settingDemoPullRequest+"=<file>.yml (release only, for a demo), %s=<X.Y.Z> (release or lifecycle), %s=<X.Y.Z> (second release), %s=<SECRET_NAME>, "+
				settingSecretFile+"=<SECRET_NAME>:<absolute file>, "+
				settingReleaseImagesConfig+"=<relative declaration> (release only), "+
				settingSnapshotDeclaration+"=<relative declaration> (snapshot only), "+
				settingReleaseOption+"=<"+OptionDraft+"|"+OptionPrerelease+"|"+OptionRetention+"> (release only), "+
				settingFixtures+"=<relative directory> (release only), "+
				"%s=<registry>/<repository>[:<tag>] (release or lifecycle), %s=<job name> (release only) or <VARIABLE_NAME>=<value>, "+
				"each name once, where a value starting with @ is %s or %s: %w",
				word, settingWorkflow, settingVersion, settingNextVersion, settingToken, settingImage, settingInterrupt, trialLayerCommit, trialRequestCommit, errs.ErrValidation)
		}
	}

	if err := settings.combinationGap(); err != nil {
		return TrialSettings{}, err
	}

	return settings, nil
}

// lifecycleJobImage is the variable the consumer's pull-request jobs run in
// and a lifecycle's development phase builds the service with: one nanolinter
// image of the java flavour, which carries nanolinter, its check tools, a JDK
// and Maven. A plain Java runtime image fails the demo's lint job on the
// forge, "nanolinter is not on PATH".
const lifecycleJobImage = "CI_JOB_IMAGE"

// ResolvedVariables returns the caller's variables with the two references
// replaced: the commit of the layer under test, and the commit the trial
// pushed as the consumer's main, which a release trial requests.
func (settings *TrialSettings) ResolvedVariables(layerCommit, requestCommit string) map[string]string {
	resolved := make(map[string]string, len(settings.Variables))

	for name, value := range settings.Variables {
		switch value {
		case trialLayerCommit:
			value = layerCommit
		case trialRequestCommit:
			value = requestCommit
		}

		resolved[name] = value
	}

	return resolved
}

// combinationGap refuses settings that are each valid and do not go
// together.
func (settings *TrialSettings) combinationGap() error {
	if settings.NextVersion != "" && (settings.InterruptAfter != "" || semver.Compare("v"+settings.NextVersion, "v"+settings.Version) <= 0) {
		return fmt.Errorf("next-version requires an increasing release version and cannot accompany interrupt-after: %w", errs.ErrValidation)
	}

	if err := settings.releaseOptionGap(); err != nil {
		return err
	}

	if settings.ReleaseImagesConfig != "" && len(settings.Images) != 0 {
		return fmt.Errorf("release-images-config selects local readback and cannot accompany external image=: %w", errs.ErrValidation)
	}

	if settings.fixedWorkflows() {
		return settings.lifecycleImages()
	}

	return nil
}

// releaseOptionGap refuses a version that is not the option's: a prerelease
// version is requested with OptionPrerelease and with no other, and retention
// shows on the first of two releases.
func (settings *TrialSettings) releaseOptionGap() error {
	if prerelease := trialPrerelease.MatchString(settings.Version); prerelease != (settings.ReleaseOption == OptionPrerelease) {
		return fmt.Errorf("%s=%s and %s=<X.Y.Z-identifiers>, a prerelease version, are given together or not at all: %w", settingReleaseOption, OptionPrerelease, settingVersion, errs.ErrValidation)
	}

	if settings.ReleaseOption == OptionRetention && settings.NextVersion == "" {
		return fmt.Errorf("%s=%s needs %s=<X.Y.Z>: the first release is trimmed when the second is published: %w", settingReleaseOption, OptionRetention, settingNextVersion, errs.ErrValidation)
	}

	return nil
}

// lifecycleImages holds a lifecycle, or a snapshot, to the images its
// development phase builds with, before anything is created: the job image pinned by
// digest, and under kaniko, the consumer's CI_IMAGE_BUILDER as in every
// consumer trial event, the kaniko job image pinned by digest too. The run
// then admits that image by its digest and source label in the development
// phase (setup-image-builder); refusing a floating reference here saves the
// two phases before it.
func (settings *TrialSettings) lifecycleImages() error {
	if !container.ValidDigestReference(settings.Variables[lifecycleJobImage]) {
		return fmt.Errorf("%s requires %s=<digest reference>: a nanolinter image of the consumer's toolchain flavour, the image its lint and build jobs run in and its development phase builds the service's producers in: %w", settings.Event, lifecycleJobImage, errs.ErrValidation)
	}

	if settings.Variables["CI_IMAGE_BUILDER"] == ImageBuilderKaniko && !container.ValidDigestReference(settings.Variables["CI_IMAGE_BUILDER_JOB_IMAGE"]) {
		return fmt.Errorf("%s with CI_IMAGE_BUILDER=kaniko requires CI_IMAGE_BUILDER_JOB_IMAGE=<digest reference>, the kaniko job image its development phase builds the image in: %w", settings.Event, errs.ErrValidation)
	}

	return nil
}

// fixedWorkflows is whether the event's workflows are fixed, so it takes no
// workflow=: a lifecycle's three, a snapshot's harness workflow.
func (settings *TrialSettings) fixedWorkflows() bool {
	return settings.Event == TrialLifecycle || settings.Event == TrialSnapshot
}

// requestsRelease is whether the event requests a release, so it takes
// version= and image=.
func (settings *TrialSettings) requestsRelease() bool {
	return settings.Event == TrialRelease || settings.Event == TrialLifecycle
}

// set takes one setting and reports whether it is one.
func (settings *TrialSettings) set(key, value string) bool {
	switch key {
	case settingReleaseImagesConfig, settingSnapshotDeclaration:
		return settings.setDeclaration(key, value)
	case settingFixtures:
		known := settings.Fixtures != ""
		settings.Fixtures = value

		return settings.Event == TrialRelease && !known && pathsafe.Relative(value)
	case settingWorkflow:
		settings.Workflow = value

		return !settings.fixedWorkflows() && trialWorkflow.MatchString(value)
	case settingDemoPullRequest:
		settings.DemoPullRequest = value

		return settings.Event == TrialRelease && trialWorkflow.MatchString(value)
	case settingToken, settingSecretFile:
		return settings.setSecret(key, value)
	case settingVersion, settingImage:
		return settings.requestsRelease() && settings.setReleaseOnly(key, value)
	case settingNextVersion, settingInterrupt:
		return settings.Event == TrialRelease && settings.setReleaseOnly(key, value)
	case settingReleaseOption:
		known := settings.ReleaseOption != ""
		settings.ReleaseOption = value

		return settings.Event == TrialRelease && !known && slices.Contains([]string{OptionDraft, OptionPrerelease, OptionRetention}, value)
	}

	return settings.setVariable(key, value)
}

// setDeclaration takes a declaration's path inside the consumer, once, for
// the one event that reads it: a release its images' declaration to read
// back, a snapshot the declaration it is taken from.
func (settings *TrialSettings) setDeclaration(key, value string) bool {
	event, declaration := TrialRelease, &settings.ReleaseImagesConfig
	if key == settingSnapshotDeclaration {
		event, declaration = TrialSnapshot, &settings.SnapshotDeclaration
	}

	if settings.Event != event || *declaration != "" || !pathsafe.Relative(value) {
		return false
	}

	*declaration = value

	return true
}

// GitLabSnapshotDeclaration reads the settings of a GitLab snapshot trial,
// one key=value a line: none, or snapshot-declaration=<relative
// declaration> alone, the declaration the snapshot is taken from in place of
// ServiceSnapshotSource's. The trial's images are archives of its
// environment, so it takes no other setting.
func GitLabSnapshotDeclaration(input string) (string, error) {
	if input == "" {
		return "", nil
	}

	settings := TrialSettings{Event: TrialSnapshot}

	for word := range strings.SplitSeq(input, "\n") {
		if key, value, _ := strings.Cut(word, "="); key != settingSnapshotDeclaration || !settings.setDeclaration(key, value) {
			return "", fmt.Errorf("a GitLab %s takes job-archive= and %s=<relative declaration>, once, not %q: %w", TrialSnapshot, settingSnapshotDeclaration, word, errs.ErrValidation)
		}
	}

	return settings.SnapshotDeclaration, nil
}

// setSecret takes a secret the trial stores for the consumer, from the one
// source that names it: a token of the environment, or a file.
func (settings *TrialSettings) setSecret(key, value string) bool {
	if key == settingSecretFile {
		return settings.setFileSecret(value)
	}

	_, fromFile := settings.FileSecrets[value]

	return !fromFile && appendOnce(&settings.TokenSecrets, value) && trialName.MatchString(value)
}

// setFileSecret keeps path/name admission and credential-source exclusivity
// together, before recording a file-backed secret.
func (settings *TrialSettings) setFileSecret(value string) bool {
	name, path, _ := strings.Cut(value, ":")
	_, seen := settings.FileSecrets[name]

	if seen || slices.Contains(settings.TokenSecrets, name) || !trialName.MatchString(name) || !filepath.IsAbs(path) {
		return false
	}

	settings.FileSecrets[name] = path

	return true
}

// setReleaseOnly takes one of the settings only a trial that releases has.
func (settings *TrialSettings) setReleaseOnly(key, value string) bool {
	switch key {
	case settingVersion:
		version, valid := trialVersionValue(value)
		settings.Version = version

		// releaseOptionGap holds a prerelease version to its option.
		return valid || (settings.Event == TrialRelease && trialPrerelease.MatchString(version))
	case settingNextVersion:
		version, valid := trialVersionValue(value)
		if settings.NextVersion != "" || !valid {
			return false
		}

		settings.NextVersion = version

		return true
	case settingImage:
		return appendOnce(&settings.Images, value) && trialImage.MatchString(value)
	default:
		known := settings.InterruptAfter != ""
		settings.InterruptAfter = value

		return !known && strings.TrimSpace(value) == value
	}
}

// trialVersionValue reads a version= setting, X.Y.Z on every forge, a leading
// v accepted, and returns it as X.Y.Z.
func trialVersionValue(value string) (string, bool) {
	version := strings.TrimPrefix(value, "v")

	return version, trialVersion.MatchString(version)
}

// appendOnce appends value and reports whether it was not there yet.
func appendOnce(list *[]string, value string) bool {
	known := slices.Contains(*list, value)
	*list = append(*list, value)

	return !known
}

func (settings *TrialSettings) setVariable(name, value string) bool {
	_, twice := settings.Variables[name]
	settings.Variables[name] = value

	reference := !strings.HasPrefix(value, "@") || value == trialLayerCommit || value == trialRequestCommit

	return !twice && reference && trialName.MatchString(name) && !strings.ContainsAny(value, "\r\n")
}

// ConsumerPin is how a consumer's workflows name the layer today.
type ConsumerPin struct {
	// Actions is what its action references start with, up to and including
	// the actions directory: https://host/owner/repository/.forgejo/actions/.
	Actions string
	// Workflows is the same of its called workflows:
	// owner/repository/.forgejo/workflows/.
	Workflows string
	// Commit is the one commit every reference ends in, or the exact
	// PIN_FORGE_CI_SHA draft marker replaced before a lab workflow is served.
	Commit string
	// BinarySHA256 is the digest of the layer's binary it passes as
	// expected-sha256, or "" when it passes none.
	BinarySHA256 string
}

// repository is the shared owner/name identity of admitted layer references.
func (pin ConsumerPin) repository() string {
	if pin.Workflows != "" {
		return strings.TrimSuffix(pin.Workflows, "/"+consumerWorkflows+"/")
	}

	if actionURL, err := url.Parse(pin.Actions); err == nil {
		return strings.TrimSuffix(strings.TrimPrefix(actionURL.Path, "/"), "/"+layerDir+"/actions/")
	}

	return ""
}

var (
	consumerActionUse   = regexp.MustCompile(`uses:[ \t]*['"]?(https://[^/\s'"]+/[^/\s]+/[^/\s]+/\.forgejo/actions/)[^@\s'"]+@([0-9a-f]{64}|[0-9a-f]{40}|PIN_FORGE_CI_SHA)\b`)
	consumerWorkflowUse = regexp.MustCompile(`uses:[ \t]*['"]?([^/\s'"]+/[^/\s]+/\.forgejo/workflows/)[^@\s'"]+@([0-9a-f]{64}|[0-9a-f]{40}|PIN_FORGE_CI_SHA)\b`)
	consumerBinary      = regexp.MustCompile(`expected-sha256:[ \t]*['"]?([0-9a-f]{64})\b`)

	// consumerLayerUse finds every reference into a layer directory, pinned
	// or not, for the check that none is left pointing away from the lab.
	consumerLayerUse = regexp.MustCompile(`uses:[ \t]*['"]?([^\s'"#]+/\.forgejo/(?:actions|workflows)/[^\s'"#]+)`)

	// foldedExpression is an expression whose closing braces a formatter put
	// on the next line.
	foldedExpression = regexp.MustCompile(`\s*\n\s*\}\}`)
)

// ReadConsumerPin reads the pin from the consumer's workflows, by file name.
// A consumer that names the layer in two places or at two commits is refused:
// one layer under test cannot stand in for both, and guessing which is meant
// would test something the consumer does not run.
// Draft templates may use the one explicit layer marker; this is lab-only
// rendering admission, not production pin validation or publication evidence.
func ReadConsumerPin(workflows map[string]string) (ConsumerPin, error) {
	var (
		pin      ConsumerPin
		conflict error
	)

	read := func(what string, into *string, value string) {
		if *into != "" && *into != value && conflict == nil {
			conflict = fmt.Errorf("the consumer's workflows name %s as both %s and %s: %w", what, *into, value, errs.ErrValidation)
		}

		*into = value
	}

	for _, name := range sortedKeys(workflows) {
		text := workflows[name]

		for _, use := range consumerActionUse.FindAllStringSubmatch(text, -1) {
			read("the layer's actions", &pin.Actions, use[1])
			read("the layer's commit", &pin.Commit, use[2])
		}

		for _, use := range consumerWorkflowUse.FindAllStringSubmatch(text, -1) {
			read("the layer's workflows", &pin.Workflows, use[1])
			read("the layer's commit", &pin.Commit, use[2])
		}

		for _, digest := range consumerBinary.FindAllStringSubmatch(text, -1) {
			read("the layer's binary", &pin.BinarySHA256, digest[1])
		}
	}

	if conflict != nil {
		return ConsumerPin{}, conflict
	}

	if pin.Actions != "" && pin.Workflows != "" && (ConsumerPin{Actions: pin.Actions}).repository() != pin.repository() {
		return ConsumerPin{}, fmt.Errorf("the consumer's actions and workflows name different layer repositories: %w", errs.ErrValidation)
	}

	if pin.Commit == "" {
		return ConsumerPin{}, fmt.Errorf("no workflow under %s uses an action or a workflow of the layer at a commit, "+
			"as https://<host>/<owner>/<repository>/.forgejo/actions/<name>@<commit> or <owner>/<repository>/.forgejo/workflows/<file>@<commit>: %w",
			consumerWorkflows, errs.ErrValidation)
	}

	return pin, nil
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}

	slices.Sort(keys)

	return keys
}

// ConsumerFacts are the lab's values for what a consumer's organization
// brings to the layer: by the name of the layer's workflow input, and by the
// name of the layer's secret.
type ConsumerFacts struct {
	Inputs  map[string]string
	Secrets map[string]string
}

// The inputs whose literal a consumer also writes where it is not an input:
// the signer image as a job's container, the key digest in the step that
// stages the key, both in its own policy tests. Their literal is replaced
// wherever it stands. Every other literal is replaced as `input: literal`.
const (
	inputSignerImage = "signer-image"
	inputKeyDigest   = "cosign-public-key-sha256"
)

// ConsumerPlan is what a trial does to a consumer: text to replace in its
// tracked files, and the repository variables and secrets its workflows read
// the lab's values from.
type ConsumerPlan struct {
	Pin ConsumerPin
	// Replace is old, new pairs, applied to every tracked text file at once.
	Replace        []string
	Variables      map[string]string
	Secrets        map[string]string
	workflowInputs map[string]string
	repository     string
}

// MergeConsumerVariables binds the trial's runtime values without overwriting
// conflicting consumer-plan values. Neither input is modified; errors name the
// variable, never its value. These are create-only bindings for fresh scratch
// repositories, not updates: no stale repository value is retained or deleted.
// Buildah's empty optional job image means the runner default, not an Actions
// variable with an empty value (which Forgejo refuses). Other empty bindings fail.
func MergeConsumerVariables(plan, runtime map[string]string) (map[string]string, error) {
	result := make(map[string]string, len(plan)+len(runtime))
	maps.Copy(result, plan)

	for _, key := range sortedKeys(runtime) {
		value := runtime[key]
		if previous, found := result[key]; found && previous != value {
			return nil, fmt.Errorf("consumer variable %s conflicts with the trial's runtime binding: %w", key, errs.ErrValidation)
		}

		result[key] = value
	}

	if builder, found := result["CI_IMAGE_BUILDER"]; found {
		switch builder {
		case ImageBuilderBuildah:
			if result["CI_IMAGE_BUILDER_JOB_IMAGE"] == "" {
				delete(result, "CI_IMAGE_BUILDER_JOB_IMAGE")
			}
		case ImageBuilderKaniko:
			if !container.ValidDigestReference(result["CI_IMAGE_BUILDER_JOB_IMAGE"]) {
				return nil, fmt.Errorf("consumer variable CI_IMAGE_BUILDER_JOB_IMAGE requires a digest-pinned Kaniko job image: %w", errs.ErrValidation)
			}
		default:
			return nil, fmt.Errorf("consumer variable CI_IMAGE_BUILDER requires a supported builder: %w", errs.ErrValidation)
		}
	}

	for _, key := range sortedKeys(result) {
		if result[key] == "" {
			return nil, fmt.Errorf("consumer variable %s requires a nonempty value: %w", key, errs.ErrValidation)
		}
	}

	return result, nil
}

// PlanConsumer reads the consumer's workflows and plans the trial.
//
// The coordinates and the pin are replaced by the layer's, as Render does for
// a fixture's placeholders. A consumer's whole tree is rendered and not only
// its workflows, because a consumer holds its documentation, its policy tests
// and its release gate to the pin its workflows use, and would fail its own
// checks on a tree that disagreed with itself.
//
// Each fact is taken the way the consumer passes it. From a variable or a
// secret, `input: ${{ vars.NAME }}`, the plan sets NAME. As a literal, the
// plan replaces the literal. Repository inputs also admit the narrow variable
// default form below. Other forms are refused with the line, because
// the trial would then run with the organization's value: a signer image the
// lab cannot pull, or a key that does not match the lab's signatures.
func (layer Layer) PlanConsumer(workflows map[string]string, facts ConsumerFacts) (ConsumerPlan, error) {
	pin, err := ReadConsumerPin(workflows)
	if err != nil {
		return ConsumerPlan{}, err
	}

	plan := ConsumerPlan{Pin: pin, Replace: layer.inPlaceOf(pin), Variables: map[string]string{}, Secrets: map[string]string{}}
	plan.workflowInputs = map[string]string{}
	plan.repository = layer.Repository

	for _, name := range sortedKeys(workflows) {
		migrated, migrationErr := migratePublicationRepository(workflows[name])
		if migrationErr != nil {
			return ConsumerPlan{}, fmt.Errorf("%s: %w", name, migrationErr)
		}

		plan.workflowInputs[workflows[name]] = migrated
	}
	// Establish base coordinates across all wrappers first. A shared image
	// workflow may receive that same variable as image-repository; it is still
	// the exact base destination, not the final release destination.
	if base := facts.Inputs["base-repository"]; base != "" {
		for _, name := range sortedKeys(workflows) {
			if err = plan.bindInput(name, foldedExpression.ReplaceAllString(workflows[name], " }}"), "base-repository", base); err != nil {
				return ConsumerPlan{}, err
			}
		}
	}

	for _, name := range sortedKeys(workflows) {
		if err = plan.bindWorkflowFacts(name, workflows[name], facts); err != nil {
			return ConsumerPlan{}, err
		}
	}

	renderedWorkflows := make(map[string]string, len(workflows))
	for _, name := range sortedKeys(workflows) {
		rendered := plan.renderText(plan.workflowInputs[workflows[name]])

		rendered, err = layer.renderAdmin(rendered)
		if err != nil {
			return ConsumerPlan{}, fmt.Errorf("administrator wrapper %s: %w", name, err)
		}

		if err = layer.requireRendered(name, rendered); err != nil {
			return ConsumerPlan{}, err
		}

		renderedWorkflows[workflows[name]] = rendered
	}

	plan.workflowInputs = renderedWorkflows

	return plan, nil
}

// inPlaceOf is the old, new pairs that put the layer where the pin is.
func (layer Layer) inPlaceOf(pin ConsumerPin) []string {
	var pairs []string

	// An empty old string is a coordinate the consumer does not use.
	for _, pair := range [][2]string{
		{pin.Actions, layer.actionBase() + layerDir + "/actions/"},
		{pin.Workflows, layer.workflowBase() + consumerWorkflows + "/"},
		{pin.Commit, layer.Commit},
		{pin.BinarySHA256, layer.BinarySHA256},
	} {
		if pair[0] != "" {
			pairs = append(pairs, pair[0], pair[1])
		}
	}

	return pairs
}

// Render applies the plan to the text of one file. No old string begins
// inside another's match, so the order of the pairs decides nothing.
func (plan *ConsumerPlan) Render(text string) string {
	if rendered, found := plan.workflowInputs[text]; found {
		return rendered
	}

	return plan.renderText(text)
}

var consumerPinSubject = regexp.MustCompile(`(?m)(^|[ \t])--single-pin-subject(?:[ \t]+|=)('[^'\r\n]*'|"[^"\r\n]*"|[^ \t\r\n'";|&<>]+)`)

// renderText binds the selected layer's validation subject, not bare repository
// names: documentation and checks of other subjects must keep their identity.
func (plan *ConsumerPlan) renderText(text string) string {
	subject := plan.Pin.repository()
	if subject != "" && plan.repository != "" {
		text = consumerPinSubject.ReplaceAllStringFunc(text, func(written string) string {
			match := consumerPinSubject.FindStringSubmatch(written)
			if unquoted(match[2]) != subject {
				return written
			}

			return strings.TrimSuffix(written, match[2]) + strings.Replace(match[2], subject, plan.repository, 1)
		})
	}

	return strings.NewReplacer(plan.Replace...).Replace(text)
}

// bindWorkflowFacts binds every input except base-repository, and every
// secret, the way one workflow passes them.
func (plan *ConsumerPlan) bindWorkflowFacts(name, workflow string, facts ConsumerFacts) error {
	text := foldedExpression.ReplaceAllString(plan.workflowInputs[workflow], " }}")

	for _, input := range sortedKeys(facts.Inputs) {
		if input == "base-repository" {
			continue
		}
		// Repository variables already bound by a base declaration keep its
		// exact coordinate when forwarded through shared image execution.
		if input == "image-repository" && facts.Inputs["base-repository"] != "" {
			text = plan.withoutBaseRepositoryForwards(text, facts.Inputs["base-repository"])
		}

		if err := plan.bindInput(name, text, input, facts.Inputs[input]); err != nil {
			return err
		}
	}

	for _, secret := range sortedKeys(facts.Secrets) {
		if err := plan.bindSecret(text, secret, facts.Secrets[secret]); err != nil {
			return err
		}
	}

	return nil
}

var variableExpression = regexp.MustCompile(`^\$\{\{\s*vars\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}$`)

// Only repository defaults have this additional admitted form. In particular,
// keys, signer images and arbitrary expressions do not acquire a fallback path.
var repositoryDefault = regexp.MustCompile(`^\$\{\{\s*vars\.([A-Za-z_][A-Za-z0-9_]*)\s*\|\|\s*'([a-z0-9.-]+(:[0-9]+)?(/[a-z0-9._-]+)+)'\s*\}\}$`)

func repositoryVariable(written string) string {
	if match := variableExpression.FindStringSubmatch(written); match != nil {
		return match[1]
	}

	if match := repositoryDefault.FindStringSubmatch(written); match != nil {
		return match[1]
	}

	return ""
}

// Omit already-bound base forwarding lines from final-repository admission.
// The actual rendered source retains its variable, which the trial sets to
// the exact base coordinate. Other keys/text are untouched.
func (plan *ConsumerPlan) withoutBaseRepositoryForwards(text, base string) string {
	line := regexp.MustCompile(`(?m)^([ \t]*image-repository:[ \t]*)(.*?)[ \t]*$`)

	return line.ReplaceAllStringFunc(text, func(written string) string {
		match := line.FindStringSubmatch(written)

		name := repositoryVariable(match[2])
		if name != "" && plan.Variables[name] == base {
			return ""
		}

		return written
	})
}

// bindInput plans one fact for every line of a workflow that passes it.
func (plan *ConsumerPlan) bindInput(file, text, input, value string) error {
	line := regexp.MustCompile(`(?m)^[ \t]*(?:-[ \t]+)?(` + regexp.QuoteMeta(input) + `:[ \t]*)(.*?)[ \t]*$`)

	for _, match := range line.FindAllStringSubmatch(text, -1) {
		key, written := match[1], match[2]
		literal := unquoted(written)

		switch from := variableExpression.FindStringSubmatch(written); {
		case from != nil:
			if err := bind(plan.Variables, "variable", from[1], value); err != nil {
				return err
			}
		case (input == "image-repository" || input == "base-repository" || input == "publication-image-repository") && repositoryVariable(written) != "":
			if err := bind(plan.Variables, "variable", repositoryVariable(written), value); err != nil {
				return err
			}
		case literal == "":
			// Not passed here: the layer takes its default, or a variable.
		case strings.ContainsAny(literal, " \t$|>#") || strings.Contains(value, "\n"):
			return fmt.Errorf("%s passes `%s%s`, which a trial cannot give the lab's value; write a literal or ${{ vars.NAME }}, "+
				"and ${{ vars.NAME }} for a value of several lines: %w", file, key, written, errs.ErrValidation)
		case input == inputSignerImage || input == inputKeyDigest:
			plan.replace(literal, value)
		default:
			plan.replace(key+written, key+value)
		}
	}

	return nil
}

// bindSecret plans one of the layer's secrets for every call that passes it
// from a secret of the consumer's own name.
func (plan *ConsumerPlan) bindSecret(text, secret, value string) error {
	from := regexp.MustCompile(`(?m)^[ \t]*` + regexp.QuoteMeta(secret) + `:[ \t]*\$\{\{\s*secrets\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}[ \t]*$`)

	for _, match := range from.FindAllStringSubmatch(text, -1) {
		if err := bind(plan.Secrets, "secret", match[1], value); err != nil {
			return err
		}
	}

	return nil
}

func (plan *ConsumerPlan) replace(old, replacement string) {
	for index := 0; index < len(plan.Replace); index += 2 {
		if plan.Replace[index] == old {
			return
		}
	}

	plan.Replace = append(plan.Replace, old, replacement)
}

// unquoted is a scalar without the quotes a consumer may have written it in.
func unquoted(written string) string {
	if len(written) >= 2 && (written[0] == '\'' || written[0] == '"') && written[len(written)-1] == written[0] {
		return written[1 : len(written)-1]
	}

	return written
}

func bind(into map[string]string, kind, name, value string) error {
	if set, found := into[name]; found && set != value {
		return fmt.Errorf("the consumer reads two different facts of the lab from the %s %s: %w", kind, name, errs.ErrValidation)
	}

	into[name] = value

	return nil
}

// requireRendered refuses a rendered workflow that still fetches an action or
// a workflow of some layer from anywhere but the layer under test: a branch
// name where the commit should be, or a second repository. It would run, and
// what ran would be the published layer and not this checkout's.
func (layer Layer) requireRendered(name, rendered string) error {
	for _, use := range consumerLayerUse.FindAllStringSubmatch(rendered, -1) {
		reference := use[1]

		// A consumer's own reusable workflows and actions are its tree's.
		if strings.HasPrefix(reference, "./") {
			continue
		}

		served := strings.HasPrefix(reference, layer.actionBase()+layerDir+"/actions/") ||
			strings.HasPrefix(reference, layer.workflowBase()+consumerWorkflows+"/")
		if !served || !strings.HasSuffix(reference, "@"+layer.Commit) {
			return fmt.Errorf("%s still uses %s after rendering, which is not the layer under test at its commit: %w", name, reference, errs.ErrValidation)
		}
	}

	return nil
}

// Consumer is HEAD of a consumer checkout, staged for a trial.
type Consumer struct {
	git    layerGit
	target Target
	// stage is a bare repository that borrows the checkout's objects. Every
	// object a trial writes lands here.
	stage string
	// history makes the base commit a child of the checkout's HEAD instead
	// of parentless: WithHistory.
	history bool

	// Commit is the checkout's HEAD and ObjectFormat the hash its history is
	// written in, which the repository that receives it must share.
	Commit       string
	ObjectFormat string
}

// OpenConsumer reads the checkout at root, an absolute path to the top of a
// work tree. A checkout that differs from HEAD is refused with the files,
// untracked ones included: the trial would otherwise pass or fail on a tree
// that is not the one in front of the caller.
func OpenConsumer(tb TB, target Target, root string) Consumer {
	tb.Helper()
	requireAccepted(tb, target)

	gitBase := strings.TrimSuffix(target.endpoint.GitURL(), "/")
	git := layerGit{
		target: target,
		env:    ToolEnvironment(tb, target, layerGitEnvironment(gitBase, target.CredentialUsername, target.Token)),
	}

	stage, err := os.MkdirTemp("", "livetest-consumer-")
	if err != nil {
		tb.Fatalf("livetest: create a staging directory: %v", err)
	}

	tb.Cleanup(func() { _ = os.RemoveAll(stage) })

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	consumer, err := openConsumer(ctx, git, root, stage)
	if err != nil {
		tb.Fatalf("livetest: open the consumer checkout: %v", err)
	}

	consumer.target = target

	return consumer
}

func openConsumer(ctx context.Context, git layerGit, root, stage string) (Consumer, error) {
	if err := git.requireCleanTop(ctx, root); err != nil {
		return Consumer{}, err
	}

	facts := make([]string, 0, 3)

	for _, args := range [][]string{
		{gitRevParse, "--verify", "HEAD^{commit}"},
		{gitRevParse, "--show-object-format"},
		{gitRevParse, "--path-format=absolute", "--git-path", "objects"},
	} {
		fact, err := git.run(ctx, root, args...)
		if err != nil {
			return Consumer{}, err
		}

		facts = append(facts, fact)
	}

	consumer := Consumer{git: git, stage: stage, Commit: facts[0], ObjectFormat: facts[1]}

	if _, err := git.run(ctx, stage, "init", gitQuiet, "--bare", "--object-format="+consumer.ObjectFormat); err != nil {
		return Consumer{}, err
	}

	// The borrowed objects are read in place; nothing is copied or written
	// back.
	alternates := filepath.Join(stage, "objects", "info", "alternates")
	if err := os.WriteFile(alternates, []byte(facts[2]+"\n"), 0o600); err != nil { //nolint:gosec // stage is the caller's fresh temporary directory; the path below it is fixed.
		return Consumer{}, fmt.Errorf("borrow the checkout's objects: %w", err)
	}

	if _, err := git.run(ctx, stage, "read-tree", consumer.Commit); err != nil {
		return Consumer{}, err
	}

	return consumer, nil
}

// requireCleanTop refuses a root that is not the top of a work tree whose
// every file is HEAD's.
func (git layerGit) requireCleanTop(ctx context.Context, root string) error {
	if !filepath.IsAbs(root) {
		return fmt.Errorf("%q is not an absolute path: %w", root, errs.ErrValidation)
	}

	top, err := git.run(ctx, root, gitRevParse, "--show-toplevel")
	if err != nil {
		return err
	}

	if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr != nil || resolved != top {
		return fmt.Errorf("%s is not the top of a Git work tree (that is %s): %w", root, top, errs.ErrValidation)
	}

	return git.requireClean(ctx, root, ".")
}

// WithHistory returns the consumer pushing its own history under the rendered
// commit, as a demo does so its changelog and release notes have something to
// say. A trial pushes one parentless commit.
func (consumer Consumer) WithHistory() Consumer {
	consumer.history = true

	return consumer
}

// Workflows returns the consumer's workflow files at its captured commit, by name.
func (consumer Consumer) Workflows(tb TB) map[string]string {
	tb.Helper()

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	workflows, err := consumer.workflows(ctx)
	if err != nil {
		tb.Fatalf("livetest: read the consumer's workflows: %v", err)
	}

	return workflows
}

// Push pushes the rendered consumer to main of the scratch repository as one
// parentless commit and returns it.
func (consumer Consumer) Push(tb TB, repo string, plan ConsumerPlan) string {
	tb.Helper()

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	commit, err := consumer.commitRendered(ctx, plan)
	if err == nil {
		err = consumer.git.push(ctx, consumer.stage, scratchRemote(consumer.target, repo), commit, defaultBranch)
	}

	if err != nil {
		tb.Fatalf("livetest: push the consumer to %s/%s: %v", consumer.target.Owner, repo, err)
	}

	return commit
}

// PushSnapshot publishes the admitted consumer tree without rewriting files.
// Native release trials install their policy in an independently pinned project.
func (consumer Consumer) PushSnapshot(tb TB, repo string) string {
	tb.Helper()
	requireAccepted(tb, consumer.target)

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	commit, err := consumer.commitSnapshot(ctx)
	if err == nil {
		err = consumer.git.push(ctx, consumer.stage, scratchRemote(consumer.target, repo), commit, defaultBranch)
	}

	if err != nil {
		tb.Fatalf("livetest: push consumer snapshot to %s/%s: %v", consumer.target.Owner, repo, err)
	}

	return commit
}

// PushChange pushes a branch one commit ahead of base, for a pull request to
// open from, and returns that commit. The commit adds one file and is signed
// with an SSH key made for it: a consumer may require a signature on every
// commit it reviews, and a key that exists for one commit claims nothing.
func (consumer Consumer) PushChange(tb TB, repo, branch, base string) string {
	tb.Helper()

	SSHKey(tb, consumer.stage)

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	commit, err := consumer.commitChange(ctx, base, filepath.Join(consumer.stage, sshKeyFile))
	if err == nil {
		err = consumer.git.push(ctx, consumer.stage, scratchRemote(consumer.target, repo), commit, branch)
	}

	if err != nil {
		tb.Fatalf("livetest: push the change under review to %s/%s: %v", consumer.target.Owner, repo, err)
	}

	return commit
}

func (consumer Consumer) workflows(ctx context.Context) (map[string]string, error) {
	listed, err := consumer.git.output(ctx, consumer.stage, nil, "ls-tree", "-z", "--name-only", consumer.Commit, consumerWorkflows+"/")
	if err != nil {
		return nil, err
	}

	workflows := map[string]string{}

	for path := range strings.SplitSeq(strings.TrimSuffix(string(listed), "\x00"), "\x00") {
		if !isWorkflowFile(path) {
			continue
		}

		content, readErr := consumer.git.output(ctx, consumer.stage, nil, "cat-file", "blob", consumer.Commit+":"+path)
		if readErr != nil {
			return nil, readErr
		}

		workflows[strings.TrimPrefix(path, consumerWorkflows+"/")] = string(content)
	}

	if len(workflows) == 0 {
		return nil, fmt.Errorf("HEAD holds no workflow under %s: %w", consumerWorkflows, errs.ErrValidation)
	}

	return workflows, nil
}

// commitSnapshot restores the admitted tree in the private index before making
// a parentless commit. An earlier rendered trial cannot leak into this snapshot.
func (consumer Consumer) commitSnapshot(ctx context.Context) (string, error) {
	if _, err := consumer.git.run(ctx, consumer.stage, "read-tree", consumer.Commit); err != nil {
		return "", err
	}

	return consumer.commit(ctx, consumerBaseMessage)
}

// commitRendered writes the plan into the staged tree and commits it without
// a parent, so the forge receives no history.
func (consumer Consumer) commitRendered(ctx context.Context, plan ConsumerPlan) (string, error) {
	git, stage := consumer.git, consumer.stage

	pristineGo, err := consumer.gitlabChecksNeedBuildah(ctx)
	if err != nil {
		return "", err
	}

	// Only text files that hold something to replace are read. -I leaves out
	// what git takes for binary.
	grep := []string{"grep", "-z", "-l", "-I", "-F"}
	for index := 0; index < len(plan.Replace); index += 2 {
		grep = append(grep, "-e", plan.Replace[index])
	}

	matched, err := git.output(ctx, stage, nil, append(grep, consumer.Commit)...)
	if err != nil {
		return "", err
	}

	for path := range strings.SplitSeq(strings.TrimSuffix(string(matched), "\x00"), "\x00") {
		path = strings.TrimPrefix(path, consumer.Commit+":")
		// The generic Go adapter has all its bindings in its generated workflow.
		// Do not rewrite application data, tests or signed source identity merely
		// because their contents happen to match a pin/trust-digest sentinel.
		if pristineGo && !strings.HasPrefix(path, consumerWorkflows+"/") {
			continue
		}

		entry, entryErr := git.run(ctx, stage, "ls-tree", consumer.Commit, "--", path)
		if entryErr != nil {
			return "", entryErr
		}

		// `<mode> blob <id>\t<path>`: a link is not text to render.
		mode, rest, _ := strings.Cut(entry, " ")
		if mode != "100644" && mode != "100755" {
			continue
		}

		content, readErr := git.output(ctx, stage, nil, "cat-file", "blob", strings.Fields(rest)[1])
		if readErr != nil {
			return "", readErr
		}

		if err = consumer.stageFile(ctx, mode, path, plan.Render(string(content))); err != nil {
			return "", err
		}
	}

	return consumer.commit(ctx, consumerBaseMessage)
}

// commitChange adds the canary to the staged tree and commits it on base,
// signed with the key at keyPath.
func (consumer Consumer) commitChange(ctx context.Context, base, keyPath string) (string, error) {
	if err := consumer.stageFile(ctx, "100644", consumerCanary, consumerCanaryText); err != nil {
		return "", err
	}

	return consumer.commit(ctx, consumerChangeMessage,
		"-c", "gpg.format=ssh", "-c", "user.signingkey="+keyPath, "commit-tree", "-S", "-p", base)
}

func (consumer Consumer) stageFile(ctx context.Context, mode, path, content string) error {
	blob, err := consumer.git.output(ctx, consumer.stage, []byte(content), "hash-object", "-w", "--stdin")
	if err != nil {
		return err
	}

	_, err = consumer.git.run(ctx, consumer.stage, "update-index", "--add", "--cacheinfo", mode+","+strings.TrimSpace(string(blob))+","+path)

	return err
}

// npmPlaceholderScope is the scope the demos' npm packages are named under.
const npmPlaceholderScope = "@example-org/"

// renderNPMScope restages every npm manifest, lockfile and JavaScript source
// with the demos' placeholder scope replaced by scope. GitHub Packages takes
// an npm package only under its owner's scope, so a hosted trial names the
// trial organization, as an adopter names their own. The sources are rendered
// with the manifests: a package that imports a workspace member by name must
// import it under the name the manifests now give it.
func (consumer Consumer) renderNPMScope(ctx context.Context, scope string) error {
	paths, err := consumer.git.output(ctx, consumer.stage, nil, "ls-files", "--",
		"package.json", "package-lock.json", "*/package.json", "*/package-lock.json", "*.js", "*.mjs", "*.cjs", "*.ts")
	if err != nil {
		return err
	}

	for path := range strings.FieldsSeq(string(paths)) {
		body, err := consumer.git.output(ctx, consumer.stage, nil, "show", ":"+path)
		if err != nil {
			return err
		}

		rendered := strings.ReplaceAll(string(body), npmPlaceholderScope, scope)
		if rendered == string(body) {
			continue
		}

		if err = consumer.stageFile(ctx, "100644", path, rendered); err != nil {
			return err
		}
	}

	return nil
}

// commit commits the staged tree. The default is a parentless commit, or with
// WithHistory one on the consumer's own history; commitTree, when given, is
// the commit-tree invocation up to the message.
func (consumer Consumer) commit(ctx context.Context, message string, commitTree ...string) (string, error) {
	tree, err := consumer.git.run(ctx, consumer.stage, "write-tree")
	if err != nil {
		return "", err
	}

	git := consumer.git

	if len(commitTree) == 0 {
		commitTree = []string{"commit-tree"}

		if consumer.history {
			commitTree, message = []string{"commit-tree", "-p", consumer.Commit}, demoBaseMessage
		}
	}

	// A commit on the consumer's own history is dated now, as a person's
	// would be: a changelog dates a release by it. A parentless trial commit
	// keeps the fixed date that makes it reproducible.
	if consumer.history {
		git = git.dated(time.Now())
	}

	return git.run(ctx, consumer.stage, append(commitTree, "-m", message, tree)...)
}

// isWorkflowFile reports a path with a workflow file's YAML extension.
func isWorkflowFile(path string) bool {
	ext := filepath.Ext(path)

	return ext == ".yml" || ext == ".yaml"
}
