// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeprovider"
)

func writeFile(t *testing.T, dir, name string) {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestUploadAttachments_UploadsEveryMatchAndNothingElse covers the glob: both
// .tgz files are uploaded against the release tag, and the .txt beside them is
// not.
//
// The uploads are compared by name. Counting them and checking each ends in
// .tgz -- what this did before -- is satisfied by uploading the same file
// twice, or by uploading some other .tgz entirely, and says nothing about the
// file the fixture put there to be left alone.
func TestUploadAttachments_UploadsEveryMatchAndNothingElse(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "asset-a.tgz")
	writeFile(t, dir, "asset-b.tgz")
	writeFile(t, dir, "ignore.txt")

	fp := fakeprovider.New(t)
	if err := apprelease.UploadAttachments(context.Background(), fp, &bytes.Buffer{}, output.Annotator{}, apprelease.UploadAttachmentsInput{
		Tag:        "v1.0.0",
		Pattern:    "*.tgz",
		WorkingDir: dir,
	}); err != nil {
		t.Fatal(err)
	}

	calls := fp.UploadReleaseAssetCalls()

	gotFiles := make([]string, 0, len(calls))

	for _, c := range calls {
		gotFiles = append(gotFiles, c.File)

		if c.Tag != "v1.0.0" {
			t.Errorf("file %s uploaded against tag %q, want v1.0.0", c.File, c.Tag)
		}
	}

	wantFiles := []string{filepath.Join(dir, "asset-a.tgz"), filepath.Join(dir, "asset-b.tgz")}
	if !slices.Equal(gotFiles, wantFiles) {
		t.Errorf("uploaded = %v, want %v", gotFiles, wantFiles)
	}
}

func TestUploadAttachments_NoMatchesNoOps(t *testing.T) {
	dir := t.TempDir()

	fp := fakeprovider.New(t)
	if err := apprelease.UploadAttachments(context.Background(), fp, &bytes.Buffer{}, output.Annotator{}, apprelease.UploadAttachmentsInput{
		Tag:        "v1.0.0",
		Pattern:    "*.tgz",
		WorkingDir: dir,
	}); err != nil {
		t.Fatal(err)
	}

	if got := len(fp.UploadReleaseAssetCalls()); got != 0 {
		t.Errorf("uploads = %d, want 0", got)
	}
}

func TestUploadAttachments_EmptyPatternNoOps(t *testing.T) {
	fp := fakeprovider.New(t)
	if err := apprelease.UploadAttachments(context.Background(), fp, &bytes.Buffer{}, output.Annotator{}, apprelease.UploadAttachmentsInput{
		Tag: "v1.0.0",
	}); err != nil {
		t.Fatal(err)
	}

	if got := len(fp.UploadReleaseAssetCalls()); got != 0 {
		t.Errorf("uploads = %d, want 0", got)
	}
}

func TestUploadAttachments_RejectsPathTraversal(t *testing.T) {
	fp := fakeprovider.New(t)

	err := apprelease.UploadAttachments(context.Background(), fp, &bytes.Buffer{}, output.Annotator{}, apprelease.UploadAttachmentsInput{
		Tag:     "v1.0.0",
		Pattern: "../etc/passwd",
	})
	if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), `contains ".."`) {
		t.Fatalf("err = %v, want ErrValidation naming the traversal", err)
	}

	if calls := fp.UploadReleaseAssetCalls(); len(calls) != 0 {
		t.Errorf("uploaded %+v despite a traversing pattern", calls)
	}
}

func TestUploadAttachments_RejectsAbsolutePath(t *testing.T) {
	fp := fakeprovider.New(t)

	err := apprelease.UploadAttachments(context.Background(), fp, &bytes.Buffer{}, output.Annotator{}, apprelease.UploadAttachmentsInput{
		Tag:     "v1.0.0",
		Pattern: "/etc/passwd",
	})
	if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "must be relative") {
		t.Fatalf("err = %v, want ErrValidation naming the containment rule", err)
	}

	if calls := fp.UploadReleaseAssetCalls(); len(calls) != 0 {
		t.Errorf("uploaded %+v despite an absolute pattern", calls)
	}
}

func TestUploadAttachments_RejectsMissingTag(t *testing.T) {
	err := apprelease.UploadAttachments(context.Background(), fakeprovider.New(t), io.Discard, output.Annotator{}, apprelease.UploadAttachmentsInput{
		Pattern: "*.tgz",
	})
	if !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), "tag is required") {
		t.Fatalf("err = %v, want ErrUsage naming the missing tag", err)
	}
}

