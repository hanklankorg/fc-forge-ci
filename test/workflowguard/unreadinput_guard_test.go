// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"bytes"
	"errors"
	"io"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"gopkg.in/yaml.v3"
)

// An input a workflow, action or component declares but never reads is a
// setting that is silently ignored: the caller sets it, sees no error, and
// believes it took effect. Each declared input therefore has to be read
// somewhere in the same file. This does not prove the read is meaningful; it
// catches the declaration that nothing reads at all. Only YAML values outside
// the inputs declaration count, without comment lines: a comment or another
// input's description that mentions an input does not read it.

func TestDeclaredInputsAreRead(t *testing.T) {
	t.Parallel()

	var findings []string

	checked := 0

	check := func(path string, names []string, body string, read func(string) *regexp.Regexp) {
		checked += len(names)
		findings = append(findings, unreadInputs(path, names, body, read)...)
	}
	for _, dir := range []string{".github/workflows", ".forgejo/workflows"} {
		for _, path := range yamlFiles(t, dir) {
			body := reporoot.ReadFile(t, path)
			check(path, workflowInputNames(t, path, body), readableText(t, path, body, isWorkflowInputDeclaration), actionsInputRead)
		}
	}

	for _, dir := range []string{".github/actions", ".forgejo/actions"} {
		for _, entry := range reporoot.ReadDir(t, dir) {
			if !entry.IsDir() {
				continue
			}

			path := dir + "/" + entry.Name() + "/action.yml"
			body := reporoot.ReadFile(t, path)
			check(path, actionInputNames(t, path, body), readableText(t, path, body, isActionInputDeclaration), actionsInputRead)
		}
	}

	for _, path := range yamlFiles(t, "templates") {
		names, rest, err := componentInputNames(reporoot.ReadFile(t, path))
		if err != nil {
			t.Errorf("%s: %v", path, err)

			continue
		}

		check(path, names, readableText(t, path, rest, nil), componentInputRead)
	}

	if len(findings) > 0 {
		t.Fatalf("declared inputs nothing reads:\n  %s", strings.Join(findings, "\n  "))
	}

	if checked == 0 {
		t.Fatal("no declared input was found; this guard measured nothing")
	}
}

func TestUnreadInputs_ReportsOnlyTheUnreadOne(t *testing.T) {
	t.Parallel()

	body := "run: echo ${{ inputs.used }} ${{ inputs['quoted'] }}\n"

	got := unreadInputs("w.yml", []string{"used", "quoted", "ignored"}, body, actionsInputRead)
	if want := []string{"w.yml: ignored"}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}

	got = unreadInputs("c.yml", []string{"job-name", "stage"}, `"$[[ inputs.job-name ]]":`, componentInputRead)
	if want := []string{"c.yml: stage"}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestUnreadInputs_MentionsAreNotReads(t *testing.T) {
	t.Parallel()

	workflow := []byte(`on:
  workflow_call:
    inputs:
      used:
        type: string
      ignored:
        # Replaces inputs.ignored, see inputs.used.
        description: "Unlike ${{ inputs.ignored }}, takes effect"
        type: string
jobs:
  build:
    # ${{ inputs.ignored }} is gone
    runs-on: ubuntu-latest # inputs.ignored
    steps:
      - run: |
          # echo "${{ inputs.ignored }}"
          echo "${{ inputs.used }}"
`)

	text := readableText(t, "w.yml", workflow, isWorkflowInputDeclaration)
	if got, want := unreadInputs("w.yml", workflowInputNames(t, "w.yml", workflow), text, actionsInputRead), []string{"w.yml: ignored"}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}

	action := []byte(`inputs:
  used:
    description: "Defaults to inputs.ignored"
  ignored:
    description: "Ignored"
runs:
  using: composite
  steps:
    - run: echo "${{ inputs.used }}"
      shell: bash
`)

	text = readableText(t, "action.yml", action, isActionInputDeclaration)
	if got, want := unreadInputs("action.yml", actionInputNames(t, "action.yml", action), text, actionsInputRead), []string{"action.yml: ignored"}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}

	names, rest, err := componentInputNames([]byte("spec:\n  inputs:\n    used:\n    ignored:\n      description: \"not $[[ inputs.ignored ]]\"\n---\n# $[[ inputs.ignored ]]\njob:\n  script: echo \"$[[ inputs.used ]]\"\n"))
	if err != nil {
		t.Fatal(err)
	}

	if got, want := unreadInputs("c.yml", names, readableText(t, "c.yml", rest, nil), componentInputRead), []string{"c.yml: ignored"}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestComponentInputNames_RefusesASpecWithoutSeparator(t *testing.T) {
	t.Parallel()

	if _, _, err := componentInputNames([]byte("spec:\n  inputs:\n    stage:\njob:\n  script: echo\n")); err == nil {
		t.Fatal("a spec header without its --- separator was accepted; its inputs would go unchecked")
	}

	names, rest, err := componentInputNames([]byte("job:\n  script: echo\n"))
	if err != nil || names != nil || len(rest) == 0 {
		t.Fatalf("a file without a spec: names=%q rest=%q err=%v", names, rest, err)
	}
}

