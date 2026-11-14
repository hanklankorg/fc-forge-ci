// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/mod/semver"
	"mvdan.cc/sh/v3/syntax"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// ParseTrialVariables accepts literal consumer variables, one KEY=value per
// line. Provider-specific recipe options and token bindings are not variables.
func ParseTrialVariables(input string) (map[string]string, error) {
	variables := make(map[string]string)

	for word := range strings.SplitSeq(input, "\n") {
		if word == "" {
			continue
		}

		key, value, found := strings.Cut(word, "=")
		if !found || !trialName.MatchString(key) {
			return nil, fmt.Errorf("consumer variables must be KEY=value with an uppercase name: %w", errs.ErrValidation)
		}

		if _, duplicate := variables[key]; duplicate {
			return nil, fmt.Errorf("consumer variable %s was given twice: %w", key, errs.ErrValidation)
		}

		variables[key] = value
	}

	return variables, nil
}

// GitLabReleaseSettings are what the caller of a GitLab consumer release trial
// decides: the requested versions, the release policy's job user, and
// literal variables for the consumer's build. The consumer's declaration,
// not a setting, decides what the release bumps.
type GitLabReleaseSettings struct {
	// Scenario selects a reviewed consumer release case; empty is ConsumerTrial.
	Scenario string
	// Version is the X.Y.Z the release requests, or "" for the trial's default.
	Version string
	// NextVersion requests a second release from the signed main left by the first.
	NextVersion string
	// JobUser is the release component's job-user input, a UID or UID:GID the
	// job-image jobs run as, or "" for the image's own user.
	JobUser   string
	Variables map[string]string
	// ReleaseOption is the release option the trial writes into the release
	// policy and judges the release by: OptionPrerelease or OptionRetention.
	// GitLab has no draft release.
	ReleaseOption string
	// Fixtures is a directory of the consumer staged over its root, in the
	// trial's private index alone, as on Forgejo; empty, none.
	Fixtures string
}

// settingJobUser is the GitLab release trial's job-user, the release policy's
// input of that name.
const settingJobUser = "job-user"

const settingScenario = "scenario"

const (
	gitlabConsumerTrialTest = "TestGitLabRelease_ConsumerTrial"
	gitlabPreseededTest     = "TestGitLabRelease_RejectsPreseededProvenance"
	gitlabRecoveryTest      = "TestGitLabRelease_ConsumerInterruptedRecovery"
)

// jobUserValue is UID[:GID] in canonical decimal: a leading zero would pass
// the policy's input check and then never equal what `id -u` prints.
var jobUserValue = regexp.MustCompile(`^(0|[1-9][0-9]*)(:(0|[1-9][0-9]*))?$`)

// ParseGitLabReleaseSettings reads one setting per line: job-user=UID[:GID],
// which the trial writes into the release policy,
// version=X.Y.Z, the release it requests, fixtures=<directory>, the
// consumer's files staged over its root, or a literal KEY=value consumer
// variable (ParseTrialVariables).
func ParseGitLabReleaseSettings(input string) (GitLabReleaseSettings, error) {
	var settings GitLabReleaseSettings

	variables := make([]string, 0)

	for word := range strings.SplitSeq(input, "\n") {
		key, value, _ := strings.Cut(word, "=")

		handled, err := settings.setRecipeOption(key, value)
		if err != nil {
			return GitLabReleaseSettings{}, err
		}

		if !handled {
			variables = append(variables, word)
		}
	}

	parsed, err := ParseTrialVariables(strings.Join(variables, "\n"))
	if err != nil {
		return GitLabReleaseSettings{}, err
	}

	if err := refusePolicyBuilderVariables(parsed); err != nil {
		return GitLabReleaseSettings{}, err
	}

	settings.Variables = parsed
	if settings.Scenario == gitlabPreseededTest && settings.NextVersion != "" {
		return GitLabReleaseSettings{}, fmt.Errorf("preseeded provenance rejection takes no next-version: %w", errs.ErrValidation)
	}

	if settings.Scenario == gitlabRecoveryTest && (settings.Version == "" || settings.Version == "0.0.0" || settings.NextVersion != "") {
		return GitLabReleaseSettings{}, fmt.Errorf("consumer recovery requires one explicit nonzero version and no next-version: %w", errs.ErrValidation)
	}

	if !validNextTrialVersion(settings) {
		return GitLabReleaseSettings{}, fmt.Errorf("next-version requires an increasing explicit version: %w", errs.ErrValidation)
	}

	option := TrialSettings{Version: settings.Version, NextVersion: settings.NextVersion, ReleaseOption: settings.ReleaseOption}
	if err := option.releaseOptionGap(); err != nil {
		return GitLabReleaseSettings{}, err
	}

	return settings, nil
}

