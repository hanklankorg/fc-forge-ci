// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"golang.org/x/mod/semver"
	"mvdan.cc/sh/v3/syntax"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// ParseTrialVariables accepts literal consumer variables, one KEY=value per
// line. Provider-specific recipe options and token bindings are not variables.
func ParseTrialVariables(input string) (map[string]string, error) {
	variables := make(map[string]string)

	for _, word := range strings.Split(input, "\n") {
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
// decides: the release policy's version bump, and literal variables for the
// consumer's build.
type GitLabReleaseSettings struct {
	// VersionBump is the release component's version-bump input: maven/npm/cargo, or ""
	// for none.
	VersionBump string
	// Version is the X.Y.Z the release requests, or "" for the trial's default.
	Version string
	// NextVersion requests a second release from the signed main left by the first.
	NextVersion string
	Variables   map[string]string
}

// settingVersionBump is a lowercase setting of a GitLab release trial, as
// version is; every uppercase KEY=value is a consumer variable.
const settingVersionBump = "version-bump"

// ParseGitLabReleaseSettings reads one setting per line: version-bump=maven|npm|cargo,
// which the trial writes into the release policy, version=X.Y.Z, the release
// it requests, or a literal KEY=value consumer variable (ParseTrialVariables).
func ParseGitLabReleaseSettings(input string) (GitLabReleaseSettings, error) {
	var settings GitLabReleaseSettings

	variables := make([]string, 0)

	for _, word := range strings.Split(input, "\n") {
		key, value, _ := strings.Cut(word, "=")

		switch key {
		case settingVersionBump:
			if settings.VersionBump != "" || (value != "maven" && value != "npm" && value != "cargo") {
				return GitLabReleaseSettings{}, fmt.Errorf("%s must be given once, as maven, npm or cargo: %w", settingVersionBump, errs.ErrValidation)
			}

			settings.VersionBump = value
		case settingVersion:
			version, valid := trialVersionValue(value)
			if settings.Version != "" || !valid {
				return GitLabReleaseSettings{}, fmt.Errorf("%s must be given once, as X.Y.Z: %w", settingVersion, errs.ErrValidation)
			}

			settings.Version = version
		case "next-version":
			version, valid := trialVersionValue(value)
			if settings.NextVersion != "" || !valid {
				return GitLabReleaseSettings{}, fmt.Errorf("next-version must be given once, as X.Y.Z: %w", errs.ErrValidation)
			}
			settings.NextVersion = version
		default:
			variables = append(variables, word)
		}
	}

	parsed, err := ParseTrialVariables(strings.Join(variables, "\n"))
	if err != nil {
		return GitLabReleaseSettings{}, err
	}

	settings.Variables = parsed
	if settings.NextVersion != "" && (settings.Version == "" || settings.VersionBump == "" || semver.Compare("v"+settings.NextVersion, "v"+settings.Version) <= 0) {
		return GitLabReleaseSettings{}, fmt.Errorf("next-version requires an increasing explicit version and isolated version-bump mode: %w", errs.ErrValidation)
	}

	return settings, nil
}

// GitLabConsumerBuildCommand scopes literal settings to the consumer's build
// subprocess, without adding variables to the independent signing policy jobs.
func GitLabConsumerBuildCommand(variables map[string]string) (string, error) {
	const command = "bash .gitlab/build-release.sh"
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
