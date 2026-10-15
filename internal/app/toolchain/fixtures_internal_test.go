// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package toolchain

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// The four mise subdirectory names appear throughout. goconst counts a string
// across the whole package, test files included, so leaving these as literals
// here would push the product file over the threshold without that file
// changing.
const (
	cacheSubdir  = "cache"
	dataSubdir   = "data"
	configSubdir = "config"
	stateSubdir  = "state"
)

type changelogTransport func(*http.Request) (*http.Response, error)

func (f changelogTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// Every case, including refusal controls, blocks the real transport. No parallel tests.
func blockChangelogHTTP(t *testing.T, reply func(*http.Request) (*http.Response, error)) {
	t.Helper()

	previous := http.DefaultTransport
	http.DefaultTransport = changelogTransport(reply)

	t.Cleanup(func() { http.DefaultTransport = previous })
}

func changelogTempDir(t *testing.T) string {
	t.Helper()

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	return root
}

func changelogFixtureRoot(t *testing.T) string {
	t.Helper()

	root := changelogTempDir(t)
	for _, pair := range [][2]string{{"HOME", "home"}, {runnerPathName, "empty-path"}, {"TMPDIR", "os-temp"}, {"TEMP", "os-temp"}, {"TMP", "os-temp"}} {
		dir := filepath.Join(root, pair[1])
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}

		t.Setenv(pair[0], dir)
	}

	blockChangelogHTTP(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusForbidden, Body: http.NoBody, Header: make(http.Header)}, nil
	})

	return root
}

func writeChangelogCanary(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(body), mode); err != nil { //nolint:gosec // owned inert canaries, never executed.
		t.Fatal(err)
	}
}

func changelogSymlink(t *testing.T, target, path string) {
	t.Helper()

	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

// Capture every owned entry without following links, including leaf identities.
func changelogUnchanged(t *testing.T, root string) func() {
	t.Helper()

	type entryState struct {
		info os.FileInfo
		body string
	}

	snapshot := func() map[string]entryState {
		state := make(map[string]entryState)

		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			info, err := entry.Info()
			if err != nil {
				return err
			}

			var body string

			switch {
			case info.Mode().IsRegular():
				var data []byte

				data, err = os.ReadFile(path) //nolint:gosec // test-owned tree is not concurrently modified.
				body = string(data)
			case info.Mode()&os.ModeSymlink != 0:
				body, err = os.Readlink(path)
			}

			state[path] = entryState{info: info, body: body}

			return err
		})
		if err != nil {
			t.Fatal(err)
		}

		return state
	}
	before := snapshot()

	return func() {
		t.Helper()

		after := snapshot()
		if len(after) != len(before) {
			t.Errorf("publication changed owned entry count: got %d, want %d", len(after), len(before))
		}

		for path, old := range before {
			current, ok := after[path]
			if !ok || !os.SameFile(old.info, current.info) || old.info.Mode() != current.info.Mode() || old.body != current.body {
				t.Errorf("publication changed owned bytes, mode, identity or link spelling: %s", path)
			}
		}
	}
}
