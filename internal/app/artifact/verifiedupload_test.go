// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package artifact_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	appartifact "github.com/diggsweden/forge-ci/internal/app/artifact"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/stretchr/testify/require"
)

type readbackStore struct {
	mode               string
	uploads, downloads int
	destination        string
}

func (s *readbackStore) UploadRunArtifact(_ context.Context, _ provider.RunArtifactUpload) (provider.RunArtifactInfo, error) {
	s.uploads++
	if s.mode == "upload-error" {
		return provider.RunArtifactInfo{}, errors.New("uncertain finalization")
	}

	return provider.RunArtifactInfo{Name: "evidence", FileCount: 2}, nil
}

func (s *readbackStore) DownloadRunArtifact(_ context.Context, in provider.RunArtifactDownload) (provider.RunArtifactInfo, error) {
	s.downloads++

	s.destination = in.Dir
	if s.mode == "unreadable" {
		return provider.RunArtifactInfo{}, errors.New("unreadable")
	}

	if err := os.MkdirAll(filepath.Join(in.Dir, "nested"), 0o700); err != nil {
		return provider.RunArtifactInfo{}, err
	}

	files := map[string]string{"index.json": "original\n", "nested/empty": ""}

	switch s.mode {
	case "missing":
		delete(files, "nested/empty")
	case "modified":
		files["index.json"] = "substituted"
	case "extra":
		files["unexpected"] = "unexpected"
	case "hidden_extra":
		files[".unexpected"] = "unexpected"
	case "hidden":
		files[".included"] = "hidden"
	}

	for name, body := range files {
		if err := os.WriteFile(filepath.Join(in.Dir, name), []byte(body), 0o600); err != nil {
			return provider.RunArtifactInfo{}, err
		}
	}

	return provider.RunArtifactInfo{}, nil
}

func TestUploadVerifiedExactInventoryAndCleanup(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"valid", "unreadable", "missing", "modified", "extra", "hidden_extra", "upload-error"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(dir, "nested"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "index.json"), []byte("original\n"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "nested/empty"), nil, 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".excluded"), []byte("hidden"), 0o600))

			store := &readbackStore{mode: mode}

			var out bytes.Buffer

			_, err := appartifact.UploadVerified(t.Context(), store, store, nil, &out, provider.RunArtifactUpload{Name: "evidence", Dir: dir}, t.TempDir())
			if mode == "valid" {
				require.NoError(t, err)
				require.Contains(t, out.String(), "Uploaded and verified")
			} else {
				require.Error(t, err)
				require.Empty(t, out.String())
			}

			require.Equal(t, 1, store.uploads)

			if mode == "upload-error" {
				require.Zero(t, store.downloads)
			} else {
				require.Equal(t, 1, store.downloads)
				require.NoDirExists(t, filepath.Dir(store.destination))
			}

			require.FileExists(t, filepath.Join(dir, "index.json"))
		})
	}
}

func TestUploadVerifiedRejectsEmptyAndOptionalInventoryBeforeMutation(t *testing.T) {
	t.Parallel()

	for _, policy := range []provider.IfNoFilesPolicy{"", provider.IfNoFilesWarn, provider.IfNoFilesIgnore} {
		store := &readbackStore{}
		_, err := appartifact.UploadVerified(t.Context(), store, store, nil, nil, provider.RunArtifactUpload{Name: "empty", Dir: t.TempDir(), IfNoFiles: policy}, t.TempDir())
		require.Error(t, err)
		require.Zero(t, store.uploads)
		require.Zero(t, store.downloads)
	}
}

func TestUploadVerifiedReusesFileGlobAndHiddenSelection(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"files", "glob", "hidden"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(dir, "nested"), 0o700))
			index, empty := filepath.Join(dir, "index.json"), filepath.Join(dir, "nested/empty")
			require.NoError(t, os.WriteFile(index, []byte("original\n"), 0o600))
			require.NoError(t, os.WriteFile(empty, nil, 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".included"), []byte("hidden"), 0o600))

			in := provider.RunArtifactUpload{Name: "evidence"}

			switch mode {
			case "files":
				in.Files = []string{index, empty}
			case "glob":
				in.Paths = []string{filepath.Join(dir, "**/*")}
			case "hidden":
				in.Dir = dir
				in.IncludeHidden = true
			}

			store := &readbackStore{mode: mode}
			_, err := appartifact.UploadVerified(t.Context(), store, store, nil, nil, in, t.TempDir())
			require.NoError(t, err)
			require.Equal(t, 1, store.uploads)
			require.Equal(t, 1, store.downloads)
		})
	}
}
