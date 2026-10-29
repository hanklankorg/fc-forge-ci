// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"archive/zip"
	"bytes"
	"errors"
	"io/fs"
	"strings"
	"testing"
)

// GitLab artifact archives contain explicit directory records. Validate those
// records before ignoring them; production ZIPTree remains strictly file-only.
func snapshotArtifactZIPTree(body []byte, limit int64) (map[string][]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, err
	}

	tree := map[string][]byte{}
	seen := map[string]bool{}
	for _, file := range reader.File {
		directory := file.Mode().Type() == fs.ModeDir
		name := file.Name
		if directory {
			name = strings.TrimSuffix(name, "/")
		}

		if !cleanZIPEntryName(name) || strings.ContainsAny(name, ":\x00") || seen[name] {
			return nil, errors.New("invalid or duplicate snapshot artifact path")
		}
		for _, part := range strings.Split(name, "/") {
			if strings.EqualFold(part, ".git") || strings.EqualFold(part, ".gitmodules") || strings.EqualFold(part, ".gitconfig") {
				return nil, errors.New("reserved snapshot artifact entry")
			}
		}

		seen[name] = true
		if directory {
			if !strings.HasSuffix(file.Name, "/") {
				return nil, errors.New("directory record lacks canonical suffix")
			}
			if _, err := readZIPEntry(file, 0); err != nil {
				return nil, err
			}
			continue
		}
		if !file.Mode().IsRegular() {
			return nil, errors.New("snapshot artifact entry is not a regular file or directory")
		}

		content, err := readZIPEntry(file, limit)
		if err != nil {
			return nil, err
		}

		limit -= int64(len(content))
		tree[name] = content
	}

	return tree, nil
}

func TestSnapshotArtifactZIPAcceptsCleanDirectoriesOnly(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     fs.FileMode
		accepted bool
	}{
		{"snapshot-context/", fs.ModeDir | 0755, true},
		{"../escape/", fs.ModeDir | 0755, false},
		{"root/../escape/", fs.ModeDir | 0755, false},
		{"/absolute/", fs.ModeDir | 0755, false},
		{".git/", fs.ModeDir | 0755, false},
		{"files/.GIT/config", 0644, false},
		{".gitmodules", 0644, false},
		{"link", fs.ModeSymlink | 0777, false},
		{"C:drive/", fs.ModeDir | 0755, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buffer bytes.Buffer
			writer := zip.NewWriter(&buffer)
			header := &zip.FileHeader{Name: tc.name}
			header.SetMode(tc.mode)
			if _, err := writer.CreateHeader(header); err != nil {
				t.Fatal(err)
			}
			file, err := writer.Create("snapshot-context/snapshot-plan.json")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.Write([]byte("{}")); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}

			tree, err := snapshotArtifactZIPTree(buffer.Bytes(), 1024)
			if (err == nil) != tc.accepted {
				t.Fatalf("accepted=%t: %v", tc.accepted, err)
			}
			if tc.accepted && (len(tree) != 1 || string(tree["snapshot-context/snapshot-plan.json"]) != "{}") {
				t.Fatal("directory inspection changed delivered files")
			}
		})
	}
}
