// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package conformance_test

import (
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	domainversion "github.com/diggsweden/forge-ci/internal/domain/version"
)

const releaseChangelog = "CHANGELOG.md"

// releaseLockfiles names, for a manifest a version bump rewrites, the lockfile
// beside it that records the same version: a bump that leaves it behind
// publishes a project whose lockfile describes the previous release.
var releaseLockfiles = map[string]string{"package.json": "package-lock.json", "Cargo.toml": "Cargo.lock"}

// releaseBump is one project a release's plan bumps: its type and directory.
type releaseBump struct {
	projectType projecttype.Type
	dir         string
}

// changelogCopy reports whether file is the copy of the changelog the bump
// installs in the directory of a project that is not the repository's root.
func (bump releaseBump) changelogCopy(file string) bool {
	return bump.dir != "." && file == path.Join(bump.dir, releaseChangelog)
}

// versionFile reports whether file is one the product's own version-bump
// pathspec names for the bump's project type, inside the bump's directory. A
// pathspec's glob prefix places the name anywhere below it.
func (bump releaseBump) versionFile(file string) bool {
	for spec := range strings.FieldsSeq(domainversion.FilePattern(bump.projectType)) {
		if spec == releaseChangelog {
			continue
		}

		if name, glob := strings.CutPrefix(spec, ":(glob)**/"); glob {
			inside := bump.dir == "." || strings.HasPrefix(file, bump.dir+"/")
			if matched, err := path.Match(name, path.Base(file)); err == nil && matched && inside {
				return true
			}
		} else if file == path.Join(bump.dir, spec) {
			return true
		}
	}

	return false
}

// releaseCommitScopeFindings holds the paths one release commit changed to
// what its plan lets a release change. It changes the root changelog; every
// other path is a version file of a project the plan bumps or the changelog
// copy in such a project's directory; every such project
// whose type has version files has one changed; and a manifest it rewrites
// takes the lockfile beside it along, where the request had one. A release
// that bumps nothing changes the changelog alone.
func releaseCommitScopeFindings(changed []string, bumps []releaseBump, requested func(path string) bool) []string {
	var findings []string

	if !slices.Contains(changed, releaseChangelog) {
		findings = append(findings, "the release commit does not change "+releaseChangelog)
	}

	for _, file := range changed {
		planned := slices.ContainsFunc(bumps, func(bump releaseBump) bool { return bump.versionFile(file) || bump.changelogCopy(file) })
		if file != releaseChangelog && !planned {
			findings = append(findings, "the release commit changes "+file+", which is neither a changelog nor a version file of a project the plan bumps")
		}

		lockfile, locked := releaseLockfiles[path.Base(file)]
		if !locked {
			continue
		}

		if lockfile = path.Join(path.Dir(file), lockfile); requested(lockfile) && !slices.Contains(changed, lockfile) {
			findings = append(findings, "the release commit bumps "+file+" and leaves "+lockfile+" at the previous version")
		}
	}

	for _, bump := range bumps {
		tagOnly := strings.TrimSpace(domainversion.FilePattern(bump.projectType)) == releaseChangelog
		if !tagOnly && !slices.ContainsFunc(changed, bump.versionFile) {
			findings = append(findings, "the plan bumps the "+string(bump.projectType)+" project in "+bump.dir+" and the release commit changes none of its version files")
		}
	}

	return findings
}

// releaseChangelogFindings holds the changelog to the release: the version is
// named in the release commit's changelog more often than in the request's,
// as a whole version and not as part of another.
func releaseChangelogFindings(before, after, version string) []string {
	mention := regexp.MustCompile(`(^|[^0-9.])` + regexp.QuoteMeta(version) + `([^0-9.]|$)`)
	if len(mention.FindAllString(after, -1)) <= len(mention.FindAllString(before, -1)) {
		return []string{releaseChangelog + " of the release commit names version " + version + " no more often than the request's did"}
	}

	return nil
}

