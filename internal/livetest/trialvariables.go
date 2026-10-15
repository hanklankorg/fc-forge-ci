// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"fmt"
	"maps"
	"slices"
	"strings"

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
