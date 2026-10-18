// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
	"github.com/diggsweden/forge-ci/internal/livetest/rawref"
)

// Exercise the default dry run and the destructive pass on owned scratch
// releases, reading the served bytes independently after each pass.
func TestRelease_AssetRetentionPreservesEvidence(t *testing.T) {
	for _, forge := range forgesClaiming(t, claimsReleaseAssets, "release assets") {
		t.Run(string(forge), func(t *testing.T) {
			target := livetest.Accept(t, forge)
			repo := livetest.NewScratchRepo(t, target, "prepublication-retention")
			slug := livetest.RepoSlug(target, repo)
			creator, ok := livetest.Provider(t, target, repo).(provider.ReleaseCreator)
			require.True(t, ok, "provider does not implement ReleaseCreator")

			ctx, cancel := context.WithTimeout(livetest.Context(t), 3*time.Minute)
			defer cancel()

			const archive = "app.tar.gz"

			names := []string{archive, "checksums.txt", "checksums.txt.bundle", "app.tar.gz.sbom.json", "app.tar.gz.cdx.sbom.json", "slsa-provenance.intoto.json", "slsa-provenance.intoto.json.bundle"}
			slices.Sort(names)

			before := make(map[string]rawref.Release)

			for _, tag := range []string{"v0.0.1", "v0.0.2"} {
				livetest.PrepareTag(t, target, repo, tag)

				assets := make([]string, 0, len(names))
				for _, name := range names {
					assets = append(assets, writeAsset(t, name, tag+" fixture bytes for "+name+"\n"))
				}

				require.NoError(t, creator.CreateRelease(ctx, slug, provider.ReleaseSpec{
					Tag: tag, Name: "retention " + tag, NotesFile: writeNotes(t, "retention fixture\n"), Assets: assets,
				}))

				before[tag] = mustReadRelease(ctx, t, target, repo, tag)
				require.Equal(t, names, before[tag].AssetNames(), "initial assets of %s", tag)

				if tag == "v0.0.1" {
					// Forge release timestamps can have one-second resolution.
					select {
					case <-time.After(time.Second):
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
			}

			args := make([]string, 0, 7)
			args = append(args, "release", "asset-retention", "--repository", slug, "--keep", "1")

			dry := livetest.CLI(t, target, repo, args...)
			if dry.ExitCode != 0 || !strings.Contains(dry.Stdout, "would delete") {
				t.Fatalf("dry run: exit=%d %s", dry.ExitCode, dry.Combined())
			}

			t.Log(dry.Stdout)

			for tag, original := range before {
				assertRetainedRelease(t, mustReadRelease(ctx, t, target, repo, tag), original, names)
			}

			run := livetest.CLI(t, target, repo, append(args, "--dry-run=false")...)
			if run.ExitCode != 0 || !strings.Contains(run.Stdout, "deleted 1 files") {
				t.Fatalf("retention: exit=%d %s", run.ExitCode, run.Combined())
			}

			t.Log(run.Stdout)

			kept := slices.DeleteFunc(slices.Clone(names), func(name string) bool { return name == archive })
			assertRetainedRelease(t, mustReadRelease(ctx, t, target, repo, "v0.0.1"), before["v0.0.1"], kept)
			assertRetainedRelease(t, mustReadRelease(ctx, t, target, repo, "v0.0.2"), before["v0.0.2"], names)
		})
	}
}

func assertRetainedRelease(t *testing.T, got, before rawref.Release, names []string) {
	t.Helper()

	if got.Tag != before.Tag || got.Name != before.Name || got.Body != before.Body || got.Draft != before.Draft || got.Prerelease != before.Prerelease {
		t.Fatal("retention changed release metadata")
	}

	if !slices.Equal(got.AssetNames(), names) {
		t.Fatalf("%s retained %v, want %v", got.Tag, got.AssetNames(), names)
	}

	for _, name := range names {
		asset, _ := got.Asset(name)

		original, _ := before.Asset(name)
		if asset != original {
			t.Errorf("retention changed the served bytes of %s/%s", got.Tag, name)
		}
	}
}
