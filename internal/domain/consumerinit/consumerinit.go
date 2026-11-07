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
	// RuntimeImages are the build components' runtime images by toolchain
	// family: java, node, go, rust and android.
	RuntimeImages map[string]string `yaml:"runtime-images"`
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
	Repository string // e.g. examplescope/forge-ci
	Tag        string // e.g. v0.90.0
	Commit     string // full commit SHA of Tag
}

// examplesByType names the example each detected project type starts from.
var examplesByType = map[projecttype.Type]string{
	projecttype.Go:            "go-cli",
	projecttype.Cargo:         "cargo-app",
	projecttype.Maven:         "maven-app",
	projecttype.Gradle:        "gradle-app",
	projecttype.GradleAndroid: "android-app",
	projecttype.NPM:           "npm-app",
}

// typesByExample is the reverse, for the nanolinter flavour of an example.
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

// FileCopy is one release file and the path it takes in the consumer.
type FileCopy struct {
	From string // slash path inside the release
	To   string // slash path relative to the consumer's root
	// KeepExisting marks a file the consumer may already have, from an
	// earlier copy or another forge's init: an existing one is kept, not
	// a conflict.
	KeepExisting bool
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
var placeholderPattern = regexp.MustCompile(`PIN_[A-Z0-9_]+|FIXME[A-Za-z0-9_-]*|FORGE_HOST|OWNER/forge-ci|[a-z]+\.example\.(?:com|org)|\bmy-(?:cli|service|app)\b|com\.example\.myapp(?:\.[a-z]+)?`)

// Placeholder is one value a written file still needs.
type Placeholder struct {
	Token string
	Line  int
}

// Remaining lists the placeholders content still holds, one per token and
// line, outside comments: a comment that explains a placeholder is not one,
// whether it fills its own line or trails the value (`main-package: . #
// FIXME: …` is filled; the FIXME was the instruction).
func Remaining(content string) []Placeholder {
	var found []Placeholder

	for index, line := range strings.Split(content, "\n") {
		line = withoutComment(line)
		if strings.TrimSpace(line) == "" {
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

// withoutComment drops a YAML/shell comment: a # at the line start or after
// whitespace, outside single or double quotes. It is a reading aid for the
// placeholder scan, not a YAML parser; a # inside a block scalar is kept or
// dropped by the same rule, which errs towards reporting.
func withoutComment(line string) string {
	var (
		quote   rune
		escaped bool // the previous rune was a backslash inside "…"
	)

	for i, r := range line {
		switch {
		case escaped:
			escaped = false
		case quote == '"' && r == '\\':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
		case r == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
			return line[:i]
		}
	}

	return line
}

// Hint says how to fill one placeholder token: where its value comes from
// and, when the binary can find it, the command that does.
func Hint(token string) string {
	switch {
	case token == "PIN_FORGE_CI_SHA":
		return "the Forge CI commit the workflows pin; forge-ci init fills it with the commit of the release tag"
	case token == "PIN_SIGNER_IMAGE_DIGEST" || token == "PIN_COSIGN_PUBLIC_KEY_SHA256":
		return "your Forge CI mirror's signer image and Cosign key digest; its operator records them as signer-image and " +
			"cosign-public-key-sha256 in " + DefaultsFile + " so that forge-ci init fills them"
	case token == "PIN_DIGEST":
		return "the lint image's digest: forge-ci container manifest digest --anonymous --ref <image>:<tag>, then write @sha256:<digest>"
	case strings.HasPrefix(token, "PIN_"):
		return "the image's digest: forge-ci container manifest digest --anonymous --ref <image>:<tag>, then write @sha256:<digest>"
	case strings.HasPrefix(token, "FIXME-release-bot"):
		return "the release bot's email; it must be the principal of the bot key's line in .forge-ci/allowed_signers " +
			"(forge-ci setup signer --principal <email> --public-key <bot key>.pub)"
	case token == "FIXME-app" || token == "FIXME-service" || strings.HasPrefix(token, "FIXME-service-"):
		return "your project's name, as the build names its binary or jar"
	case strings.HasPrefix(token, "FIXME"):
		return "a value only you know; the comment above the line says which (bot-name: the release bot's display name)"
	case token == "FORGE_HOST" || token == "OWNER/forge-ci":
		return "your forge host and Forge CI mirror; forge-ci init fills them from the URL it is given"
	case strings.Contains(token, ".example."):
		return "your own host: the forge, registry or package repository the line names"
	case strings.HasPrefix(token, "com.example.myapp"):
		return "your Android application id"
	case strings.HasPrefix(token, "my-"):
		return "your project's name (artifact, binary or image); forge-ci init fills it when the build files name the project"
	default:
		return "a value for your project"
	}
}

// Project is what init reads from the consumer's own build files. Empty
// fields are unknown and leave their placeholders for a person to fill.
type Project struct {
	// Name is the artifact, binary or image name: the Go module's last path
	// element, the package.json/Cargo.toml name, the POM artifactId, or the
	// Gradle rootProject.name.
	Name string
	// MainPackage is the Go main package, "." or "./cmd/<x>".
	MainPackage string
}

// projectName is what a name has to look like to be written into a
// workflow unquoted: a binary, artifact or image name.
var projectName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// FillsName reports whether init writes p.Name for the example's names. The
// one example name is both the image name, which must be lowercase, and the
// jar or binary file name, which keeps the build's case; a name with capitals
// cannot be both, so it is left for the person rather than lowercased.
func (p Project) FillsName() bool {
	return projectName.MatchString(p.Name)
}

// ProjectReplacements fills the example's project placeholders with what the
// consumer's build files say. The example main packages are listed even when
// none was detected, kept as they are: ApplyProject then leaves ./cmd/my-cli
// for the person to fill rather than guessing ./cmd/<name>.
func ProjectReplacements(p Project) []Replacement {
	var replacements []Replacement

	for _, example := range []string{"./cmd/my-cli", "./cmd/my-service"} {
		mainPackage := example
		if p.MainPackage == "." || strings.HasPrefix(p.MainPackage, "./cmd/") {
			mainPackage = p.MainPackage
		}

		replacements = append(replacements, Replacement{Old: example, New: mainPackage})
	}

	if p.FillsName() {
		for _, old := range []string{"FIXME-service", "FIXME-app", "my-service", "my-cli", "my-app"} {
			replacements = append(replacements, Replacement{Old: old, New: p.Name})
		}
	}

	return replacements
}

// ApplyProject substitutes the project replacements in one left-to-right pass
// on whole tokens only, so that my-app in my-application, or FIXME-app in
// FIXME-apple, is left alone. The earliest match wins, and on a tie the first
// replacement listed: ./cmd/my-cli is replaced whole and never as ./cmd/<name>,
// and no replacement rewrites another's output.
func ApplyProject(content string, r []Replacement) string {
	var out strings.Builder

	prev := byte(' ') // the byte before content, once part of it is written

	for {
		at, match := -1, Replacement{}
		for _, rep := range r {
			if i := strings.Index(content, rep.Old); rep.Old != "" && i >= 0 && (at < 0 || i < at) {
				at, match = i, rep
			}
		}

		if at < 0 {
			out.WriteString(content)

			return out.String()
		}

		before := prev
		if at > 0 {
			before = content[at-1]
		}

		end := at + len(match.Old)
		whole := !wordByte(before) && (end == len(content) || !wordByte(content[end]))
		prev = match.Old[len(match.Old)-1]

		out.WriteString(content[:at])

		if whole {
			out.WriteString(match.New)
		} else {
			out.WriteString(match.Old)
		}

		content = content[end:]
	}
}

func wordByte(b byte) bool {
	return b == '_' || ('0' <= b && b <= '9') || ('a' <= b && b <= 'z') || ('A' <= b && b <= 'Z')
}
