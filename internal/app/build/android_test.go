// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/adapters/ghaoutput"
	appbuild "github.com/diggsweden/forge-ci/internal/app/build"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

// TestAndroidArtifactNames_EmitsFourNames covers what this layer owns:
// which sink keys the four names land under, and that every input reaches
// the resolver. How the names themselves compose is the domain's claim,
// tested in internal/domain/build.
//
// Each case compares the whole output map rather than the keys it expects,
// so an added or renamed output is caught too -- these are consumed by the
// workflow to name uploaded artifacts.
func TestAndroidArtifactNames_EmitsFourNames(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		in   appbuild.AndroidArtifactNamesInput
		want map[string]string
	}{
		{
			name: "repo name only",
			in:   appbuild.AndroidArtifactNamesInput{RepoName: "demo-app"},
			want: map[string]string{
				"debug-name":   "demo-app - APK debug",
				"release-name": "demo-app - APK release",
				"aab-name":     "demo-app - AAB release",
				"sbom-name":    "demo-app - build SBOM",
			},
		},
		{
			name: "date, prefix and flavor all reach the resolver",
			in: appbuild.AndroidArtifactNamesInput{
				IncludeDate: true,
				Prefix:      "Nightly",
				RepoName:    "demo-app",
				Flavor:      "fdroid",
				Today:       time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
			},
			want: map[string]string{
				"debug-name":   "2026-05-10 - Nightly - demo-app - fdroid - APK debug",
				"release-name": "2026-05-10 - Nightly - demo-app - fdroid - APK release",
				"aab-name":     "2026-05-10 - Nightly - demo-app - fdroid - AAB release",
				"sbom-name":    "2026-05-10 - Nightly - demo-app - fdroid - build SBOM",
			},
		},
		{
			// An override replaces the composed name entirely, so a
			// consumer pinning an exact artifact name is not surprised by
			// a date or flavor appearing in it.
			name: "an override outranks every other input",
			in: appbuild.AndroidArtifactNamesInput{
				IncludeDate: true,
				Prefix:      "ci",
				RepoName:    "myapp",
				Flavor:      "prod",
				Override:    "wallet-android-demo",
				Today:       time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
			},
			want: map[string]string{
				"debug-name":   "wallet-android-demo-debug",
				"release-name": "wallet-android-demo-release",
				"aab-name":     "wallet-android-demo",
				"sbom-name":    "wallet-android-demo-sbom",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := fakeoutputsink.New(t)

			if err := appbuild.AndroidArtifactNames(context.Background(), sink, io.Discard, tc.in); err != nil {
				t.Fatalf("AndroidArtifactNames: %v", err)
			}

			if got := sink.AllScalar(); !maps.Equal(got, tc.want) {
				t.Errorf("outputs =\n%v\nwant\n%v", got, tc.want)
			}
		})
	}
}

func TestAndroidArtifactNames_RequiresRepoNameOrOverride(t *testing.T) {
	t.Parallel()

	sink := fakeoutputsink.New(t)

	err := appbuild.AndroidArtifactNames(context.Background(), sink, io.Discard, appbuild.AndroidArtifactNamesInput{})
	if !errors.Is(err, errs.ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}

	// Nothing half-emitted: a workflow reading three of four names would
	// upload artifacts under names the fourth step never agreed to.
	if got := sink.Keys(); len(got) != 0 {
		t.Errorf("emitted %q with neither repo-name nor override", got)
	}
}

func TestAndroidVersionInfo_ReadsGradleProperties(t *testing.T) {
	t.Parallel()

	fsys := testfs.NewReal(t)
	fsys.WriteFile("gradle.properties", []byte("versionName=1.0.0-beta1\nversionCode=5\n"))

	sink := fakeoutputsink.New(t)

	var stderr bytes.Buffer

	if err := appbuild.AndroidVersionInfo(context.Background(), sink, &stderr, output.Annotator{}, appbuild.AndroidVersionInfoInput{Dir: fsys.Root}); err != nil {
		t.Fatalf("AndroidVersionInfo: %v", err)
	}

	want := map[string]string{"version": "1.0.0-beta1", "version-code": "5"}
	if got := sink.AllScalar(); !maps.Equal(got, want) {
		t.Errorf("outputs = %v, want %v", got, want)
	}

	if got := strings.TrimSpace(stderr.String()); got != "Version: 1.0.0-beta1 (5)" {
		t.Errorf("stderr = %q", got)
	}
}

