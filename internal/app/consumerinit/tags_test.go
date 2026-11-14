// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package consumerinit_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appinit "github.com/diggsweden/forge-ci/internal/app/consumerinit"
)

type recordingTagRemote struct {
	fakeRemote

	resolved, exported string
	lists              int
}

func (r *recordingTagRemote) VersionTags(ctx context.Context, url string) ([]string, error) {
	r.lists++

	return r.fakeRemote.VersionTags(ctx, url)
}

func (r *recordingTagRemote) TagCommit(ctx context.Context, url, tag string) (string, error) {
	r.resolved = tag

	return r.fakeRemote.TagCommit(ctx, url, tag)
}

func (r *recordingTagRemote) Export(ctx context.Context, url, tag, dest string, paths []string) ([]string, error) {
	r.exported = tag

	return r.fakeRemote.Export(ctx, url, tag, dest, paths)
}

func TestRunReleaseTagSelectionPreservesExactRef(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, ref, want string
		tags            []string
	}{
		{name: "catalog", tags: []string{"0.90.0"}, want: "0.90.0"},
		{name: "mixed", tags: []string{"v0.90.0", "0.91.0"}, want: "0.91.0"},
		{name: "tie", tags: []string{"0.90.0", "v0.90.0"}, want: "v0.90.0"},
		{name: "explicit_catalog", ref: "0.90.0", tags: []string{"v0.91.0"}, want: "0.90.0"},
		{name: "explicit_prefixed", ref: "v0.90.0", tags: []string{"0.91.0"}, want: "v0.90.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			remote := &recordingTagRemote{tags: tc.tags, defaults: "forge: gitlab\n"}
			dir := t.TempDir()

			result, err := appinit.Run(t.Context(), appinit.Input{Remote: remote, URL: "https://gitlab.example.com/team/forge-ci", Ref: tc.ref, Dir: dir, Example: "go-cli"})
			if err != nil {
				t.Fatal(err)
			}

			if result.Source.Tag != tc.want || remote.resolved != tc.want || remote.exported != tc.want {
				t.Fatalf("tag spelling changed: source=%q resolved=%q exported=%q want=%q", result.Source.Tag, remote.resolved, remote.exported, tc.want)
			}

			if tc.ref != "" && remote.lists != 0 {
				t.Fatal("explicit --ref unexpectedly queried latest releases")
			}

			body, err := os.ReadFile(filepath.Join(dir, ".gitlab-ci.yml"))
			if err != nil {
				t.Fatal(err)
			}

			if !strings.Contains(string(body), "@"+strings.TrimPrefix(tc.want, "v")) {
				t.Fatal("generated Catalog component does not use the selected version")
			}
		})
	}
}
