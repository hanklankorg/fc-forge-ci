// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package evidence

import (
	"os"
	"os/exec"
	"testing"
)

// UnderUmask reruns named regression tests in a fresh process. The parent test
// process (including parallel tests) never changes its umask. The pattern must
// exclude the calling wrapper test.
func UnderUmask(t *testing.T, pattern string) {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", `umask 077; exec "$@"`, "umask-regression", bin, "-test.run", pattern, "-test.count=1")
	var diagnostic Diagnostic
	cmd.Stdout, cmd.Stderr = &diagnostic, &diagnostic
	if err := cmd.Run(); err != nil {
		t.Fatalf("restrictive-umask regressions: %v\n%s", err, diagnostic.Text(os.Environ()))
	}
}
