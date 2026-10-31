// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	appbuild "github.com/diggsweden/forge-ci/internal/app/build"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

type fakeGradle struct {
	args   []string
	calls  [][]string
	dir    string
	runErr error

	// captureInitScript: when set, fake reads the --init-script file's
	// contents into this pointer for the test to inspect.
	captureInitScript *string
}

func (f *fakeGradle) RunInherit(ctx context.Context, _, _ io.Writer, args ...string) error {
	return f.RunInDirInherit(ctx, "", nil, nil, args...)
}

func (f *fakeGradle) RunInDirInherit(_ context.Context, dir string, _, _ io.Writer, args ...string) error {
	f.args = args
	f.calls = append(f.calls, append([]string{}, args...))

	f.dir = dir
	if f.captureInitScript != nil {
		// Read the contents of the init script — must happen before
		// GradleSBOM cleans it up.
		for i, a := range args {
			if a == "--init-script" && i+1 < len(args) {
				body, err := os.ReadFile(args[i+1])
				if err == nil {
					*f.captureInitScript = string(body)
				}

				break
			}
		}
	}

	return f.runErr
}

// projectWithGradlew creates a tempdir containing an executable gradlew
// stub and returns the dir.
func projectWithGradlew(t *testing.T) string {
	t.Helper()
	fsys := testfs.NewReal(t)
	fsys.WriteFile("gradlew", []byte("#!/bin/sh\nexit 0\n"))

	if err := os.Chmod(fsys.Path("gradlew"), 0o755); err != nil {
		t.Fatal(err)
	}

	return fsys.Root
}

func TestGradleSBOM_HappyPath_PassesInitScriptAndCyclonedxBomTask(t *testing.T) {
	dir := projectWithGradlew(t)

	var captured string

	ops := &fakeGradle{captureInitScript: &captured}

	err := appbuild.GradleSBOM(context.Background(), ops, io.Discard, io.Discard, appbuild.GradleSBOMInput{
		CycloneDXVersion: "3.2.1",
		WorkingDir:       dir,
	})
	if err != nil {
		t.Fatalf("GradleSBOM: %v", err)
	}

	if len(ops.args) != 3 || ops.args[0] != "--init-script" || ops.args[2] != "cyclonedxBom" {
		t.Fatalf("unexpected args: %v", ops.args)
	}

	if ops.dir != dir {
		t.Fatalf("dir = %q, want %q", ops.dir, dir)
	}

	// The init script must reference the real plugin artifact, not
	// the plugin-portal marker — see RenderGradleInitScript for the
	// rationale (markers don't resolve in init-script classpath).
	if !strings.Contains(captured, `classpath("org.cyclonedx:cyclonedx-gradle-plugin:3.2.1")`) {
		t.Errorf("init script missing pinned version:\n%s", captured)
	}

	// And the negative the comment above is really about: the marker
	// coordinate resolves for a plugins {} block but not on an init-script
	// classpath, so its appearance here would be the bug that rationale
	// describes.
	if strings.Contains(captured, `classpath("org.cyclonedx.bom`) {
		t.Errorf("init script uses the plugin-portal marker:\n%s", captured)
	}
}

func TestGradleSBOM_DeletesInitScriptAfter(t *testing.T) {
	dir := projectWithGradlew(t)

	var initPath string

	captured := func(_ context.Context, _, _ io.Writer, args ...string) error {
		for i, a := range args {
			if a == "--init-script" && i+1 < len(args) {
				initPath = args[i+1]
			}
		}

		return nil
	}
	if err := appbuild.GradleSBOM(context.Background(), runFn(captured), io.Discard, io.Discard, appbuild.GradleSBOMInput{
		CycloneDXVersion: "3.0.0",
		WorkingDir:       dir,
	}); err != nil {
		t.Fatalf("GradleSBOM: %v", err)
	}

	if initPath == "" {
		t.Fatal("did not capture init-script path")
	}

	if _, err := os.Stat(initPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("init script %s still exists after GradleSBOM, expected cleanup", initPath)
	}
}

