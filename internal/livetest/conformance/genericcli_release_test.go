// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/livetest"
)

// Shared by release-Git acceptance on every forge. The request's regular Git
// blobs opt in, so unrelated consumers retain their own payload contracts.
func requireCLIRelease(t *testing.T, target livetest.Target, repo string, keys releaseKeys, tag, prepared, kind string, admitted []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(livetest.Context(t), time.Minute)
	defer cancel()
	release := mustReadRelease(ctx, t, target, repo, tag)
	name := map[string]string{"go": "forge-ci", "cargo": "nanolinter"}[kind]
	if name == "" {
		t.Fatalf("invalid generic CLI kind %q", kind)
	}
	download := func(name string) []byte {
		if _, ok := release.Asset(name); !ok {
			t.Fatalf("CLI release missing %s", name)
		}
		return livetest.Download(t, target, livetest.ReleaseAssetURL(t, target, repo, tag, name), 256<<20)
	}
	checksums := download("checksums.txt")
	if err := livetest.VerifyBlob(t, target, keys.cosignPublicPath, checksums, download("checksums.txt.bundle")); err != nil {
		t.Fatal(err)
	}
	payloads := map[string][]byte{"checksums.txt": checksums}
	for _, file := range []string{name, "source-identity.json", "build-identity.json"} {
		payloads[file] = download(file)
	}
	if err := checkCLIIdentity(kind, strings.TrimPrefix(tag, "v"), prepared, admitted, payloads); err != nil {
		t.Fatal(err)
	}
	t.Logf("verified CLI payload/source/prepared-release identity for %s at %s", tag, prepared)
}