func (settings *GitLabReleaseSettings) setRecipeOption(key, value string) (bool, error) {
	switch key {
	case settingScenario:
		if settings.Scenario != "" || value == "" {
			return true, fmt.Errorf("scenario must be supplied once: %w", errs.ErrValidation)
		}

		if _, err := GitLabConsumerReleaseScenario(value); err != nil {
			return true, err
		}

		settings.Scenario = value
	case settingJobUser:
		return true, setTrialJobUser(&settings.JobUser, value)
	case settingVersion:
		// releaseOptionGap holds a prerelease version to its option.
		if settings.Version == "" && trialPrerelease.MatchString(value) {
			settings.Version = value

			return true, nil
		}

		return true, setTrialVersion(&settings.Version, key, value)
	case settingReleaseOption:
		if settings.ReleaseOption != "" || (value != OptionPrerelease && value != OptionRetention) {
			return true, fmt.Errorf("%s must be given once, as %s or %s: %w", settingReleaseOption, OptionPrerelease, OptionRetention, errs.ErrValidation)
		}

		settings.ReleaseOption = value
	case "next-version":
		return true, setTrialVersion(&settings.NextVersion, key, value)
	default:
		return settings.setFixtures(key, value)
	}

	return true, nil
}

// setFixtures takes fixtures=<directory> and reports whether key is it.
func (settings *GitLabReleaseSettings) setFixtures(key, value string) (bool, error) {
	if key != settingFixtures {
		return false, nil
	}

	if settings.Fixtures != "" || !pathsafe.Relative(value) {
		return true, fmt.Errorf("%s must be given once, as a relative directory of the consumer: %w", settingFixtures, errs.ErrValidation)
	}

	settings.Fixtures = value

	return true, nil
}

// ParseGitLabLifecycleSettings reads a GitLab lifecycle's settings: a
// release's, but scenario= and next-version=, which select another release
// case or a second release. A lifecycle's phases are fixed and it requests
// one release, of its merged main.
func ParseGitLabLifecycleSettings(input string) (GitLabReleaseSettings, error) {
	settings, err := ParseGitLabReleaseSettings(input)
	if err != nil {
		return GitLabReleaseSettings{}, err
	}

	if settings.Scenario != "" || settings.NextVersion != "" || settings.ReleaseOption != "" || settings.Fixtures != "" {
		return GitLabReleaseSettings{}, fmt.Errorf("%s takes no %s=, next-version=, %s= or %s=; its phases are fixed and it requests one release: %w", TrialLifecycle, settingScenario, settingReleaseOption, settingFixtures, errs.ErrValidation)
	}

	return settings, nil
}

// GitLabConsumerReleaseScenario admits an exact registered case, not a caller's
// regular expression. The archive/checkout recipe is shared by these cases.
func GitLabConsumerReleaseScenario(name string) (string, error) {
	switch name {
	case "", gitlabConsumerTrialTest:
		return gitlabConsumerTrialTest, nil
	case gitlabPreseededTest, gitlabRecoveryTest:
		return name, nil
	default:
		return "", fmt.Errorf("scenario must be one of %s: %w", strings.Join([]string{gitlabConsumerTrialTest, gitlabPreseededTest, gitlabRecoveryTest}, ", "), errs.ErrValidation)
	}
}

// refusePolicyBuilderVariables refuses build variables that name what the
// release policy's inputs set: the image builder, which a build variable
// would override inside the build alone, and the job user, which the jobs
// refuse when a variable differs from job-user.
func refusePolicyBuilderVariables(variables map[string]string) error {
	for name, instead := range map[string]string{
		"CI_IMAGE_BUILDER":           "select kaniko with " + GitLabImageBuilderEnv + "=kaniko",
		"CI_IMAGE_BUILDER_JOB_IMAGE": "select kaniko with " + GitLabImageBuilderEnv + "=kaniko",
		"RC_JOB_USER":                "give " + settingJobUser + "=UID[:GID]",
	} {
		if _, set := variables[name]; set {
			return fmt.Errorf("%s is the GitLab release policy's to set, not a build variable; %s: %w", name, instead, errs.ErrValidation)
		}
	}

	return nil
}

