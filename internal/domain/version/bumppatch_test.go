// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/domain/version"
)

const rootPOMBump = `diff --git a/pom.xml b/pom.xml
index 1111111..2222222 100644
--- a/pom.xml
+++ b/pom.xml
@@ -4,7 +4,7 @@
   <modelVersion>4.0.0</modelVersion>
   <groupId>se.digg</groupId>
   <artifactId>app</artifactId>
-  <version>0.1.9-SNAPSHOT</version>
+  <version>0.2.0</version>
   <packaging>jar</packaging>
   <name>app</name>
   <properties>
`

const modulePOMBump = `diff --git a/core/pom.xml b/core/pom.xml
index 3333333..4444444 100644
--- a/core/pom.xml
+++ b/core/pom.xml
@@ -3,6 +3,6 @@
   <parent>
     <groupId>se.digg</groupId>
     <artifactId>app</artifactId>
-    <version>0.1.9-SNAPSHOT</version>
+    <version>0.2.0</version>
   </parent>
   <artifactId>core</artifactId>
`

func mavenFiles(t *testing.T) []string {
	t.Helper()

	files, ok := version.BumpPatchFiles(projecttype.Maven)
	require.True(t, ok)

	return files
}

func TestVerifyBumpPatch_AcceptsVersionSubstitutions(t *testing.T) {
	t.Parallel()

	got, err := version.VerifyBumpPatch([]byte(rootPOMBump+modulePOMBump), mavenFiles(t), "0.2.0")
	require.NoError(t, err)
	require.Equal(t, []string{"core/pom.xml", "pom.xml"}, got.Paths)
	require.Equal(t, "0.1.9-SNAPSHOT", got.OldVersion)
}

func TestVerifyBumpPatch_RefusesWhatABumpDoesNotDo(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"empty":                    "",
		"no trailing newline":      strings.TrimSuffix(rootPOMBump, "\n"),
		"other file":               strings.ReplaceAll(rootPOMBump, "pom.xml", "build.sh"),
		"file named like a pom":    strings.ReplaceAll(rootPOMBump, "pom.xml", "pom.xml.sh"),
		"parent path":              strings.ReplaceAll(rootPOMBump, "a/pom.xml", "a/../pom.xml"),
		"git directory":            strings.ReplaceAll(rootPOMBump, "pom.xml", ".git/pom.xml"),
		"quoted path":              strings.Replace(rootPOMBump, "diff --git a/pom.xml b/pom.xml", `diff --git "a/pom.xml" "b/pom.xml"`, 1),
		"two paths":                strings.Replace(rootPOMBump, "b/pom.xml\n", "b/core/pom.xml\n", 1),
		"mode change":              strings.Replace(rootPOMBump, "index 1111111..2222222 100644\n", "old mode 100644\nnew mode 100755\n", 1),
		"new file":                 strings.Replace(rootPOMBump, "index 1111111..2222222 100644\n--- a/pom.xml", "new file mode 100644\nindex 0000000..2222222\n--- /dev/null", 1),
		"deleted file":             strings.Replace(rootPOMBump, "+++ b/pom.xml", "+++ /dev/null", 1),
		"rename":                   "diff --git a/pom.xml b/core/pom.xml\nsimilarity index 100%\nrename from pom.xml\nrename to core/pom.xml\n",
		"binary":                   "diff --git a/pom.xml b/pom.xml\nindex 1111111..2222222 100644\nGIT binary patch\nliteral 3\nKcmZ?wbN~PV\n\n",
		"added line":               strings.Replace(rootPOMBump, "+  <version>0.2.0</version>\n", "+  <version>0.2.0</version>\n+  <build><plugins/></build>\n", 1),
		"removed line":             strings.Replace(rootPOMBump, "+  <version>0.2.0</version>\n", "", 1),
		"more than the version":    strings.Replace(rootPOMBump, "+  <version>0.2.0</version>", "+  <version>0.2.0</version><!-- x -->", 1),
		"version change elsewhere": strings.Replace(rootPOMBump, "-  <version>0.1.9-SNAPSHOT</version>\n+  <version>0.2.0</version>", "-  <exec>true</exec>\n+  <exec>0.2.0</exec>", 1),
		"wrong new version":        strings.Replace(rootPOMBump, "+  <version>0.2.0</version>", "+  <version>0.3.0</version>", 1),
		"two old versions":         rootPOMBump + strings.ReplaceAll(modulePOMBump, "0.1.9-SNAPSHOT", "0.1.8"),
		"same file twice":          rootPOMBump + rootPOMBump,
		"hunk count mismatch":      strings.Replace(rootPOMBump, "@@ -4,7 +4,7 @@", "@@ -4,9 +4,7 @@", 1),
		"unknown header":           strings.Replace(rootPOMBump, "index 1111111..2222222 100644\n", "index 1111111..2222222 100644\nweird header\n", 1),
		"no content change":        "diff --git a/pom.xml b/pom.xml\nindex 1111111..2222222 100644\n--- a/pom.xml\n+++ b/pom.xml\n",
	}

	for name, patch := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := version.VerifyBumpPatch([]byte(patch), mavenFiles(t), "0.2.0")
			require.ErrorIs(t, err, errs.ErrValidation)
		})
	}
}