// partialUploader fails exactly the files it is told to and records every
// attempt. fakeprovider can only fail all uploads or none, which is why the
// partial case below needs its own double.
type partialUploader struct {
	failBasenames map[string]bool
	refused       bool
	attempted     []string
}

func (p *partialUploader) UploadReleaseAsset(_ context.Context, _ string, file string) error {
	p.attempted = append(p.attempted, filepath.Base(file))
	if p.failBasenames[filepath.Base(file)] {
		if p.refused {
			return fmt.Errorf("HTTP 403: %w", errs.ErrPermissionDenied)
		}

		return errUploadNetworkDown
	}

	return nil
}

// A release that lacks one of its files is not published: any failed upload
// fails the command, after the remaining files were attempted, and the error
// names each file the release lacks.
func TestUploadAttachments_AnyFailedUploadFailsAfterAttemptingTheRest(t *testing.T) {
	for name, failing := range map[string][]string{
		"one failure among several": {"a.tgz.sig"},
		"every upload failing":      {"a.tgz", "a.tgz.sig", "b.tgz"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "a.tgz")
			writeFile(t, dir, "a.tgz.sig")
			writeFile(t, dir, "b.tgz")

			uploader := &partialUploader{failBasenames: map[string]bool{}}
			for _, file := range failing {
				uploader.failBasenames[file] = true
			}

			var stderr bytes.Buffer

			err := apprelease.UploadAttachments(context.Background(), uploader, &bytes.Buffer{},
				output.NewAnnotator(&stderr, output.FormatGitHub), apprelease.UploadAttachmentsInput{Tag: "v1.0.0", Pattern: "*.tgz,*.sig", WorkingDir: dir})
			if !errors.Is(err, errs.ErrDependencyUnavailable) {
				t.Fatalf("err = %v, want ErrDependencyUnavailable", err)
			}

			// A failure must not abort the loop.
			if want := []string{"a.tgz", "a.tgz.sig", "b.tgz"}; !slices.Equal(uploader.attempted, want) {
				t.Errorf("attempted = %v, want %v", uploader.attempted, want)
			}

			for _, file := range []string{"a.tgz", "a.tgz.sig", "b.tgz"} {
				failed := slices.Contains(failing, file)
				if named := strings.Contains(err.Error(), filepath.Join(dir, file)+",") || strings.Contains(err.Error(), filepath.Join(dir, file)+":"); named != failed {
					t.Errorf("%s named as failed = %t, want %t: %v", file, named, failed, err)
				}

				if failed && !strings.Contains(stderr.String(), "Failed to upload "+filepath.Join(dir, file)+":") {
					t.Errorf("no warning for %s: %s", file, stderr.String())
				}
			}
		})
	}
}

// An upload the forge refused is not an unavailable dependency: the next
// attempt is refused too.
func TestUploadAttachments_ARefusedUploadIsNotUnavailable(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.tgz")

	err := apprelease.UploadAttachments(context.Background(), &partialUploader{failBasenames: map[string]bool{"a.tgz": true}, refused: true}, &bytes.Buffer{},
		output.NewAnnotator(&bytes.Buffer{}, output.FormatGitHub), apprelease.UploadAttachmentsInput{Tag: "v1.0.0", Pattern: "*.tgz", WorkingDir: dir})
	if !errors.Is(err, errs.ErrValidation) || errors.Is(err, errs.ErrDependencyUnavailable) {
		t.Fatalf("err = %v, want a failure that is not unavailable", err)
	}
}

// Named so the double returns an identity rather than an ad-hoc message.
var errUploadNetworkDown error = &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}

func TestUploadAttachments_SkipsDirectories(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	writeFile(t, dir, "asset.tgz")

	fp := fakeprovider.New(t)
	if err := apprelease.UploadAttachments(context.Background(), fp, &bytes.Buffer{}, output.Annotator{}, apprelease.UploadAttachmentsInput{
		Tag:        "v1.0.0",
		Pattern:    "*",
		WorkingDir: dir,
	}); err != nil {
		t.Fatal(err)
	}

	gotFiles := make([]string, 0, 1)
	for _, c := range fp.UploadReleaseAssetCalls() {
		gotFiles = append(gotFiles, c.File)
	}

	// "*" matches the directory too. It must not be handed to the provider,
	// which would try to upload it as a file.
	wantFiles := []string{filepath.Join(dir, "asset.tgz")}
	if !slices.Equal(gotFiles, wantFiles) {
		t.Errorf("uploaded = %v, want %v", gotFiles, wantFiles)
	}
}