// TestGradleSBOM_Refusals covers the three ways GradleSBOM declines,
// each carrying its own sentinel. They are not interchangeable: a missing
// flag is the operator's mistake (ErrUsage), an absent wrapper is a
// missing input, and a wrapper that is present but not runnable is a
// validation failure. Every case was previously matched on message text,
// so all three could have collapsed to one sentinel unnoticed.
func TestGradleSBOM_Refusals(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		version    string
		gradlew    string // "" writes none
		gradlewFor os.FileMode
		wantErr    error
	}{
		{
			name:       "no cyclonedx version",
			gradlew:    "#!/bin/sh\n",
			gradlewFor: 0o755,
			wantErr:    errs.ErrUsage,
		},
		{
			name:    "no gradlew",
			version: "3.0",
			wantErr: errs.ErrMissingInput,
		},
		{
			name:       "gradlew present but not executable",
			version:    "3.0",
			gradlew:    "#!/bin/sh\n",
			gradlewFor: 0o644,
			wantErr:    errs.ErrValidation,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fsys := testfs.NewReal(t)
			if tc.gradlew != "" {
				fsys.WriteFile("gradlew", []byte(tc.gradlew))

				if err := os.Chmod(fsys.Path("gradlew"), tc.gradlewFor); err != nil {
					t.Fatal(err)
				}
			}

			ops := &fakeGradle{}

			err := appbuild.GradleSBOM(context.Background(), ops, io.Discard, io.Discard, appbuild.GradleSBOMInput{
				CycloneDXVersion: tc.version,
				WorkingDir:       fsys.Root,
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}

			if len(ops.args) != 0 {
				t.Errorf("invoked gradlew anyway: %v", ops.args)
			}
		})
	}
}

// runFn adapts a closure into the GradleOps interface.
type runFn func(ctx context.Context, out, stderr io.Writer, args ...string) error

func (f runFn) RunInherit(ctx context.Context, out, stderr io.Writer, args ...string) error {
	return f(ctx, out, stderr, args...)
}

func (f runFn) RunInDirInherit(ctx context.Context, _ string, out, stderr io.Writer, args ...string) error {
	return f(ctx, out, stderr, args...)
}

func TestGradleApplication_RunsTasks(t *testing.T) {
	ops := &fakeGradle{}
	if err := appbuild.GradleApplication(context.Background(), ops, &bytes.Buffer{}, &bytes.Buffer{}, appbuild.GradleApplicationInput{
		Tasks: "assemble check",
	}); err != nil {
		t.Fatal(err)
	}

	want := []string{"assemble", "check", "-x", "test"}
	if !slices.Equal(ops.args, want) {
		t.Errorf("args = %v, want %v", ops.args, want)
	}
	if len(ops.calls) != 2 || !slices.Equal(ops.calls[0], []string{"test"}) {
		t.Fatalf("mandatory tests did not precede packaging: %v", ops.calls)
	}
}

func TestGradleApplicationPackagingOptionsCannotSuppressTests(t *testing.T) {
	ops := &fakeGradle{}
	if err := appbuild.GradleApplication(t.Context(), ops, io.Discard, io.Discard, appbuild.GradleApplicationInput{Tasks: "assemble --dry-run"}); err != nil {
		t.Fatal(err)
	}
	if len(ops.calls) != 2 || !slices.Equal(ops.calls[0], []string{"test"}) {
		t.Fatalf("packaging options reached the mandatory test invocation: %v", ops.calls)
	}
	failed := &fakeGradle{runErr: errors.New("test process failed")}
	if err := appbuild.GradleApplication(t.Context(), failed, io.Discard, io.Discard, appbuild.GradleApplicationInput{Tasks: "assemble"}); err == nil || len(failed.calls) != 1 {
		t.Fatalf("packaging ran after failed mandatory tests: %v, %v", failed.calls, err)
	}
}

func TestGradleApplication_RefusesConsumerTestOptOut(t *testing.T) {
	ops := &fakeGradle{}
	if err := appbuild.GradleApplication(context.Background(), ops, &bytes.Buffer{}, &bytes.Buffer{}, appbuild.GradleApplicationInput{
		Tasks:     "assemble",
		SkipTests: true,
	}); !errors.Is(err, errs.ErrUsage) {
		t.Fatalf("test opt-out was not refused: %v", err)
	}

	if len(ops.args) != 0 {
		t.Fatalf("ran gradle before refusal: %v", ops.args)
	}
}

