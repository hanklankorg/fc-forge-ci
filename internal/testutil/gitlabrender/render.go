// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package gitlabrender provides provider-free interpolation and structural
// checks for this repository's GitLab components and their child pipelines.
// It does not replace GitLab CI lint or prove runtime rules or authorization.
package gitlabrender

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// requiredInputFixtures gives each input a component requires, one without a
// default, the value a consumer could pass. Every other input renders with its
// default.
var requiredInputFixtures = map[string]any{
	"forge-ci-repository":              "team/engine",
	"consumer-project":                 "team/app",
	"server-url":                       "https://gitlab.invalid",
	"registry":                         "registry.gitlab.invalid",
	"job-image":                        "registry.invalid/team/job@sha256:" + strings.Repeat("1", 64),
	"build-image":                      "registry.invalid/team/build@sha256:" + strings.Repeat("2", 64),
	"builder-job-image":                "registry.invalid/team/builder@sha256:" + strings.Repeat("9", 64),
	"signer-image":                     "registry.invalid/team/signer@sha256:" + strings.Repeat("3", 64),
	"publication-project":              "team/publication",
	"publication-image-repository":     "registry.invalid/team/publication/app",
	"cosign-public-key-sha256":         strings.Repeat("4", 64),
	"release-git-host-key-fingerprint": "SHA256:" + strings.Repeat("A", 43),
	"request-allowed-signers":          "release@team.invalid ssh-ed25519 AAAA",
	"bot-name":                         "Release Bot",
	"bot-email":                        "release@team.invalid",
	"checks":                           []any{"make check"},
	"build":                            []any{"make dist"},
	"commands":                         []any{"make check"},
	"engine-sha":                       strings.Repeat("5", 40),
	"engine-reference":                 "v1.2.3",
	"changelog-config":                 ".chglog/config.yml",
	"commit-body-config":               ".forge-ci/commit-body.yml",
	"sign-distro-packages":             false,
	"release-asset-retention":          "",
	"base-stages":                      "",
	"base-plan":                        "",
	"transparency-log":                 "none",
	"release-branch":                   "",
	"changelog-creator":                "git-cliff",
	"megalinter-image":                 "registry.invalid/team/megalinter@sha256:" + strings.Repeat("8", 64),
	"nanolinter-image":                 "registry.invalid/team/nanolinter@sha256:" + strings.Repeat("7", 64),
	"runtime-image":                    "registry.invalid/team/runtime@sha256:" + strings.Repeat("9", 64),
	"branch":                           "main",
	"image-repository":                 "registry.invalid/team/app",
	// The build runners, platform-bound, have no default: amd64 is named,
	// and [] states there is no arm64 runner.
	"build-runner-tags-linux-amd64": []any{"build-amd64"},
	"build-runner-tags-linux-arm64": []any{},
	// The administrator settings of a child component, forwarded by its
	// parent: not the defaults, so a literal shows which value it took.
	"enforce-lint":                false,
	"enforce-vulnerabilities":     false,
	"enforce-application-tests":   false,
	"enforce-reproducible-builds": false,
}

// FixtureValue supplies an inert typed value for a placeholder. Returned arrays
// are copied so independently rendered examples cannot modify shared fixtures.
func FixtureValue(name string) (any, bool) {
	value, known := requiredInputFixtures[name]
	if list, ok := value.([]any); ok {
		return slices.Clone(list), known
	}

	return value, known
}

// Problems renders a component with the given inputs, the inputs
// it requires and its defaults, and returns what GitLab would refuse in it,
// with the repository's files it includes, and in every child pipeline of
// the repository's files it triggers.
func Problems(t *testing.T, path string, given map[string]any) []string {
	t.Helper()

	return componentProblems(t, path, given, true)
}

func componentProblems(t *testing.T, path string, given map[string]any, fillRequired bool) []string {
	t.Helper()

	body, children, problems := pipeline(t, path, given, fillRequired)

	for _, problem := range ConfigProblems(body) {
		problems = append(problems, path+": "+problem)
	}

	for _, child := range children {
		inputs, _ := child["inputs"].(map[string]any)
		problems = append(problems, componentProblems(t, repositoryTemplate(child), inputs, false)...)
	}

	return problems
}

// pipeline renders a component and merges into it the repository's
// files it includes, the including document over them, as GitLab does. The
// repository's files its trigger jobs include are its child pipelines.
func pipeline(t *testing.T, label string, given map[string]any, fillRequired bool) (map[string]any, []map[string]any, []string) {
	t.Helper()

	body, problems := component(t, label, given, fillRequired)

	return expandPipeline(t, body, problems)
}

