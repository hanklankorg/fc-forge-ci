// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package doctor

import (
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

const checkGitLabComponentJobs = "gitlab component job names"

// checkGitLabComponentJobNames refuses two forge-ci component includes in
// .gitlab-ci.yml that resolve to one job name. GitLab merges jobs of the same
// name without a word, so the second include silently rewrites the first.
// A component's job is its job-name input, or the component's name, which is
// every forge-ci component's default. A component is forge-ci's when its
// project is named as the repository of repoSlug, this binary's owner/repo.
// It reports nothing for a repository without .gitlab-ci.yml.
func checkGitLabComponentJobNames(root, repoSlug string) (Check, bool) {
	body, err := os.ReadFile(filepath.Join(root, ".gitlab-ci.yml")) //nolint:gosec // operator-selected repository root.
	if err != nil || repoSlug == "" {
		return Check{}, false
	}

	repoName := path.Base(repoSlug)

	pipeline, valid := gitLabPipelineRoot(body)
	if !valid {
		// Parsing is GitLab CI Lint's to judge; this check reads what it can.
		return Check{}, false
	}

	includers := map[string][]string{}

	for _, include := range gitLabIncludes(yamlChild(pipeline, "include")) {
		job, component, ok := forgeCIComponentJob(include, repoName)
		if ok {
			includers[job] = append(includers[job], component)
		}
	}

	var clashes []string

	for _, job := range slices.Sorted(maps.Keys(includers)) {
		if len(includers[job]) > 1 {
			clashes = append(clashes, fmt.Sprintf("%q from %s", job, strings.Join(includers[job], " and ")))
		}
	}

	if len(clashes) > 0 {
		return Check{
			Name: checkGitLabComponentJobs, Severity: SeverityFail,
			Message:     "forge-ci components in .gitlab-ci.yml define the same job, which GitLab merges into one: " + strings.Join(clashes, "; "),
			Remediation: "give each include of the same component its own job-name input",
		}, true
	}

	return Check{
		Name: checkGitLabComponentJobs, Severity: SeverityOK,
		Message: "each forge-ci component in .gitlab-ci.yml defines its own job name",
	}, true
}

// gitLabIncludes is the include entries of a pipeline: one entry or a list.
func gitLabIncludes(include *yaml.Node) []*yaml.Node {
	if include == nil {
		return nil
	}

	if include.Kind == yaml.SequenceNode {
		return include.Content
	}

	return []*yaml.Node{include}
}

// forgeCIComponentJob is the job name an include of a forge-ci component
// defines, and the component's name. An include of anything else, or one
// whose job name is an expression GitLab evaluates, is not one.
func forgeCIComponentJob(include *yaml.Node, repoName string) (string, string, bool) {
	reference := yamlChild(include, "component")
	if reference == nil || reference.Kind != yaml.ScalarNode {
		return "", "", false
	}

	// <server>/<project path>/<component>@<pin>
	location, _, _ := strings.Cut(reference.Value, "@")
	project, component := path.Split(location)

	if path.Base(strings.TrimSuffix(project, "/")) != repoName || component == "" {
		return "", "", false
	}

	job := component

	if name := yamlChild(yamlChild(include, "inputs"), "job-name"); name != nil {
		if name.Kind != yaml.ScalarNode || strings.ContainsAny(name.Value, "$") {
			return "", "", false
		}

		job = name.Value
	}

	return job, component, true
}
