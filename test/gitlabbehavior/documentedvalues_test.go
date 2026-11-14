// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/gitlabrender"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// GitLab checks an input's value against the input's own type, options and
// regex as it creates the pipeline, before any job runs, and refuses the
// whole pipeline when one fails: a regex that refuses the value its own
// documentation names stops every adopter who copies it. The artifacts-config
// regex once refused .forge-ci/artifacts.yml, the path its description names.
// The defaults render through TestComponentsRenderAValidConfigurationWithTheirDefaults;
// this holds every other value the repository shows an adopter to the same
// check: the includes of the examples, the demos, the documentation and the
// components' own Consume headers, the example values the input descriptions
// name, and the defaults and examples of the inputs table in docs/gitlab.md.

// documentedComponent is an include of one of this repository's components,
// capturing the component's name.
var documentedComponent = regexp.MustCompile(`^[^@\s]+/forge-ci/([a-z0-9-]+)@\S+$`)

// documentedPlaceholder is a value an adopter replaces before use: a pin, a
// fix-me marker or an <angle-bracketed> description. Such a value is not
// checked; what replaces it is the adopter's.
var documentedPlaceholder = regexp.MustCompile(`PIN_[A-Z0-9_]*|REPLACE_[A-Z0-9_]+|FIXME|<[^<>]+>`)

// demoPlaceholder is an image or the instance's URL, which the live tier
// binds before it pushes a demo's gate, so the value GitLab checks is the
// trial's, never this text.
var demoPlaceholder = regexp.MustCompile(`^\$CI_(JOB_IMAGE(_[A-Z0-9]+)?|LINT_IMAGE|SERVER_URL)$`)

// documentedInclude is one include of a component where an adopter reads it.
type documentedInclude struct {
	where     string
	component string
	inputs    map[string]any
	// abbreviated marks an include the documentation shortens, "inputs: #
	// as above", whose required inputs another example shows.
	abbreviated bool
	demo        bool
}

func TestDocumentedIncludesPassValuesTheComponentAccepts(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)
	includes := documentedIncludes(t, root)

	sources := map[string]int{}
	for _, include := range includes {
		sources[strings.SplitN(include.where, "/", 2)[0]]++
	}

	for _, source := range []string{"examples", "test", "docs", "templates"} {
		if sources[source] == 0 {
			t.Errorf("no documented include found under %s/; the guard has drifted", source)
		}
	}

	specs := map[string]map[string]map[string]any{}

	for _, include := range includes {
		spec, known := specs[include.component]
		if !known {
			spec = gitlabrender.InputSpec(t, "templates/"+include.component+".yml")
			specs[include.component] = spec
		}

		problems := documentedIncludeProblems(include, spec)
		for _, problem := range problems {
			t.Error(problem)
		}

		if len(problems) != 0 {
			continue
		}

		given := map[string]any{}

		for name, value := range include.inputs {
			if text, ok := value.(string); ok && (documentedPlaceholder.MatchString(text) || include.demo && demoPlaceholder.MatchString(text)) {
				if fixture, known := gitlabrender.FixtureValue(name); known {
					value = fixture
				} else if fallback, known := spec[name]["default"]; known {
					value = fallback
				} else {
					t.Fatalf("%s: placeholder input %s needs a render fixture", include.where, name)
				}
			}

			given[name] = value
		}

		for _, problem := range gitlabrender.Problems(t, "templates/"+include.component+".yml", given) {
			t.Errorf("%s: %s", include.where, problem)
		}
	}
}

// The check itself refuses what GitLab refuses and leaves placeholders and,
// for a shortened documentation include, the required inputs alone.
func TestDocumentedIncludeProblemsFindsWhatGitLabRefuses(t *testing.T) {
	t.Parallel()

	spec := map[string]map[string]any{
		"artifacts-config": {"default": "", "regex": `^$|^[A-Za-z0-9_][A-Za-z0-9_./-]*$`},
		"runtime-image":    {"regex": `^[^[:space:]]+@sha256:[0-9a-f]{64}$`},
		"release-images":   {"type": "boolean", "default": false},
	}

	got := documentedIncludeProblems(documentedInclude{where: "x", component: "c", inputs: map[string]any{
		"artifacts-config": ".forge-ci/artifacts.yml",
		"release-images":   "true",
		"unknown":          "v",
	}}, spec)

	want := []string{
		`x: c input artifacts-config: ".forge-ci/artifacts.yml" does not match ^$|^[A-Za-z0-9_][A-Za-z0-9_./-]*$`,
		`x: c input release-images: true is not a boolean`,
		`x: c passes unknown, which the component does not declare`,
		`x: c leaves out the required input runtime-image`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("problems:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	placeholders := documentedIncludeProblems(documentedInclude{where: "x", component: "c", inputs: map[string]any{
		"runtime-image": "registry.example/runtime@sha256:PIN_DIGEST",
	}}, spec)
	if len(placeholders) != 0 {
		t.Fatalf("a placeholder was checked: %v", placeholders)
	}

	if problems := documentedIncludeProblems(documentedInclude{where: "x", component: "c", abbreviated: true}, spec); len(problems) != 0 {
		t.Fatalf("a shortened include was held to the required inputs: %v", problems)
	}
}