// TestUploadAttachments_SelectedFilesCannotLeaveTheWorkingDirectory is the
// boundary control the pattern-level refusals above do not give.
//
// Those two tests reject a pattern string containing ".." or a leading "/",
// which is the only escape route if the workspace itself is trustworthy. It
// is not: a release job checks out a repository and runs whatever is in it,
// and a symlink committed to that repository is part of the workspace before
// any pattern is read. Each case below uses a pattern that passes the string
// check unchanged and reaches something the working directory does not
// contain — so what is being asserted is that no such file is handed to the
// provider, and the provider is asked to prove it saw nothing.
//
// The two directory-link cases are the ones that were escaping: a link whose
// own final component is the match was already skipped, because a symlink is
// not a regular file, but a link used as an intermediate path component put a
// genuine regular file from outside the workspace on the upload list.
func TestUploadAttachments_SelectedFilesCannotLeaveTheWorkingDirectory(t *testing.T) {
	for name, tc := range map[string]struct {
		pattern string
		// setup builds a layout whose parent holds secret.tgz and returns the
		// working directory to hand to UploadAttachments.
		setup func(t *testing.T, base, work string)
	}{
		"a link to the parent directory": {
			pattern: "*/*.tgz",
			setup: func(t *testing.T, _, work string) {
				t.Helper()
				symlink(t, "..", filepath.Join(work, "up"))
			},
		},
		"a link to an outside directory": {
			pattern: "vendor/*.tgz",
			setup: func(t *testing.T, base, work string) {
				t.Helper()
				symlink(t, base, filepath.Join(work, "vendor"))
			},
		},
		"a link naming an outside file directly": {
			pattern: "*.tgz",
			setup: func(t *testing.T, base, work string) {
				t.Helper()
				symlink(t, filepath.Join(base, "secret.tgz"), filepath.Join(work, "leak.tgz"))
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			work := filepath.Join(base, "workspace")

			if err := os.Mkdir(work, 0o750); err != nil {
				t.Fatal(err)
			}

			writeFile(t, base, "secret.tgz")
			tc.setup(t, base, work)

			fp := fakeprovider.New(t)

			var out bytes.Buffer

			err := apprelease.UploadAttachments(context.Background(), fp, &out, output.Annotator{}, apprelease.UploadAttachmentsInput{
				Tag:        "v1.0.0",
				Pattern:    tc.pattern,
				WorkingDir: work,
			})
			if err != nil {
				t.Fatalf("err = %v, want the escape to be skipped rather than fatal", err)
			}

			if calls := fp.UploadReleaseAssetCalls(); len(calls) != 0 {
				t.Errorf("uploaded %+v from outside the working directory", calls)
			}

			if !strings.Contains(out.String(), "nothing to upload") {
				t.Errorf("the empty result was not reported:\n%s", out.String())
			}
		})
	}
}

// TestUploadAttachments_LinksDoNotHideRealMatches is the positive control for
// the test above: a working directory containing an escaping link still
// uploads the files that genuinely live in it. Without this, refusing
// everything would satisfy the boundary assertions.
func TestUploadAttachments_LinksDoNotHideRealMatches(t *testing.T) {
	outside := t.TempDir()
	work := t.TempDir()

	writeFile(t, outside, "secret.tgz")
	writeFile(t, work, "real.tgz")
	symlink(t, filepath.Join(outside, "secret.tgz"), filepath.Join(work, "leak.tgz"))

	fp := fakeprovider.New(t)
	if err := apprelease.UploadAttachments(context.Background(), fp, io.Discard, output.Annotator{}, apprelease.UploadAttachmentsInput{
		Tag:        "v1.0.0",
		Pattern:    "*.tgz",
		WorkingDir: work,
	}); err != nil {
		t.Fatal(err)
	}

	got := make([]string, 0, 1)
	for _, c := range fp.UploadReleaseAssetCalls() {
		got = append(got, c.File)
	}

	if want := []string{filepath.Join(work, "real.tgz")}; !slices.Equal(got, want) {
		t.Errorf("uploaded = %v, want %v", got, want)
	}
}

// TestUploadAttachments_RejectsAnUnopenableWorkingDirectory keeps the new
// containment root from failing open: if the directory cannot be opened, the
// answer is a classified error, not an empty match set that reads as success.
func TestUploadAttachments_RejectsAnUnopenableWorkingDirectory(t *testing.T) {
	fp := fakeprovider.New(t)

	err := apprelease.UploadAttachments(context.Background(), fp, io.Discard, output.Annotator{}, apprelease.UploadAttachmentsInput{
		Tag:        "v1.0.0",
		Pattern:    "*.tgz",
		WorkingDir: filepath.Join(t.TempDir(), "absent"),
	})
	if !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}

	if calls := fp.UploadReleaseAssetCalls(); len(calls) != 0 {
		t.Errorf("uploaded %+v despite an unusable working directory", calls)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()

	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, target, err)
	}
}
