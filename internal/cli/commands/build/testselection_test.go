// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/diggsweden/forge-ci/internal/cli"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestReleaseCLIRejectsInvalidSelectionsBeforeNativeExecution(t *testing.T) {
	for _, tc := range []struct{ ecosystem, selection string }{
		{"go", `{"packages":["./..."],"gate":"policy","run":null}`},
		{"go", `{"packages":["./..."]}`},
		{"go", `{"packages":["./..."],"gate":"optional"}`},
		{"cargo", `{"runs":[{"kind":"package-test","package":"app","name":"guard","gate":"policy"}],"runſ":[{"kind":"package-bin","package":"app","name":"main","gate":"application"}]}`},
	} {
		t.Run(tc.ecosystem+tc.selection, func(t *testing.T) {
			var out, stderr bytes.Buffer

			cmd := cli.New(cli.BuildInfo{})
			cmd.Writer, cmd.ErrWriter = &out, &stderr

			err := cmd.Run(t.Context(), []string{"forge-ci", "--provider", "local", "--runner", "local", "build", tc.ecosystem, "run", "--working-dir", t.TempDir(), "--test-selection-json", tc.selection})
			if !errors.Is(err, errs.ErrUsage) || out.Len() != 0 {
				t.Fatalf("selection not refused before build: %v %s", err, &out)
			}
		})
	}
}
