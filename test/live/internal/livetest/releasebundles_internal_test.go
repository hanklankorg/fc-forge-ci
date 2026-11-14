// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"testing"
)

func TestVerifyReleaseBundles(t *testing.T) {
	errRead := errors.New("inert read failure")
	errIntegrity := errors.New("inert bundle does not sign subject bytes")

	for _, test := range []struct {
		name      string
		names     []string
		change    func(map[string]string)
		readFail  string
		want      map[string]bool
		wantCalls []string
		wantError string
		wantCause error
	}{
		{
			name: "one valid pair", names: []string{"app", "app.bundle"},
			want: map[string]bool{"app": true}, wantCalls: []string{"app app.bundle"},
		},
		{
			name: "multiple valid pairs", names: []string{"app", "app.bundle", "checksums", "checksums.bundle"},
			want: map[string]bool{"app": true, "checksums": true}, wantCalls: []string{"app app.bundle", "checksums checksums.bundle"},
		},
		{name: "no bundles", names: []string{"app", "checksums"}},
		{name: "empty inventory"},
		{
			name: "orphan bundle", names: []string{"app.bundle"},
			wantError: `release bundle "app.bundle" has no offered subject "app": release evidence contract violated`, wantCause: errReleaseEvidence,
		},
		{
			name: "missing subject bytes", names: []string{"app", "app.bundle"},
			change: func(files map[string]string) { delete(files, "app") }, wantCalls: []string{"app app.bundle"},
			wantError: `verify release bundle "app.bundle" for "app": read subject "app": file does not exist`, wantCause: fs.ErrNotExist,
		},
		{
			name: "missing bundle bytes", names: []string{"app", "app.bundle"},
			change: func(files map[string]string) { delete(files, "app.bundle") }, wantCalls: []string{"app app.bundle"},
			wantError: `verify release bundle "app.bundle" for "app": read bundle "app.bundle": file does not exist`, wantCause: fs.ErrNotExist,
		},
		{
			name: "subject read failure", names: []string{"app", "app.bundle"}, readFail: "app", wantCalls: []string{"app app.bundle"},
			wantError: `verify release bundle "app.bundle" for "app": read subject "app": inert read failure`, wantCause: errRead,
		},
		{
			name: "bundle read failure", names: []string{"app", "app.bundle"}, readFail: "app.bundle", wantCalls: []string{"app app.bundle"},
			wantError: `verify release bundle "app.bundle" for "app": read bundle "app.bundle": inert read failure`, wantCause: errRead,
		},
		{
			name: "wrong subject bundle", names: []string{"app", "app.bundle"},
			change: func(files map[string]string) { files["app.bundle"] = files["checksums.bundle"] }, wantCalls: []string{"app app.bundle"},
			wantError: `verify release bundle "app.bundle" for "app": inert bundle does not sign subject bytes`, wantCause: errIntegrity,
		},
		{
			name: "changed subject bytes", names: []string{"app", "app.bundle"},
			change: func(files map[string]string) { files["app"] = "substituted bytes" }, wantCalls: []string{"app app.bundle"},
			wantError: `verify release bundle "app.bundle" for "app": inert bundle does not sign subject bytes`, wantCause: errIntegrity,
		},
		{
			name: "corrupt bundle", names: []string{"app", "app.bundle"},
			change: func(files map[string]string) { files["app.bundle"] = "corrupt" }, wantCalls: []string{"app app.bundle"},
			wantError: `verify release bundle "app.bundle" for "app": inert bundle does not sign subject bytes`, wantCause: errIntegrity,
		},
		{
			name: "mixed inventory", names: []string{"app", "app.bundle", "checksums", "checksums.asc", "checksums.sig", "checksums.pem"},
			want: map[string]bool{"app": true}, wantCalls: []string{"app app.bundle"},
		},
		{
			name: "continue after failed pair", names: []string{"app", "app.bundle", "checksums", "checksums.bundle"},
			change: func(files map[string]string) { files["app.bundle"] = "corrupt" },
			want:   map[string]bool{"checksums": true}, wantCalls: []string{"app app.bundle", "checksums checksums.bundle"},
			wantError: `verify release bundle "app.bundle" for "app": inert bundle does not sign subject bytes`, wantCause: errIntegrity,
		},
		{
			name: "continue after orphan", names: []string{"missing.bundle", "app", "app.bundle"},
			want: map[string]bool{"app": true}, wantCalls: []string{"app app.bundle"},
			wantError: `release bundle "missing.bundle" has no offered subject "missing": release evidence contract violated`, wantCause: errReleaseEvidence,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := map[string]string{"app": "application bytes", "app.bundle": "signed:application bytes", "checksums": "checksum bytes", "checksums.bundle": "signed:checksum bytes"}
			if test.change != nil {
				test.change(files)
			}

			read := func(name string) (string, error) {
				if name == test.readFail {
					return "", errRead
				}

				body, found := files[name]
				if !found {
					return "", fs.ErrNotExist
				}

				return body, nil
			}

			var calls []string

			verified, err := VerifyReleaseBundles(test.names, func(subject, bundle string) error {
				calls = append(calls, subject+" "+bundle)

				blob, err := read(subject)
				if err != nil {
					return fmt.Errorf("read subject %q: %w", subject, err)
				}

				signature, err := read(bundle)
				if err != nil {
					return fmt.Errorf("read bundle %q: %w", bundle, err)
				}

				if signature != "signed:"+blob {
					return errIntegrity
				}

				return nil
			})
			if verified == nil || !maps.Equal(verified, test.want) || !slices.Equal(calls, test.wantCalls) {
				t.Fatalf("got verified=%v calls=%v, want verified=%v calls=%v", verified, calls, test.want, test.wantCalls)
			}

			if test.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}

				return
			}

			if !errors.Is(err, test.wantCause) || err.Error() != test.wantError {
				t.Fatalf("got %v, want %s (wrapping %v)", err, test.wantError, test.wantCause)
			}
		})
	}
}

// GitHub's coverage check deliberately excludes signature sidecars. Rejecting
// an orphan bundle must therefore be independent of even complete GPG coverage.
func TestReleaseBundlesRejectGitHubOrphanDespiteCompleteSignatureCoverage(t *testing.T) {
	assets := map[string]int64{"app": 1, "app.asc": 2, "app.sig": 3, "app.pem": 4, "missing.bundle": 5}

	bundles, err := VerifyReleaseBundles(slices.Collect(maps.Keys(assets)), func(subject, bundle string) error {
		t.Fatalf("orphan must not reach native download/verification: %s %s", subject, bundle)

		return nil
	})
	if !errors.Is(err, errReleaseEvidence) || err.Error() != `release bundle "missing.bundle" has no offered subject "missing": release evidence contract violated` {
		t.Fatalf("orphan accepted or wrong diagnostic: %v", err)
	}

	if unsigned := unsignedSubjects(assets, bundles, map[string]bool{"app": true}); len(unsigned) != 0 {
		t.Fatalf("GPG-covered subject or signature sidecars incorrectly treated as unsigned: %v", unsigned)
	}
}