func TestVerifyBumpPatch_RefusesOversizedPatch(t *testing.T) {
	t.Parallel()

	patch := strings.Repeat("x", version.MaxBumpPatchBytes) + "\n"
	_, err := version.VerifyBumpPatch([]byte(patch), mavenFiles(t), "0.2.0")
	require.ErrorIs(t, err, errs.ErrValidation)
}

func TestBumpPatchFiles_OnlyVerifiedTypes(t *testing.T) {
	t.Parallel()

	for _, pt := range []projecttype.Type{projecttype.Go, projecttype.Gradle, projecttype.GradleAndroid, projecttype.Meta, projecttype.XcodeIOS} {
		_, ok := version.BumpPatchFiles(pt)
		require.False(t, ok, pt)
	}
	for kind, expected := range map[projecttype.Type][]string{
		projecttype.Maven: {"pom.xml"},
		projecttype.NPM:   {"package.json", "package-lock.json", "npm-shrinkwrap.json"},
		projecttype.Cargo: {"Cargo.toml", "Cargo.lock"},
	} {
		files, ok := version.BumpPatchFiles(kind)
		require.True(t, ok, kind)
		require.Equal(t, expected, files)
	}
}

// What `mvn versions:set` wrote for a POM with a reproducible-build timestamp:
// the version and project.build.outputTimestamp, in one hunk.
const versionsSetBump = `diff --git a/pom.xml b/pom.xml
index fca68a2..0b1c2d3 100644
--- a/pom.xml
+++ b/pom.xml
@@ -6,14 +6,14 @@
 
   <groupId>se.itiquette.demo</groupId>
   <artifactId>hello-maven</artifactId>
-  <version>0.1.0</version>
+  <version>0.2.0</version>
   <packaging>jar</packaging>
   <name>Hello Maven</name>
 
   <properties>
     <maven.compiler.release>25</maven.compiler.release>
     <project.build.sourceEncoding>UTF-8</project.build.sourceEncoding>
-    <project.build.outputTimestamp>2026-10-15T00:00:00Z</project.build.outputTimestamp>
+    <project.build.outputTimestamp>2026-10-15T19:35:53Z</project.build.outputTimestamp>
   </properties>
 
   <dependencies>
`

// A patch that moves the timestamp and no version is no version bump.
const timestampOnlyBump = `diff --git a/pom.xml b/pom.xml
index fca68a2..0b1c2d3 100644
--- a/pom.xml
+++ b/pom.xml
@@ -16,3 +16,3 @@
     <project.build.sourceEncoding>UTF-8</project.build.sourceEncoding>
-    <project.build.outputTimestamp>2026-10-15T00:00:00Z</project.build.outputTimestamp>
+    <project.build.outputTimestamp>2026-10-15T19:35:53Z</project.build.outputTimestamp>
   </properties>
`

// versions:set moves the reproducible-build timestamp with the version, as the
// GitHub bump commits it; the timestamp may move, and only to a timestamp.
func TestVerifyBumpPatch_AcceptsTheOutputTimestampVersionsSetWrites(t *testing.T) {
	t.Parallel()

	got, err := version.VerifyBumpPatch([]byte(versionsSetBump), mavenFiles(t), "0.2.0")
	require.NoError(t, err)
	require.Equal(t, []string{"pom.xml"}, got.Paths)
	require.Equal(t, "0.1.0", got.OldVersion)

	for name, patch := range map[string]string{
		"not a timestamp":     strings.Replace(versionsSetBump, "+    <project.build.outputTimestamp>2026-10-15T19:35:53Z<", "+    <project.build.outputTimestamp>${evil}<", 1),
		"another property":    strings.ReplaceAll(versionsSetBump, "project.build.outputTimestamp", "maven.compiler.release"),
		"indentation changed": strings.Replace(versionsSetBump, "+    <project.build.outputTimestamp>", "+  <project.build.outputTimestamp>", 1),
		"timestamp alone":     timestampOnlyBump,
	} {
		_, err := version.VerifyBumpPatch([]byte(patch), mavenFiles(t), "0.2.0")
		require.ErrorIs(t, err, errs.ErrValidation, name)
	}
}
