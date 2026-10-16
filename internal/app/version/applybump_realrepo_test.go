//go:build integration

// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	gitadapter "github.com/diggsweden/forge-ci/internal/adapters/git"
	appversion "github.com/diggsweden/forge-ci/internal/app/version"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/testutil/isolatedgit"
)

// The patch here is made the way prepare-release.yml's key-free bump job makes
// it, with real git on both sides, so the parser and git apply agree on the
// format git actually writes.
func TestApplyBump_RealGitRoundTrip(t *testing.T) {
	repo := isolatedgit.NewRepo(t)
	root := strings.ReplaceAll(applyBumpPOM, "%s", "0.1.9")
	module := "<project>\n  <parent>\n    <groupId>se.digg</groupId>\n    <artifactId>app</artifactId>\n    <version>0.1.9</version>\n  </parent>\n\n  <artifactId>core</artifactId>\n</project>\n"
	repo.AddFile("pom.xml", root, "chore: root")
	repo.AddFile("core/pom.xml", module, "chore: module")

	// What `mvn versions:set -DnewVersion=0.2.0 -DprocessAllModules=true` does.
	for file, body := range map[string]string{"pom.xml": root, "core/pom.xml": module} {
		require.NoError(t, os.WriteFile(filepath.Join(repo.Dir, file), []byte(strings.ReplaceAll(body, "0.1.9", "0.2.0")), 0o600))
	}

	patch := repo.Git("diff", "--binary", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames",
		"--src-prefix=a/", "--dst-prefix=b/")
	repo.Git("checkout", "--", ".")

	patchPath := filepath.Join(t.TempDir(), "version-bump.patch")
	require.NoError(t, os.WriteFile(patchPath, []byte(patch+"\n"), 0o600))

	sum := sha256.Sum256([]byte(patch + "\n"))
	pathsFile := filepath.Join(t.TempDir(), "paths.txt")

	t.Chdir(repo.Dir)

	got, err := appversion.ApplyBump(context.Background(), gitadapter.New(), &bytes.Buffer{}, appversion.ApplyBumpInput{
		PatchPath:   patchPath,
		SHA256:      hex.EncodeToString(sum[:]),
		ProjectType: projecttype.Maven,
		Version:     "v0.2.0",
		WorkingDir:  ".",
		PathsFile:   pathsFile,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"core/pom.xml", "pom.xml"}, got.Paths)

	for file := range map[string]bool{"pom.xml": true, "core/pom.xml": true} {
		body, readErr := os.ReadFile(filepath.Join(repo.Dir, file))
		require.NoError(t, readErr)
		require.Contains(t, string(body), "<version>0.2.0</version>")
		require.NotContains(t, string(body), "0.1.9")
	}

	paths, err := os.ReadFile(pathsFile)
	require.NoError(t, err)
	require.Equal(t, "core/pom.xml\npom.xml\n", string(paths))
}
