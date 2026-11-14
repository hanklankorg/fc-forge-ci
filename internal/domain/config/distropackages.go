// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package config

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// Defaults of the `distro-packages:` block: where a release's packages go in
// the forge's Debian, RPM and Alpine registries when the declaration names
// no other place. One word, stable, across the three formats: a user adds
// `<registry>/debian stable main`, `<registry>/rpm/stable.repo` and
// `<registry>/alpine/stable/main`.
const (
	DefaultDebianDistribution = "stable"
	DefaultDebianComponent    = "main"
	DefaultRPMGroup           = "stable"
	DefaultAlpineBranch       = "stable"
	DefaultAlpineRepository   = "main"
)

// DistroPackageNamePattern is one path segment of a registry address: a
// distribution, a component, a branch, a repository, or a step of an RPM
// group. Each becomes part of the URL the package and the release's
// credential are sent to, so it is held to a plain name.
const DistroPackageNamePattern = `[a-z0-9]+([._-][a-z0-9]+)*`

// The longest name and RPM group. Forgejo keeps a package file's place in
// package_file.composite_key, a string column without a stated length,
// which is VARCHAR(255): "<distribution>|<component>" for Debian,
// "<branch>|<repository>|<architecture>" for Alpine, the group for RPM. Two
// names of 64 and an architecture fit, and so does a group of 128.
const (
	DistroPackageNameMaxLength  = 64
	DistroPackageGroupMaxLength = 128
)

var (
	distroPackageName  = regexp.MustCompile(`^` + DistroPackageNamePattern + `$`)
	distroPackageGroup = regexp.MustCompile(`^` + DistroPackageGroupPattern() + `$`)
)

// DistroPackageGroupPattern is an RPM group: one name, or several joined by
// a slash, as in rocky/el9.
func DistroPackageGroupPattern() string {
	return DistroPackageNamePattern + `(/` + DistroPackageNamePattern + `)*`
}

// DistroPackages is the top-level `distro-packages:` block of artifacts.yml.
// Its presence is the opt-in: every .deb, .rpm and .apk file of the release
// is published to the forge's own package registries, beside being a
// release file. It carries only the places, so an empty block publishes
// with the defaults. It is a block of the release and not of an artifact,
// as `sign` is: the packages are files of the release's manifest, which
// does not say which project built them.
type DistroPackages struct {
	Debian DebianRegistry `json:"debian" yaml:"debian,omitempty"`
	RPM    RPMRegistry    `json:"rpm"    yaml:"rpm,omitempty"`
	Alpine AlpineRegistry `json:"alpine" yaml:"alpine,omitempty"`
}

// DebianRegistry places .deb files: `deb <registry> <distribution> <component>`.
type DebianRegistry struct {
	Distribution string `json:"distribution" yaml:"distribution,omitempty"`
	Component    string `json:"component"    yaml:"component,omitempty"`
}

// RPMRegistry places .rpm files in one repository of the registry.
type RPMRegistry struct {
	Group string `json:"group" yaml:"group,omitempty"`
}

// AlpineRegistry places .apk files: `<registry>/<branch>/<repository>`.
type AlpineRegistry struct {
	Branch     string `json:"branch"     yaml:"branch,omitempty"`
	Repository string `json:"repository" yaml:"repository,omitempty"`
}

// Resolved returns the declaration with every omitted place set to its
// default. Nil stays nil: a release without the block publishes no package
// to a registry.
func (d *DistroPackages) Resolved() *DistroPackages {
	if d == nil {
		return nil
	}

	resolved := *d
	resolved.Debian.Distribution = orDefault(d.Debian.Distribution, DefaultDebianDistribution)
	resolved.Debian.Component = orDefault(d.Debian.Component, DefaultDebianComponent)
	resolved.RPM.Group = orDefault(d.RPM.Group, DefaultRPMGroup)
	resolved.Alpine.Branch = orDefault(d.Alpine.Branch, DefaultAlpineBranch)
	resolved.Alpine.Repository = orDefault(d.Alpine.Repository, DefaultAlpineRepository)

	return &resolved
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}

	return value
}

// Validate checks the block as declared: omitted places are valid, since
// Resolved gives them their defaults.
func (d *DistroPackages) Validate() error {
	if d == nil {
		return nil
	}

	for _, place := range []struct {
		key, value string
		pattern    *regexp.Regexp
		longest    int
	}{
		{"debian.distribution", d.Debian.Distribution, distroPackageName, DistroPackageNameMaxLength},
		{"debian.component", d.Debian.Component, distroPackageName, DistroPackageNameMaxLength},
		{"rpm.group", d.RPM.Group, distroPackageGroup, DistroPackageGroupMaxLength},
		{"alpine.branch", d.Alpine.Branch, distroPackageName, DistroPackageNameMaxLength},
		{"alpine.repository", d.Alpine.Repository, distroPackageName, DistroPackageNameMaxLength},
	} {
		if place.value != "" && !place.pattern.MatchString(place.value) {
			return fmt.Errorf("distro-packages.%s %q must be lowercase letters and digits joined by '.', '_' or '-'%s: %w",
				place.key, place.value, groupHint(place.key), errs.ErrInvalidConfig)
		}

		if len(place.value) > place.longest {
			return fmt.Errorf("distro-packages.%s is longer than %d characters: %w", place.key, place.longest, errs.ErrInvalidConfig)
		}
	}

	return nil
}

// checkDistroPackagesDocument refuses what the decoder would read as an
// absent block or an omitted place, and the schema refuses: a block or a
// format without a mapping, and a place that is empty or no string. A bare
// `distro-packages:` would otherwise release without publishing a package.
func checkDistroPackagesDocument(data []byte) error {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil || len(document.Content) == 0 {
		return nil //nolint:nilerr // Parse reports a document that does not decode.
	}

	block := mappingEntry(document.Content[0], "distro-packages")
	if block == nil {
		return nil
	}

	if block.Kind != yaml.MappingNode {
		return fmt.Errorf("distro-packages must be a mapping; write `distro-packages: {}` to publish with the default places: %w", errs.ErrInvalidConfig)
	}

	for index := 0; index+1 < len(block.Content); index += 2 {
		format, places := block.Content[index].Value, block.Content[index+1]
		if places.Kind != yaml.MappingNode {
			return fmt.Errorf("distro-packages.%s must be a mapping; leave it out for the default places: %w", format, errs.ErrInvalidConfig)
		}

		for place := 0; place+1 < len(places.Content); place += 2 {
			if value := places.Content[place+1]; value.Kind != yaml.ScalarNode || value.Tag != "!!str" || value.Value == "" {
				return fmt.Errorf("distro-packages.%s.%s must be a string that is not empty; leave it out for the default: %w",
					format, places.Content[place].Value, errs.ErrInvalidConfig)
			}
		}
	}

	return nil
}

// mappingEntry returns the value of a key of a mapping, nil without one.
func mappingEntry(node *yaml.Node, key string) *yaml.Node {
	if node.Kind != yaml.MappingNode {
		return nil
	}

	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}

	return nil
}

func groupHint(key string) string {
	if strings.HasSuffix(key, ".group") {
		return ", in steps joined by '/'"
	}

	return ""
}