// isWorkflowInputDeclaration reports the inputs mapping of a workflow trigger.
func isWorkflowInputDeclaration(path []string) bool {
	return len(path) == 3 && path[0] == "on" && path[2] == "inputs"
}

// isActionInputDeclaration reports an action's top-level inputs mapping.
func isActionInputDeclaration(path []string) bool {
	return len(path) == 1 && path[0] == "inputs"
}

// readableText returns the YAML values of every document in body where an
// input can be read: outside the declaration skip reports, without comments.
// YAML comments are dropped by the parser; comment lines inside a value, such
// as a shell comment in a run script, are removed here.
func readableText(t *testing.T, path string, body []byte, skip func([]string) bool) string {
	t.Helper()

	var text strings.Builder

	var walk func(node *yaml.Node, at []string)

	walk = func(node *yaml.Node, at []string) {
		if skip != nil && skip(at) {
			return
		}

		switch node.Kind {
		case yaml.DocumentNode, yaml.SequenceNode:
			for _, child := range node.Content {
				walk(child, at)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(node.Content); i += 2 {
				walk(node.Content[i], at)
				walk(node.Content[i+1], append(slices.Clone(at), node.Content[i].Value))
			}
		case yaml.ScalarNode:
			for line := range strings.SplitSeq(node.Value, "\n") {
				if !strings.HasPrefix(strings.TrimSpace(line), "#") {
					text.WriteString(line)
					text.WriteByte('\n')
				}
			}
		case yaml.AliasNode:
			// The anchored value is walked where it is defined.
		}
	}

	decoder := yaml.NewDecoder(bytes.NewReader(body))

	for {
		var doc yaml.Node
		if err := decoder.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			t.Fatalf("%s: %v", path, err)
		}

		walk(&doc, nil)
	}

	return text.String()
}

func actionsInputRead(name string) *regexp.Regexp {
	quoted := regexp.QuoteMeta(name)

	return regexp.MustCompile(`inputs(\.` + quoted + `\b|\[\s*['"]` + quoted + `['"]\s*\])`)
}

func componentInputRead(name string) *regexp.Regexp {
	return regexp.MustCompile(`\$\[\[\s*inputs\.` + regexp.QuoteMeta(name) + `\b`)
}

func unreadInputs(file string, names []string, body string, read func(string) *regexp.Regexp) []string {
	var findings []string

	for _, name := range names {
		// \b would accept a read of a longer name such as `name-suffix`.
		if !inputReadExactly(read(name), name, body) {
			findings = append(findings, file+": "+name)
		}
	}

	return findings
}

func inputReadExactly(pattern *regexp.Regexp, name, body string) bool {
	for _, loc := range pattern.FindAllStringIndex(body, -1) {
		end := loc[1]
		if end < len(body) && body[end] == '-' && strings.HasSuffix(body[loc[0]:end], name) {
			continue
		}

		return true
	}

	return false
}

func workflowInputNames(t *testing.T, path string, body []byte) []string {
	t.Helper()

	var doc struct {
		On yaml.Node `yaml:"on"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		t.Fatalf("%s: %v", path, err)
	}

	var names []string

	if doc.On.Kind != yaml.MappingNode {
		return nil
	}

	for i := 0; i+1 < len(doc.On.Content); i += 2 {
		trigger := doc.On.Content[i].Value
		if trigger != "workflow_call" && trigger != "workflow_dispatch" {
			continue
		}

		var declared struct {
			Inputs map[string]any `yaml:"inputs"`
		}
		if err := doc.On.Content[i+1].Decode(&declared); err != nil {
			t.Fatalf("%s: %s: %v", path, trigger, err)
		}

		for name := range declared.Inputs {
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}

	slices.Sort(names)

	return names
}

func actionInputNames(t *testing.T, path string, body []byte) []string {
	t.Helper()

	var doc struct {
		Inputs map[string]any `yaml:"inputs"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		t.Fatalf("%s: %v", path, err)
	}

	return inputKeys(doc.Inputs)
}

// componentInputNames returns the spec's inputs and the job document after
// the `---` that ends the spec header, where reads have to be. A file without
// a spec header is all job document. A spec without the separator is refused:
// its inputs could not be told from the jobs that read them.
func componentInputNames(body []byte) ([]string, []byte, error) {
	header, rest, found := bytes.Cut(body, []byte("\n---\n"))
	if !found {
		if componentSpecHeader.Match(body) {
			return nil, nil, errors.New("declares spec: without the --- line that ends the spec header")
		}

		return nil, body, nil
	}

	var doc struct {
		Spec struct {
			Inputs map[string]any `yaml:"inputs"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(header, &doc); err != nil {
		return nil, nil, err
	}

	return inputKeys(doc.Spec.Inputs), rest, nil
}

var componentSpecHeader = regexp.MustCompile(`(?m)^spec:`)

func inputKeys(m map[string]any) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}

	slices.Sort(names)

	return names
}

func yamlFiles(t *testing.T, dir string) []string {
	t.Helper()

	var paths []string

	for _, entry := range reporoot.ReadDir(t, dir) {
		if !entry.IsDir() && (strings.HasSuffix(entry.Name(), ".yml") || strings.HasSuffix(entry.Name(), ".yaml")) {
			paths = append(paths, dir+"/"+entry.Name())
		}
	}

	return paths
}
