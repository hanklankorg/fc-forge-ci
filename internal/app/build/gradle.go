// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	appenforcement "github.com/diggsweden/forge-ci/internal/app/enforcement"
	"github.com/diggsweden/forge-ci/internal/domain/build"
	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// GradleOps is the slice of gradle-adapter methods the build use cases
// need. Tests inject a fake; production passes adapter/gradle.New().
type GradleOps interface {
	RunInherit(ctx context.Context, w, stderr io.Writer, args ...string) error
	RunInDirInherit(ctx context.Context, dir string, w, stderr io.Writer, args ...string) error
}

// Release orchestration binds cwd-based Gradle steps to its selected project.
type projectGradle struct {
	GradleOps

	dir string
}

func (g projectGradle) RunInherit(ctx context.Context, out, stderr io.Writer, args ...string) error {
	return g.RunInDirInherit(ctx, g.dir, out, stderr, args...)
}

// GradleSBOMInput drives GradleSBOM.
type GradleSBOMInput struct {
	// CycloneDXVersion is the cyclonedx-gradle-plugin version
	// (Renovate-managed in the calling workflow).
	CycloneDXVersion string
	// WorkingDir is the project root containing ./gradlew. Used to
	// pre-flight-check for the wrapper. Empty → current directory.
	WorkingDir string
}

// resolveGradleVersion reads the JVM Gradle convention `version=` from
// gradle.properties, returning "" when the file or property is absent (some
// projects compute version in build.gradle(.kts)). GradleReleaseBuild threads
// it to the build summary.
func resolveGradleVersion(dir string) string {
	if dir == "" {
		dir = "."
	}

	body, err := os.ReadFile(filepath.Join(dir, "gradle.properties")) //nolint:gosec // dir is CLI-flag-derived; filename component is hardcoded.
	if err != nil {
		return ""
	}

	return gradleProperty(string(body), "version")
}

// GradleApplicationInput drives GradleApplication.
type GradleApplicationInput struct {
	// Tasks is the gradle task list, space-separated. The workflow passes
	// $GRADLE_TASKS verbatim. Whitespace-separated tokens become separate argv
	// entries — no shell re-splitting.
	Tasks string
	// TestTask is engine-selected; Android binds its module aggregate here.
	// Empty selects Gradle's standard test task selector.
	TestTask string
	// SkipTests is a refused legacy consumer opt-out.
	SkipTests bool
}

// GradleApplication runs engine-owned test tasks before requested packaging.
// Packaging arguments cannot suppress the separate test invocation.
func GradleApplication(ctx context.Context, ops GradleOps, out, stderr io.Writer, in GradleApplicationInput) error {
	if err := appenforcement.RequireTests(in.SkipTests); err != nil {
		return err
	}
	tasks := strings.Fields(in.Tasks)
	if len(tasks) == 0 {
		return fmt.Errorf("at least one gradle task is required: %w", errs.ErrUsage)
	}
	if err := enforcement.GradleTestArguments(tasks); err != nil {
		return err
	}
	configuration, requestedTasks, err := gradleInvocationArguments(tasks)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out, "Running Gradle tasks: %s\n", strings.Join(tasks, " "))

	testTask := in.TestTask
	if testTask == "" {
		testTask = "test"
	}
	testTasks := []string{testTask}
	for _, task := range requestedTasks {
		name := task[strings.LastIndex(task, ":")+1:]
		if name == "test" || (strings.HasPrefix(name, "test") && strings.HasSuffix(name, "UnitTest")) {
			if !slices.Contains(testTasks, task) {
				testTasks = append(testTasks, task)
			}
		}
	}
	testArgs := append(configuration, testTasks...)
	_, err = appenforcement.TestRun(ctx, out, stderr, enforcement.Gradle, func(stdout, stderr io.Writer) error {
		return ops.RunInherit(ctx, stdout, stderr, testArgs...)
	})
	if err != nil {
		return err
	}
	// Tests already ran. Packaging errors are never waived by test policy.
	packageArgs := append([]string{}, tasks...)
	for _, task := range testTasks {
		packageArgs = append(packageArgs, "-x", task)
	}
	return ops.RunInherit(ctx, out, stderr, packageArgs...)
}

