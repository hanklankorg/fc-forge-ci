// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

// requirePackageSignatures verifies the OpenPGP signature of every distro
// package among names, the files a release of either lab forge offers, with
// the trial's own GnuPG home, and returns how many verified: every package
// has a sidecar that verifies, and a release of no package, such as a jar's,
// has none to verify. download reads one of the files.
func requirePackageSignatures(t *testing.T, target livetest.Target, keys releaseKeys, names []string, download func(name string, limit int64) []byte) int {
	t.Helper()

	ctx, cancel := context.WithTimeout(livetest.Context(t), 5*time.Minute)
	defer cancel()

	verified, err := livetest.VerifyPackageSignatures(ctx, filepath.Join(keys.dir, "gnupg"), livetest.ToolEnvironment(t, target, nil), names, func(name string) ([]byte, error) {
		limit := int64(maxReleaseFile)
		if strings.HasSuffix(name, ".apk") {
			// Android APKs share Alpine's suffix and can exceed the small
			// fixture limit. A signature keeps the original bound.
			limit = 256 << 20
		}

		return download(name, limit), nil
	})
	if err != nil {
		t.Fatalf("package signatures: %v", err)
	}

	t.Logf("verified %d published GPG package sidecars", len(verified))

	return len(verified)
}

// distroPackage is one package of a release in one of the owner's Debian,
// RPM and Alpine registries: the versions the registry listed before the
// release was requested, and those the release added to it.
type distroPackage struct {
	registry, name  string
	before, created []string
}

// awaitedDistroPackages are the distro packages the consumer's release
// publishes to the owner's registries, before the release is requested, each
// with what its registry lists by then. No registry may hold the version of
// one yet, since the release would leave it as it is or be refused.
func awaitedDistroPackages(t *testing.T, target livetest.Target, repo string, settings livetest.TrialSettings) []distroPackage {
	t.Helper()

	names, err := livetest.ForgejoPlannedDistroPackages(os.Getenv(consumerCheckoutEnv), settings.Workflow)
	if err != nil {
		t.Fatalf("planned distro packages: %v", err)
	}

	awaited := make([]distroPackage, 0, len(livetest.DistroRegistries)*len(names))

	for _, name := range names {
		for _, registry := range livetest.DistroRegistries {
			before := livetest.PublishedPackageVersions(t, target, repo, registry, name)
			if held := livetest.DistroPackageVersions(before, settings.Version); len(held) != 0 {
				t.Fatalf("the owner's %s registry already holds %s %v, which a release cannot publish again; remove it or request another version", registry, name, held)
			}

			awaited = append(awaited, distroPackage{registry: registry, name: name, before: before})
		}
	}

	return awaited
}

// publishedDistroPackages reads the owner's registries once the release run
// is over and returns each awaited package with the versions the run added,
// as the registry spells them. The registries outlive the repository, so
// exactly those versions are removed when the test ends: nothing is listed
// again by then, and no version is removed for looking like the release's.
// They are this run's because the registry held none of them before the
// request and the launcher's lock of the lab account keeps every other run
// out of the account's registries until the cleanup is over. A kept demo
// keeps its packages.
func publishedDistroPackages(t *testing.T, target livetest.Target, repo string, awaited []distroPackage) []distroPackage {
	t.Helper()

	published := make([]distroPackage, 0, len(awaited))

	for _, pkg := range awaited {
		pkg.created = livetest.NewPackageVersions(pkg.before, livetest.PublishedPackageVersions(t, target, repo, pkg.registry, pkg.name))
		if livetest.DemoName() == "" && len(pkg.created) != 0 {
			livetest.DeletePublishedPackagesAtCleanup(t, target, repo, pkg.registry, pkg.name, pkg.created)
		}

		published = append(published, pkg)
	}

	return published
}

// requireForgejoDistroPackages judges the distro packages of a release that
// is over. Where the consumer's release workflow has the layer sign them,
// every package the release offers has an OpenPGP signature that verifies
// with the trial's key. Each package published, of those the release sends
// to the owner's registries, was added there at the release's version and
// at no other, and such a release signed at least one package: its
// declaration is refused without a package.
func requireForgejoDistroPackages(t *testing.T, target livetest.Target, repo string, keys releaseKeys, settings livetest.TrialSettings, tag string, published []distroPackage) {
	t.Helper()

	signs, err := livetest.ForgejoSignsDistroPackages(os.Getenv(consumerCheckoutEnv), settings.Workflow)
	if err != nil {
		t.Fatalf("distro packages: %v", err)
	}

	signed := 0

	if signs {
		release := releaseOfTag(t, target, repo, tag)
		urls := make(map[string]string, len(release.Assets))

		for _, asset := range release.Assets {
			urls[asset.Name] = asset.URL
		}

		signed = requirePackageSignatures(t, target, keys, assetNames(release), func(name string, limit int64) []byte {
			return livetest.Download(t, target, urls[name], limit)
		})
	}

	for _, pkg := range published {
		if released := livetest.DistroPackageVersions(pkg.created, settings.Version); len(released) == 0 || len(released) != len(pkg.created) {
			t.Errorf("the release run succeeded, but it added %v of %s to the owner's %s registry, not %s alone", pkg.created, pkg.name, pkg.registry, settings.Version)
		}
	}

	if signs && len(published) != 0 && signed == 0 {
		t.Errorf("the release publishes distro packages and its workflow signs them, but it offers no signed package")
	}
}