func setTrialJobUser(destination *string, value string) error {
	if *destination != "" || !jobUserValue.MatchString(value) {
		return fmt.Errorf("%s must be given once, as UID or UID:GID: %w", settingJobUser, errs.ErrValidation)
	}

	*destination = value

	return nil
}

func setTrialVersion(destination *string, key, value string) error {
	version, valid := trialVersionValue(value)
	if *destination != "" || !valid {
		return fmt.Errorf("%s must be given once, as X.Y.Z: %w", key, errs.ErrValidation)
	}

	*destination = version

	return nil
}

func validNextTrialVersion(settings GitLabReleaseSettings) bool {
	return settings.NextVersion == "" || (settings.Version != "" && semver.Compare("v"+settings.NextVersion, "v"+settings.Version) > 0)
}

// GitLabConsumerBuildScript and GitLabConsumerExportScript are the consumer's
// release build, and its export of files from a Containerfile stage, which
// the policy's export jobs run with the selected image builder.
const (
	GitLabConsumerBuildScript  = ".gitlab/build-release.sh"
	GitLabConsumerExportScript = ".gitlab/export-release.sh"
)

// GitLabConsumerBaseStages is the file a consumer names its graph of base
// images in, one line in the form of the release policy's base-stages input.
// Its planner, the policy's base-plan, is base-plan.sh at its top level.
const (
	GitLabConsumerBaseStages = ".gitlab/base-stages"
	gitLabConsumerBasePlan   = "bash base-plan.sh"
)

// GitLabBaseStages reads the consumer's base image graph for the release
// policy. A consumer without the file has no base stage.
func GitLabBaseStages(root string) (string, string, error) {
	if root == "" {
		return "", "", nil
	}

	body, err := os.ReadFile(filepath.Join(root, GitLabConsumerBaseStages)) //nolint:gosec // A reviewed consumer checkout, read as data.
	if os.IsNotExist(err) {
		return "", "", nil
	}

	if err != nil {
		return "", "", fmt.Errorf("read the base image graph: %w", err)
	}

	return strings.TrimSpace(string(body)), gitLabConsumerBasePlan, nil
}

// GitLabConsumerBuildCommand scopes literal settings to the consumer's build
// subprocess, without adding variables to the independent signing policy jobs.
func GitLabConsumerBuildCommand(variables map[string]string) (string, error) {
	return GitLabConsumerCommand(GitLabConsumerBuildScript, variables)
}

// GitLabConsumerExportCommands derives the native export invocation once for
// both live release policies and offline rendering. An absent script means no
// export jobs; other inspection errors and nonregular scripts are refused.
func GitLabConsumerExportCommands(root string, variables map[string]string) ([]string, error) {
	if root == "" {
		return []string{}, nil
	}

	info, err := os.Lstat(filepath.Join(root, GitLabConsumerExportScript))
	if os.IsNotExist(err) {
		return []string{}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("inspect native export script: %w", err)
	}

	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("native export script must be a regular file: %w", errs.ErrValidation)
	}

	command, err := GitLabConsumerCommand(GitLabConsumerExportScript, variables)
	if err != nil {
		return nil, err
	}

	return []string{command}, nil
}

// GitLabConsumerCommand runs the consumer's script with the literal settings
// scoped to it.
func GitLabConsumerCommand(script string, variables map[string]string) (string, error) {
	command := "bash " + script
	if len(variables) == 0 {
		return command, nil
	}

	words := []string{"env", "--"}

	for _, key := range slices.Sorted(maps.Keys(variables)) {
		word, err := syntax.Quote(key+"="+variables[key], syntax.LangBash)
		if err != nil {
			return "", fmt.Errorf("quote consumer variable %s: %w", key, err)
		}

		words = append(words, word)
	}

	return strings.Join(words, " ") + " " + command, nil
}
