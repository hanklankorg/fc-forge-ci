// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate_test

import (
	"io"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	appvalidate "github.com/diggsweden/forge-ci/internal/app/validate"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
)

func TestIsolationChecksInputsAgainstTheSuppliedLayer(t *testing.T) {
	t.Parallel()

	const pin = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	caller := `jobs:
  build:
    steps:
      - uses: https://forge.example/team/engine/.forgejo/actions/tool@` + pin + `
        with: {message: hello}
  prepare:
    uses: team/engine/.forgejo/workflows/prepare.yml@` + pin + `
    with: {version: v1.2.3}
`

	for _, scenario := range []struct {
		name string
		edit func(fstest.MapFS)
		want string
	}{
		{name: "declared inputs"},
		{name: "unknown action input", want: "unknown-message", edit: func(tree fstest.MapFS) {
			tree["caller/release.yml"].Data = []byte(strings.ReplaceAll(caller, "message: hello", "unknown-message: hello"))
		}},
		{name: "unknown workflow input", want: "unknown-version", edit: func(tree fstest.MapFS) {
			tree["caller/release.yml"].Data = []byte(strings.ReplaceAll(caller, "version: v1.2.3", "unknown-version: v1.2.3"))
		}},
		{name: "missing declaration fails closed", want: "read pinned declaration", edit: func(tree fstest.MapFS) {
			delete(tree, "layer/.forgejo/actions/tool/action.yml")
		}},
		{name: "consumer cannot supply the declaration", want: "message", edit: func(tree fstest.MapFS) {
			tree["caller/.forgejo/actions/tool/action.yml"] = &fstest.MapFile{Data: []byte("inputs: {message: {}}")}
			tree["layer/.forgejo/actions/tool/action.yml"].Data = []byte("inputs: {different: {}}")
		}},
		{name: "path traversal refused", want: "no supported pinned", edit: func(tree fstest.MapFS) {
			tree["caller/release.yml"].Data = []byte(strings.ReplaceAll(caller, "actions/tool@", "actions/../tool@"))
		}},
		{name: "ambiguous ref refused", want: "no supported pinned", edit: func(tree fstest.MapFS) {
			tree["caller/release.yml"].Data = []byte(strings.ReplaceAll(caller, "actions/tool@", "actions/tool@main@"))
		}},
		{name: "inputless workflow", edit: func(tree fstest.MapFS) {
			tree["caller/release.yml"].Data = []byte(strings.ReplaceAll(caller, "    with: {version: v1.2.3}\n", ""))
			tree["layer/.forgejo/workflows/prepare.yml"].Data = []byte("on:\n  workflow_call:\n")
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()

			tree := fstest.MapFS{
				"caller/release.yml":                     {Data: []byte(caller)},
				"layer/.forgejo/actions/tool/action.yml": {Data: []byte("inputs: {message: {}}")},
				"layer/.forgejo/workflows/prepare.yml":   {Data: []byte("on:\n  workflow_call:\n    inputs: {version: {type: string}}\n")},
			}
			if scenario.edit != nil {
				scenario.edit(tree)
			}

			var diagnostics strings.Builder

			err := appvalidate.Isolation(io.Discard, output.NewAnnotator(&diagnostics, output.FormatText), appvalidate.IsolationInput{
				Workflow: "caller/release.yml", BuildJob: "build", SinglePinSubject: "team/engine", ContractRoot: "layer", FS: tree,
			})
			if scenario.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, errs.ErrValidation)
				require.Contains(t, diagnostics.String(), scenario.want)
			}
		})
	}
}
