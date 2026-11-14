// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"bytes"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"testing"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// coreTags are the only explicit tags strict YAML may carry.
func coreTags() []string {
	return []string{"!!map", "!!seq", "!!str", "!!bool", "!!int", "!!float", "!!null"}
}

// strictVisitor records the first construct strict YAML forbids.
type strictVisitor struct{ err error }

func (v *strictVisitor) Visit(node ast.Node) ast.Visitor {
	switch typed := node.(type) {
	case *ast.AliasNode:
		v.err = fmt.Errorf("%w: alias %s", errRule, typed.String())
	case *ast.TagNode:
		if !slices.Contains(coreTags(), typed.Start.Value) {
			v.err = fmt.Errorf("%w: custom tag %s", errRule, typed.Start.Value)
		}
	case *ast.MappingValueNode:
		if _, ok := typed.Key.(*ast.StringNode); !ok {
			v.err = fmt.Errorf("%w: non-string mapping key %s", errRule, typed.Key.String())
		}
	}

	if v.err != nil {
		return nil
	}

	return v
}

// strictYAML accepts exactly one non-null document with no aliases, no
// non-core tags, and only unique string mapping keys. The parser itself
// rejects duplicate keys.
func strictYAML(data []byte) error {
	parsed, err := parser.ParseBytes(data, 0)
	if err != nil {
		return err
	}

	if len(parsed.Docs) != 1 || parsed.Docs[0].Body == nil {
		return fmt.Errorf("%w: not exactly one document", errRule)
	}

	switch parsed.Docs[0].Body.(type) {
	case *ast.NullNode, *ast.CommentGroupNode:
		return fmt.Errorf("%w: null document", errRule)
	}

	visitor := &strictVisitor{}
	ast.Walk(visitor, parsed.Docs[0].Body)

	return visitor.err
}

// eachLayerYAMLFile calls visit with every YAML file of the Forgejo layer and
// of its consumer examples, and fails when there is none.
func eachLayerYAMLFile(t *testing.T, visit func(relative string, data []byte)) {
	t.Helper()

	root := reporoot.Path(t)
	files := 0

	for _, tree := range append([]string{".forgejo"}, forgejoExampleTrees()...) {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, entry fs.DirEntry, err error) error {
			if err != nil || !entry.Type().IsRegular() {
				return err
			}

			if extension := filepath.Ext(path); extension != ".yml" && extension != ".yaml" {
				return nil
			}

			files++

			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}

			visit(relative, reporoot.ReadFile(t, relative))

			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	if files == 0 {
		t.Error("found no YAML files")
	}
}

// Every YAML file of the Forgejo layer and of its consumer examples is strict,
// so no workflow or action can depend on parser-specific handling of ambiguous
// YAML. The GitHub and GitLab layers are outside this rule: they use anchors.
func TestEveryLayerYAMLFileIsStrict(t *testing.T) {
	t.Parallel()

	eachLayerYAMLFile(t, func(relative string, data []byte) {
		if err := strictYAML(data); err != nil {
			t.Errorf("%s is not strict YAML: %v", relative, err)
		}
	})
}

// yamlfmt keeps blank lines by writing a marker comment in their place and
// taking it out again after formatting. Inside a folded scalar the marker is
// text, so it stays, and a description then ends in it for every reader of the
// action. No file carries one.
func TestNoLayerYAMLFileCarriesAFormatterMarker(t *testing.T) {
	t.Parallel()

	eachLayerYAMLFile(t, func(relative string, data []byte) {
		if bytes.Contains(data, []byte("#magic___")) {
			t.Errorf("%s carries yamlfmt's line-break marker `#magic___^_^___line`; remove it from the text", relative)
		}
	})
}

func TestStrictYAMLRejections(t *testing.T) {
	t.Parallel()

	for name, document := range map[string]string{
		"empty":            "",
		"comment only":     "# nothing\n",
		"null":             "null\n",
		"duplicate key":    "root:\n  key: one\n  key: two\n",
		"quoted duplicate": "key: one\n\"key\": two\n",
		"alias":            "root: &value one\nalias: *value\n",
		"merge key":        "base: &base {a: 1}\nderived:\n  <<: *base\n",
		"custom tag":       "root: !custom {key: value}\n",
		"pseudo-core tag":  "root: !!custom {key: value}\n",
		"non-string key":   "1: value\n",
		"null key":         "~: value\n",
		"multi-document":   "one: 1\n---\ntwo: 2\n",
		"malformed":        "root: [one\n",
	} {
		if strictYAML([]byte(document)) == nil {
			t.Errorf("strict YAML accepted %s: %q", name, document)
		}
	}

	for _, document := range []string{
		"first:\n  key: one\nsecond:\n  key: two\n",
		"on:\n  push:\nvalue: !!str 1\n",
	} {
		if err := strictYAML([]byte(document)); err != nil {
			t.Errorf("strict YAML rejected %q: %v", document, err)
		}
	}
}