func TestGradleApplicationSharesBuildConfiguration(t *testing.T) {
	for _, property := range []string{"-Pfixture=ci", "--project-prop fixture=ci", "-P fixture=ci", "-Dfixture=ci", "--system-prop=fixture=ci"} {
		t.Run(property, func(t *testing.T) {
			calls := 0
			ops := runFn(func(_ context.Context, _, _ io.Writer, args ...string) error {
				calls++
				// Model a project that cannot configure either phase without its property.
				for _, token := range strings.Fields(property) {
					if !slices.Contains(args, token) {
						return fmt.Errorf("project configuration is missing %q", token)
					}
				}
				if !slices.Contains(args, "--offline") || !slices.Contains(args, "plain") {
					return errors.New("global build configuration was lost")
				}
				return nil
			})
			err := appbuild.GradleApplication(t.Context(), ops, io.Discard, io.Discard, appbuild.GradleApplicationInput{
				Tasks: property + " --offline --console plain assemble",
			})
			if err != nil || calls != 2 {
				t.Fatalf("configuration-required phases: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestGradleApplicationRefusesAmbiguousConfigurationBeforeExecution(t *testing.T) {
	for _, tasks := range []string{"assemble --unknown", "test --tests only.OneTest", "assemble --project-prop", "--offline", "assemble --no-daemon=true"} {
		t.Run(tasks, func(t *testing.T) {
			ops := &fakeGradle{}
			err := appbuild.GradleApplication(t.Context(), ops, io.Discard, io.Discard, appbuild.GradleApplicationInput{Tasks: tasks})
			if !errors.Is(err, errs.ErrUsage) || len(ops.calls) != 0 {
				t.Fatalf("expected pre-effect usage refusal: calls=%v err=%v", ops.calls, err)
			}
		})
	}
}

func TestGradleApplication_InvokesRequiredAndExplicitUnitTestsBeforePackaging(t *testing.T) {
	ops := &fakeGradle{}
	err := appbuild.GradleApplication(t.Context(), ops, io.Discard, io.Discard, appbuild.GradleApplicationInput{
		TestTask: "client:test",
		Tasks:    "assemble client:test :widget:testDemoUnitTest :widget:testDemoUnitTest --dry-run",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertGradleCalls(t, ops.calls,
		[]string{"client:test", ":widget:testDemoUnitTest"},
		[]string{"assemble", "client:test", ":widget:testDemoUnitTest", ":widget:testDemoUnitTest", "--dry-run", "-x", "client:test", "-x", ":widget:testDemoUnitTest"},
	)
}

func TestGradleApplication_PackagingFailureRemainsBlocking(t *testing.T) {
	cause := errors.New("owned packaging failure")
	var calls [][]string
	err := appbuild.GradleApplication(t.Context(), runFn(func(_ context.Context, _, _ io.Writer, args ...string) error {
		calls = append(calls, slices.Clone(args))
		if len(calls) == 1 {
			return nil
		}
		return cause
	}), io.Discard, io.Discard, appbuild.GradleApplicationInput{Tasks: "test assemble"})
	if !errors.Is(err, cause) {
		t.Fatalf("packaging failure was lost: %v", err)
	}
	assertGradleCalls(t, calls, []string{"test"}, []string{"test", "assemble", "-x", "test"})
}

func TestGradleApplication_RejectsEmptyTasks(t *testing.T) {
	t.Parallel()

	for _, tasks := range []string{"", "   ", "\t\n"} {
		ops := &fakeGradle{}

		err := appbuild.GradleApplication(context.Background(), ops, &bytes.Buffer{}, &bytes.Buffer{}, appbuild.GradleApplicationInput{Tasks: tasks})
		if !errors.Is(err, errs.ErrUsage) {
			t.Errorf("tasks %q: err = %v, want ErrUsage", tasks, err)
		}

		// Refused rather than run bare: ./gradlew with no task runs the
		// default task, which is not what an empty --tasks asked for.
		if len(ops.args) != 0 {
			t.Errorf("tasks %q: invoked gradlew with %v", tasks, ops.args)
		}
	}
}
