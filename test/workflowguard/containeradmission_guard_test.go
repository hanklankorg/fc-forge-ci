// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// admissionExempt are the workflows whose container jobs are admitted
// elsewhere, with the reason.
var admissionExempt = map[string]string{
	// It is the canonical admission: canonical-admit refuses a builder or
	// receive image that is not a digest before either container starts.
	"canonical-cli-bootstrap.yml": "admits its own images in canonical-admit",
}

var containerImageInput = regexp.MustCompile(`inputs(?:\['([a-z0-9-]+)'\]|\.([a-z0-9-]+))`)

// statusOverride matches the status functions that run a job although one
// of its needs failed, such as a resolver that refused an image.
var (
	statusOverride  = regexp.MustCompile(`always\(\)|cancelled\(\)|failure\(\)`)
	requiredSuccess = regexp.MustCompile(`needs\.([a-z0-9-]+)\.result == 'success'`)
)

type admissionWorkflow struct {
	On struct {
		WorkflowCall *struct {
			Inputs map[string]struct {
				Required bool `yaml:"required"`
				Default  any  `yaml:"default"`
			} `yaml:"inputs"`
		} `yaml:"workflow_call"`
	} `yaml:"on"`
	Jobs map[string]struct {
		Uses      string            `yaml:"uses"`
		If        string            `yaml:"if"`
		Needs     yaml.Node         `yaml:"needs"`
		With      map[string]string `yaml:"with"`
		Container yaml.Node         `yaml:"container"`
		Steps     []admissionStep   `yaml:"steps"`
	} `yaml:"jobs"`
}

type admissionStep struct {
	Name string `yaml:"name"`
}

// TestEveryContainerImageIsAdmittedByTheResolver: a job container starts
// before any of its steps, and an empty image runs the job on the runner
// itself, so every image a reusable workflow's container job runs is named in
// that workflow's resolver call, which refuses anything but a digest before
// the job starts. A job whose if overrides the needs status must still
// require the resolver's success. The line is required unless the job picks the image from its
// plan's matrix; then the plan job refuses a planned family that was not
// passed.
func TestEveryContainerImageIsAdmittedByTheResolver(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob(filepath.Join(reporoot.Path(t), ".github", "workflows", "*.yml"))
	require.NoError(t, err)

	admitted := 0

	for _, path := range paths {
		name := filepath.Base(path)

		body, err := os.ReadFile(path)
		require.NoError(t, err)

		require.NotContains(t, string(body), "needs.resolve-bootstrap.outputs['runtime-image",
			"%s falls back to a resolved image; the caller passes its digest", name)
		require.NotContains(t, string(body), "outputs.forge-ci-binary-ref",
			"%s relays the engine commit; read inputs['forge-ci-binary-ref'], which the resolver admitted", name)

		var workflow admissionWorkflow
		require.NoError(t, yaml.Unmarshal(body, &workflow), name)

		for jobName, job := range workflow.Jobs {
			if !strings.HasPrefix(job.Uses, "./.github/workflows/") {
				continue
			}

			for key, value := range job.With {
				if strings.HasPrefix(key, "runtime-image") || key == "canonical-builder-image" {
					require.Truef(t, strings.Contains(value, "inputs") || strings.Contains(value, "@"),
						"%s/%s passes %s by tag; the callee admits only a digest: %s", name, jobName, key, value)
				}
			}
		}

		if workflow.On.WorkflowCall == nil || admissionExempt[name] != "" {
			continue
		}

		lines := resolverImageLines(t, name, workflow)

		for jobName, job := range workflow.Jobs {
			image := containerImage(&job.Container)
			if image == "" {
				continue
			}

			refs := containerImageInput.FindAllStringSubmatch(image, -1)
			require.NotEmptyf(t, refs, "%s/%s runs a container image that is not a caller input: %s", name, jobName, image)
			require.Truef(t, needsTransitively(workflow, jobName, "resolve-bootstrap"),
				"%s/%s starts its container without needing resolve-bootstrap, which admits it", name, jobName)

			require.Truef(t, runImpliesResolverSuccess(workflow, jobName),
				"%s/%s overrides the needs status in its if, so its container starts after the resolver refused its image; "+
					"add needs.resolve-bootstrap.result == 'success'", name, jobName)

			for _, ref := range refs {
				input := ref[1] + ref[2]
				optional, listed := lines[input]
				require.Truef(t, listed, "%s/%s runs inputs['%s'], which the resolver call does not admit", name, jobName, input)

				if optional {
					require.Containsf(t, image, "matrix.", "%s admits %s as optional, but %s runs it on every call", name, input, jobName)
				}

				admitted++
			}

			if strings.Contains(image, "matrix.") {
				require.Truef(t, planRefusesMissingFamilies(workflow), "%s/%s picks its image from the plan, and no plan job refuses a planned family that was not passed", name, jobName)
			}
		}

		if len(lines) > 0 {
			spec := workflow.On.WorkflowCall.Inputs[binaryRefInput]
			require.Truef(t, spec.Required, "%s: %s must be required", name, binaryRefInput)
			require.Nilf(t, spec.Default, "%s: %s must have no default", name, binaryRefInput)
		}
	}

	require.Greater(t, admitted, 40, "the admission guard found too few container images; did the layout change?")
}

