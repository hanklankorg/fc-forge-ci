// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate

import (
	"fmt"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/git"

	"gopkg.in/yaml.v3"
)

// yamlUsesValues returns the scalar value of every uses: key in the document,
// at any depth.
func yamlUsesValues(data []byte) ([]string, error) {
	doc, err := parseYAMLNode(data)
	if err != nil {
		return nil, err
	}

	var values []string

	walkYAML(doc, map[*yaml.Node]bool{}, func(node *yaml.Node) {
		if node.Kind != yaml.MappingNode {
			return
		}

		// A YAML mapping node stores its children flat as key, value, key,
		// value, so the cursor advances two at a time.
		for pair := 0; pair+1 < len(node.Content); pair += 2 {
			mappingKey, ok := resolvedScalar(node.Content[pair])
			if !ok || mappingKey != "uses" {
				continue
			}

			if value, ok := resolvedScalar(node.Content[pair+1]); ok {
				values = append(values, value)
			}
		}
	})

	return values, nil
}

func yamlScalarValues(data []byte) ([]string, error) {
	doc, err := parseYAMLNode(data)
	if err != nil {
		return nil, err
	}

	var values []string

	walkYAML(doc, map[*yaml.Node]bool{}, func(node *yaml.Node) {
		if node.Kind == yaml.ScalarNode {
			values = append(values, node.Value)
		}
	})

	return values, nil
}

func parseYAMLNode(data []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse workflow YAML: %w", err)
	}

	return &doc, nil
}

func walkYAML(node *yaml.Node, seen map[*yaml.Node]bool, visit func(*yaml.Node)) {
	if node == nil || seen[node] {
		return
	}

	seen[node] = true

	visit(node)

	if node.Kind == yaml.AliasNode {
		walkYAML(node.Alias, seen, visit)

		return
	}

	for _, child := range node.Content {
		walkYAML(child, seen, visit)
	}
}

func resolvedScalar(node *yaml.Node) (string, bool) {
	seen := map[*yaml.Node]bool{}
	for node != nil && node.Kind == yaml.AliasNode && !seen[node] {
		seen[node] = true
		node = node.Alias
	}

	if node == nil || node.Kind != yaml.ScalarNode {
		return "", false
	}

	return node.Value, true
}

// pinnedSubjectSHA returns the 40-hex commit a uses: value pins subject to,
// or "" when the value does not reference subject as whole path components.
//
// A bare substring match also accepted a sibling repository whose name merely
// starts with the subject ("org/ci" inside "org/ci-extras/…"), so that
// repository's pin was collected as if it were the subject's: pin-reachability
// then reported it orphaned in the wrong clone, and the single-pin check
// reported "mixed pins" for a directory that pinned each repository once.
func pinnedSubjectSHA(uses, subject string) string {
	end := subjectEnd(uses, subject)
	if end < 0 {
		return ""
	}

	at := strings.LastIndexByte(uses[end:], '@')
	if at < 0 {
		return ""
	}

	if ref := uses[end+at+1:]; isSHA1(ref) {
		return ref
	}

	return ""
}

// referencesSubject reports whether a uses: value names subject as whole path
// components, whatever it is pinned to.
func referencesSubject(uses, subject string) bool {
	return subjectEnd(uses, subject) >= 0
}

// subjectEnd returns the index just past the first occurrence of subject as
// whole path components in uses, or -1 when there is none.
func subjectEnd(uses, subject string) int {
	if subject == "" {
		return -1
	}

	for start := 0; start < len(uses); {
		idx := strings.Index(uses[start:], subject)
		if idx < 0 {
			return -1
		}

		idx += start
		end := idx + len(subject)

		if subjectIsPathComponent(uses, idx, end) {
			return end
		}

		start = idx + 1
	}

	return -1
}

// subjectIsPathComponent reports whether uses[idx:end] is bounded by path
// separators: it starts the value or follows a "/", and a "/" or the "@ref"
// separator follows it.
func subjectIsPathComponent(uses string, idx, end int) bool {
	if idx > 0 && uses[idx-1] != '/' {
		return false
	}

	return end < len(uses) && (uses[end] == '/' || uses[end] == '@')
}

func isSHA1(value string) bool {
	// A workflow pin is a SHA-1 commit; the 64-hex SHA-256 form is not accepted here.
	return len(value) == 40 && git.ValidCommitSHA(value)
}