func documentedIncludeProblems(include documentedInclude, spec map[string]map[string]any) []string {
	var problems []string

	for _, name := range sortedNames(include.inputs) {
		declared, ok := spec[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: %s passes %s, which the component does not declare", include.where, include.component, name))

			continue
		}

		value := include.inputs[name]
		if text, ok := value.(string); ok && (documentedPlaceholder.MatchString(text) || (include.demo && demoPlaceholder.MatchString(text))) {
			continue
		}

		if problem := gitlabrender.InputProblem(declared, value); problem != "" {
			problems = append(problems, fmt.Sprintf("%s: %s input %s: %s", include.where, include.component, name, problem))
		}
	}

	if include.abbreviated {
		return problems
	}

	for _, name := range sortedNames(spec) {
		if _, hasDefault := spec[name]["default"]; hasDefault {
			continue
		}

		if _, passed := include.inputs[name]; !passed {
			problems = append(problems, fmt.Sprintf("%s: %s leaves out the required input %s", include.where, include.component, name))
		}
	}

	return problems
}

// documentedIncludes collects every include of a component an adopter reads.
func documentedIncludes(t *testing.T, root string) []documentedInclude {
	t.Helper()

	var includes []documentedInclude

	walkFiles(t, root, "examples", func(rel string) bool {
		return strings.Contains(rel, "/gitlab/") && isYAML(rel)
	}, func(rel string, body []byte) {
		includes = append(includes, yamlIncludes(t, rel, body, false)...)
	})

	walkFiles(t, root, "test/live/fixtures/consumers", func(rel string) bool {
		return isYAML(rel) && (strings.HasSuffix(rel, "/consumer/.gitlab-ci.yml") || strings.Contains(rel, "/consumer/.gitlab/"))
	}, func(rel string, body []byte) {
		includes = append(includes, yamlIncludes(t, rel, body, true)...)
	})

	walkFiles(t, root, "docs", func(rel string) bool { return filepath.Ext(rel) == ".md" }, func(rel string, body []byte) {
		includes = append(includes, markdownIncludes(t, rel, body)...)
	})
	includes = append(includes, markdownIncludes(t, "README.md", reporoot.ReadFile(t, "README.md"))...)

	walkFiles(t, root, "templates", func(rel string) bool { return isYAML(rel) && strings.Count(rel, "/") == 1 }, func(rel string, body []byte) {
		if header := consumeHeader(body); header != "" {
			includes = append(includes, yamlIncludes(t, rel+" Consume header", []byte(header), false)...)
		}
	})

	return includes
}

func isYAML(rel string) bool { return filepath.Ext(rel) == ".yml" || filepath.Ext(rel) == ".yaml" }

