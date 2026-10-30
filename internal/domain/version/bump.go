// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// UpdatePropertyResult tags whether the line was rewritten in place
// (Updated) or appended at the end (Added). The bash logged both cases
// distinctly so callers can mirror that.
type UpdatePropertyResult int

// Recognised UpdatePropertyResult values.
const (
	UpdatePropertyUpdated UpdatePropertyResult = iota
	UpdatePropertyAdded
)

// UpdateOrAddProperty rewrites the first exact property key (case-sensitive,
// anchored, with optional whitespace before sep) to `key<sep><value>`, or
// appends a new line if no match.
//
// An assignment already holding value is left as written, including its spacing.
// Returns the body and whether the property was found or added.
func UpdateOrAddProperty(body, key, value, sep string) (string, UpdatePropertyResult) {
	lines := strings.Split(body, "\n")
	for index, line := range lines {
		// The key may be indented, and the separator may carry spaces the
		// file did not: "versionCode = 41" is the same property as
		// "versionCode=41". Rewriting keeps the indentation it found.
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, key) && strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(trimmed, key)), strings.TrimSpace(sep)) {
			_, current, _ := strings.Cut(trimmed, strings.TrimSpace(sep))
			if strings.TrimSpace(current) == value {
				return body, UpdatePropertyUpdated
			}

			lines[index] = line[:len(line)-len(trimmed)] + key + sep + value

			return strings.Join(lines, "\n"), UpdatePropertyUpdated
		}
	}
	// Append. Preserve the body's trailing newline shape: if the input
	// ends with "\n" the appended line ends with "\n" too.
	suffix := key + sep + value + "\n"
	if !strings.HasSuffix(body, "\n") && body != "" {
		suffix = "\n" + suffix
	}

	return body + suffix, UpdatePropertyAdded
}

// IncrementVersionCodeResult reports whether the existing versionCode
// was incremented (with old / new) or whether a new versionCode=1
// line was appended.
type IncrementVersionCodeResult struct {
	Body  string
	Old   int
	New   int
	Added bool // true → "versionCode=1" appended
}

var versionCodeLine = regexp.MustCompile(`(?m)^[ \t]*versionCode[ \t]*=[ \t]*([^\s]*)`)

// IncrementVersionCode reads the first `versionCode=N` line from body,
// rewrites it with N+1, and returns the new body. When no
// `versionCode=` line exists, appends `versionCode=1`.
//
// Mirrors `increment_version_code` helper. A non-numeric value
// is treated as 0 (the bash's `tr -d ' '` + arithmetic would fail on
// non-numerics; this is a stricter, defined behaviour).
func IncrementVersionCode(body string) IncrementVersionCodeResult {
	m := versionCodeLine.FindStringSubmatchIndex(body)
	if m == nil {
		// Append.
		out, _ := UpdateOrAddProperty(body, "versionCode", "1", "=")

		return IncrementVersionCodeResult{Body: out, Old: 0, New: 1, Added: true}
	}

	rawValue := body[m[2]:m[3]]
	cur, _ := strconv.Atoi(strings.TrimSpace(rawValue))
	next := cur + 1

	out := body[:m[0]] + "versionCode=" + strconv.Itoa(next) + body[m[1]:]

	return IncrementVersionCodeResult{Body: out, Old: cur, New: next}
}

var gradleJVMVersionLine = regexp.MustCompile(`(?m)^[ \t]*version[ \t]*=.*`)