func TestAndroidVersionInfo_MissingFileFallsBackToUnknown(t *testing.T) {
	t.Parallel()

	fsys := testfs.NewReal(t) // no gradle.properties
	sink := fakeoutputsink.New(t)

	var stderr bytes.Buffer

	if err := appbuild.AndroidVersionInfo(context.Background(), sink, &stderr, output.NewAnnotator(&stderr, output.FormatGitHub), appbuild.AndroidVersionInfoInput{Dir: fsys.Root}); err != nil {
		t.Fatalf("AndroidVersionInfo: %v", err)
	}

	// Both outputs are still emitted, so a consumer always has something
	// to read -- absence is a warning here, not a failure.
	want := map[string]string{"version": "unknown", "version-code": "unknown"}
	if got := sink.AllScalar(); !maps.Equal(got, want) {
		t.Errorf("outputs = %v, want %v", got, want)
	}

	if !strings.Contains(stderr.String(), "gradle.properties not found") {
		t.Errorf("expected warning in stderr: %q", stderr.String())
	}
}

// TestAndroidVersionInfo_NormalizesCRLFForRealSink verifies that normalized
// Gradle values satisfy the production sink's scalar contract.
func TestAndroidVersionInfo_NormalizesCRLFForRealSink(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		wantOut string
	}{
		{
			name:    "LF",
			body:    "versionName=1.2.3\nversionCode=42\n",
			wantOut: "version=1.2.3\nversion-code=42\n",
		},
		{
			name:    "CRLF",
			body:    "versionName=1.2.3\r\nversionCode=42\r\n",
			wantOut: "version=1.2.3\nversion-code=42\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fsys := testfs.NewReal(t)
			fsys.WriteFile("gradle.properties", []byte(tc.body))

			outPath := filepath.Join(t.TempDir(), "gha_output")
			if err := os.WriteFile(outPath, nil, 0o600); err != nil {
				t.Fatal(err)
			}

			t.Setenv("GITHUB_OUTPUT", outPath)

			err := appbuild.AndroidVersionInfo(context.Background(), ghaoutput.NewFromEnv(), io.Discard, output.Annotator{}, appbuild.AndroidVersionInfoInput{Dir: fsys.Root})
			if err != nil {
				t.Fatalf("AndroidVersionInfo: %v", err)
			}

			body, readErr := os.ReadFile(outPath)
			if readErr != nil {
				t.Fatal(readErr)
			}

			if string(body) != tc.wantOut {
				t.Errorf("output = %q, want %q", body, tc.wantOut)
			}
		})
	}
}

func TestAndroidGradleBuild_RefusesConsumerTestOptOut(t *testing.T) {
	ops := &fakeGradle{}
	if err := appbuild.AndroidGradleBuild(context.Background(), ops, io.Discard, io.Discard, appbuild.AndroidGradleBuildInput{
		Tasks: "assembleRelease app:bundleRelease", SkipTests: true,
	}); !errors.Is(err, errs.ErrUsage) {
		t.Fatalf("test opt-out was not refused: %v", err)
	}

	if len(ops.args) != 0 {
		t.Fatalf("ran gradle before refusal: %v", ops.args)
	}
}

func TestAndroidGradleBuild_RunsTestsBeforePackaging(t *testing.T) {
	ops := &fakeGradle{}
	if err := appbuild.AndroidGradleBuild(context.Background(), ops, io.Discard, io.Discard, appbuild.AndroidGradleBuildInput{
		Tasks: "assembleDebug",
	}); err != nil {
		t.Fatal(err)
	}

	assertGradleCalls(t, ops.calls, []string{"test"}, []string{"assembleDebug", "-x", "test"})
}

