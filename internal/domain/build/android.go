// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/summary"
)

// AndroidArtifactNamesInput drives ResolveAndroidArtifactNames. Mirrors
//
// When Override is non-empty, the override-mode names are emitted (the
// canonical artifact name from artifacts.yml drives them). Otherwise
// the date-stamped composed names are emitted from the remaining
// fields.
type AndroidArtifactNamesInput struct {
	Library     bool
	IncludeDate bool
	Prefix      string
	RepoName    string
	Flavor      string
	Override    string

	// Today is interpolated as YYYY-MM-DD into the date stamp. Tests
	// pass a fixed time; production passes time.Now().
	Today time.Time
}

// AndroidArtifactNames is the four output names the workflow consumes.
type AndroidArtifactNames struct {
	AARName     string
	DebugName   string
	ReleaseName string
	AABName     string
	SBOMName    string
}

// ResolveAndroidArtifactNames computes the four artifact names. It is
// pure: same input → same output, no I/O.
func ResolveAndroidArtifactNames(in AndroidArtifactNamesInput) (AndroidArtifactNames, error) {
	if in.RepoName == "" && in.Override == "" {
		return AndroidArtifactNames{}, fmt.Errorf("repo-name is required (or set --override): %w", errs.ErrUsage)
	}

	if in.Override != "" {
		if in.Library {
			return AndroidArtifactNames{AARName: in.Override, SBOMName: in.Override + "-sbom"}, nil
		}

		return AndroidArtifactNames{
			DebugName:   in.Override + "-debug",
			ReleaseName: in.Override + "-release",
			AABName:     in.Override,
			SBOMName:    in.Override + "-sbom",
		}, nil
	}

	dateStamp := ""
	if in.IncludeDate {
		dateStamp = in.Today.UTC().Format("2006-01-02") + " - "
	}

	prefix := ""
	if in.Prefix != "" {
		prefix = in.Prefix + " - "
	}

	flavorSuffix := ""
	if in.Flavor != "" {
		flavorSuffix = " - " + in.Flavor
	}

	base := dateStamp + prefix + in.RepoName + flavorSuffix
	if in.Library {
		return AndroidArtifactNames{AARName: base + " - AAR release", SBOMName: base + " - build SBOM"}, nil
	}

	return AndroidArtifactNames{
		DebugName:   base + " - APK debug",
		ReleaseName: base + " - APK release",
		AABName:     base + " - AAB release",
		SBOMName:    base + " - build SBOM",
	}, nil
}

// ResolveAndroidBuildTasksInput drives ResolveAndroidBuildTasks.
type ResolveAndroidBuildTasksInput struct {
	Library     bool
	Flavor      string
	BuildTypes  string // exact comma/whitespace-separated "debug" and/or "release" values
	IncludeAAB  bool
	BuildModule string // empty → "app"
	RunTests    bool   // run the selected module's enabled local unit-test variants
}

// AndroidBuildTypeSet is the exact parsed Android variant selection.
type AndroidBuildTypeSet struct {
	Debug   bool
	Release bool
}

// ResolveAndroidBuildTasks computes artifact tasks and an optional module unit-
// test aggregate. AGP may disable release unit-test variants, so do not invent
// variant task names. The test aggregate does not select connected-device tests.
func ResolveAndroidBuildTasks(in ResolveAndroidBuildTasksInput) (string, error) {
	// The module is trimmed and defaulted, and a module containing whitespace
	// is refused. The task list is a space-separated string that is later split
	// into Gradle arguments, so an untrimmed "  " produced the task
	// "  :bundleRelease", and "app extra" would have become two tasks.
	module := strings.TrimSpace(in.BuildModule)
	if module == "" {
		module = "app"
	}

	if strings.ContainsFunc(module, unicode.IsSpace) {
		return "", fmt.Errorf("build-module %q must be a single Gradle module name: %w", module, errs.ErrValidation)
	}

	flavorCap := capitalizeFirst(in.Flavor)
	var parts []string
	if in.RunTests {
		parts = append(parts, module+":test")
	}
	if in.Library {
		return strings.Join(append(parts, module+":assemble"+flavorCap+"Release"), " "), nil
	}

	buildTypes, err := ParseAndroidBuildTypes(in.BuildTypes)
	if err != nil {
		return "", err
	}

	if buildTypes.Debug {
		parts = append(parts, "assemble"+flavorCap+"Debug")
	}

	if buildTypes.Release {
		parts = append(parts, "assemble"+flavorCap+"Release")
	}

	if in.IncludeAAB && buildTypes.Release {
		parts = append(parts, module+":bundle"+flavorCap+"Release")
	}

	return strings.Join(parts, " "), nil
}

// ParseAndroidBuildTypes accepts only exact comma/whitespace-separated debug
// and release tokens. Config validation and planning share this parser with
// task resolution so substring lookalikes cannot enable a variant.
func ParseAndroidBuildTypes(raw string) (AndroidBuildTypeSet, error) {
	tokens := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
	if len(tokens) == 0 {
		return AndroidBuildTypeSet{}, fmt.Errorf("build-types must contain debug and/or release: %w", errs.ErrValidation)
	}

	var parsed AndroidBuildTypeSet

	for _, token := range tokens {
		switch token {
		case "debug":
			parsed.Debug = true
		case "release":
			parsed.Release = true
		default:
			return AndroidBuildTypeSet{}, fmt.Errorf("unsupported Android build type %q (want debug and/or release): %w", token, errs.ErrValidation)
		}
	}

	return parsed, nil
}

