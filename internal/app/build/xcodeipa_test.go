// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	"path/filepath"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

func xcodeExportFixture(t *testing.T) *testfs.Real {
	t.Helper()
	fsys := testfs.NewReal(t)

	root, err := filepath.EvalSymlinks(fsys.Root)
	if err != nil {
		t.Fatal(err)
	}

	fsys.Root = root
	for _, key := range []string{"TMPDIR", "TEMP", "TMP"} {
		t.Setenv(key, fsys.MkdirAll(key))
	}

	return fsys
}

// TestXcodeReleaseBuild_MissingExportOptionsNamesItsVariable covers the
// refusal. The error names the env var the operator has to set, taken from the
// caller rather than hard-coded, so a workflow using its own name gets told
// that name.
func TestXcodeReleaseBuild_MissingExportOptionsNamesItsVariable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		varName string
		want    string
	}{
		{
			name:    "caller-supplied name",
			varName: "IOS_EXPORT_OPTIONS",
			want:    "export options not found in variable IOS_EXPORT_OPTIONS",
		},
		{
			name: "no name falls back to the documented default",
			want: "export options not found in variable EXPORT_OPTIONS_BASE64",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fsys, in := xcodeReleaseFixture(t)
			in.ExportOptionsBase64, in.ExportOptionsVar = "", tc.varName

			assertXcodePreflightRefusal(t, fsys, in, errs.ErrMissingInput, tc.want)
		})
	}
}
