// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

var changelogReference = regexp.MustCompile(`^ {0,3}\[[^\]\r\n]+\]:[ \t]+\S`)

// ChangelogSection extracts one Keep a Changelog level-two version section.
// Version headings may be bracketed, linked or plain, with or without v.
// Fenced examples are not headings. Reference definitions remain available to
// the extracted Markdown, including definitions after older release sections.
func ChangelogSection(body, version string) (string, error) {
	version = strings.TrimPrefix(version, "v")
	if !semver.IsValid("v" + version) {
		return "", fmt.Errorf("release notes: a SemVer release-version is required for changelog extraction: %w", errs.ErrUsage)
	}

	lines := strings.SplitAfter(body, "\n")

	start, end, err := changelogSectionBounds(lines, version)
	if err != nil {
		return "", err
	}

	var section strings.Builder
	section.WriteString(strings.TrimRight(strings.Join(lines[start:end], ""), "\r\n"))
	section.WriteByte('\n')

	fence := ""

	// Preserve definitions outside the section too; definitions in fenced
	// examples do not define links in Markdown prose.
	for index, line := range lines {
		text := strings.TrimRight(line, "\r\n")
		if skipChangelogFence(text, &fence) || (index >= start && index < end) {
			continue
		}

		if changelogReference.MatchString(text) {
			section.WriteByte('\n')
			section.WriteString(text)
			section.WriteByte('\n')
		}
	}

	return section.String(), nil
}

func changelogSectionBounds(lines []string, version string) (int, int, error) {
	start, end := -1, len(lines)
	fence := ""

	for index, line := range lines {
		text := strings.TrimRight(line, "\r\n")
		if skipChangelogFence(text, &fence) {
			continue
		}

		heading, found := strings.CutPrefix(strings.TrimLeft(text, " "), "## ")
		if !found || len(text)-len(strings.TrimLeft(text, " ")) > 3 {
			continue
		}

		if start >= 0 && end == len(lines) {
			end = index
		}

		if !changelogVersionHeading(heading, version) {
			continue
		}

		if start >= 0 {
			return 0, 0, fmt.Errorf("release notes: duplicate changelog section for %s: %w", version, errs.ErrValidation)
		}

		start = index
	}

	if start < 0 {
		return 0, 0, fmt.Errorf("release notes: changelog has no section for %s: %w", version, errs.ErrValidation)
	}

	return start, end, nil
}

func changelogVersionHeading(heading, version string) bool {
	heading = strings.TrimSpace(heading)
	if strings.HasPrefix(heading, "[") {
		label, _, found := strings.Cut(strings.TrimPrefix(heading, "["), "]")

		return found && strings.TrimPrefix(label, "v") == version
	}

	word, _, _ := strings.Cut(heading, " ")

	return strings.TrimPrefix(word, "v") == version
}

// skipChangelogFence tracks the fenced code blocks allowed in Markdown prose.
func skipChangelogFence(line string, fence *string) bool {
	text := strings.TrimLeft(line, " ")
	if len(line)-len(text) > 3 {
		return *fence != ""
	}

	if *fence != "" {
		if strings.HasPrefix(text, *fence) && strings.Trim(text, string((*fence)[0])+" \t") == "" {
			*fence = ""
		}

		return true
	}

	if strings.HasPrefix(text, "```") || strings.HasPrefix(text, "~~~") {
		*fence = text[:len(text)-len(strings.TrimLeft(text, string(text[0])))]

		return true
	}

	return false
}