// UpdateGradleJVMVersion rewrites the first `^version=` line to the
// given version, or appends one if none exists. Mirrors the JVM gradle
// branch of bump-version.sh — strictly anchored on `version=` (with
// separator) so `versionName=` / `versionCode=` are not affected.
func UpdateGradleJVMVersion(body, version string) (string, UpdatePropertyResult) {
	// Properties interpret backslashes as escapes, including line continuation.
	version = strings.ReplaceAll(version, `\`, `\\`)

	if gradleJVMVersionLine.MatchString(body) {
		return gradleJVMVersionLine.ReplaceAllStringFunc(body, func(string) string { return "version=" + version }), UpdatePropertyUpdated
	}

	suffix := "version=" + version + "\n"
	if !strings.HasSuffix(body, "\n") && body != "" {
		suffix = "\n" + suffix
	}

	return body + suffix, UpdatePropertyAdded
}

// UpdateXcodeMarketingVersion rewrites the first
// `^MARKETING_VERSION = ` line to the new value, or appends a new line
// when no match. Used for versions.xcconfig.
func UpdateXcodeMarketingVersion(body, version string) (string, UpdatePropertyResult) {
	return UpdateOrAddProperty(body, "MARKETING_VERSION", version, " = ")
}

// CargoSection identifies which Cargo.toml section drives the version.
// Explicit package versions own the release; inherited versions use the workspace.
type CargoSection int

// Recognised CargoSection values.
const (
	CargoSectionNone CargoSection = iota
	CargoSectionWorkspacePackage
	CargoSectionPackage
)

// UpdateCargoVersion replaces only the owning version's string token, preserving
// comments, formatting and unrelated manifest content. A package must either
// declare a string version or explicitly inherit the workspace version.
func UpdateCargoVersion(body, version string) (string, CargoSection, error) {
	if !utf8.ValidString(version) {
		return body, CargoSectionNone, fmt.Errorf("cargo version must be valid UTF-8: %w", errs.ErrValidation)
	}
	// Keep the double-quoted layout using JSON's basic-string escapes. TOML
	// additionally requires escaping DEL, which JSON permits as a literal byte.
	encoded, err := json.Marshal(version)
	if err != nil {
		return body, CargoSectionNone, fmt.Errorf("encode cargo version: %w: %w", err, errs.ErrValidation)
	}

	var document map[string]any
	if err := toml.Unmarshal([]byte(body), &document); err != nil {
		return body, CargoSectionNone, fmt.Errorf("parse cargo manifest: %w: %w", err, errs.ErrValidation)
	}

	section, err := cargoVersionOwner(document)
	if err != nil {
		return body, CargoSectionNone, err
	}

	target := []string{"workspace", "package", "version"}
	if section == CargoSectionPackage {
		target = []string{"package", "version"}
	}

	// Decode first to reject duplicate keys/tables; use syntax ranges only for
	// the edit, so quoted keys and multiline strings cannot impersonate headers.
	var parser unstable.Parser
	parser.Reset([]byte(body))

	var table []string

	for parser.NextExpression() {
		node := parser.Expression()
		if node.Kind != unstable.Table && node.Kind != unstable.ArrayTable && node.Kind != unstable.KeyValue {
			continue
		}

		var key []string
		for keys := node.Key(); keys.Next(); {
			key = append(key, string(keys.Node().Data))
		}

		if node.Kind != unstable.KeyValue {
			table = key

			continue
		}

		if !slices.Equal(append(append([]string{}, table...), key...), target) || node.Value().Kind != unstable.String {
			continue
		}

		raw := node.Value().Raw
		start, end := int(raw.Offset), int(raw.Offset)+int(raw.Length)

		return body[:start] + strings.ReplaceAll(string(encoded), "\x7f", `\u007f`) + body[end:], section, nil
	}

	return body, section, fmt.Errorf("cargo manifest has no writable string version in [package] or [workspace.package]: %w", errs.ErrValidation)
}

func cargoVersionOwner(document map[string]any) (CargoSection, error) {
	raw, exists := document["package"]
	if !exists {
		return CargoSectionWorkspacePackage, nil
	}

	pkg, ok := raw.(map[string]any)
	if !ok {
		return CargoSectionNone, fmt.Errorf("invalid [package] table: %w", errs.ErrValidation)
	}

	switch value := pkg["version"].(type) {
	case string:
		return CargoSectionPackage, nil
	case map[string]any:
		if value["workspace"] == true && len(value) == 1 {
			return CargoSectionWorkspacePackage, nil
		}
	}

	return CargoSectionNone, fmt.Errorf("[package] has no version field of string or workspace = true inheritance type: %w", errs.ErrValidation)
}
