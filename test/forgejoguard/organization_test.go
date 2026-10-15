// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/goccy/go-yaml/ast"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// organizationName matches the organizations and the forge this layer came
// from or is published by. None of them may be something a fork has to edit.
var organizationName = regexp.MustCompile(`(?i)itiquette|codefloe|diggsweden`)

// organizationMentions returns the path of every key or scalar beneath node
// that names an organization or its forge. Comments are not part of the tree.
func organizationMentions(node ast.Node, path []string) [][]string {
	var found [][]string

	switch typed := node.(type) {
	case *ast.MappingNode, *ast.MappingValueNode:
		for _, entry := range mappingEntries(typed) {
			key := scalarString(entry.Key)
			if organizationName.MatchString(key) {
				found = append(found, append(slices.Clone(path), key))
			}

			found = append(found, organizationMentions(entry.Value, append(slices.Clone(path), key))...)
		}
	case *ast.SequenceNode:
		for _, item := range typed.Values {
			found = append(found, organizationMentions(item, append(slices.Clone(path), "[]"))...)
		}
	case nil:
	default:
		if organizationName.MatchString(scalarString(node)) {
			found = append(found, path)
		}
	}

	return found
}

// layerActions lists the composite actions as repository-relative paths.
func layerActions(t *testing.T) []string {
	t.Helper()

	var paths []string

	for _, entry := range reporoot.ReadDir(t, ".forgejo/actions") {
		if entry.IsDir() {
			paths = append(paths, ".forgejo/actions/"+entry.Name()+"/action.yml")
		}
	}

	if len(paths) == 0 {
		t.Fatal("found no Forgejo action to inspect")
	}

	return paths
}

// The reusable workflows and the actions are served from any organization's
// copy of this repository. They name no organization and no forge, anywhere in
// the document: a name in a default, a step, a script, a label or a
// description would follow the files into a fork.
func TestLayerNamesNoOrganization(t *testing.T) {
	t.Parallel()

	for _, path := range append(layerWorkflows(t), layerActions(t)...) {
		for _, mention := range organizationMentions(loadDocument(t, path), nil) {
			t.Errorf("%s names an organization at %s", path, strings.Join(mention, "."))
		}
	}
}

// The walk above finds a name wherever a document can carry one.
func TestOrganizationMentionsReachKeysScalarsAndSequences(t *testing.T) {
	t.Parallel()

	document, err := parseDocument([]byte("inputs:\n  x:\n    default: Itiquette/r\njobs:\n  codefloe-job:\n    steps:\n      - run: clone diggsweden/r\n"))
	if err != nil {
		t.Fatal(err)
	}

	found := organizationMentions(document, nil)
	got := make([]string, 0, len(found))

	for _, mention := range found {
		got = append(got, strings.Join(mention, "."))
	}

	want := []string{"inputs.x.default", "jobs.codefloe-job", "jobs.codefloe-job.steps.[].run"}
	if !slices.Equal(got, want) {
		t.Fatalf("mentions %q, want %q", got, want)
	}
}

// The helper repository has two sources, the caller's input and then the
// caller's variable, and no default: on a Forgejo instance a built-in owner
// names a repository that is not there. With neither, the preflight stops
// with a message that names both, before any checkout.
func TestHelperRepositoryIsTheCallersInputOrVariable(t *testing.T) {
	t.Parallel()

	const want = "${{ inputs['forge-ci-repository'] || vars.FORGE_CI_REPOSITORY }}"

	for _, path := range layerWorkflows(t) {
		step, ok := loadWorkflow(t, path).jobs["coordinates"].stepByID("validate")
		if !ok {
			t.Errorf("%s: coordinates job has no validate step", path)

			continue
		}

		got := strings.Join(strings.Fields(scalarString(mappingValue(mappingValue(step, "env"), "HELPERS_REPOSITORY_INPUT"))), " ")
		if got != want {
			t.Errorf("%s resolves the helper repository as %q, want %q", path, got, want)
		}

		accepted, output, stderr := runPreflightWithStderr(t, preflightScript(t, path), "", exampleSigner)
		if accepted || output != "" {
			t.Errorf("%s: an unnamed helper repository: accepted=%t output %q", path, accepted, output)
		}

		for _, source := range []string{"forge-ci-repository input", "FORGE_CI_REPOSITORY variable"} {
			if !strings.Contains(stderr, source) {
				t.Errorf("%s: the refusal does not name the %s: %q", path, source, stderr)
			}
		}
	}
}

// No line of any file of the layer, of the signer image definition or of the
// consumer examples names an organization, comments included: only the
// copyright line may name its holder.
func TestLayerFilesNameNoOrganization(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)
	files := 0

	for _, tree := range append([]string{".forgejo", "containers/signer"}, forgejoExampleTrees()...) {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, entry fs.DirEntry, err error) error {
			if err != nil || !entry.Type().IsRegular() {
				return err
			}

			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}

			// A downloaded or locally built binary, which Git ignores, embeds its
			// module path.
			if relative == filepath.Join(".forgejo", "bin", "forge-ci") {
				return nil
			}

			files++

			for number, line := range strings.Split(string(reporoot.ReadFile(t, relative)), "\n") {
				if strings.Contains(line, "SPDX-FileCopyrightText:") {
					continue
				}

				if organizationName.MatchString(line) {
					t.Errorf("%s:%d names an organization: %s", filepath.ToSlash(relative), number+1, strings.TrimSpace(line))
				}
			}

			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	if files == 0 {
		t.Fatal("found no layer file to inspect")
	}
}

// The release example names the serving repository as a literal in every call
// of a layer workflow: the owner and repository of the `uses:` line beside it.
// A consumer who copies it depends on no variable to find the layer.
func TestConsumerReleaseTemplateNamesTheServingRepository(t *testing.T) {
	t.Parallel()

	for _, kit := range consumerKits() {
		calls := 0

		for name, target := range loadWorkflow(t, kit.release()).jobs {
			uses := scalarString(mappingValue(target.node, "uses"))
			if uses == "" {
				continue
			}

			calls++

			repository, _, found := strings.Cut(uses, "/.forgejo/workflows/")
			if got := scalarString(mappingValue(mappingValue(target.node, "with"), "forge-ci-repository")); !found || got != repository {
				t.Errorf("%s job %s calls %s and passes forge-ci-repository %q, want the literal %q", kit.release(), name, uses, got, repository)
			}
		}

		if calls == 0 {
			t.Errorf("%s calls no layer workflow, so the check proves nothing", kit.release())
		}
	}
}
