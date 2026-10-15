// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appbuild "github.com/diggsweden/forge-ci/internal/app/build"
	"github.com/diggsweden/forge-ci/internal/domain/ci"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

func TestXcodeArtifactName_EmitsNameWithTag(t *testing.T) {
	t.Parallel()
	sink := fakeoutputsink.New(t)

	var out bytes.Buffer

	if err := appbuild.XcodeArtifactName(context.Background(), sink, &out, appbuild.XcodeArtifactNameInput{RepositoryName: "demo", IncludeTag: true, RefName: "v1.2.3"}); err != nil {
		t.Fatal(err)
	}

	if got := sink.Single("ipa-name"); got != "demo-v1.2.3" {
		t.Errorf("ipa-name = %q", got)
	}

	if !strings.Contains(out.String(), "IPA artifact: demo-v1.2.3") {
		t.Errorf("out = %s", out.String())
	}
}

func TestXcodeArtifactName_IncludeTagWithEmptyRefKeepsBaseName(t *testing.T) {
	t.Parallel()
	sink := fakeoutputsink.New(t)

	if err := appbuild.XcodeArtifactName(context.Background(), sink, io.Discard, appbuild.XcodeArtifactNameInput{RepositoryName: "demo", IncludeTag: true}); err != nil {
		t.Fatal(err)
	}

	if got := sink.Single("ipa-name"); got != "demo" {
		t.Errorf("ipa-name = %q", got)
	}
}

func TestXcodeXCConfig_DecodesAndWritesOwnerOnlyFile(t *testing.T) {
	t.Parallel()
	fsys := testfs.NewReal(t)

	body := []byte("SETTING = value\n")

	decoded, err := appbuild.DecodeXcodeXCConfigForTest(base64.StdEncoding.EncodeToString(body))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(decoded, body) {
		t.Errorf("decoded = %q", decoded)
	}

	path, err := appbuild.WriteXcodeXCConfigForTest(decoded, fsys.Root)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(path, filepath.Join(fsys.Root, "ci-")) || !strings.HasSuffix(path, ".xcconfig") {
		t.Errorf("xcconfig path = %q", path)
	}

	got, err := os.ReadFile(path) //nolint:gosec // test fixture; path is the returned xcconfig path.
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, body) {
		t.Errorf("body = %q", got)
	}

	// The xcconfig is a secret -- it carries build settings including
	// signing identity and team id -- and lands in a shared temp dir.
	// os.CreateTemp happens to make it 0600; asserting it keeps that true
	// if the write is ever done another way.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("xcconfig mode = %v, want 0600", perm)
	}
}

func TestXcodeXCConfig_MissingSecretNoops(t *testing.T) {
	t.Parallel()
	fsys := testfs.NewReal(t)

	decoded, err := appbuild.DecodeXcodeXCConfigForTest("")
	if err != nil {
		t.Fatal(err)
	}

	if decoded != nil {
		t.Errorf("decoded = %q for a run with no xcconfig", decoded)
	}

	path, err := appbuild.WriteXcodeXCConfigForTest(decoded, fsys.Root)
	if err != nil {
		t.Fatal(err)
	}

	// An empty path is what tells the archive step to pass no -xcconfig at
	// all; an empty file handed to xcodebuild would be a different build.
	if path != "" {
		t.Errorf("xcconfig path = %q for a run with no xcconfig", path)
	}

	entries, err := os.ReadDir(fsys.Root)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 0 {
		t.Errorf("wrote %v for a run with no xcconfig", entries)
	}
}

// xcodeVersionInfo runs the two metadata steps XcodeReleaseBuild runs, in its
// order: resolve the selected project's source versions, then report them as
// the `version` and `build` outputs. The release build does other work between
// the two; a resolution refusal there, as here, reaches no report.
func xcodeVersionInfo(ctx context.Context, sink ci.OutputSink, stderr io.Writer, annot output.Annotator, in appbuild.XcodeVersionInfoInput) error {
	got, missing, err := appbuild.ResolveXcodeVersionInfoForTest(in)
	if err != nil {
		return err
	}

	return appbuild.ReportXcodeVersionInfoForTest(ctx, sink, stderr, annot, got, missing)
}

// makeXcodeProject writes a minimal .xcodeproj/project.pbxproj under the
// provided testfs root containing the given key/value pairs.
func makeXcodeProject(t *testing.T, fsys *testfs.Real, name, version, build string) string {
	t.Helper()

	dir := fsys.Path(name + ".xcodeproj")
	body := "MARKETING_VERSION = " + version + ";\nCURRENT_PROJECT_VERSION = " + build + ";\n"
	fsys.WriteFile(filepath.Join(name+".xcodeproj", "project.pbxproj"), []byte(body))

	return dir
}