// DocumentProblems expands the current checkout's named component definitions
// in a rendered caller and checks its structure and child pipelines. Remote
// origins, pins and variable-dependent rules still need their dedicated guards.
func DocumentProblems(t *testing.T, label string, document []byte) []string {
	t.Helper()

	var body map[string]any
	if err := yaml.Unmarshal(document, &body); err != nil {
		t.Fatalf("%s is not YAML: %v", label, err)
	}

	merged, children, problems := expandPipeline(t, body, nil)
	for _, problem := range ConfigProblems(merged) {
		problems = append(problems, label+": "+problem)
	}

	for _, child := range children {
		inputs, _ := child["inputs"].(map[string]any)
		problems = append(problems, componentProblems(t, repositoryTemplate(child), inputs, false)...)
	}

	return problems
}

func expandPipeline(t *testing.T, body map[string]any, problems []string) (map[string]any, []map[string]any, []string) {
	t.Helper()

	merged := map[string]any{}

	var children []map[string]any

	includes, _ := body["include"].([]any)
	for _, item := range includes {
		include, _ := item.(map[string]any)
		if template := repositoryTemplate(include); template != "" {
			inputs, _ := include["inputs"].(map[string]any)
			included, grandchildren, includedProblems := pipeline(t, template, inputs, false)
			Merge(merged, included)

			children = append(children, grandchildren...)
			problems = append(problems, includedProblems...)
		}
	}

	Merge(merged, body)
	delete(merged, "include")

	for _, value := range body {
		job, _ := value.(map[string]any)
		trigger, _ := job["trigger"].(map[string]any)
		triggered, _ := trigger["include"].([]any)

		for _, item := range triggered {
			if include, _ := item.(map[string]any); repositoryTemplate(include) != "" {
				children = append(children, include)
			}
		}
	}

	return merged, children, problems
}

// repositoryTemplate is the path of this repository's template an include
// names by project, or empty.
func repositoryTemplate(include map[string]any) string {
	if component, ok := include["component"].(string); ok {
		reference, _, pinned := strings.Cut(component, "@")
		name := path.Base(reference)

		if pinned && regexp.MustCompile(`^[a-z0-9-]+$`).MatchString(name) {
			return "templates/" + name + ".yml"
		}
	}

	file, _ := include["file"].(string)
	if _, byProject := include["project"]; !byProject || !strings.HasPrefix(file, "/templates/") {
		return ""
	}

	return strings.TrimPrefix(file, "/")
}

// component interpolates a component's inputs as GitLab does: each
// takes the value given, else its default, else its fixture, and must then
// satisfy the input's type, options and regex.
func component(t *testing.T, path string, given map[string]any, fillRequired bool) (map[string]any, []string) {
	t.Helper()

	values, document, problems := inputValues(t, path, given, fillRequired)

	var parsed any
	if err := yaml.Unmarshal([]byte(document), &parsed); err != nil {
		t.Fatalf("%s is not YAML: %v", path, err)
	}

	rendered, err := interpolate(parsed, values)
	if err != nil {
		problems = append(problems, fmt.Sprintf("%s: %v", path, err))
	}

	body, _ := rendered.(map[string]any)

	return body, problems
}

// componentInputValues is the interpolation values of a component, each
// input the value given, else its default, else its fixture, the document
// after its spec, and what GitLab refuses in the values.
func componentSource(t *testing.T, path string) (map[string]map[string]any, string) {
	t.Helper()

	text := string(reporoot.ReadFile(t, path))

	var spec struct {
		Spec struct {
			Inputs map[string]map[string]any `yaml:"inputs"`
		} `yaml:"spec"`
	}

	header, document, found := strings.Cut(text, "\n---\n")
	if !found {
		document = text
	} else if err := yaml.Unmarshal([]byte(header), &spec); err != nil {
		t.Fatal(err)
	}

	return spec.Spec.Inputs, document
}

// InputSpec reads the component's own input declarations.
func InputSpec(t *testing.T, path string) map[string]map[string]any {
	t.Helper()

	spec, _ := componentSource(t, path)
	if len(spec) == 0 {
		t.Fatalf("%s declares no inputs", path)
	}

	return spec
}