func TestAndroidGradleBuild_RequiresTasks(t *testing.T) {
	t.Parallel()

	for _, tasks := range []string{"", "  ", "\t\n"} {
		ops := &fakeGradle{}

		err := appbuild.AndroidGradleBuild(context.Background(), ops, io.Discard, io.Discard, appbuild.AndroidGradleBuildInput{Tasks: tasks})
		if !errors.Is(err, errs.ErrUsage) {
			t.Errorf("tasks %q: err = %v, want ErrUsage", tasks, err)
		}

		// This is the guard that catches an unresolvable build-types
		// value: ResolveAndroidBuildTasks returns "" for one it does not
		// recognise, and running gradle bare would build the default
		// task instead of failing.
		if ops.args != nil {
			t.Errorf("tasks %q: invoked gradle with %v", tasks, ops.args)
		}
	}
}

func TestAndroidListArtifacts_FindsApkAndAab(t *testing.T) {
	t.Parallel()

	fsys := testfs.NewReal(t)
	module := "app"
	fsys.WriteFile(filepath.Join(module, "build", "outputs", "apk", "debug", "demo.apk"), []byte("apk"))
	fsys.WriteFile(filepath.Join(module, "build", "outputs", "bundle", "release", "demo.aab"), []byte("aab"))
	// Decoys: two non-artifacts a real build genuinely produces under
	// outputs/ -- the apk metadata and the R8 mapping -- and an .apk
	// outside the module's outputs tree, which must not be picked up.
	fsys.WriteFile(filepath.Join(module, "build", "outputs", "apk", "debug", "manifest.json"), []byte("{}"))
	fsys.WriteFile(filepath.Join(module, "build", "outputs", "mapping", "release", "mapping.txt"), []byte("map"))
	fsys.WriteFile(filepath.Join("other", "build", "outputs", "apk", "debug", "stray.apk"), []byte("apk"))

	var out bytes.Buffer

	if err := appbuild.AndroidListArtifacts(&out, appbuild.AndroidListArtifactsInput{
		BuildModule: module, Root: fsys.Root,
	}); err != nil {
		t.Fatal(err)
	}

	// Whole and in walk order. The listing is short and deterministic, so
	// comparing it entire catches an extra entry as readily as a missing
	// one -- the previous checks could not see a stray artifact from
	// another module.
	want := "Built artifacts:\n" +
		filepath.Join(fsys.Root, module, "build", "outputs", "apk", "debug", "demo.apk") + "\n" +
		filepath.Join(fsys.Root, module, "build", "outputs", "bundle", "release", "demo.aab") + "\n"
	if got := out.String(); got != want {
		t.Errorf("listing = %q, want %q", got, want)
	}
}

func TestAndroidListArtifacts_NoArtifactsPrintsNotice(t *testing.T) {
	t.Parallel()

	fsys := testfs.NewReal(t)

	var out bytes.Buffer

	if err := appbuild.AndroidListArtifacts(&out, appbuild.AndroidListArtifactsInput{
		BuildModule: "app", Root: fsys.Root,
	}); err != nil {
		t.Fatal(err)
	}

	if got, want := out.String(), "Built artifacts:\nNo artifacts found\n"; got != want {
		t.Errorf("listing = %q, want %q", got, want)
	}
}

func TestAndroidListArtifacts_RequiresBuildModule(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	// Without a module there is no outputs tree to scan, and defaulting to
	// one would list another module's artifacts as this one's.
	err := appbuild.AndroidListArtifacts(&out, appbuild.AndroidListArtifactsInput{Root: t.TempDir()})
	if !errors.Is(err, errs.ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}

	if out.Len() != 0 {
		t.Errorf("wrote %q on a refused run", out.String())
	}
}
