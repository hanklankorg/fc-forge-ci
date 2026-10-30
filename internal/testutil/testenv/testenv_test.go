// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package testenv_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

func TestNew_GoSubprocessDoesNotCreateTelemetry(t *testing.T) {
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	ambientTelemetry := t.TempDir()
	t.Setenv("TEST_TELEMETRY_DIR", ambientTelemetry)
	t.Run("isolated", func(t *testing.T) {
		env := testenv.New(t)
		cmd := exec.CommandContext(t.Context(), goBinary, "env", "GOTELEMETRYDIR")
		cmd.Env = append(os.Environ(), "GOENV=off", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off")
		body, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go env: %v\n%s", err, body)
		}
		if got := strings.TrimSpace(string(body)); got != os.DevNull {
			t.Fatalf("Go subprocess telemetry directory = %q, want %q", got, os.DevNull)
		}
		if _, err := os.Stat(filepath.Join(env.Home, ".config", "go", "telemetry")); !os.IsNotExist(err) {
			t.Fatalf("Go subprocess left telemetry state: %v", err)
		}
	})
	if got := os.Getenv("TEST_TELEMETRY_DIR"); got != ambientTelemetry {
		t.Fatalf("caller telemetry setting was not restored: %q", got)
	}
	if entries, err := os.ReadDir(ambientTelemetry); err != nil || len(entries) != 0 {
		t.Fatalf("Go subprocess contaminated ambient telemetry directory: %v, %v", entries, err)
	}
}

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
