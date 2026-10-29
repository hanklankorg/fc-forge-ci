// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml/ast"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// Forgejo ignores GitHub's permissions key. No current workflow requests OIDC;
// enabling it needs a separate review of the issuer and the consuming service.
func forgejoAuthorizationIssues(root ast.Node) []string {
	var issues []string

	check := func(scope string, node ast.Node) {
		if mappingValue(node, "permissions") != nil {
			issues = append(issues, scope+": Forgejo does not enforce permissions")
		}

		if oidc := mappingValue(node, "enable-openid-connect"); oidc != nil && scalarString(oidc) != "false" {
			issues = append(issues, scope+": OIDC enablement needs explicit review")
		}
	}

	check("workflow", root)

	for _, entry := range mappingEntries(mappingValue(root, "jobs")) {
		check("jobs."+scalarString(entry.Key), entry.Value)
	}

	return issues
}

func TestForgejoAuthorization(t *testing.T) {
	t.Parallel()

	check := func(relative string, body []byte) {
		root, err := parseDocument(body)
		if err != nil {
			t.Fatalf("%s: %v", relative, err)
		}

		for _, issue := range forgejoAuthorizationIssues(root) {
			t.Errorf("%s: %s", relative, issue)
		}
	}
	root := reporoot.Path(t)
	// Discover standalone examples as well as release kits and live callers.
	// GitHub workflows in the same trees retain their native permission model.
	for _, tree := range []string{".forgejo/workflows", "examples", "test/live/demos"} {
		checked := 0

		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, entry fs.DirEntry, err error) error {
			if err != nil || !entry.Type().IsRegular() {
				return err
			}

			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}

			relative = filepath.ToSlash(relative)
			if forgejoAuthorizationWorkflow(relative) {
				checked++

				check(relative, reporoot.ReadFile(t, relative))
			}

			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		if checked == 0 {
			t.Errorf("found no Forgejo workflows in %s", tree)
		}
	}
}

func forgejoAuthorizationWorkflow(relative string) bool {
	if extension := filepath.Ext(relative); extension != ".yml" && extension != ".yaml" {
		return false
	}

	return strings.HasPrefix(relative, ".forgejo/workflows/") ||
		strings.Contains(relative, "/.forgejo/workflows/") ||
		(strings.HasPrefix(relative, "examples/") && strings.Contains(relative, "/forgejo/"))
}

func TestForgejoAuthorizationWorkflowSelection(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		path string
		want bool
	}{
		{".forgejo/workflows/release.yml", true},
		{"examples/forgejo/signer-image.yaml", true},
		{"examples/new-kit/forgejo/snapshot.yml", true},
		{"test/live/demos/app/consumer/.forgejo/workflows/release.yml", true},
		{".github/workflows/release.yml", false},
		{"examples/new-kit/github/release.yml", false},
		{"test/live/demos/app/consumer/test/live/github/workflows/release.yml", false},
		{".forgejo/actions/build/action.yml", false},
		{"examples/new-kit/forgejo/README.md", false},
	} {
		if got := forgejoAuthorizationWorkflow(test.path); got != test.want {
			t.Errorf("workflow selection for %s = %v, want %v", test.path, got, test.want)
		}
	}
}

func TestForgejoAuthorizationScopes(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		yaml string
		want int
	}{
		{"no grants", "on: push\njobs: {build: {runs-on: docker}}", 0},
		{"workflow permissions", "permissions: {contents: read}\njobs: {}", 1},
		{"job permissions", "jobs: {build: {permissions: {}}}", 1},
		{"both scopes", "permissions: {}\njobs: {build: {'permissions': {contents: read}}}", 2},
		{"workflow oidc", "enable-openid-connect: true\njobs: {}", 1},
		{"job oidc expression", "jobs: {sign: {enable-openid-connect: '${{ inputs.oidc }}'}}", 1},
		{"oidc disabled", "enable-openid-connect: false\njobs: {build: {enable-openid-connect: false}}", 0},
		{"data is not authority", "on: {workflow_call: {inputs: {permissions: {type: string}}}}\njobs: {build: {steps: [{run: 'echo permissions', env: {permissions: read}}]}}", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			root, err := parseDocument([]byte(test.yaml))
			if err != nil {
				t.Fatal(err)
			}

			if issues := forgejoAuthorizationIssues(root); len(issues) != test.want {
				t.Fatalf("issues = %v, want %d", issues, test.want)
			}
		})
	}
}