func inputValues(t *testing.T, path string, given map[string]any, fillRequired bool) (map[string]any, string, []string) {
	t.Helper()

	spec, document := componentSource(t, path)

	var problems []string

	for name := range given {
		if _, declared := spec[name]; !declared {
			problems = append(problems, fmt.Sprintf("%s: input %s is not declared", path, name))
		}
	}

	values := map[string]any{"component.sha": strings.Repeat("6", 40), "component.reference": "v1.2.3"}

	for name, input := range spec {
		value, ok := given[name]
		if !ok {
			value, ok = input["default"]
		}

		if !ok {
			if !fillRequired {
				problems = append(problems, fmt.Sprintf("%s: required input %s is missing", path, name))

				continue
			}

			value, ok = FixtureValue(name)
		}

		if !ok {
			t.Fatalf("%s requires input %s: give it a fixture", path, name)
		}

		if problem := InputProblem(input, value); problem != "" {
			problems = append(problems, fmt.Sprintf("%s: input %s: %s", path, name, problem))
		}

		values["inputs."+name] = value
	}

	return values, document, problems
}

// Script interpolates a component's inputs in a script, as GitLab
// does, each the value given, else its default, else its fixture.
func Script(t *testing.T, path, script string, given map[string]any) string {
	t.Helper()

	values, _, problems := inputValues(t, path, given, true)
	if len(problems) != 0 {
		t.Fatal(strings.Join(problems, "\n"))
	}

	rendered, err := interpolate(script, values)
	if err != nil {
		t.Fatal(err)
	}

	text, ok := rendered.(string)
	if !ok {
		t.Fatalf("%s renders a script that is not text", path)
	}

	return text
}

// InputProblem checks an input's declared type, options and regex.
func InputProblem(input map[string]any, value any) string {
	kind, _ := input["type"].(string)

	switch kind {
	case "array":
		if _, ok := value.([]any); !ok {
			return fmt.Sprintf("%v is not an array", value)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Sprintf("%v is not a boolean", value)
		}
	default:
		text, ok := value.(string)
		if !ok {
			return fmt.Sprintf("%v is not a string", value)
		}

		if options, ok := input["options"].([]any); ok && !slices.Contains(options, any(text)) {
			return fmt.Sprintf("%q is not one of %v", text, options)
		}

		if pattern, ok := input["regex"].(string); ok && !regexp.MustCompile(pattern).MatchString(text) {
			return fmt.Sprintf("%q does not match %s", text, pattern)
		}
	}

	return ""
}

var gitlabInterpolation = regexp.MustCompile(`\$\[\[\s*([a-z0-9._-]+)\s*\]\]`)

var errUndeclaredInput = errors.New("an undeclared input")

// InputReferences returns the input names interpolated into text, in order.
func InputReferences(text string) []string {
	var names []string

	for _, match := range gitlabInterpolation.FindAllStringSubmatch(text, -1) {
		if name, input := strings.CutPrefix(match[1], "inputs."); input {
			names = append(names, name)
		}
	}

	return names
}

// interpolate interpolates the parsed document as GitLab does: in
// keys and string values, after YAML parsing. A string that is one
// interpolation alone takes the value itself, an array or boolean included;
// within other text a value takes its text, JSON for an array.
func interpolate(node any, values map[string]any) (any, error) {
	switch value := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))

		for key, child := range value {
			name, err := interpolate(key, values)
			if err != nil {
				return nil, err
			}

			rendered, err := interpolate(child, values)
			if err != nil {
				return nil, err
			}

			out[fmt.Sprint(name)] = rendered
		}

		return out, nil
	case []any:
		out := make([]any, len(value))

		for i, child := range value {
			rendered, err := interpolate(child, values)
			if err != nil {
				return nil, err
			}

			out[i] = rendered
		}

		return out, nil
	case string:
		return interpolateString(value, values)
	default:
		return node, nil
	}
}

func interpolateString(value string, values map[string]any) (any, error) {
	if whole := gitlabInterpolation.FindStringSubmatchIndex(value); whole != nil && whole[0] == 0 && whole[1] == len(value) {
		input, ok := values[value[whole[2]:whole[3]]]
		if !ok {
			return nil, fmt.Errorf("%s names %w", value, errUndeclaredInput)
		}

		return input, nil
	}

	var err error

	text := gitlabInterpolation.ReplaceAllStringFunc(value, func(block string) string {
		input, ok := values[gitlabInterpolation.FindStringSubmatch(block)[1]]
		if !ok {
			err = fmt.Errorf("%s names %w", block, errUndeclaredInput)

			return block
		}

		if text, isText := input.(string); isText {
			return text
		}

		encoded, _ := json.Marshal(input)

		return string(encoded)
	})

	return text, err
}

