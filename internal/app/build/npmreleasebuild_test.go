// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	appbuild "github.com/diggsweden/forge-ci/internal/app/build"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

var errNPMTestsFailed = errors.New("test assertion failed")

type fakeNPMRunner struct {
	calls   [][]string
	failOn  map[string]error // first-arg → error
	dirs    []string
	outputs []io.Writer
	errors  []io.Writer
}

func (f *fakeNPMRunner) RunInherit(_ context.Context, dir string, stdout, stderr io.Writer, args ...string) error {
	f.calls = append(f.calls, args)
	f.dirs = append(f.dirs, dir)
	f.outputs = append(f.outputs, stdout)
	f.errors = append(f.errors, stderr)

	if len(args) > 0 && f.failOn != nil {
		if err := f.failOn[args[0]]; err != nil {
			return err
		}
	}

	if len(args) == 4 && args[0] == "pack" && args[1] == "--json" && args[2] == "--pack-destination" {
		if err := os.WriteFile(filepath.Join(args[3], "org-app-1.0.0.tgz"), npmTarballBytes("1.0.0"), 0o600); err != nil {
			return err
		}

		_, err := io.WriteString(stdout, `[{"name":"@org/app","version":"1.0.0","filename":"org-app-1.0.0.tgz"}]`)

		return err
	}

	if len(args) == 6 && args[0] == "--yes" {
		return os.WriteFile(args[5], []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6","version":1}`), 0o600)
	}

	return nil
}

func npmTarballBytes(version string) []byte {
	var buffer bytes.Buffer

	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	body := `{"name":"@org/app","version":"` + version + `"}`
	_ = tarWriter.WriteHeader(&tar.Header{Name: "package/package.json", Mode: 0o600, Size: int64(len(body))})
	_, _ = io.WriteString(tarWriter, body)
	_ = tarWriter.Close()
	_ = gzipWriter.Close()

	return buffer.Bytes()
}

func (f *fakeNPMRunner) firstArgs() []string {
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c[0])
	}

	return out
}

func newNPMDir(t *testing.T) string {
	t.Helper()
	fsys := testfs.NewReal(t)
	fsys.WriteFile("package.json", []byte(`{"name":"@org/app","version":"1.0.0","scripts":{"build":"tsc","test":"node --test"}}`))

	return fsys.Root
}

func TestNPMReleaseBuild_RequiresCommittedReleaseVersionBeforeAnyTool(t *testing.T) {
	t.Parallel()
	directory := newNPMDir(t)
	npm, npx := &fakeNPMRunner{}, &fakeNPMRunner{}

	err := appbuild.NPMReleaseBuild(t.Context(), nil, npm, npx, output.Annotator{}, io.Discard, io.Discard, appbuild.NPMReleaseBuildInput{
		Dir: directory, ExpectVersion: "v1.0.1",
	})
	if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "release commit") {
		t.Fatalf("wrong committed package version did not fail preflight: %v", err)
	}

	if len(npm.calls) != 0 || len(npx.calls) != 0 {
		t.Fatalf("version mismatch reached npm/npx: %v %v", npm.calls, npx.calls)
	}

	body, err := os.ReadFile(filepath.Join(directory, "package.json"))
	if err != nil || !strings.Contains(string(body), `"version":"1.0.0"`) {
		t.Fatalf("build rewrote the source version: %s %v", body, err)
	}
}

func TestNPMReleaseBuild_RunsFullSequence(t *testing.T) {
	t.Parallel()
	dir := newNPMDir(t)
	npmRunner := &fakeNPMRunner{}
	npxRunner := &fakeNPMRunner{}

	var out bytes.Buffer

	err := appbuild.NPMReleaseBuild(context.Background(), &recordingSummarySink{}, npmRunner, npxRunner, output.Annotator{}, &out, &out, appbuild.NPMReleaseBuildInput{
		Dir: dir, EnableBuildSBOM: true,
		PackageScope:    "@org",
		SBOMToolVersion: "4.2.1",
	})
	if err != nil {
		t.Fatal(err)
	}

	// npm ran: ci, test, run (build script), pack — in order.
	if got := strings.Join(npmRunner.firstArgs(), ","); got != "ci,test,run,pack" {
		t.Errorf("npm calls = %v, want ci,test,run,pack", npmRunner.firstArgs())
	}

	// npx ran cyclonedx-npm once with the pinned version.
	if len(npxRunner.calls) != 1 || !strings.Contains(strings.Join(npxRunner.calls[0], " "), "@cyclonedx/cyclonedx-npm@4.2.1") {
		t.Errorf("npx calls = %v", npxRunner.calls)
	}

	require.Equal(t, [][]string{{"ci"}, {"test"}, {"run", "build"}, {"pack", "--json", "--pack-destination", npmRunner.calls[3][3]}}, npmRunner.calls)
	require.Equal(t, []string{"--yes", "@cyclonedx/cyclonedx-npm@4.2.1", "--output-format", "json", "--output-file", npxRunner.calls[0][5]}, npxRunner.calls[0])
	require.Equal(t, "bom.json", filepath.Base(npxRunner.calls[0][5]))
	require.Equal(t, dir, filepath.Dir(filepath.Dir(npxRunner.calls[0][5])))

	for i, callDir := range npmRunner.dirs {
		require.Equal(t, dir, callDir)
		require.Same(t, &out, npmRunner.errors[i])

		if i < 3 {
			require.Same(t, &out, npmRunner.outputs[i])
		}
	}

	require.Equal(t, []string{dir}, npxRunner.dirs)
	require.Same(t, &out, npxRunner.outputs[0])
	require.Same(t, &out, npxRunner.errors[0])

	body, err := os.ReadFile(filepath.Join(dir, "org-app-1.0.0.tgz"))
	require.NoError(t, err)
	require.Equal(t, npmTarballBytes("1.0.0"), body)
	body, err = os.ReadFile(filepath.Join(dir, "bom.json"))
	require.NoError(t, err)
	require.True(t, json.Valid(body))
	require.JSONEq(t, `{"bomFormat":"CycloneDX","specVersion":"1.6","version":1}`, string(body))
}

