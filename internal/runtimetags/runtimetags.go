// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package runtimetags recognises forge-ci runtime-image references
// (`forge-ci-runtime-*:<tag>`) in the shipped workflows and components. The
// workflow guards use it to keep any released runtime image from being named
// there: GitHub workflows resolve the image, GitLab components require it.
package runtimetags

import (
	"regexp"
	"strings"
)

var refPattern = regexp.MustCompile("forge-ci-runtime(?:-[a-z0-9.]+)*:([^\\s\"'`,;(){}\\[\\]<>]+)")

func references(content string) [][]int {
	var found [][]int

	for _, match := range refPattern.FindAllStringSubmatchIndex(content, -1) {
		if match[0] > 0 && !strings.ContainsRune("/ \t\r\n\"'`=([{", rune(content[match[0]-1])) {
			continue
		}

		found = append(found, match)
	}

	return found
}

// Tags returns complete runtime-reference tags, including nonstable or invalid
// pins so validators cannot mistake a stable prefix for a valid full token.
func Tags(content string) []string {
	refs := references(content)

	tags := make([]string, 0, len(refs))
	for _, match := range refs {
		tags = append(tags, content[match[2]:match[3]])
	}

	return tags
}