// resolverImageLines reads the `images` lines of the workflow's resolver call:
// input name to whether the line marks it optional. Each line must read the
// input of its own name.
func resolverImageLines(t *testing.T, name string, workflow admissionWorkflow) map[string]bool {
	t.Helper()

	lines := map[string]bool{}

	resolver, ok := workflow.Jobs["resolve-bootstrap"]
	if !ok {
		return lines
	}

	require.Equalf(t, "./.github/workflows/resolve-forge-ci-bootstrap.yml", resolver.Uses, "%s: resolve-bootstrap", name)
	require.Equalf(t, engineCommitInput, resolver.With[binaryRefInput], "%s: resolve-bootstrap", name)

	for line := range strings.SplitSeq(strings.TrimSpace(resolver.With["images"]), "\n") {
		if line == "" {
			continue
		}

		input, value, found := strings.Cut(line, "=")
		require.Truef(t, found, "%s: images line %q", name, line)

		input, optional := strings.CutSuffix(input, "?")
		require.Equalf(t, "${{ inputs['"+input+"'] }}", value, "%s: images line %q admits another value than its input", name, line)

		lines[input] = optional
	}

	return lines
}

func containerImage(node *yaml.Node) string {
	switch node.Kind {
	case yaml.ScalarNode:
		return node.Value
	case yaml.MappingNode:
		if image := mappingChild(node, "image"); image != nil {
			return image.Value
		}
	default:
		// Another node names no image.
	}

	return ""
}

func needsList(node *yaml.Node) []string {
	switch node.Kind {
	case yaml.ScalarNode:
		return []string{node.Value}
	case yaml.SequenceNode:
		var needs []string
		for _, item := range node.Content {
			needs = append(needs, item.Value)
		}

		return needs
	default:
		// Another node names no job.
	}

	return nil
}

func needsTransitively(workflow admissionWorkflow, job, want string) bool {
	seen := map[string]bool{}
	queue := []string{job}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		spec := workflow.Jobs[current]
		for _, need := range needsList(&spec.Needs) {
			if need == want {
				return true
			}

			if !seen[need] {
				seen[need] = true
				queue = append(queue, need)
			}
		}
	}

	return false
}

// runImpliesResolverSuccess reports whether the job can only run once
// resolve-bootstrap succeeded. Without a status function in its if, a job
// runs only when all its needs succeeded. With one, its if must exclude a
// failed ancestor (!failure()) or require the success of the resolver or of
// a job whose own run implies it. GitHub refuses cyclic needs, so the
// recursion ends.
func runImpliesResolverSuccess(workflow admissionWorkflow, job string) bool {
	if job == "resolve-bootstrap" {
		return true
	}

	spec := workflow.Jobs[job]

	var candidates []string

	switch {
	case !statusOverride.MatchString(spec.If):
		candidates = needsList(&spec.Needs)
	case strings.Contains(strings.ReplaceAll(spec.If, " ", ""), "!failure()"):
		return needsTransitively(workflow, job, "resolve-bootstrap")
	default:
		for _, match := range requiredSuccess.FindAllStringSubmatch(spec.If, -1) {
			candidates = append(candidates, match[1])
		}
	}

	return slices.ContainsFunc(candidates, func(need string) bool {
		return runImpliesResolverSuccess(workflow, need)
	})
}

func planRefusesMissingFamilies(workflow admissionWorkflow) bool {
	for _, job := range workflow.Jobs {
		if slices.ContainsFunc(job.Steps, func(step admissionStep) bool {
			return step.Name == "Require the toolchain image of every planned artifact"
		}) {
			return true
		}
	}

	return false
}
