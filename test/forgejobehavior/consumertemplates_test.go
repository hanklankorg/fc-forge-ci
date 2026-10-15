// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// consumerReleaseTemplates are the release workflows of the consumer kits,
// one per ecosystem example the Forgejo layer builds. test/forgejoguard holds
// the list to every examples/**/forgejo/ pair.
func consumerReleaseTemplates() []string {
	return []string{
		"examples/go-cli/forgejo/release.yml",
		"examples/go-service/forgejo/release.yml",
		"examples/cargo-app/forgejo/release.yml",
	}
}

// consumerTemplates are the consumer kits' workflows, with the placeholders a
// consumer replaces: the forge host, the owner and the forge-ci pin.
func consumerTemplates() []string {
	templates := consumerReleaseTemplates()
	for _, release := range consumerReleaseTemplates() {
		templates = append(templates, strings.TrimSuffix(release, "release.yml")+"pullrequest.yml")
	}

	return templates
}

// Every action and reusable workflow of the layer the consumer kit references
// exists in this checkout, and the kit's signer job calls the sign-and-publish
// workflow, so a consumer copying the kit gets calls that resolve. The prepare
// job's target is covered by TestPrepareReleaseInputWiring.
func TestConsumerTemplateReferencesResolve(t *testing.T) {
	t.Parallel()
	root := reporoot.Path(t)
	reference := regexp.MustCompile(`(?m)^\s*(?:- )?uses: (?:https://FORGE_HOST/)?OWNER/forge-ci/(\.forgejo/(?:actions/[a-z0-9-]+|workflows/[a-z0-9-]+\.yml))@PIN_FORGE_CI_SHA$`)
	found := map[string]bool{}

	for _, template := range consumerTemplates() {
		for _, match := range reference.FindAllStringSubmatch(string(reporoot.ReadFile(t, template)), -1) {
			found[match[1]] = true
		}
	}

	actions, workflows := 0, 0

	for target := range found {
		path := filepath.Join(root, filepath.FromSlash(target))
		if strings.HasPrefix(target, ".forgejo/actions/") {
			actions++
			path = filepath.Join(path, "action.yml")
		} else {
			workflows++
		}

		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			t.Errorf("the consumer kit references %s, which this checkout lacks: %v", target, err)
		}
	}

	if actions == 0 || workflows == 0 {
		t.Fatalf("found %d action and %d workflow references; the extraction has drifted", actions, workflows)
	}

	// Every `uses:` of the kit is one of those references: the kit names no
	// other repository, and no reference escapes the placeholders.
	uses := regexp.MustCompile(`(?m)^\s*(?:- )?uses:.*$`)

	for _, template := range consumerTemplates() {
		for _, line := range uses.FindAllString(string(reporoot.ReadFile(t, template)), -1) {
			if !reference.MatchString(line) {
				t.Errorf("%s: %q is not a placeholder reference to this layer", template, strings.TrimSpace(line))
			}
		}
	}

	for _, template := range consumerReleaseTemplates() {
		var release struct {
			Jobs map[string]struct {
				Uses string `yaml:"uses"`
			} `yaml:"jobs"`
		}
		loadYAML(t, template, &release)

		if uses := release.Jobs["sign-and-publish"].Uses; !strings.Contains(uses, "/.forgejo/workflows/sign-and-publish-release.yml@") {
			t.Errorf("%s: the sign-and-publish job calls %q, not the sign-and-publish workflow", template, uses)
		}
	}
}
