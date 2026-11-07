// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/osfs"
	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
)

type assembleDistCancelDownloader struct {
	assembleDistDownloader
	cancel context.CancelFunc
}

func (d *assembleDistCancelDownloader) DownloadRunArtifact(ctx context.Context, in provider.RunArtifactDownload) (provider.RunArtifactInfo, error) {
	if in.Name == "final" && d.cancel != nil {
		d.cancel()
	}

	return d.assembleDistDownloader.DownloadRunArtifact(ctx, in)
}

func TestAssembleDist_FinalDownloadFailureSemantics(t *testing.T) {
	ordinaryErr := errors.New("download failed")
	for _, tt := range []struct {
		name     string
		failure  error
		cancel   bool
		required bool
		wantErr  error
	}{
		{name: "optional canceled", failure: context.Canceled, wantErr: context.Canceled},
		{name: "optional wrapped canceled", failure: fmt.Errorf("download: %w", context.Canceled), wantErr: context.Canceled},
		{name: "optional deadline", failure: context.DeadlineExceeded, wantErr: context.DeadlineExceeded},
		{name: "optional wrapped deadline", failure: fmt.Errorf("download: %w", context.DeadlineExceeded), wantErr: context.DeadlineExceeded},
		{name: "canceled context with not found", failure: os.ErrNotExist, cancel: true, wantErr: context.Canceled},
		{name: "canceled context with successful download", cancel: true, wantErr: context.Canceled},
		{name: "optional not found", failure: os.ErrNotExist},
		{name: "optional ordinary failure", failure: ordinaryErr},
		{name: "required ordinary failure", failure: ordinaryErr, required: true, wantErr: ordinaryErr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			chdirTempForAssembleDist(t)
			writeFileForAssembleDist(t, "dist/nested/keep.txt", "keep on failure\n")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			dl := &assembleDistCancelDownloader{
				assembleDistDownloader: assembleDistDownloader{fail: map[string]error{"final": tt.failure}},
			}
			if tt.cancel {
				dl.cancel = cancel
			}
			sink := fakeoutputsink.New(t)
			var stderr bytes.Buffer
			plan := fmt.Sprintf(`{"version":1,"items":[{"kind":"build_artifact","name":"build","path":"dist/","required":true},{"kind":"build_artifact","name":"final","path":"dist/","required":%t}]}`, tt.required)
			got, err := apprelease.AssembleDist(ctx, dl, osfs.New(), sink, &stderr, apprelease.AssembleDistInput{
				ArtifactTransferPlanJSON: plan,
				Path:                     "dist/",
				LedgerFiles:              "missing-ledger.json",
				PruneDirs:                true,
			})
			if len(dl.calls) != 2 || dl.calls[1].Name != "final" {
				t.Fatalf("download calls = %+v", dl.calls)
			}
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				if got.Digest != "" || sink.Single("digest") != "" || strings.Contains(stderr.String(), " digest:") {
					t.Fatalf("failed assembly published success: result=%+v outputs=%v stderr=%q", got, sink.AllScalar(), stderr.String())
				}
				if body := readFileForAssembleDist(t, "dist/nested/keep.txt"); body != "keep on failure\n" {
					t.Fatalf("nested file changed: %q", body)
				}
				if _, err := os.Stat("dist/release-images.json"); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failed download reached ledger merge: %v", err)
				}
				if strings.Contains(stderr.String(), "warning: optional artifact") {
					t.Fatalf("fatal download error treated as optional: %q", stderr.String())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Digest == "" || sink.Single("digest") != got.Digest {
				t.Fatalf("missing successful digest: result=%+v outputs=%v", got, sink.AllScalar())
			}
			if !strings.Contains(stderr.String(), "warning: optional artifact 'final' was not downloaded:") {
				t.Fatalf("missing optional warning: %q", stderr.String())
			}
			if _, err := os.Stat("dist/nested"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("successful assembly did not prune: %v", err)
			}
		})
	}
}

type assembleDistCancelWriter struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (w *assembleDistCancelWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("wrote empty ledger")) {
		w.cancel()
	}
	return w.Buffer.Write(p)
}

func TestAssembleDist_CancellationAfterLedgerMergeDoesNotPrune(t *testing.T) {
	chdirTempForAssembleDist(t)
	writeFileForAssembleDist(t, "dist/nested/keep.txt", "keep\n")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stderr := &assembleDistCancelWriter{cancel: cancel}
	sink := fakeoutputsink.New(t)
	got, err := apprelease.AssembleDist(ctx, &assembleDistDownloader{}, osfs.New(), sink, stderr, apprelease.AssembleDistInput{
		ArtifactNames: "build",
		LedgerFiles:   "missing-ledger.json",
		PruneDirs:     true,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
	if got.Digest != "" || sink.Single("digest") != "" || strings.Contains(stderr.String(), " digest:") {
		t.Fatalf("canceled assembly published success: result=%+v stderr=%q", got, stderr.String())
	}
	if body := readFileForAssembleDist(t, "dist/nested/keep.txt"); body != "keep\n" {
		t.Fatalf("nested file changed: %q", body)
	}
}

type assembleDistCancelWorkspace struct {
	cancel   context.CancelFunc
	acquired bool
}

func (*assembleDistCancelWorkspace) ContainDistPath(string) error { return nil }

func (w *assembleDistCancelWorkspace) AcquireDist(string, bool) (string, error) {
	w.acquired = true
	w.cancel()
	// Hashing this nonexistent path would fail with a filesystem error rather
	// than cancellation, so the error below also proves hashing was skipped.
	return "missing-dist", nil
}

func TestAssembleDist_CancellationBeforeHashing(t *testing.T) {
	chdirTempForAssembleDist(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	workspace := &assembleDistCancelWorkspace{cancel: cancel}
	sink := fakeoutputsink.New(t)
	var stderr bytes.Buffer
	got, err := apprelease.AssembleDist(ctx, &assembleDistDownloader{}, workspace, sink, &stderr, apprelease.AssembleDistInput{
		ArtifactTransferPlanJSON: `{"version":1,"items":[]}`,
	})
	if !workspace.acquired || !errors.Is(err, context.Canceled) {
		t.Fatalf("acquired = %t, error = %v, want cancellation after acquisition", workspace.acquired, err)
	}
	if got.Digest != "" || sink.Single("digest") != "" || strings.Contains(stderr.String(), " digest:") {
		t.Fatalf("canceled assembly published success: result=%+v stderr=%q", got, stderr.String())
	}
}
