// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package testenv_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

func TestNew_IsolatesAndProvidesTempPaths(t *testing.T) {
	env := testenv.New(t)
	if env.Home == "" || env.Temp == "" {
		t.Fatalf("env = %+v", env)
	}

	if got := os.Getenv("HOME"); got != env.Home {
		t.Errorf("HOME = %q, want %q", got, env.Home)
	}

	if got := os.Getenv("TMPDIR"); got != env.Temp {
		t.Errorf("TMPDIR = %q, want %q", got, env.Temp)
	}

	if filepath.Dir(env.Path("x", "y")) != filepath.Join(env.Temp, "x") {
		t.Errorf("unexpected derived path: %q", env.Path("x", "y"))
	}
}