// Jobs renders a component with the given inputs, the inputs it requires and
// its defaults, and returns each of its visible jobs with its parents merged
// in, as the runner receives it. It fails the test on a refused input.
func Jobs(t *testing.T, path string, given map[string]any) map[string]map[string]any {
	t.Helper()

	body, problems := component(t, path, given, true)
	if len(problems) != 0 {
		t.Fatal(strings.Join(problems, "\n"))
	}

	jobs := map[string]map[string]any{}

	for name, value := range body {
		job, isJob := value.(map[string]any)
		if !isJob || strings.HasPrefix(name, ".") || slices.Contains(gitlabGlobalKeywords, name) {
			continue
		}

		jobs[name] = ResolveExtends(t, body, job)
	}

	return jobs
}

// gitlabGlobalKeywords are the top-level keys that are not jobs.
var gitlabGlobalKeywords = []string{
	"after_script", "before_script", "cache", "default", "image", "include",
	"services", "spec", "stages", "variables", "workflow",
}

// ConfigProblems checks the structural errors covered by this offline validator,
// including images, inheritance, needs, stages and runnable job bodies. It runs
// before rule admission and returns sorted diagnostics.
func ConfigProblems(body map[string]any) []string {
	// A component's jobs run in the stages of the pipeline including it; a
	// child pipeline's declares its own.
	stages, declared := body["stages"].([]any)
	if declared {
		stages = append(stages, ".pre", ".post")
	}

	var problems []string

	for name, value := range body {
		job, isJob := value.(map[string]any)
		if !isJob || slices.Contains(gitlabGlobalKeywords, name) {
			continue
		}

		fields, err := resolveExtends(body, job, map[string]bool{})
		if err != nil {
			problems = append(problems, fmt.Sprintf("job %q: %v", name, err))

			continue
		}

		if strings.HasPrefix(name, ".") {
			continue
		}

		problems = append(problems, gitlabJobProblems(body, name, fields, stages)...)
	}

	sort.Strings(problems)

	return problems
}

func gitlabJobProblems(body map[string]any, name string, fields map[string]any, stages []any) []string {
	var problems []string

	if image, ok := fields["image"]; ok {
		imageName := image
		if options, isMap := image.(map[string]any); isMap {
			imageName = options["name"]
		}

		if text, _ := imageName.(string); strings.TrimSpace(text) == "" {
			problems = append(problems, fmt.Sprintf("job %q: image name is empty", name))
		}
	}

	stage, _ := fields["stage"].(string)
	if stage == "" {
		stage = "test"
	}

	if stages != nil && !slices.Contains(stages, any(stage)) {
		problems = append(problems, fmt.Sprintf("job %q: stage %q is not in stages", name, stage))
	}

	_, script := fields["script"]
	_, run := fields["run"]
	_, trigger := fields["trigger"]

	if !script && !run && !trigger {
		problems = append(problems, fmt.Sprintf("job %q: no script, run or trigger", name))
	}

	return append(problems, gitlabNeedsProblems(body, name, fields)...)
}

func gitlabNeedsProblems(body map[string]any, name string, fields map[string]any) []string {
	var problems []string

	// A job its rules drop needs nothing.
	needs, _ := fields["needs"].([]any)
	if gitlabRulesDrop(fields["rules"]) {
		needs = nil
	}

	for _, need := range needs {
		needed, optional := need, false

		if options, isMap := need.(map[string]any); isMap {
			if _, crossPipeline := options["pipeline"]; crossPipeline {
				continue
			}

			if _, crossProject := options["project"]; crossProject {
				continue
			}

			needed, optional = options["job"], options["optional"] == true
		}

		if optional {
			continue
		}

		neededName, _ := needed.(string)

		neededJob, defined := body[neededName].(map[string]any)

		if !defined {
			problems = append(problems, fmt.Sprintf("job %q: needs unknown job %q", name, neededName))
		} else if inherited, err := resolveExtends(body, neededJob, map[string]bool{}); err == nil && gitlabRulesDrop(inherited["rules"]) {
			problems = append(problems, fmt.Sprintf("job %q: needs job %q, which its rules drop", name, neededName))
		}
	}

	return problems
}

// gitlabRulesDrop says whether rules, rendered, drop their job as GitLab
// creates the pipeline. Rules that read a variable are decided only then, so
// they drop nothing here, nor do rules other than an if alone.
func gitlabRulesDrop(rules any) bool {
	list, _ := rules.([]any)
	for _, rule := range list {
		options, _ := rule.(map[string]any)
		if condition, literal := options["if"].(string); !literal || len(options) != 1 || strings.Contains(condition, "$") {
			return false
		}
	}

	return !RulesAdmit(rules)
}
