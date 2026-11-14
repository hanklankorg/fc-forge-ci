// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	"testing"

	appbuild "github.com/diggsweden/forge-ci/internal/app/build"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

func TestGradleMetadata_ResolvesVersion(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		props string
		want  string
	}{
		{
			name:  "plain assignment",
			props: "org.gradle.jvmargs=-Xmx1g\nversion=1.2.3\n",
			want:  "1.2.3",
		},
		{
			name:  "value is trimmed",
			props: "version=  1.2.3  \n",
			want:  "1.2.3",
		},
		{
			name:  "java properties separators are accepted",
			props: "version = 1.2.3\n",
			want:  "1.2.3",
		},
		{
			name:  "colon separator is accepted",
			props: "version: 1.2.3\n",
			want:  "1.2.3",
		},
		{
			name:  "indented line still counts",
			props: "  version=1.2.3\n",
			want:  "1.2.3",
		},
		{
			// A longer key must not be mistaken for this one: Android
			// projects carry versionCode and versionName alongside version.
			name:  "a longer key is not this key",
			props: "versionCode=42\nversionName=nine\nversion=1.2.3\n",
			want:  "1.2.3",
		},
		{
			name:  "a commented version is not read",
			props: "#version=9.9.9\nversion=1.2.3\n",
			want:  "1.2.3",
		},
		{
			name:  "first assignment wins",
			props: "version=1.2.3\nversion=4.5.6\n",
			want:  "1.2.3",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fsys := testfs.NewReal(t)
			fsys.WriteFile("gradle.properties", []byte(tc.props))

			if got := appbuild.ResolveGradleVersionForTest(fsys.Root); got != tc.want {
				t.Errorf("version = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGradleMetadata_ResolvesNoneWhenMissing(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		props string // "" writes no gradle.properties at all
	}{
		{name: "no version key", props: "org.gradle.jvmargs=-Xmx1g\n"},
		{name: "no gradle.properties", props: ""},
		{name: "version key with no value", props: "version=\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fsys := testfs.NewReal(t)
			if tc.props != "" {
				fsys.WriteFile("gradle.properties", []byte(tc.props))
			}

			// Absence is not a failure: some projects compute the version in
			// build.gradle(.kts), so the release build carries on with none.
			if got := appbuild.ResolveGradleVersionForTest(fsys.Root); got != "" {
				t.Errorf("version = %q, want none", got)
			}
		})
	}
}
