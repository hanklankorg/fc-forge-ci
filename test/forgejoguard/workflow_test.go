// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

type workflow struct {
	jobs map[string]job
	env  ast.Node
}

type job struct {
	node ast.Node
}

// parseDocument returns the single document body.
func parseDocument(data []byte) (ast.Node, error) {
	parsed, err := parser.ParseBytes(data, 0)
	if err != nil || parsed == nil || len(parsed.Docs) != 1 || parsed.Docs[0].Body == nil {
		return nil, fmt.Errorf("%w: workflow is not one parseable YAML document", errRule)
	}

	return parsed.Docs[0].Body, nil
}

func parseWorkflow(data []byte) (workflow, error) {
	root, err := parseDocument(data)
	if err != nil {
		return workflow{}, err
	}

	jobsNode := mappingValue(root, "jobs")
	if jobsNode == nil {
		return workflow{}, fmt.Errorf("%w: workflow declares no jobs", errRule)
	}

	jobs := make(map[string]job)
	for _, entry := range mappingEntries(jobsNode) {
		jobs[scalarString(entry.Key)] = job{node: entry.Value}
	}

	if len(jobs) == 0 {
		return workflow{}, fmt.Errorf("%w: workflow declares no jobs", errRule)
	}

	return workflow{jobs: jobs, env: mappingValue(root, "env")}, nil
}

func loadWorkflow(t *testing.T, relative string) workflow {
	t.Helper()

	parsed, err := parseWorkflow(reporoot.ReadFile(t, relative))
	if err != nil {
		t.Fatalf("%s: %v", relative, err)
	}

	return parsed
}

// loadDocument returns the parsed body of a repository-relative YAML file.
func loadDocument(t *testing.T, relative string) ast.Node {
	t.Helper()

	root, err := parseDocument(reporoot.ReadFile(t, relative))
	if err != nil {
		t.Fatalf("%s: %v", relative, err)
	}

	return root
}

// mappingEntries normalizes the two shapes a mapping can take: a mapping with
// several entries, and a mapping with exactly one.
func mappingEntries(node ast.Node) []*ast.MappingValueNode {
	switch typed := node.(type) {
	case *ast.MappingNode:
		return typed.Values
	case *ast.MappingValueNode:
		return []*ast.MappingValueNode{typed}
	}

	return nil
}

func mappingValue(node ast.Node, key string) ast.Node {
	for _, entry := range mappingEntries(node) {
		if scalarString(entry.Key) == key {
			return entry.Value
		}
	}

	return nil
}

func sequenceValues(node ast.Node) []ast.Node {
	if sequence, ok := node.(*ast.SequenceNode); ok {
		return sequence.Values
	}

	return nil
}

func scalarString(node ast.Node) string {
	if node == nil {
		return ""
	}

	if str, ok := node.(*ast.StringNode); ok {
		return str.Value
	}

	return strings.TrimSpace(node.String())
}

// anyString reports whether any scalar or mapping key beneath the node
// satisfies match. Checks use this rather than the file text, so indentation
// and quoting cannot change the answer. The walk short-circuits.
func anyString(node ast.Node, match func(string) bool) bool {
	switch typed := node.(type) {
	case *ast.MappingNode, *ast.MappingValueNode:
		for _, entry := range mappingEntries(typed) {
			if match(scalarString(entry.Key)) || anyString(entry.Value, match) {
				return true
			}
		}
	case *ast.SequenceNode:
		for _, item := range typed.Values {
			if anyString(item, match) {
				return true
			}
		}
	case nil:
		return false
	default:
		return match(scalarString(node))
	}

	return false
}

// mentions reports whether any scalar or key beneath the node contains text.
func mentions(node ast.Node, text string) bool {
	return anyString(node, func(value string) bool { return strings.Contains(value, text) })
}

// requireWalkReaches asserts the walker finds content the node is known to
// contain. Every "must not appear" check below needs one: a walker that finds
// nothing satisfies every absence assertion, so the negative checks would pass
// for a broken walk.
func requireWalkReaches(t *testing.T, node ast.Node, text, subject string) {
	t.Helper()

	if !mentions(node, text) {
		t.Fatalf("%s: the walk did not reach known content %q, so the absence checks prove nothing",
			subject, text)
	}
}

// containerImage returns the job's container image. Forgejo accepts either a
// bare string or a mapping with an image key.
func (j job) containerImage() string {
	container := mappingValue(j.node, "container")
	if container == nil {
		return ""
	}

	if image := mappingValue(container, "image"); image != nil {
		return scalarString(image)
	}

	return scalarString(container)
}

func (j job) steps() []ast.Node {
	return sequenceValues(mappingValue(j.node, "steps"))
}

// findStep returns the first step with the given name, from any job.
func (w workflow) findStep(stepName string) (ast.Node, bool) {
	for _, job := range w.jobs {
		for _, step := range job.steps() {
			if scalarString(mappingValue(step, "name")) == stepName {
				return step, true
			}
		}
	}

	return nil, false
}

// stepByID returns the job's step with the given id.
func (j job) stepByID(id string) (ast.Node, bool) {
	for _, step := range j.steps() {
		if scalarString(mappingValue(step, "id")) == id {
			return step, true
		}
	}

	return nil, false
}

// runsOn returns the job's runner label when it is a scalar.
func (j job) runsOn() string {
	return scalarString(mappingValue(j.node, "runs-on"))
}

func (j job) env(key string) (string, bool) {
	env := mappingValue(j.node, "env")
	if env == nil {
		return "", false
	}

	value := mappingValue(env, key)
	if value == nil {
		return "", false
	}

	return scalarString(value), true
}
