// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	domaingit "github.com/diggsweden/forge-ci/internal/domain/git"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// supplyChainViolations reads a workflow or a composite action: jobs carry
// the uses of a workflow, runs.steps those of an action.
func supplyChainViolations(body []byte) []string {
	var workflow struct {
		eventWorkflow `yaml:",inline"`

		Runs struct {
			Steps []eventStep `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		return []string{err.Error()}
	}

	var failures []string
	if workflow.Permissions.Value == "write-all" {
		failures = append(failures, "workflow write-all permissions are forbidden")
	}

	var refs []string
	for _, step := range workflow.Runs.Steps {
		refs = append(refs, step.Uses)
	}

	for _, job := range workflow.Jobs {
		if job.Permissions.Value == "write-all" {
			failures = append(failures, "job write-all permissions are forbidden")
		}

		refs = append(refs, job.Uses)
		for _, step := range job.Steps {
			refs = append(refs, step.Uses)
		}
	}

	for _, ref := range refs {
		if ref == "" || strings.HasPrefix(ref, "./") {
			continue
		}

		if after, ok := strings.CutPrefix(ref, "docker://"); ok {
			if !domaincontainer.ValidDigestReference(after) {
				failures = append(failures, "unpinned container action: "+ref)
			}

			continue
		}

		_, revision, ok := strings.Cut(ref, "@")
		if !ok || !domaingit.ValidCommitSHA(revision) {
			failures = append(failures, "unpinned external uses: "+ref)
		}
	}

	return failures
}

func TestExternalWorkflowReferencesAreImmutable(t *testing.T) {
	t.Parallel()
	root := reporoot.Path(t)

	var paths []string

	for _, tree := range []string{
		filepath.Join(root, ".github", "workflows"),
		filepath.Join(root, ".forgejo", "workflows"),
		filepath.Join(root, ".forgejo", "actions"),
	} {
		err := filepath.WalkDir(tree, func(path string, entry fs.DirEntry, err error) error {
			if err == nil && !entry.IsDir() && (filepath.Ext(path) == ".yml" || filepath.Ext(path) == ".yaml") {
				paths = append(paths, path)
			}

			return err
		})
		require.NoError(t, err)
	}

	require.NotEmpty(t, paths)

	for _, path := range paths {
		body, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Empty(t, supplyChainViolations(body), path)
	}
}
