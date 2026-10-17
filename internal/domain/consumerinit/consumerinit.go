// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package consumerinit is the pure part of `forge-ci init`: which example
// files a forge takes and where they go, the operator's defaults file, the
// placeholder substitutions, and the placeholders a written file still holds.
package consumerinit

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

// DefaultsFile is where an operator's Forge CI mirror records the values
// every consumer of that mirror pins. It is read at the pinned commit only.
const DefaultsFile = ".forge-ci/consumer-defaults.yml"

// Defaults is the operator's defaults file. Every key is optional except
// forge on a mirror outside github.com; a missing value leaves its
// placeholder in the written files, and the report names it.
type Defaults struct {
	Forge                 string            `yaml:"forge"`
	SignerImage           string            `yaml:"signer-image"`
	CosignPublicKeySHA256 string            `yaml:"cosign-public-key-sha256"`
	CheckImage            string            `yaml:"check-image"`
	NanolinterImages      map[string]string `yaml:"nanolinter-images"`
}

// ParseDefaults decodes the defaults file strictly: an unknown key is an
// operator's typo, not something to ignore.
func ParseDefaults(data []byte) (Defaults, error) {
	var defaults Defaults

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	if err := dec.Decode(&defaults); err != nil {
		return Defaults{}, fmt.Errorf("%s: %w: %w", DefaultsFile, err, errs.ErrInvalidConfig)
	}

	return defaults, nil
}

// Source is the Forge CI mirror a consumer pins: its host and repository
// path, the release tag and the commit it names.
type Source struct {
	Host       string // e.g. codefloe.com
	Repository string // e.g. itiquette/forge-ci
	Tag        string // e.g. v0.90.0
	Commit     string // full commit SHA of Tag
}

// examplesByType names the example each detected project type starts from.
//
//nolint:gochecknoglobals // static, read-only table.
var examplesByType = map[projecttype.Type]string{
	projecttype.Go:            "go-cli",
	projecttype.Cargo:         "cargo-app",
	projecttype.Maven:         "maven-app",
	projecttype.Gradle:        "gradle-app",
	projecttype.GradleAndroid: "android-app",
	projecttype.NPM:           "npm-app",
}

// typesByExample is the reverse, for the nanolinter flavour of an example.
//
//nolint:gochecknoglobals // static, read-only table.
var typesByExample = map[string]projecttype.Type{
	"go-cli": projecttype.Go, "go-service": projecttype.Go, "cargo-app": projecttype.Cargo,
	"maven-app": projecttype.Maven, "gradle-app": projecttype.Gradle,
	"android-app": projecttype.GradleAndroid, "npm-app": projecttype.NPM,
}

// ExampleFor returns the example a project type starts from, or "" when
// there is none.
func ExampleFor(t projecttype.Type) string { return examplesByType[t] }

// Examples lists every example name, sorted.
func Examples() []string {
	names := make([]string, 0, len(typesByExample))
	for name := range typesByExample {
		names = append(names, name)
	}

	slices.Sort(names)

	return names
}

// FileCopy is one example file and the path it takes in the consumer.
type FileCopy struct {
	From string // path inside the examples
	To   string // slash path relative to the consumer's root
}

// Replacement is one placeholder and its value, applied in order.
type Replacement struct{ Old, New string }

// FlavourOf is the nanolinter flavour an example lints in.
func FlavourOf(example string) string { return pipeline.NanolinterFlavour(typesByExample[example]) }

// Apply substitutes the replacements in order.
func Apply(content string, r []Replacement) string {
	for _, rep := range r {
		content = strings.ReplaceAll(content, rep.Old, rep.New)
	}

	return content
}

// placeholderPattern finds what an example leaves for a person to fill in.
var placeholderPattern = regexp.MustCompile(`PIN_[A-Z0-9_]+|FIXME[A-Za-z0-9_-]*|FORGE_HOST|OWNER/forge-ci|[a-z]+\.example\.(?:com|org)`)

// Placeholder is one value a written file still needs.
type Placeholder struct {
	Token string
	Line  int
}

// Remaining lists the placeholders content still holds, one per token and
// line, outside comment lines: a comment that explains a placeholder is not
// one.
func Remaining(content string) []Placeholder {
	var found []Placeholder

	for index, line := range strings.Split(content, "\n") {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "#") {
			continue
		}

		seen := map[string]bool{}
		for _, token := range placeholderPattern.FindAllString(line, -1) {
			if !seen[token] {
				seen[token] = true
				found = append(found, Placeholder{Token: token, Line: index + 1})
			}
		}
	}

	return found
}
