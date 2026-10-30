// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package evidence

import (
	"strings"
	"testing"
)

func TestDiagnosticRedactionAndBound(t *testing.T) {
	var d Diagnostic
	_, _ = d.Write([]byte("npm failed: credential-canary"))
	if got := d.Text([]string{"NPM_TOKEN=credential-canary"}); strings.Contains(got, "credential-canary") || !strings.Contains(got, "npm failed") {
		t.Fatalf("unsafe or missing diagnostic: %q", got)
	}
	_, _ = d.Write([]byte(" BEGIN PRIVATE KEY"))
	if got := d.Text(nil); !strings.Contains(got, "redacted") || strings.Contains(got, "npm failed") {
		t.Fatalf("key output not redacted: %q", got)
	}
	_, _ = d.Write([]byte(strings.Repeat("x", 17<<10)))
	if got := d.Text(nil); !strings.Contains(got, "exceeded") {
		t.Fatalf("unbounded output: %q", got)
	}
}