func TestReleaseCommitScopeFindings(t *testing.T) {
	t.Parallel()

	tree := func(files ...string) func(string) bool {
		return func(file string) bool { return slices.Contains(files, file) }
	}
	npm, cargo, maven := releaseBump{projecttype.NPM, "."}, releaseBump{projecttype.Cargo, "service"}, releaseBump{projecttype.Maven, "."}

	for name, test := range map[string]struct {
		changed, requested []string
		bumps              []releaseBump
		findings           int
	}{
		"a release that bumps nothing changes the changelog": {changed: []string{"CHANGELOG.md"}},
		"a tag-only Go release":                              {changed: []string{"CHANGELOG.md"}, bumps: []releaseBump{{projecttype.Go, "."}}},
		"npm takes its lockfile along": {
			changed: []string{"CHANGELOG.md", "package-lock.json", "package.json"}, requested: []string{"package-lock.json"}, bumps: []releaseBump{npm},
		},
		"a nested Cargo project": {
			changed: []string{"CHANGELOG.md", "service/Cargo.lock", "service/Cargo.toml"}, requested: []string{"service/Cargo.lock"}, bumps: []releaseBump{cargo},
		},
		"a nested project takes a changelog copy": {
			changed: []string{"CHANGELOG.md", "service/CHANGELOG.md", "service/Cargo.toml"}, bumps: []releaseBump{cargo},
		},
		"a changelog copy nobody planned": {changed: []string{"CHANGELOG.md", "docs/CHANGELOG.md", "package.json"}, bumps: []releaseBump{npm}, findings: 1},
		"a manifest without a lockfile":   {changed: []string{"CHANGELOG.md", "package.json"}, bumps: []releaseBump{npm}},
		"every module of a reactor":       {changed: []string{"CHANGELOG.md", "app/pom.xml", "library/pom.xml", "pom.xml"}, bumps: []releaseBump{maven}},
		"no changelog":                    {changed: []string{"pom.xml"}, bumps: []releaseBump{maven}, findings: 1},
		"an unrelated edit":               {changed: []string{"CHANGELOG.md", "src/main.go"}, findings: 1},
		"a workflow":                      {changed: []string{"CHANGELOG.md", ".forgejo/workflows/release.yml"}, findings: 1},
		"a version file nobody planned":   {changed: []string{"CHANGELOG.md", "Cargo.toml"}, findings: 1},
		"another project type's file":     {changed: []string{"CHANGELOG.md", "package.json", "pom.xml"}, bumps: []releaseBump{npm}, findings: 1},
		"a manifest outside the project": {
			changed: []string{"CHANGELOG.md", "Cargo.toml", "service/Cargo.toml"}, bumps: []releaseBump{cargo}, findings: 1,
		},
		"a planned bump that did not happen": {changed: []string{"CHANGELOG.md"}, bumps: []releaseBump{npm}, findings: 1},
		"a stale npm lockfile": {
			changed: []string{"CHANGELOG.md", "package.json"}, requested: []string{"package-lock.json"}, bumps: []releaseBump{npm}, findings: 1,
		},
		"a stale Cargo lockfile": {
			changed: []string{"CHANGELOG.md", "service/Cargo.toml"}, requested: []string{"service/Cargo.lock"}, bumps: []releaseBump{cargo}, findings: 1,
		},
	} {
		if findings := releaseCommitScopeFindings(test.changed, test.bumps, tree(test.requested...)); len(findings) != test.findings {
			t.Errorf("%s: findings are %v, want %d", name, findings, test.findings)
		}
	}
}

func TestReleaseChangelogFindings(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		before, after string
		findings      int
	}{
		"a new section":           {before: "# Changelog\n", after: "# Changelog\n\n## [0.0.1] - 2026-01-01\n"},
		"a v-prefixed heading":    {after: "## [v0.0.1]\n"},
		"a second release":        {before: "## [0.0.1]\n", after: "## [0.0.2]\n\n## [0.0.1]\n", findings: 1},
		"unchanged":               {before: "## [0.0.1]\n", after: "## [0.0.1]\n", findings: 1},
		"part of another version": {after: "## [10.0.10]\n## [0.0.11]\n", findings: 1},
		"absent":                  {after: "# Changelog\n", findings: 1},
	} {
		if findings := releaseChangelogFindings(test.before, test.after, "0.0.1"); len(findings) != test.findings {
			t.Errorf("%s: findings are %v, want %d", name, findings, test.findings)
		}
	}
}
