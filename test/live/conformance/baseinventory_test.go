// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"os"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

// baseInventoryEnv is set for a trial whose release must publish a base image
// inventory: the base-graph lane's. Another trial is held to its inventory
// when its prepared commit plans base images, and to none when it does not.
const baseInventoryEnv = "FORGE_CI_LIVE_BASE_INVENTORY"

// requireBaseImageInventory holds the release of a base image graph to what
// it claims. The declaration is read from the verified prepared commit: a
// consumer with a base-plan.sh declares each images/<name>/Containerfile.
// The release's inventory must list exactly those, each by a digest in the
// lab's base repository. The registry must serve that digest under the
// image's own final tag, so a name is never answered for by another image's
// digest, and Cosign must verify the reference against the trial's key.
func requireBaseImageInventory(t *testing.T, target livetest.Target, repo, prepared, tag string, keys releaseKeys) {
	t.Helper()

	mirror := livetest.MirrorRepo(t, target, repo)

	if planner, err := mirror.Git(t, "ls-tree", "--name-only", prepared, "--", "base-plan.sh"); err != nil {
		t.Fatal(err)
	} else if strings.TrimSpace(planner) == "" {
		if os.Getenv(baseInventoryEnv) != "" {
			t.Fatalf("the trial releases a base image graph, and its prepared commit %s has no base-plan.sh", prepared)
		}

		return
	}

	paths, err := mirror.Git(t, "ls-tree", "-r", "--name-only", prepared, "--", "images")
	if err != nil {
		t.Fatal(err)
	}

	declared := declaredBaseImages(paths)
	if len(declared) == 0 {
		t.Fatal("the prepared commit plans base images and declares none under images/")
	}

	host, err := livetest.RegistryHost(target)
	if err != nil {
		t.Fatal(err)
	}

	var inventory []byte

	for _, release := range livetest.Releases(t, target, repo) {
		if release.TagName != tag {
			continue
		}

		for _, asset := range release.Assets {
			if asset.Name == baseImageInventory {
				inventory = livetest.Download(t, target, asset.URL, maxReleaseFile)
			}
		}
	}

	if inventory == nil {
		t.Fatalf("release %s publishes no %s", tag, baseImageInventory)
	}

	listing, problems := baseInventoryImages(inventory, declared, strings.ToLower(host+"/"+livetest.RepoSlug(target, repo))+"-base")
	for _, problem := range problems {
		t.Errorf("release %s: %s", tag, problem)
	}

	authDir := livetest.RegistryAuthConfigDir(t, target, t.TempDir())

	for _, image := range listing {
		if served, found := livetest.ImageDigest(t, target, repo+"-base", image.Tag); !found || !strings.HasSuffix(image.Ref, "@"+served) {
			t.Errorf("release %s lists image %q as %s, and the registry serves %q (found %t) under its tag %s", tag, image.Flavor, image.Ref, served, found, image.Tag)

			continue
		}

		verifyCosignSignature(t, target, image.Ref, keys.cosignPublicPath, authDir)
	}

	t.Logf("release %s: %d of %d declared base images read back by name, digest and signature", tag, len(listing), len(declared))
}
