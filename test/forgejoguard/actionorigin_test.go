// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"gopkg.in/yaml.v3"
)

func externalActionOriginValid(action string) bool {
	for _, name := range []string{"extractions/setup-just", "extractions/setup-crate"} {
		if strings.Contains(action, name) {
			return regexp.MustCompile(`^https://github\.com/` + regexp.QuoteMeta(name) + `@[0-9a-f]{40}$`).MatchString(action)
		}
	}
	return true
}

func TestExternalActionOriginGuard(t *testing.T) {
	for action, want := range map[string]bool{
		"extractions/setup-just@v3":                                                             false,
		"extractions/setup-crate@53165ef7e734c5c07cb06b3c8e7b647c5aa16db3":                      false,
		"https://forge.example/extractions/setup-just@53165ef7e734c5c07cb06b3c8e7b647c5aa16db3": false,
		"https://github.com/extractions/setup-just@v3":                                          false,
		"https://github.com/extractions/setup-just@53165ef7e734c5c07cb06b3c8e7b647c5aa16db3":    true,
	} {
		if got := externalActionOriginValid(action); got != want {
			t.Errorf("origin guard(%q) = %v, want %v", action, got, want)
		}
	}
}

func TestNestedNativeExternalActionOrigins(t *testing.T) {
	root := filepath.Join(reporoot.Path(t), ".forgejo", "actions")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || (entry.Name() != "action.yml" && entry.Name() != "action.yaml") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var document yaml.Node
		if err := yaml.Unmarshal(body, &document); err != nil {
			return err
		}
		var visit func(*yaml.Node)
		visit = func(node *yaml.Node) {
			if node.Kind == yaml.MappingNode {
				for i := 0; i+1 < len(node.Content); i += 2 {
					if node.Content[i].Value == "uses" && !externalActionOriginValid(node.Content[i+1].Value) {
						t.Errorf("%s:%d: external action requires explicit GitHub origin and full SHA: %s", path, node.Content[i+1].Line, node.Content[i+1].Value)
					}
				}
			}
			for _, child := range node.Content {
				visit(child)
			}
		}
		visit(&document)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
