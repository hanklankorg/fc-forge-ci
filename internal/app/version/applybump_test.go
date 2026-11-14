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

	appversion "github.com/diggsweden/forge-ci/internal/app/version"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

const applyBumpPOM = `<?xml version="1.0" encoding="UTF-8"?>
<project>
  <modelVersion>4.0.0</modelVersion>
  <groupId>se.digg</groupId>
  <artifactId>app</artifactId>
  <version>%s</version>
</project>
`

const applyBumpPatch = `diff --git a/pom.xml b/pom.xml
index 1111111..2222222 100644
--- a/pom.xml
+++ b/pom.xml
@@ -3,4 +3,4 @@
   <modelVersion>4.0.0</modelVersion>
   <groupId>se.digg</groupId>
   <artifactId>app</artifactId>
-  <version>0.1.9</version>
+  <version>0.2.0</version>
`

// fakeBumpApplier stands in for git apply: the real apply runs in the
// integration test. It records the calls and rewrites the version.
type fakeBumpApplier struct {
	calls []string
	fail  bool
}

func (f *fakeBumpApplier) ApplyPatch(_ context.Context, _ string, checkOnly bool) error {
	if checkOnly {
		f.calls = append(f.calls, "check")
		if f.fail {
			return errs.ErrValidation
		}

		return nil
	}

	f.calls = append(f.calls, "apply")

	return os.WriteFile("pom.xml", []byte(strings.ReplaceAll(applyBumpPOM, "%s", "0.2.0")), 0o600)
}

func applyBumpFixture(t *testing.T, declared string) (appversion.ApplyBumpInput, *fakeBumpApplier) {
	t.Helper()

	dir := t.TempDir()
	writeFile(t, dir, "pom.xml", strings.ReplaceAll(applyBumpPOM, "%s", declared))
	writeFile(t, dir, "version-bump.patch", applyBumpPatch)
	t.Chdir(dir)

	sum := sha256.Sum256([]byte(applyBumpPatch))

	return appversion.ApplyBumpInput{
		PatchPath:   "version-bump.patch",
		SHA256:      hex.EncodeToString(sum[:]),
		ProjectType: projecttype.Maven,
		Version:     "v0.2.0",
		WorkingDir:  ".",
		PathsFile:   filepath.Join(dir, "paths.txt"),
	}, &fakeBumpApplier{}
}

func TestApplyBump_AppliesAVerifiedBumpAndNamesItsFiles(t *testing.T) {
	in, ops := applyBumpFixture(t, "0.1.9")

	var out bytes.Buffer

	got, err := appversion.ApplyBump(context.Background(), ops, &out, in)
	require.NoError(t, err)
	require.Equal(t, []string{"check", "apply"}, ops.calls)
	require.Equal(t, "0.1.9", got.OldVersion)

	paths, err := os.ReadFile(in.PathsFile)
	require.NoError(t, err)
	require.Equal(t, "pom.xml\n", string(paths))
	require.Contains(t, out.String(), "0.1.9 -> 0.2.0")
}

func TestApplyBump_RefusesBeforeApplying(t *testing.T) {
	cases := map[string]func(t *testing.T, in *appversion.ApplyBumpInput){
		"digest mismatch": func(_ *testing.T, in *appversion.ApplyBumpInput) {
			in.SHA256 = strings.Repeat("0", 64)
		},
		"patch changed after its digest": func(t *testing.T, in *appversion.ApplyBumpInput) {
			t.Helper()
			require.NoError(t, os.WriteFile(in.PatchPath, []byte(strings.Replace(applyBumpPatch, "0.2.0", "0.2.0-evil", 1)), 0o600))
		},
		"project type without an isolated bump": func(_ *testing.T, in *appversion.ApplyBumpInput) {
			in.ProjectType = projecttype.Go
		},
		"prerelease version": func(_ *testing.T, in *appversion.ApplyBumpInput) {
			in.Version = "0.2.0-rc.1"
		},
		"working directory outside the repository": func(_ *testing.T, in *appversion.ApplyBumpInput) {
			in.WorkingDir = "../elsewhere"
		},
		"root project file untouched": func(_ *testing.T, in *appversion.ApplyBumpInput) {
			in.WorkingDir = "module"
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in, ops := applyBumpFixture(t, "0.1.9")
			mutate(t, &in)

			_, err := appversion.ApplyBump(context.Background(), ops, &bytes.Buffer{}, in)
			require.Error(t, err)
			require.Empty(t, ops.calls, "nothing may be applied after a refusal")
			require.NoFileExists(t, in.PathsFile)
		})
	}
}

// The substitution alone does not prove the old version is the project's: a
// line holding 0.1.9 for another reason could be moved to the release version.
func TestApplyBump_RefusesAnOldVersionTheProjectDoesNotDeclare(t *testing.T) {
	in, ops := applyBumpFixture(t, "1.0.0")

	_, err := appversion.ApplyBump(context.Background(), ops, &bytes.Buffer{}, in)
	require.ErrorIs(t, err, errs.ErrValidation)
	require.Empty(t, ops.calls)
}

func TestApplyBump_RefusesAPatchGitCannotApply(t *testing.T) {
	in, ops := applyBumpFixture(t, "0.1.9")
	ops.fail = true

	_, err := appversion.ApplyBump(context.Background(), ops, &bytes.Buffer{}, in)
	require.Error(t, err)
	require.Equal(t, []string{"check"}, ops.calls)
	require.NoFileExists(t, in.PathsFile)
}
