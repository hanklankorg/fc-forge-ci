// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"codefloe.com/itiquette/forge-lab-contract/labcontract"
	"golang.org/x/mod/semver"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
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
	consumerCanaryText = "# SPDX-FileCopyrightText" + ": The Forge CI Authors\n#\n" +
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

// The two events a trial can start a consumer's workflow with.
const (
	TrialPullRequest = "pull-request"
	TrialRelease     = "release"
)

// ConsumerTrial is the scenario name of a Forgejo consumer trial's scratch
// repositories: the consumer's copy, and LayerScenario of it for the layer
// under test that its workflows call.
const ConsumerTrial = "consumer-trial"

// LayerScenario is the scenario name of the scratch repository a scenario
// pushes the layer under test to, beside its own.
func LayerScenario(scenario string) string { return scenario + "-layer" }

// ConsumerTrialLayerURL is the web URL of the repository a Forgejo consumer
// trial pushes the layer under test to, for the publisher of the lab's kaniko
// job image: a run admits that image only when its source label names the
// repository the layer's workflows run from.
func ConsumerTrialLayerURL() (string, error) {
	lab, err := currentLabContract()
	if err != nil {
		return "", err
	}

	return consumerTrialLayerURL(lab)
}

// consumerTrialLayerURL is the layer's address as PushLayer serves it: the
// endpoint's web root, the credential's owner and the scratch repository.
func consumerTrialLayerURL(lab *labcontract.Contract) (string, error) {
	endpoint, err := lab.Endpoint(labcontract.Kind(provider.ForgeForgejo))
	if err != nil {
		return "", err
	}

	return strings.TrimSuffix(endpoint.WebBaseURL, "/") + "/" + endpoint.Owner() + "/" + ScratchRepo(LayerScenario(ConsumerTrial)), nil
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
	// Workflow is the file under .forgejo/workflows whose run is judged.
	Workflow string
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
}