func walkFiles(t *testing.T, root, dir string, want func(string) bool, visit func(string, []byte)) {
	t.Helper()

	err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		rel = filepath.ToSlash(rel)
		if !want(rel) {
			return nil
		}

		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		visit(rel, body)

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// yamlIncludes reads the includes of this repository's components in one
// pipeline document.
func yamlIncludes(t *testing.T, where string, body []byte, demo bool) []documentedInclude {
	t.Helper()

	var pipeline struct {
		Include []map[string]any `yaml:"include"`
	}
	if err := yaml.Unmarshal(body, &pipeline); err != nil {
		t.Fatalf("%s is not YAML: %v", where, err)
	}

	var includes []documentedInclude

	for _, entry := range pipeline.Include {
		component, _ := entry["component"].(string)

		match := documentedComponent.FindStringSubmatch(component)
		if match == nil {
			continue
		}

		raw, present := entry["inputs"]
		inputs, _ := raw.(map[string]any)
		includes = append(includes, documentedInclude{
			where: where, component: match[1], inputs: inputs, demo: demo,
			abbreviated: present && raw == nil,
		})
	}

	return includes
}

// markdownIncludes reads the includes in the YAML blocks of a Markdown file;
// a block in a list item is indented with it.
func markdownIncludes(t *testing.T, rel string, body []byte) []documentedInclude {
	t.Helper()

	var includes []documentedInclude

	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	line, start := 0, 0

	var block strings.Builder

	indent, open := "", false

	for scanner.Scan() {
		line++
		text := scanner.Text()
		trimmed := strings.TrimLeft(text, " ")

		switch {
		case !open && trimmed == "```yaml":
			indent, open, start = text[:len(text)-len(trimmed)], true, line

			block.Reset()
		case open && trimmed == "```":
			open = false

			if strings.Contains(block.String(), "component:") {
				includes = append(includes, yamlIncludes(t, fmt.Sprintf("%s:%d", rel, start), []byte(block.String()), false)...)
			}
		case open:
			block.WriteString(strings.TrimPrefix(text, indent) + "\n")
		}
	}

	return includes
}

// consumeHeader is the YAML a component's "# Consume" comment shows, its
// comment markers removed.
func consumeHeader(body []byte) string {
	var header strings.Builder

	inside := false

	for text := range strings.SplitSeq(string(body), "\n") {
		if strings.HasPrefix(text, "# Consume") {
			inside = true

			continue
		}

		if !inside {
			continue
		}

		rest, comment := strings.CutPrefix(text, "# ")
		if !comment || !strings.HasPrefix(rest, " ") {
			break
		}

		header.WriteString(rest + "\n")
	}

	return header.String()
}

// describedValue is an example value an input's description names: "for
// example X", "e.g. X" or "Empty reads X".
var describedValue = regexp.MustCompile(`(?:for example|e\.g\.|[Ee]mpty reads) ([^\s,;()]+)`)

// Every value an input's description names as an example, or as what an
// empty value reads, is one the input accepts.
func TestInputDescriptionsNameValuesTheInputAccepts(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(filepath.Join(reporoot.Path(t), "templates"))
	if err != nil {
		t.Fatal(err)
	}

	named := 0

	for _, entry := range entries {
		if entry.IsDir() || !isYAML(entry.Name()) {
			continue
		}

		path := "templates/" + entry.Name()

		for name, input := range gitlabrender.InputSpec(t, path) {
			description, _ := input["description"].(string)

			for _, match := range describedValue.FindAllStringSubmatch(description, -1) {
				named++

				value := strings.TrimSuffix(match[1], ".")
				if problem := gitlabrender.InputProblem(input, typedValue(input, value)); problem != "" {
					t.Errorf("%s: input %s describes %q: %s", path, name, value, problem)
				}
			}
		}
	}

	if named == 0 {
		t.Fatal("no input description names a value; the guard has drifted")
	}
}

// typedValue is a value named in prose as the input's type reads it; text
// that is no boolean stays text, which a boolean input refuses.
func typedValue(input map[string]any, value string) any {
	switch input["type"] {
	case "boolean":
		if value == "true" || value == "false" {
			return value == "true"
		}

		return value
	case "array":
		return []any{value}
	default:
		return value
	}
}

// tableValue is a value the inputs table names as a default or an example:
// "default `X`", "by default `X`", "for example `X`", or "`X` (default)".
var tableValue = regexp.MustCompile("(?:[Dd]efault|for example) `([^`]+)`|`([^`]+)` \\(default\\)")

// Every default and example the inputs table of docs/gitlab.md names is a
// value one of the inputs its row names accepts.
func TestInputsTableNamesValuesTheInputsAccept(t *testing.T) {
	t.Parallel()

	text := string(reporoot.ReadFile(t, "docs/gitlab.md"))

	_, table, found := strings.Cut(text, "\n## Inputs and variables\n")
	table, _, _ = strings.Cut(table, "\n## ")

	if !found {
		t.Fatal("docs/gitlab.md has no Inputs and variables section")
	}

	component, checked := "", 0

	for row := range strings.SplitSeq(table, "\n") {
		cells := strings.Split(row, "|")
		if len(cells) != 5 || strings.HasPrefix(strings.TrimSpace(cells[1]), "---") || strings.TrimSpace(cells[1]) == "Component" {
			continue
		}

		if name := strings.Trim(strings.TrimSpace(cells[1]), "`"); name != "" {
			component = name
		}

		path := "templates/" + component + ".yml"
		if _, err := os.Stat(filepath.Join(reporoot.Path(t), path)); err != nil {
			continue // build-*, publish-*: a family, described in prose
		}

		spec := gitlabrender.InputSpec(t, path)

		var inputs []map[string]any

		for _, name := range backticked(cells[2]) {
			if input, ok := spec[name]; ok {
				inputs = append(inputs, input)
			}
		}

		for _, match := range tableValue.FindAllStringSubmatch(cells[3], -1) {
			value := match[1] + match[2]
			if _, isInput := spec[value]; isInput || len(inputs) == 0 {
				continue
			}

			checked++

			if !slices.ContainsFunc(inputs, func(input map[string]any) bool {
				return gitlabrender.InputProblem(input, typedValue(input, value)) == ""
			}) {
				t.Errorf("docs/gitlab.md: the %s row names %q, which none of %s accepts", component, value, strings.TrimSpace(cells[2]))
			}
		}
	}

	if checked == 0 {
		t.Fatal("the inputs table names no default or example; the guard has drifted")
	}
}

// backticked is the text between each pair of backticks.
func backticked(text string) []string {
	parts := strings.Split(text, "`")
	inside := make([]string, 0, len(parts)/2)

	for index := 1; index < len(parts); index += 2 {
		inside = append(inside, parts[index])
	}

	return inside
}

func sortedNames[V any](values map[string]V) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}

	slices.Sort(names)

	return names
}