// Share supported global configuration across both invocations. Task-specific
// and unknown options are rejected rather than silently testing another build.
// Dry-run remains packaging-only and cannot suppress mandatory tests.
func gradleInvocationArguments(args []string) (configuration, tasks []string, err error) {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if !strings.HasPrefix(arg, "-") {
			tasks = append(tasks, arg)
			continue
		}
		name, _, attached := strings.Cut(arg, "=")
		switch {
		case arg == "--dry-run" || arg == "-m":
			continue
		case (strings.HasPrefix(arg, "-P") || strings.HasPrefix(arg, "-D")) && len(arg) > 2:
			configuration = append(configuration, arg)
			continue
		}
		switch name {
		case "-P", "-D", "--project-prop", "--system-prop", "-p", "--project-dir", "-c", "--settings-file", "-b", "--build-file", "-I", "--init-script", "-g", "--gradle-user-home", "--include-build", "--console", "--warning-mode", "--max-workers", "--priority", "--configuration-cache-problems":
			configuration = append(configuration, arg)
			if !attached {
				if index+1 == len(args) || strings.HasPrefix(args[index+1], "-") {
					return nil, nil, fmt.Errorf("Gradle option %q requires a value: %w", arg, errs.ErrUsage)
				}
				index++
				configuration = append(configuration, args[index])
			}
		case "--daemon", "--no-daemon", "--offline", "--parallel", "--no-parallel", "--configure-on-demand", "--no-configure-on-demand", "--configuration-cache", "--no-configuration-cache", "--build-cache", "--no-build-cache", "--refresh-dependencies", "--rerun-tasks", "--continue", "--stacktrace", "-s", "--full-stacktrace", "-S", "--info", "-i", "--debug", "-d", "--warn", "-w", "--quiet", "-q":
			if attached {
				return nil, nil, fmt.Errorf("Gradle flag %q does not take a value: %w", arg, errs.ErrUsage)
			}
			configuration = append(configuration, arg)
		default:
			return nil, nil, fmt.Errorf("unsupported Gradle option %q at the mandatory test gate: %w", arg, errs.ErrUsage)
		}
	}
	if len(tasks) == 0 {
		return nil, nil, fmt.Errorf("at least one Gradle task is required: %w", errs.ErrUsage)
	}
	return configuration, tasks, nil
}

// GradleSBOM applies the cyclonedx-gradle-plugin via a throwaway init
// script and runs `cyclonedxBom` to produce build/reports/bom.json.
// This is the Gradle build-SBOM implementation behind `forge-ci build gradle sbom`.
//
// Init-script cleanup is attempted before return regardless of success.
// Removal errors are returned alongside any native Gradle failure.
func GradleSBOM(ctx context.Context, ops GradleOps, out, stderr io.Writer, in GradleSBOMInput) (err error) {
	if in.CycloneDXVersion == "" {
		return fmt.Errorf("cyclonedx-gradle version is required: pass --cyclonedx-version <ver> or set $CYCLONEDX_GRADLE_VERSION: %w", errs.ErrUsage)
	}

	wd := in.WorkingDir
	if wd == "" {
		wd, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("getwd: %w", err)
		}
	}

	wrapper := filepath.Join(wd, "gradlew")

	info, err := os.Stat(wrapper)
	if err != nil || info.IsDir() {
		return fmt.Errorf("gradlew not found or not a file in %s: %w", wd, errs.ErrMissingInput)
	}
	// 0o111 = any execute bit set (owner/group/other).
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("gradlew not executable in %s: %w", wd, errs.ErrValidation)
	}

	tempDir, err := gradleSBOMTempDir()
	if err != nil {
		return fmt.Errorf("resolve Gradle SBOM scratch: %w", err)
	}

	f, err := os.CreateTemp(tempDir, "*.init.gradle.kts") //nolint:varnamelen // idiomatic short name (testing/http/io conventions).
	if err != nil {
		return fmt.Errorf("create init script tempfile: %w", err)
	}

	initPath := f.Name()

	defer func() {
		if cleanupErr := os.Remove(initPath); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("remove Gradle SBOM init script: %w", cleanupErr))
		}
	}()

	if _, err = f.WriteString(build.RenderGradleInitScript(in.CycloneDXVersion)); err != nil {
		return errors.Join(fmt.Errorf("write init script: %w", err), f.Close())
	}

	if err = f.Close(); err != nil {
		return fmt.Errorf("close init script: %w", err)
	}

	// Adapter labels its error with the first non-flag arg
	// ("cyclonedxBom"); wrapping again here would double-prefix and
	// shadow the more useful adapter label.
	return ops.RunInDirInherit(ctx, wd, out, stderr, "--init-script", initPath, "cyclonedxBom")
}

// SBOM scratch follows the OS temp selection, independently of signing TempDir.
// Canonicalize trusted OS aliases for both early preflight and actual creation.
func gradleSBOMTempDir() (string, error) {
	dir, err := mobileTempRoot()
	if err != nil {
		return "", err
	}

	root, err := pathsafe.OpenRoot(dir)
	if err != nil {
		return "", err
	}

	return root.Name(), errors.Join(checkBuildWritableRoot(root), root.Close())
}

func gradleProperty(body, key string) string {
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if len(line) <= len(key) || !strings.HasPrefix(line, key) || !gradlePropertySeparator(line[len(key)]) {
			continue
		}

		rest := strings.TrimPrefix(line, key)
		rest = strings.TrimLeft(rest, " \t\f")

		if strings.HasPrefix(rest, "=") || strings.HasPrefix(rest, ":") {
			rest = rest[1:]
		}

		return strings.TrimSpace(rest)
	}

	return ""
}

func gradlePropertySeparator(value byte) bool {
	return value == '=' || value == ':' || value == ' ' || value == '\t' || value == '\f'
}