var (
	trialName     = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	trialImage    = regexp.MustCompile(`^[a-z0-9.-]+(:[0-9]+)?(/[a-z0-9._-]+)+(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?$`)
	trialWorkflow = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.ya?ml$`)
	trialVersion  = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
)

// The trial's own settings, and the workflow each event judges by default.
const (
	settingWorkflow    = "workflow"
	settingVersion     = "version"
	settingNextVersion = "next-version"
	settingToken       = "token"
	settingSecretFile  = "secret-file"
	settingImage       = "image"
	settingInterrupt   = "interrupt-after"

	trialPullRequestWorkflow = "pullrequest.yml"
	trialReleaseWorkflow     = "release.yml"
)

// ParseTrialSettings reads the words after the checkout and the event. Each
// is key=value. A lowercase key is one of the trial's own settings, workflow,
// version, next-version, token, image and interrupt-after; any other key is the name of a repository
// variable, in capitals as Forgejo stores it.
//
// No word carries a secret. A caller can ask for the lab account's token, by
// naming the repository secret that receives it, and for a token from one of
// its own files, by secret-file=<SECRET_NAME>:<absolute path> (owner-only file,
// one token on one line). An external registry's token comes from a file
// too (see ExternalRegistryCredential).
func ParseTrialSettings(event string, words []string) (TrialSettings, error) {
	settings := TrialSettings{Event: event, Variables: map[string]string{}, FileSecrets: map[string]string{}}

	switch event {
	case TrialPullRequest:
		settings.Workflow = trialPullRequestWorkflow
	case TrialRelease:
		settings.Workflow, settings.Version = trialReleaseWorkflow, "0.0.1"
	default:
		return TrialSettings{}, fmt.Errorf("the event is %q, want %s or %s: %w", event, TrialPullRequest, TrialRelease, errs.ErrValidation)
	}

	for _, word := range words {
		if key, value, _ := strings.Cut(word, "="); value == "" || !settings.set(key, value) {
			return TrialSettings{}, fmt.Errorf("setting %q is none of %s=<file>.yml, %s=<X.Y.Z> (release only), %s=<X.Y.Z> (second release), %s=<SECRET_NAME>, "+
				settingSecretFile+"=<SECRET_NAME>:<absolute file>, "+
				"%s=<registry>/<repository>[:<tag>] (release only), %s=<job name> (release only) or <VARIABLE_NAME>=<value>, "+
				"each name once, where a value starting with @ is %s or %s: %w",
				word, settingWorkflow, settingVersion, settingNextVersion, settingToken, settingImage, settingInterrupt, trialLayerCommit, trialRequestCommit, errs.ErrValidation)
		}
	}

	if settings.NextVersion != "" && (settings.InterruptAfter != "" || semver.Compare("v"+settings.NextVersion, "v"+settings.Version) <= 0) {
		return TrialSettings{}, fmt.Errorf("next-version requires an increasing release version and cannot accompany interrupt-after: %w", errs.ErrValidation)
	}

	return settings, nil
}

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

// set takes one setting and reports whether it is one.
func (settings *TrialSettings) set(key, value string) bool {
	switch key {
	case settingWorkflow:
		settings.Workflow = value

		return trialWorkflow.MatchString(value)
	case settingToken:
		_, fromFile := settings.FileSecrets[value]

		return !fromFile && appendOnce(&settings.TokenSecrets, value) && trialName.MatchString(value)
	case settingSecretFile:
		name, path, _ := strings.Cut(value, ":")
		_, seen := settings.FileSecrets[name]

		if seen || slices.Contains(settings.TokenSecrets, name) || !trialName.MatchString(name) || !filepath.IsAbs(path) {
			return false
		}

		settings.FileSecrets[name] = path

		return true
	case settingVersion, settingNextVersion, settingImage, settingInterrupt:
		return settings.Event == TrialRelease && settings.setReleaseOnly(key, value)
	}

	return settings.setVariable(key, value)
}

// setReleaseOnly takes one of the settings only a release trial has.
func (settings *TrialSettings) setReleaseOnly(key, value string) bool {
	switch key {
	case settingVersion:
		version, valid := trialVersionValue(value)
		settings.Version = version

		return valid
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
	// Commit is the one commit every reference ends in.
	Commit string
	// BinarySHA256 is the digest of the layer's binary it passes as
	// expected-sha256, or "" when it passes none.
	BinarySHA256 string
}

var (
	consumerActionUse   = regexp.MustCompile(`uses:[ \t]*['"]?(https://[^/\s'"]+/[^/\s]+/[^/\s]+/\.forgejo/actions/)[^@\s'"]+@([0-9a-f]{40})\b`)
	consumerWorkflowUse = regexp.MustCompile(`uses:[ \t]*['"]?([^/\s'"]+/[^/\s]+/\.forgejo/workflows/)[^@\s'"]+@([0-9a-f]{40})\b`)
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
		text := foldedExpression.ReplaceAllString(plan.workflowInputs[workflows[name]], " }}")

		for _, input := range sortedKeys(facts.Inputs) {
			if input == "base-repository" {
				continue
			}
			// Repository variables already bound by a base declaration keep its
			// exact coordinate when forwarded through shared image execution.
			if input == "image-repository" && facts.Inputs["base-repository"] != "" {
				text = plan.withoutBaseRepositoryForwards(text, facts.Inputs["base-repository"])
			}

			if err = plan.bindInput(name, text, input, facts.Inputs[input]); err != nil {
				return ConsumerPlan{}, err
			}
		}

		for _, secret := range sortedKeys(facts.Secrets) {
			if err = plan.bindSecret(text, secret, facts.Secrets[secret]); err != nil {
				return ConsumerPlan{}, err
			}
		}
	}

	for _, name := range sortedKeys(workflows) {
		if err = layer.requireRendered(name, plan.Render(workflows[name])); err != nil {
			return ConsumerPlan{}, err
		}
	}

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
	if migrated, found := plan.workflowInputs[text]; found {
		text = migrated
	}
	return strings.NewReplacer(plan.Replace...).Replace(text)
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
	if err := os.WriteFile(alternates, []byte(facts[2]+"\n"), 0o600); err != nil {
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

// Workflows returns the consumer's workflow files at HEAD, by file name.
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
		if ext := filepath.Ext(path); ext != ".yml" && ext != ".yaml" {
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