func TestNPMReleaseBuild_SkipTests(t *testing.T) {
	t.Parallel()

	npmRunner := &fakeNPMRunner{}

	err := appbuild.NPMReleaseBuild(context.Background(), &recordingSummarySink{}, npmRunner, &fakeNPMRunner{}, output.Annotator{}, io.Discard, io.Discard, appbuild.NPMReleaseBuildInput{
		Dir: newNPMDir(t), SkipTests: true, EnableBuildSBOM: false,
	})
	if err == nil || len(npmRunner.calls) != 0 {
		t.Fatalf("consumer test opt-out was not refused before effects: err=%v calls=%v", err, npmRunner.calls)
	}
}

func TestNPMReleaseBuild_FailingTestsStopBeforeBuildSBOMAndPack(t *testing.T) {
	t.Parallel()

	npmRunner := &fakeNPMRunner{failOn: map[string]error{"test": errNPMTestsFailed}}

	npxRunner := &fakeNPMRunner{}
	summary := &recordingSummarySink{}

	err := appbuild.NPMReleaseBuild(t.Context(), summary, npmRunner, npxRunner, output.Annotator{}, io.Discard, io.Discard, appbuild.NPMReleaseBuildInput{
		Dir: newNPMDir(t), EnableBuildSBOM: true,
		SBOMToolVersion: "4.2.1",
	})
	if !errors.Is(err, errNPMTestsFailed) {
		t.Fatalf("test failure was not returned: %v", err)
	}

	if got := strings.Join(npmRunner.firstArgs(), ","); got != "ci,test" {
		t.Errorf("npm continued after failing tests: %v", npmRunner.firstArgs())
	}

	if len(npxRunner.calls) != 0 || summary.buf.Len() != 0 {
		t.Fatalf("failed tests must not generate an SBOM or successful build summary")
	}
}

func TestNPMReleaseBuild_MissingTestScriptIsReportedAsSkipped(t *testing.T) {
	t.Parallel()

	dir := newNPMDir(t)
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"@org/app","version":"1.0.0","scripts":{"build":"tsc"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	npmRunner := &fakeNPMRunner{}
	summary := &recordingSummarySink{}

	var out bytes.Buffer

	err := appbuild.NPMReleaseBuild(t.Context(), summary, npmRunner, &fakeNPMRunner{}, output.Annotator{}, &out, io.Discard, appbuild.NPMReleaseBuildInput{
		Dir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(npmRunner.firstArgs(), ","); got != "ci,run,pack" {
		t.Fatalf("missing test script should not execute npm test: %s", got)
	}

	if !strings.Contains(out.String(), "tests not run") || !strings.Contains(summary.buf.String(), "Tests:** ⊘ Skipped") {
		t.Fatalf("absent tests were not reported honestly: output=%s summary=%s", out.String(), summary.buf.String())
	}
}

func TestNPMReleaseBuild_NoBuildSBOM(t *testing.T) {
	t.Parallel()

	npxRunner := &fakeNPMRunner{}
	npmRunner := &fakeNPMRunner{}

	err := appbuild.NPMReleaseBuild(context.Background(), &recordingSummarySink{}, npmRunner, npxRunner, output.Annotator{}, io.Discard, io.Discard, appbuild.NPMReleaseBuildInput{
		Dir: newNPMDir(t), EnableBuildSBOM: false,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(npxRunner.calls) != 0 {
		t.Errorf("npx ran despite EnableBuildSBOM=false: %v", npxRunner.calls)
	}

	// Dropping the SBOM must drop only the SBOM.
	if got := strings.Join(npmRunner.firstArgs(), ","); got != "ci,test,run,pack" {
		t.Errorf("npm calls = %v, want ci,test,run,pack", npmRunner.firstArgs())
	}
}

func TestNPMReleaseBuild_ScopeMismatchFails(t *testing.T) {
	t.Parallel()

	npmRunner := &fakeNPMRunner{}
	npxRunner := &fakeNPMRunner{}
	summary := &recordingSummarySink{}

	var stderr bytes.Buffer

	err := appbuild.NPMReleaseBuild(context.Background(), summary, npmRunner, npxRunner, output.NewAnnotator(&stderr, output.FormatGitHub), io.Discard, io.Discard, appbuild.NPMReleaseBuildInput{
		Dir:          newNPMDir(t),
		PackageScope: "@other",
	})
	if !errors.Is(err, errs.ErrInvalidConfig) {
		t.Fatalf("err = %v, want ErrInvalidConfig", err)
	}

	// The scope decides where the package would be published, so it is
	// checked before anything is installed, built or packed.
	if len(npmRunner.calls) != 0 || len(npxRunner.calls) != 0 {
		t.Errorf("ran tools despite the scope mismatch: npm=%v npx=%v", npmRunner.calls, npxRunner.calls)
	}

	if summary.buf.Len() != 0 {
		t.Errorf("wrote a summary for a build that never started: %q", summary.buf.String())
	}

	if !strings.Contains(stderr.String(), "must be scoped as @other/<pkg>") {
		t.Errorf("stderr = %q", stderr.String())
	}
}
