// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	appbuild "github.com/diggsweden/forge-ci/internal/app/build"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// Regression for a CLI/adapter-selected release profile: install traverses
// verify before MavenLibrary is reached. Every producer run must be key-free,
// including when the caller explicitly supplied the opposite property. Native
// quality/test failures must still stop the release at their original step.
func TestMavenReleaseBuild_UnsignedLibraryPreservesNativeGates(t *testing.T) {
	for _, failureGoal := range []string{"", "install", "test", "package"} {
		t.Run("failure="+failureGoal, func(t *testing.T) {
			options := []string{"--batch-mode", "-Pcentral-release", "-Dgpg.skip=false"}
			before := slices.Clone(options)
			gateFailure := errors.New("project-owned quality gate failed")
			var goals []string
			ops := mavenPreflightOps{run: func(_ context.Context, _, _ io.Writer, args []string) error {
				if !slices.Equal(args[:len(options)], options) || args[len(args)-1] != "-Dgpg.skip=true" {
					t.Fatalf("unsigned producer policy/profile lost: %v", args)
				}
				goal := args[len(options)]
				goals = append(goals, goal)
				if goal == "test" && (slices.Contains(args, "-DskipTests") || slices.Contains(args, "-DskipTests=true")) {
					t.Fatal("project tests were disabled")
				}
				if goal == "package" && !slices.Contains(args, "-DskipTests=false") {
					t.Fatal("packaging no longer preserves project tests/attached artifacts")
				}
				if goal == failureGoal {
					return gateFailure
				}
				return nil
			}}
			err := appbuild.MavenReleaseBuild(t.Context(), &recordingSummarySink{}, ops, io.Discard, io.Discard, appbuild.MavenReleaseBuildInput{
				Dir: newMavenDir(t), BuildType: "lib", CLIOpts: options,
				EnableBuildSBOM: true, SBOMToolVersion: "2.9.1",
			})
			if !slices.Equal(options, before) {
				t.Fatal("mutated caller Maven options")
			}
			if failureGoal != "" {
				if !errors.Is(err, gateFailure) || goals[len(goals)-1] != failureGoal {
					t.Fatalf("project failure was hidden or build continued: goals=%v err=%v", goals, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"install", "clean", "test", "package", "org.cyclonedx:cyclonedx-maven-plugin:2.9.1:makeAggregateBom"}
			if !slices.Equal(goals, want) {
				t.Fatalf("reactor/test/package/SBOM sequence changed: %v", goals)
			}
		})
	}
}

func newMavenDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	pom := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <groupId>se.digg.example</groupId>
  <artifactId>demo</artifactId>
  <version>1.2.3</version>
</project>`)

	if err := os.WriteFile(filepath.Join(dir, "pom.xml"), pom, 0o644); err != nil {
		t.Fatal(err)
	}

	return dir
}

// mavenSteps strips the forwarded CLI-opts prefix from each recorded
// invocation, leaving the step the orchestrator chose. The opts
// themselves are asserted separately, and the exact argv of each step is
// pinned by the MavenLibrary/MavenApplication tests in maven_test.go.
func mavenSteps(t *testing.T, runs [][]string, opts []string) [][]string {
	t.Helper()

	out := make([][]string, 0, len(runs))

	for _, reader := range runs {
		if len(reader) < len(opts) || !slices.Equal(reader[:len(opts)], opts) {
			t.Errorf("CLI opts not forwarded to mvn call: %v", reader)

			continue
		}

		out = append(out, reader[len(opts):])
	}

	return out
}

func TestMavenReleaseBuild_RunsStepsInOrder(t *testing.T) {
	t.Parallel()

	opts := []string{"-B", "-ntp"}

	for _, tc := range []struct {
		name      string
		buildType string
		profile   string
		skipTests bool
		sbom      bool
		want      [][]string
	}{
		{
			name:      "app",
			buildType: "app",
			sbom:      true,
			want: [][]string{
				{"install", "-DskipTests"},
				{"clean", "package"},
				{"org.cyclonedx:cyclonedx-maven-plugin:2.9.1:makeAggregateBom"},
			},
		},
		{
			name:      "app skipping tests",
			buildType: "app",
			skipTests: true,
			want: [][]string{
				{"install", "-DskipTests"},
				{"clean", "package", "-DskipTests"},
			},
		},
		{
			// A library compiles, tests, then packages separately. Its selected
			// profile is forwarded to every lifecycle phase.
			name:      "lib with a profile",
			buildType: "lib",
			profile:   "central-release",
			sbom:      true,
			want: [][]string{
				{"install", "-DskipTests", "-Dgpg.skip=true"},
				{"clean", "compile", "-Pcentral-release", "-Dgpg.skip=true"},
				{"test", "-Pcentral-release", "-Dgpg.skip=true"},
				{"package", "-DskipTests=false", "-Pcentral-release", "-Dgpg.skip=true"},
				{"org.cyclonedx:cyclonedx-maven-plugin:2.9.1:makeAggregateBom", "-Dgpg.skip=true"},
			},
		},
		{
			// Skipping tests drops the separate test run, and the package
			// step is told so explicitly rather than left to a default.
			name:      "lib skipping tests",
			buildType: "lib",
			skipTests: true,
			want: [][]string{
				{"install", "-DskipTests", "-Dgpg.skip=true"},
				{"clean", "compile", "-Dgpg.skip=true"},
				{"package", "-DskipTests=true", "-Dgpg.skip=true"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ops := &fakeMaven{}

			var out bytes.Buffer

			err := appbuild.MavenReleaseBuild(context.Background(), &recordingSummarySink{}, ops, &out, &out, appbuild.MavenReleaseBuildInput{
				Dir: newMavenDir(t), SkipTests: tc.skipTests, EnableBuildSBOM: tc.sbom,
				BuildType:       tc.buildType,
				Profile:         tc.profile,
				CLIOpts:         opts,
				SBOMToolVersion: "2.9.1",
			})
			if tc.skipTests {
				if err == nil || len(ops.runs) != 0 {
					t.Fatalf("test opt-out: err=%v runs=%v", err, ops.runs)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}

			// Ordered and complete. The previous assertions searched a
			// joined string for three fragments, so they saw neither order
			// nor the steps they did not name -- and the "SBOM did not run"
			// check passed equally on a run where nothing ran.
			if got := mavenSteps(t, ops.runs, opts); !slices.EqualFunc(got, tc.want, slices.Equal) {
				t.Errorf("mvn steps =\n%v\nwant\n%v", got, tc.want)
			}
		})
	}
}

func TestMavenReleaseBuild_RejectsUnknownBuildType(t *testing.T) {
	t.Parallel()

	ops := &fakeMaven{}
	summary := &recordingSummarySink{}

	err := appbuild.MavenReleaseBuild(context.Background(), summary, ops, &bytes.Buffer{}, &bytes.Buffer{}, appbuild.MavenReleaseBuildInput{
		Dir:       newMavenDir(t),
		BuildType: "fatjar",
	})
	if !errors.Is(err, errs.ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}

	// Validated first, so an unknown build type does not half-build.
	if len(ops.runs) != 0 {
		t.Errorf("ran mvn for an unknown build type: %v", ops.runs)
	}

	if summary.buf.Len() != 0 {
		t.Errorf("wrote a summary for a build that never started: %q", summary.buf.String())
	}
}

// A release builds the version its release commit carries. A project that
// still declares another version is refused before anything is packaged.
func TestMavenReleaseBuild_ExpectVersion(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		expect string
		ok     bool
	}{
		{expect: "v1.2.3", ok: true},
		{expect: "1.2.3", ok: true},
		{expect: "", ok: true},
		{expect: "v1.2.4", ok: false},
	} {
		t.Run(tc.expect, func(t *testing.T) {
			t.Parallel()

			ops := &fakeMaven{}

			err := appbuild.MavenReleaseBuild(context.Background(), &recordingSummarySink{}, ops, &bytes.Buffer{}, &bytes.Buffer{}, appbuild.MavenReleaseBuildInput{
				Dir:           newMavenDir(t),
				BuildType:     "app",
				ExpectVersion: tc.expect,
			})
			if tc.ok {
				if err != nil {
					t.Fatal(err)
				}

				return
			}

			if !errors.Is(err, errs.ErrValidation) {
				t.Fatalf("err = %v, want a validation error", err)
			}

			for _, run := range ops.runs {
				if slices.Contains(run, "package") {
					t.Errorf("packaged after a version mismatch: %v", run)
				}
			}
		})
	}
}
