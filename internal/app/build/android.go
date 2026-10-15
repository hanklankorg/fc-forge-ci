// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/build"
	"github.com/diggsweden/forge-ci/internal/domain/ci"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// AndroidArtifactNamesInput drives AndroidArtifactNames.
type AndroidArtifactNamesInput struct {
	IncludeDate bool
	Prefix      string
	RepoName    string
	Flavor      string
	Override    string

	// Today is interpolated into the date stamp. Empty → time.Now().
	Today time.Time
}

// AndroidArtifactNames computes the four artifact names and writes
// each to the OutputSink under its kebab-case key. The four
// `printf "Artifact: ..."` status lines also go to stderr to match
// the bash.
func AndroidArtifactNames(ctx context.Context, sink ci.OutputSink, stderr io.Writer, in AndroidArtifactNamesInput) error {
	today := in.Today
	if today.IsZero() {
		today = time.Now()
	}

	names, err := build.ResolveAndroidArtifactNames(build.AndroidArtifactNamesInput{
		IncludeDate: in.IncludeDate,
		Prefix:      in.Prefix,
		RepoName:    in.RepoName,
		Flavor:      in.Flavor,
		Override:    in.Override,
		Today:       today,
	})
	if err != nil {
		return err
	}

	type kv struct{ k, v string }
	for _, p := range []kv{ //nolint:varnamelen // idiomatic short name (testing/http/io conventions).
		{"debug-name", names.DebugName},
		{"release-name", names.ReleaseName},
		{"aab-name", names.AABName},
		{"sbom-name", names.SBOMName},
	} {
		if err := sink.Set(ctx, p.k, p.v); err != nil {
			return fmt.Errorf("set %s: %w", p.k, err)
		}
	}

	_, _ = fmt.Fprintf(stderr, "Debug artifact: %s\n", names.DebugName)
	_, _ = fmt.Fprintf(stderr, "Release artifact: %s\n", names.ReleaseName)
	_, _ = fmt.Fprintf(stderr, "AAB artifact: %s\n", names.AABName)
	_, _ = fmt.Fprintf(stderr, "SBOM artifact: %s\n", names.SBOMName)

	return nil
}

// AndroidVersionInfoInput drives AndroidVersionInfo.
type AndroidVersionInfoInput struct {
	// Dir is the directory containing gradle.properties. Empty → cwd.
	Dir string
}

// AndroidVersionInfo reads gradle.properties (if present) and writes
// `version` and `version-code` outputs. Falls through to "unknown" for
// either field when gradle.properties is missing.
func AndroidVersionInfo(ctx context.Context, sink ci.OutputSink, stderr io.Writer, annot output.Annotator, in AndroidVersionInfoInput) error {
	dir := in.Dir
	if dir == "" {
		var err error

		dir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("getwd: %w", err)
		}
	}

	path := filepath.Join(dir, "gradle.properties")

	data, err := os.ReadFile(path) //nolint:gosec // path is CLI-flag-derived; filename component is hardcoded.
	if err != nil {
		annot.Warningf("gradle.properties not found, version info unavailable")

		if err := sink.Set(ctx, "version", "unknown"); err != nil {
			return err
		}

		return sink.Set(ctx, "version-code", "unknown")
	}

	v, c := build.ParseGradleVersionFromProperties(string(data)) //nolint:varnamelen // idiomatic short name (testing/http/io conventions).
	if err := sink.Set(ctx, "version", v); err != nil {
		return err
	}

	if err := sink.Set(ctx, "version-code", c); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stderr, "Version: %s (%s)\n", v, c)

	return nil
}

// AndroidGradleBuildInput drives AndroidGradleBuild.
type AndroidGradleBuildInput struct {
	// Tasks is the space-separated gradle task list (typically resolved by
	// build.ResolveAndroidBuildTasks).
	Tasks string
	// SkipTests adds `-x test` to the gradle invocation.
	SkipTests bool
}

// AndroidGradleBuild runs `./gradlew <tasks> [-x test]`. Mirrors.
func AndroidGradleBuild(ctx context.Context, ops GradleOps, w, stderr io.Writer, in AndroidGradleBuildInput) error { //nolint:varnamelen // idiomatic short name (testing/http/io conventions).
	if strings.TrimSpace(in.Tasks) == "" {
		return fmt.Errorf("gradle tasks are required: pass --tasks <list> or set $GRADLE_TASKS: %w", errs.ErrUsage)
	}

	_, _ = fmt.Fprintf(w, "Running Gradle tasks: %s\n", in.Tasks)

	args := strings.Fields(in.Tasks)
	if in.SkipTests {
		args = append(args, "-x", "test")
	}

	return ops.RunInherit(ctx, w, stderr, args...)
}

// AndroidListArtifactsInput drives AndroidListArtifacts.
type AndroidListArtifactsInput struct {
	// BuildModule is typically "app". The directory walked is
	// <BuildModule>/build/outputs.
	BuildModule string
	// Root is the project root. Empty → cwd.
	Root string
}

// AndroidListArtifacts walks <BuildModule>/build/outputs for *.apk and
// *.aab files and prints their absolute paths to w, one per line.
//
//nolint:cyclop // lists APK+AAB by build-type with conditional include/skip per pattern.
func AndroidListArtifacts(w io.Writer, in AndroidListArtifactsInput) error { //nolint:varnamelen // idiomatic short name (testing/http/io conventions).
	if in.BuildModule == "" {
		return fmt.Errorf("build module is required: pass --build-module <name> or set $BUILD_MODULE: %w", errs.ErrUsage)
	}

	if !pathsafe.Relative(in.BuildModule) || in.BuildModule == "." {
		return fmt.Errorf("build module must be a relative child directory: %w", errs.ErrUsage)
	}

	root := in.Root
	if root == "" {
		var err error

		root, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("getwd: %w", err)
		}
	}

	outputs := filepath.Join(root, in.BuildModule, "build", "outputs")

	paths, err := androidArtifactPaths(outputs)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(w, "Built artifacts:\n")
	for _, path := range paths {
		_, _ = fmt.Fprintln(w, filepath.Join(outputs, path))
	}

	if len(paths) == 0 {
		_, _ = fmt.Fprintln(w, "No artifacts found")
	}

	return nil
}

// androidArtifactPaths discovers the full set before any path is printed. The
// opened root and the walker both reject links; an APK or AAB must be a
// regular file.
func androidArtifactPaths(directory string) ([]string, error) {
	root, err := pathsafe.OpenRoot(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	defer func() { _ = root.Close() }()

	var paths []string

	err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("mobile artifact tree contains a symlink: %w", errs.ErrValidation)
		}

		if ext := strings.ToLower(filepath.Ext(path)); ext == ".apk" || ext == ".aab" {
			if !entry.Type().IsRegular() {
				return fmt.Errorf("mobile application artifact must be a regular file: %w", errs.ErrValidation)
			}

			paths = append(paths, filepath.FromSlash(path))
		}

		return nil
	})

	return paths, err
}
