// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// DistroRegistries are the owner's package registries a Forgejo release
// publishes distro packages to, as the forge's package API names their type.
var DistroRegistries = []string{"debian", "rpm", "alpine"}

// ForgejoSignsDistroPackages reports whether the release workflow of the
// consumer at root (a file name under .forgejo/workflows) has the layer sign
// distro packages: the sign-distro-packages input of its one call of
// sign-and-publish-release.yml, which the layer takes for true when the call
// leaves it out.
func ForgejoSignsDistroPackages(root, workflow string) (bool, error) {
	path := filepath.Join(root, ".forgejo", "workflows", workflow)

	body, err := os.ReadFile(path) //nolint:gosec // G304: the workflow of the consumer checkout the caller names
	if err != nil {
		return false, fmt.Errorf("read the release workflow: %w", err)
	}

	_, jobs, err := forgejoReleaseJobs(string(body))
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}

	_, signer, err := forgejoSigningJob(jobs)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}

	inputs, _ := signer["with"].(map[string]any)

	written, passed := inputs["sign-distro-packages"]

	return !passed || fmt.Sprint(written) == variableTrue, nil
}

// ForgejoPlannedDistroPackages are the names of the packages the Forgejo
// release of the consumer at root publishes to the owner's Debian, RPM and
// Alpine registries: none unless the declaration its release workflow (a
// file name under .forgejo/workflows) passes prepare-release.yml has a
// distro-packages block, and then the names its GoReleaser configuration
// packages under, which is the one builder of distro packages a trial knows.
// A consumer without the declaration publishes none.
func ForgejoPlannedDistroPackages(root, workflow string) ([]string, error) {
	artifactsConfig, err := forgejoCalledArtifactsConfig(filepath.Join(root, ".forgejo", "workflows", workflow), "prepare-release.yml")
	if err != nil {
		return nil, err
	}

	declaration := filepath.Join(root, cmp.Or(artifactsConfig, DefaultArtifactsConfig))
	goreleaser := filepath.Join(root, ".goreleaser.yml")

	body, err := os.ReadFile(declaration) //nolint:gosec // G304: the declaration of the consumer checkout the caller names
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	cfg, err := config.Parse(body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", declaration, err)
	}

	if cfg.DistroPackages == nil {
		return nil, nil
	}

	body, err = os.ReadFile(goreleaser) //nolint:gosec // G304: the configuration of the consumer checkout the caller names
	if err != nil {
		return nil, fmt.Errorf("%s declares distro-packages; read the GoReleaser configuration that names them: %w", declaration, err)
	}

	var packaging struct {
		Project   string `yaml:"project_name"`
		Packagers []struct {
			Name string `yaml:"package_name"`
		} `yaml:"nfpms"`
	}
	if err := yaml.Unmarshal(body, &packaging); err != nil {
		return nil, fmt.Errorf("%s: %w", goreleaser, err)
	}

	var names []string

	for _, packager := range packaging.Packagers {
		// GoReleaser packages under the project's name where a packager
		// names none.
		if name := cmp.Or(packager.Name, packaging.Project); name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}

	if len(names) == 0 {
		return nil, fmt.Errorf("%s names no package under nfpms: %w", goreleaser, errs.ErrMalformedInput)
	}

	return names, nil
}

// DistroPackageVersions are the versions among listed, as a registry lists a
// package, that are the release's: the version itself, or the version with
// the package revision a format appends to it, as RPM's 1.2.3-1. It decides
// what a trial refuses to start on and what it accepts as the release's; no
// version is removed by it.
func DistroPackageVersions(listed []string, version string) []string {
	var matching []string

	for _, candidate := range listed {
		if candidate == version || strings.HasPrefix(candidate, version+"-") {
			matching = append(matching, candidate)
		}
	}

	return matching
}

// NewPackageVersions are the versions a registry lists after a release that
// it did not list before it, spelled as the registry spells them. They are
// the release's own where nothing else publishes to the registry meanwhile:
// the launcher holds the lock of its lab account on the provider through the
// run and its cleanup (docs/live-runs.md, Execution scopes and locks), and
// the registries are that account's. A suite started without the launcher
// has no such lock.
func NewPackageVersions(before, after []string) []string {
	var created []string

	for _, version := range after {
		if !slices.Contains(before, version) && !slices.Contains(created, version) {
			created = append(created, version)
		}
	}

	return created
}

// DeletePublishedPackagesAtCleanup removes exactly versions of one package,
// which the caller read from the registry while the test ran, when the test
// ends. The cleanup lists nothing with the test's context, which is over by
// then: each removal looks the version up under the cleanup's own.
func DeletePublishedPackagesAtCleanup(tb TB, target Target, repo, ecosystem, name string, versions []string) {
	tb.Helper()
	requireAccepted(tb, target)

	deletePublishedPackagesAtCleanup(tb, target, repo, ecosystem, name, versions, func(ctx context.Context) packageRequest {
		return packageRequests(ctx, target)
	})
}

func deletePublishedPackagesAtCleanup(tb TB, target Target, repo, ecosystem, name string, versions []string, requests func(context.Context) packageRequest) {
	tb.Helper()

	tb.Cleanup(func() {
		if err := refuseDemoDeletion(target); err != nil {
			tb.Errorf("livetest: %v", err)

			return
		}

		for _, version := range versions {
			ctx, cancel := cleanupContext(timeoutAPI)

			if err := deletePublishedPackage(target, repo, ecosystem, name, version, requests(ctx)); err != nil {
				tb.Errorf("livetest: cleanup %s package %s@%s: %v", ecosystem, name, version, err)
			}

			cancel()
		}
	})
}