func hasAndroidBuildType(raw, want string) bool {
	parsed, err := ParseAndroidBuildTypes(raw)
	if err != nil {
		return false
	}

	switch want {
	case "debug":
		return parsed.Debug
	case "release":
		return parsed.Release
	default:
		return false
	}
}

// capitalizeFirst returns the input with its first ASCII byte upper-cased.
// The remainder is preserved because Gradle product flavors may be camelCase.
func capitalizeFirst(s string) string {
	if s == "" {
		return ""
	}

	return strings.ToUpper(s[:1]) + s[1:]
}

// ParseGradleVersionFromProperties extracts versionName and versionCode
// from literal key=value lines, allowing surrounding whitespace. Escapes,
// continuation lines and alternative Java-properties separators are not parsed.
// Missing lines yield "unknown", matching the bash fallback.
func ParseGradleVersionFromProperties(body string) (string, string) {
	versionName := "unknown" //nolint:goconst // test fixture / generic identifier — extracting would explode setup boilerplate.
	versionCode := "unknown"

	for line := range strings.SplitSeq(body, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}

		key, value = strings.TrimSpace(key), strings.TrimSpace(value)

		switch {
		case versionName == "unknown" && key == "versionName":
			versionName = value
		case versionCode == "unknown" && key == "versionCode":
			versionCode = value
		}

		if versionName != "unknown" && versionCode != "unknown" {
			break
		}
	}

	return versionName, versionCode
}

// AndroidSummaryInput drives RenderAndroidSummary.
type AndroidSummaryInput struct {
	Library     bool
	AARName     string
	JavaVersion string
	JDKDist     string
	BuildModule string
	Flavor      string
	BuildTypes  string
	IncludeAAB  bool
	Signing     bool
	SkipTests   bool
	Version     string
	VersionCode string
	DebugName   string
	ReleaseName string
	AABName     string
}

// RenderAndroidSummary is the pure markdown body of.
func RenderAndroidSummary(in AndroidSummaryInput, now time.Time) string {
	var b strings.Builder //nolint:varnamelen // idiomatic short name (testing/http/io conventions).

	if in.Library {
		in.BuildTypes, in.IncludeAAB, in.Signing = "release (library)", false, false
	}

	_, _ = fmt.Fprintf(&b, "## Android Variants Build Summary 📱\n\n")

	_, _ = fmt.Fprintf(&b, "### Configuration\n")
	_, _ = fmt.Fprintf(&b, "| Setting | Value |\n")
	_, _ = fmt.Fprintf(&b, "|---------|-------|\n")
	_, _ = fmt.Fprintf(&b, "| **Java** | %s (%s) |\n", summary.LiteralText(in.JavaVersion), summary.LiteralText(in.JDKDist))
	_, _ = fmt.Fprintf(&b, "| **Module** | %s |\n", summary.LiteralText(in.BuildModule))

	flavor := in.Flavor
	if flavor == "" {
		flavor = "default"
	}

	_, _ = fmt.Fprintf(&b, "| **Flavor** | %s |\n", summary.LiteralText(flavor))
	_, _ = fmt.Fprintf(&b, "| **Build Types** | %s |\n", summary.LiteralText(in.BuildTypes))
	_, _ = fmt.Fprintf(&b, "| **Include AAB** | %s |\n", checkmark(in.IncludeAAB))
	_, _ = fmt.Fprintf(&b, "| **Signing** | %s |\n", boolStatus(in.Signing))

	if in.SkipTests {
		_, _ = fmt.Fprintf(&b, "| **Tests** | ⊘ Skipped |\n")
	} else {
		_, _ = fmt.Fprintf(&b, "| **Tests** | ✓ Executed |\n")
	}

	if in.Version != "" && in.Version != "unknown" {
		if in.Library {
			_, _ = fmt.Fprintf(&b, "| **Version** | %s |\n", summary.LiteralText(in.Version))
		} else {
			_, _ = fmt.Fprintf(&b, "| **Version** | %s (%s) |\n", summary.LiteralText(in.Version), summary.LiteralText(in.VersionCode))
		}
	}

	_, _ = fmt.Fprintf(&b, "\n### Artifacts Generated\n")

	_, _ = b.WriteString(androidArtifactSummary(in))
	_, _ = fmt.Fprintf(&b, "\n*Build completed at %s*\n", now.UTC().Format("2006-01-02 15:04:05 UTC"))

	return b.String()
}

func androidArtifactSummary(in AndroidSummaryInput) string {
	if in.Library {
		return "✓ Release AAR: " + summary.InlineCode(in.AARName) + "\n"
	}

	var builder strings.Builder
	if hasAndroidBuildType(in.BuildTypes, "debug") {
		_, _ = fmt.Fprintf(&builder, "✓ Debug APK: %s\n", summary.InlineCode(in.DebugName))
	}

	if hasAndroidBuildType(in.BuildTypes, "release") {
		_, _ = fmt.Fprintf(&builder, "✓ Release APK: %s\n", summary.InlineCode(in.ReleaseName))
	}

	if in.IncludeAAB && hasAndroidBuildType(in.BuildTypes, "release") {
		_, _ = fmt.Fprintf(&builder, "✓ Release AAB: %s\n", summary.InlineCode(in.AABName))
	}

	return builder.String()
}

func checkmark(b bool) string {
	if b {
		return "✓"
	}

	return "✗"
}

func boolStatus(b bool) string {
	if b {
		return "✓ Enabled"
	}

	return "⊘ Disabled"
}