func TestXcodeVersionInfo_ExplicitProject(t *testing.T) {
	fsys := testfs.NewReal(t)
	root := fsys.Root
	proj := makeXcodeProject(t, fsys, "Demo", "1.2.3", "42")
	sink := fakeoutputsink.New(t)

	err := xcodeVersionInfo(context.Background(), sink, io.Discard, output.Annotator{}, appbuild.XcodeVersionInfoInput{
		Project: proj,
		Root:    root,
	})
	if err != nil {
		t.Fatalf("resolve and report Xcode version info: %v", err)
	}

	if got := sink.Single("version"); got != "1.2.3" {
		t.Errorf("version = %q", got)
	}

	if got := sink.Single("build"); got != "42" {
		t.Errorf("build = %q", got)
	}
}

func TestXcodeVersionInfo_PrintsStderrLine(t *testing.T) {
	fsys := testfs.NewReal(t)
	root := fsys.Root
	proj := makeXcodeProject(t, fsys, "Demo", "2.1.0", "15")
	sink := fakeoutputsink.New(t)

	var stderr bytes.Buffer

	err := xcodeVersionInfo(context.Background(), sink, &stderr, output.Annotator{}, appbuild.XcodeVersionInfoInput{
		Project: proj,
		Root:    root,
	})
	if err != nil {
		t.Fatalf("resolve and report Xcode version info: %v", err)
	}

	if got := strings.TrimSpace(stderr.String()); got != "Version: 2.1.0 (15)" {
		t.Errorf("stderr = %q", got)
	}
}

func TestXcodeVersionInfo_AutoDiscoversXcodeproj(t *testing.T) {
	fsys := testfs.NewReal(t)
	root := fsys.Root
	makeXcodeProject(t, fsys, "Demo", "9.9.9", "100")

	sink := fakeoutputsink.New(t)
	if err := xcodeVersionInfo(context.Background(), sink, io.Discard, output.Annotator{}, appbuild.XcodeVersionInfoInput{
		Root: root,
	}); err != nil {
		t.Fatalf("resolve and report Xcode version info: %v", err)
	}

	if got := sink.Single("version"); got != "9.9.9" {
		t.Errorf("version = %q", got)
	}

	if got := sink.Single("build"); got != "100" {
		t.Errorf("build = %q", got)
	}
}

// With no selector, retain the historical first-project behavior. This is not
// evidence that the discovered project belongs to any selected archive scheme.
func TestXcodeVersionInfo_AutoDiscoveryIsDeterministic(t *testing.T) {
	t.Parallel()

	for range 5 {
		fsys := testfs.NewReal(t)
		// Written second-first, so insertion order and lexical order differ.
		makeXcodeProject(t, fsys, "Zebra", "2.0.0", "200")
		makeXcodeProject(t, fsys, "Alpha", "1.0.0", "100")

		sink := fakeoutputsink.New(t)
		if err := xcodeVersionInfo(context.Background(), sink, io.Discard, output.Annotator{}, appbuild.XcodeVersionInfoInput{
			Root: fsys.Root,
		}); err != nil {
			t.Fatal(err)
		}

		if got := sink.Single("version"); got != "1.0.0" {
			t.Fatalf("version = %q, want the lexically first project's 1.0.0", got)
		}

		if got := sink.Single("build"); got != "100" {
			t.Fatalf("build = %q, want the lexically first project's 100", got)
		}
	}
}

func TestXcodeVersionInfo_NoProjectFallsBackToUnknown(t *testing.T) {
	fsys := testfs.NewReal(t)
	root := fsys.Root // no .xcodeproj
	sink := fakeoutputsink.New(t)

	var stderr bytes.Buffer
	if err := xcodeVersionInfo(context.Background(), sink, &stderr, output.NewAnnotator(&stderr, output.FormatGitHub), appbuild.XcodeVersionInfoInput{
		Root: root,
	}); err != nil {
		t.Fatalf("resolve and report Xcode version info: %v", err)
	}

	if got := sink.Single("version"); got != "unknown" {
		t.Errorf("version = %q, want unknown", got)
	}

	if got := sink.Single("build"); got != "unknown" {
		t.Errorf("build = %q, want unknown", got)
	}

	if !strings.Contains(stderr.String(), "::warning::Could not determine version from project file") {
		t.Errorf("missing warning in stderr:\n%s", stderr.String())
	}
}
