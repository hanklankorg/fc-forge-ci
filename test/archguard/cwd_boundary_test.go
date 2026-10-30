// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package archguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/stretchr/testify/require"
)

func TestGuardWorkingDirectoryBoundary_CompiledTestsRunFromRoot(t *testing.T) {
	t.Parallel()

	binary, err := os.Executable()
	require.NoError(t, err)
	// Resolve the effective default before replacing HOME. getenv alone loses
	// the module cache when neither GOMODCACHE nor GOPATH was explicitly set.
	moduleCache := guardModuleCache(t, os.Environ())
	require.DirExists(t, moduleCache)
	cmd := exec.CommandContext(t.Context(), binary, "-test.run=^Test(ExecAdaptersClassifyStartFailures|OnlyCompositionRootMintsOperatorCredentials|LayersDoNotNameRunContextVars|ForgeAwareCommandsHaveALiveScenario)$")
	root := t.TempDir()
	goRoot := guardGoRoot(t)
	cmd.Dir = reporoot.Path(t)
	cmd.Env = []string{
		"PATH=/usr/bin:/bin", "HOME=" + root, "TMPDIR=" + root,
		"GOROOT=" + goRoot, "GOMODCACHE=" + moduleCache,
		"GOCACHE=" + filepath.Join(root, "go-build"), "CGO_ENABLED=0", "GOENV=off",
		"GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "TEST_TELEMETRY_DIR=" + os.DevNull,
	}
	body, err := cmd.CombinedOutput()
	require.NoError(t, err, string(body))
	t.Logf("compiled guards passed from repository root with fresh HOME/GOCACHE and effective GOMODCACHE=%s", moduleCache)
}

// guardGoRoot returns the GOROOT of the toolchain running these tests, so
// metadata and export data never come from a different Go. An explicit GOROOT
// wins, as the compiled-guard child is started with one and a bare PATH.
// Otherwise go test has put its own GOROOT/bin first on PATH, so the go
// command found there is the running toolchain.
func guardGoRoot(t *testing.T) string {
	t.Helper()

	if goRoot := os.Getenv("GOROOT"); goRoot != "" {
		return goRoot
	}

	cmd := exec.CommandContext(t.Context(), "go", "env", "GOROOT")
	cmd.Dir = reporoot.Path(t)

	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "TEST_TELEMETRY_DIR="+os.DevNull)

	body, err := cmd.CombinedOutput()
	require.NoError(t, err, string(body))
	goRoot := strings.TrimSpace(string(body))
	require.True(t, filepath.IsAbs(goRoot), "GOROOT must be absolute: %q", goRoot)

	return goRoot
}

func guardModuleCache(t *testing.T, env []string) string {
	t.Helper()

	goRoot := guardGoRoot(t)
	cmd := exec.CommandContext(t.Context(), filepath.Join(goRoot, "bin", "go"), "env", "GOMODCACHE")
	cmd.Dir = reporoot.Path(t)
	cmd.Env = env
	cmd.Env = append(cmd.Env, "GOROOT="+goRoot, "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "TEST_TELEMETRY_DIR="+os.DevNull)
	body, err := cmd.CombinedOutput()
	require.NoError(t, err, string(body))
	cache := strings.TrimSpace(string(body))
	require.NotEmpty(t, cache)
	require.True(t, filepath.IsAbs(cache), "effective GOMODCACHE must be absolute: %q", cache)

	return cache
}

func TestGuardWorkingDirectoryBoundary_EffectiveModuleCache(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	// Runs after the parallel children, before TempDir cleanup. A metadata
	// query must not start telemetry work in this hermetic HOME.
	t.Cleanup(func() {
		require.NoDirExists(t, filepath.Join(root, ".config", "go", "telemetry"))
	})
	for _, tc := range []struct {
		name string
		env  []string
		want string
	}{
		{"default from HOME", nil, filepath.Join(root, "go", "pkg", "mod")},
		{"default from GOPATH", []string{"GOPATH=" + filepath.Join(root, "gopath")}, filepath.Join(root, "gopath", "pkg", "mod")},
		{"explicit module cache", []string{"GOMODCACHE=" + filepath.Join(root, "modules")}, filepath.Join(root, "modules")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// No ambient GOMODCACHE, GOPATH, or persisted go env settings enter
			// these cases. go env derives paths without creating/downloading them.
			env := append([]string{"HOME=" + root, "TMPDIR=" + root, "GOENV=off"}, tc.env...)
			require.Equal(t, tc.want, guardModuleCache(t, env))
		})
	}
}
