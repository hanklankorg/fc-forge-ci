// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package doctor

import (
	"bytes"
	"strings"
	"testing"

	appdoctor "github.com/diggsweden/forge-ci/internal/app/doctor"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// TestWriteTextReport_EndsWithOneNextLine: the human report ends with exactly
// one "Next:" line, chosen by what the checks found: a failure to fix, a
// warning to review, or nothing in the way of a pull request. The scope line
// and the GitLab note keep their place above it. Inside a CI job only the
// failure case keeps its Next: line; the pull-request advice is for a person.
func TestWriteTextReport_EndsWithOneNextLine(t *testing.T) {
	t.Parallel()

	ok := appdoctor.Check{Name: "a", Severity: appdoctor.SeverityOK, Message: "fine"}
	warn := appdoctor.Check{Name: "b", Severity: appdoctor.SeverityWarn, Message: "advisory", Remediation: "later"}
	fail := appdoctor.Check{Name: "c", Severity: appdoctor.SeverityFail, Message: "broken", Remediation: "now"}

	local := string(provider.RunnerLocal)
	ci := string(provider.RunnerGitHub)

	for name, tc := range map[string]struct {
		checks   []appdoctor.Check
		runner   string
		forgeAPI string
		wantNext string // "" means no Next: line at all
		wantNote bool
	}{
		"all ok":            {checks: []appdoctor.Check{ok}, runner: local, wantNext: "Next: commit the Forge CI files"},
		"a warning remains": {checks: []appdoctor.Check{ok, warn}, runner: local, wantNext: "Next: review the WARN lines above"},
		"a failure wins":    {checks: []appdoctor.Check{warn, fail}, runner: local, wantNext: "Next: fix the FAIL lines above"},
		"gitlab keeps its note": {
			checks: []appdoctor.Check{ok}, runner: local, forgeAPI: string(provider.ForgeGitLab),
			wantNext: "Next: commit the Forge CI files", wantNote: true,
		},
		"a passing CI job gets no pull-request advice": {checks: []appdoctor.Check{ok, warn}, runner: ci, wantNext: ""},
		"a failing CI job still says what to fix":      {checks: []appdoctor.Check{ok, fail}, runner: ci, wantNext: "Next: fix the FAIL lines above"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer

			writeTextReport(&out, appdoctor.Environment{Runner: tc.runner, ForgeAPI: tc.forgeAPI}, tc.checks, appdoctor.CountFailures(tc.checks))

			lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
			last := lines[len(lines)-1]

			wantCount := 1
			if tc.wantNext == "" {
				wantCount = 0
			} else if !strings.HasPrefix(last, tc.wantNext) {
				t.Fatalf("last line = %q, want prefix %q\n%s", last, tc.wantNext, out.String())
			}

			if n := strings.Count(out.String(), "\nNext: "); n != wantCount {
				t.Errorf("want %d Next: line(s), got %d\n%s", wantCount, n, out.String())
			}

			if !strings.Contains(out.String(), "Scope: local configuration checks only") {
				t.Errorf("scope line missing\n%s", out.String())
			}

			if got := strings.Contains(out.String(), "GitLab include pins"); got != tc.wantNote {
				t.Errorf("GitLab note present = %v, want %v\n%s", got, tc.wantNote, out.String())
			}
		})
	}
}
