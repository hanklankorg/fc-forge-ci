// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	appenforcement "github.com/diggsweden/forge-ci/internal/app/enforcement"
	"github.com/diggsweden/forge-ci/internal/clicolor"
	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

// JVMReproducibilityInput drives `validate jvm-reproducibility`.
type JVMReproducibilityInput struct {
	ConfigPlanJSON string
}

// JVMReproducibility checks every planned Maven/Gradle (JVM + Android)
// artifact's build file for the settings reproducible archives need. Cargo
// has a symmetric check on Cargo.lock; this is the JVM-side counterpart.
//
// It is a static prerequisite check on the committed text, not evidence
// that a build is byte-identical: it never runs Maven or Gradle, so a
// setting that is present but overridden later, inherited from a file it
// does not read, or (for Gradle) written only inside a block comment or string
// is judged by its text alone. Byte-identical output is shown only by
// rebuilding, which belongs to the black-box reproducibility tier.
//
// A missing setting is a reproducible-builds finding. The administrator's
// enforce-reproducible-builds policy (strict by default) decides whether
// findings block; either way the report includes the exact snippet to paste
// into pom.xml / build.gradle. An unreadable or malformed build file, or a
// missing Gradle build script, is never a finding and always blocks. There
// is no consumer or per-artifact opt-out.
//
// Heuristic detection:
//
//   - Maven: real XML parse of pom.xml. Looks for
//     <project.build.outputTimestamp> in <properties>. A non-empty
//     value is taken as "configured" (we don't validate the literal —
//     it can be a fixed ISO-8601 or a property reference like
//     ${git.commit.author.time}).
//   - Gradle: line-by-line text check on build.gradle.kts / build.gradle,
//     skipping `//` line comments but with no block-comment, string or
//     evaluation-order awareness.
//     Looks for "preserveFileTimestamps" set to false AND
//     "reproducibleFileOrder" set to true, in either the Groovy bean
//     spelling or the Kotlin DSL is-getter spelling
//     ("isPreserveFileTimestamps = false"). The Gradle DSL is a
//     script, so a real parse would require a JVM; the substring
//     approach matches every form we've seen in the wild (with or
//     without "= ", inside an AbstractArchiveTask block or applied
//     directly).
func JVMReproducibility(ctx context.Context, out io.Writer, annot output.Annotator, in JVMReproducibilityInput) error {
	policy, err := enforcement.Current()
	if err != nil {
		return err
	}

	return jvmReproducibility(ctx, out, annot, in, policy)
}

// reproducibility is one build file's result: configured, a missing setting
// (a policy finding) or a file that could not be judged (always blocking).
type reproducibility int

const (
	reproducibleConfigured reproducibility = iota
	reproducibleMissing
	reproducibleBroken
)

func jvmReproducibility(_ context.Context, out io.Writer, annot output.Annotator, in JVMReproducibilityInput, policy enforcement.Policy) error {
	plan, err := parseConfigPlan(in.ConfigPlanJSON)
	if err != nil {
		return err
	}

	maven, gradle := jvmArtifactsFromPlan(plan)
	if len(maven)+len(gradle) == 0 {
		annot.Noticef("No Maven/Gradle artifacts to check for reproducibility")

		return nil
	}

	// Every directory is checked for safety before any is inspected, so a
	// bad later artifact refuses the run with nothing reported.
	mavenDirs, err := plannedWorkingDirs(maven)
	if err != nil {
		return err
	}

	gradleDirs, err := plannedWorkingDirs(gradle)
	if err != nil {
		return err
	}

	strict := policy.EnforceReproducibleBuilds
	results := make([]reproducibility, 0, len(mavenDirs)+len(gradleDirs))

	for _, dir := range mavenDirs {
		results = append(results, checkMavenReproducibility(dir, out, annot, strict))
	}

	for _, dir := range gradleDirs {
		results = append(results, checkGradleReproducibility(dir, out, annot, strict))
	}

	missing := 0

	for _, result := range results {
		switch result {
		case reproducibleBroken:
			return fmt.Errorf("jvm build files could not be checked for reproducibility: %w", errs.ErrValidation)
		case reproducibleMissing:
			missing++
		case reproducibleConfigured:
		}
	}

	if err := appenforcement.FindingsFor(out, policy, enforcement.ReproducibleBuilds, missing); err != nil {
		return fmt.Errorf("jvm reproducibility settings missing: %w", err)
	}

	return nil
}

// reproducibilityFinding annotates a missing setting at the severity the
// policy gives it: an error when it blocks, a warning when it is reported only.
func reproducibilityFinding(annot output.Annotator, strict bool, format string, args ...any) {
	if strict {
		annot.Errorf(format, args...)

		return
	}

	annot.Warningf(format+" (reported; enforce-reproducible-builds=false)", args...)
}

// parseConfigPlan unwraps the static config-plan JSON; same shape and
// version-pinning logic as plannedCargoArtifacts but reads the
// top-level config plan rather than the publish-stage plan, because
// Maven/Gradle artifacts live in release-build-stage, not publish.
func parseConfigPlan(value string) (pipeline.ConfigPlan, error) {
	if strings.TrimSpace(value) == "" {
		return pipeline.ConfigPlan{}, fmt.Errorf("config-plan-json is required: %w", errs.ErrUsage)
	}

	plan, err := pipeline.DecodeConfigPlan(value)
	if err != nil {
		return pipeline.ConfigPlan{}, err
	}

	if plan.Version != pipeline.ConfigPlanVersion {
		return pipeline.ConfigPlan{}, fmt.Errorf("config-plan-json has unsupported version %d: %w", plan.Version, errs.ErrInvalidConfig)
	}

	if err := pipeline.ValidateConfigPlan(plan); err != nil {
		return pipeline.ConfigPlan{}, err
	}

	return plan, nil
}

// jvmArtifactsFromPlan splits the planned artifacts into Maven and Gradle
// (JVM and Android) lists, in plan order.
func jvmArtifactsFromPlan(plan pipeline.ConfigPlan) ([]pipeline.PlannedArtifact, []pipeline.PlannedArtifact) {
	var maven, gradle []pipeline.PlannedArtifact

	for _, art := range plan.Artifacts.All {
		switch art.ProjectType {
		case projecttype.Maven:
			maven = append(maven, art)
		case projecttype.Gradle, projecttype.GradleAndroid:
			gradle = append(gradle, art)
		default:
			// NPM / Go / Cargo / XcodeIOS / Python / Meta / Auto /
			// Unknown — not JVM artifacts, skipped.
		}
	}

	return maven, gradle
}

// mavenPOM is the minimal slice of pom.xml needed for the
// reproducibility check. Anything we don't read goes through
// xml.Decoder unmodified; we never round-trip the document.
type mavenPOM struct {
	XMLName    xml.Name `xml:"project"`
	Properties struct {
		// The XML element name itself contains dots ("project.build.outputTimestamp").
		// encoding/xml can't bind that with a struct tag, so we capture all child
		// elements and look up the field by name below.
		Any []struct {
			XMLName xml.Name
			Value   string `xml:",chardata"`
		} `xml:",any"`
	} `xml:"properties"`
}

// mavenOutputTimestamp returns the (possibly empty) value of the
// <project.build.outputTimestamp> property. The bool reports whether
// the element was present at all — missing element and empty value
// are the two failure modes JVMReproducibility surfaces separately.
func mavenOutputTimestamp(pom mavenPOM) (string, bool) {
	for _, p := range pom.Properties.Any {
		if p.XMLName.Local == "project.build.outputTimestamp" {
			return strings.TrimSpace(p.Value), true
		}
	}

	return "", false
}

// checkMavenReproducibility reports whether the Maven artifact at dir declares
// the explicit reproducible-build setting.
func checkMavenReproducibility(dir string, out io.Writer, annot output.Annotator, strict bool) reproducibility {
	pomPath := filepath.Join(dir, "pom.xml")

	body, err := readWorkspaceFile(pomPath)
	if err != nil {
		annot.Errorf("pom.xml unreadable in %s: %v", displayDir(dir), err)

		return reproducibleBroken
	}

	var pom mavenPOM
	if err := xml.Unmarshal(body, &pom); err != nil { //nolint:musttag // the inner anonymous struct uses `xml:",any"` to capture every <properties> child element; explicit per-element tags wouldn't help since the element name we want (`project.build.outputTimestamp`) contains dots and isn't a valid struct-tag identifier.
		annot.Errorf("pom.xml in %s is malformed: %v", displayDir(dir), err)

		return reproducibleBroken
	}

	value, present := mavenOutputTimestamp(pom)

	switch {
	case !present:
		failMavenMissing(dir, "<project.build.outputTimestamp> property is not set", out, annot, strict)

		return reproducibleMissing
	case value == "":
		failMavenMissing(dir, "<project.build.outputTimestamp> is set but empty", out, annot, strict)

		return reproducibleMissing
	default:
		annot.Noticef("Maven reproducibility configured in %s (outputTimestamp=%s); declaration only, rebuild not checked", displayDir(dir), value)
		_, _ = fmt.Fprintf(out, "%s %s: project.build.outputTimestamp=%s\n", clicolor.Check(out), displayDir(dir), value)

		return reproducibleConfigured
	}
}

func failMavenMissing(dir, reason string, out io.Writer, annot output.Annotator, strict bool) {
	reproducibilityFinding(annot, strict, "%s in %s — explicit-settings policy failed; effective build behavior was not evaluated", reason, displayDir(dir))

	_, _ = fmt.Fprintf(out, "%s %s: %s\n", clicolor.Cross(out), displayDir(dir), reason)
	_, _ = fmt.Fprintf(out, "  Fix: add to pom.xml <properties>:\n")
	_, _ = fmt.Fprintf(out, "    <project.build.outputTimestamp>2026-01-01T00:00:00Z</project.build.outputTimestamp>\n")
	_, _ = fmt.Fprintln(out, "  Maven 3.10+/4.0.0-beta-5+ defaults or a parent POM may already provide reproducible settings.")
	_, _ = fmt.Fprintln(out, "  This policy reads this POM only; missing text is not proof of differing bytes. Compare clean rebuilds.")
	_, _ = fmt.Fprintf(out, "  Reference: https://maven.apache.org/guides/mini/guide-reproducible-builds.html\n")
}

// gradleBuildScripts is the ordered list of filenames `checkGradle`
// considers a build script. The first existing file wins — Gradle
// itself follows the same precedence (Kotlin DSL preferred over
// Groovy when both exist, but in practice projects pick one).
var gradleBuildScripts = []string{
	"build.gradle.kts",
	"build.gradle",
}

// checkGradleReproducibility reports whether the Gradle artifact at dir
// declares the explicit reproducible-archive settings.
func checkGradleReproducibility(dir string, out io.Writer, annot output.Annotator, strict bool) reproducibility {
	var (
		body []byte
		path string
		err  error
	)

	for _, name := range gradleBuildScripts {
		candidate := filepath.Join(dir, name)

		body, err = readWorkspaceFile(candidate)
		if err == nil {
			path = candidate

			break
		}
	}

	if path == "" {
		annot.Errorf("no build.gradle{,.kts} found in %s", displayDir(dir))

		return reproducibleBroken
	}

	preserve := gradleHasReproducibilitySetting(body, "preserveFileTimestamps", "false")
	order := gradleHasReproducibilitySetting(body, "reproducibleFileOrder", "true")

	switch {
	case preserve && order:
		annot.Noticef("Gradle reproducibility configured in %s; recognized text only, rebuild not checked", displayDir(dir))
		_, _ = fmt.Fprintf(out, "%s %s: preserveFileTimestamps=false + reproducibleFileOrder=true\n", clicolor.Check(out), displayDir(dir))

		return reproducibleConfigured
	case !preserve && !order:
		failGradleMissing(dir, "neither preserveFileTimestamps=false nor reproducibleFileOrder=true found", out, annot, strict)

		return reproducibleMissing
	case !preserve:
		failGradleMissing(dir, "preserveFileTimestamps=false not found", out, annot, strict)

		return reproducibleMissing
	default:
		failGradleMissing(dir, "reproducibleFileOrder=true not found", out, annot, strict)

		return reproducibleMissing
	}
}

// gradleHasReproducibilitySetting reports whether the build script
// assigns key to expectedValue. It tolerates the Groovy and Kotlin
// DSL idioms ("key = value", "key value", "key=value", with or
// without whitespace) and trailing characters within a line.
//
// This text check does not evaluate Gradle or load applied scripts/plugins.
// It can accept settings that are later overridden or appear in block comments;
// settings inherited from another file fail the check because they are not read.
func gradleHasReproducibilitySetting(body []byte, key, expectedValue string) bool {
	// Groovy assigns the bean property ("preserveFileTimestamps = false");
	// the Kotlin DSL reaches the same boolean through its is-getter
	// ("isPreserveFileTimestamps = false"), which is the spelling Gradle's
	// reproducible-archives guide shows for build.gradle.kts. Matching only
	// the Groovy spelling failed every Kotlin build that had done exactly
	// what the guide says.
	spellings := []string{key, "is" + strings.ToUpper(key[:1]) + key[1:]}

	for line := range strings.SplitSeq(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}

		for _, spelling := range spellings {
			if lineAssignsGradleSetting(trimmed, spelling, expectedValue) {
				return true
			}
		}
	}

	return false
}

// lineAssignsGradleSetting reports whether line assigns expectedValue to key,
// with key and value both bounded by non-identifier characters so
// "myPreserveFileTimestamps" and "trueX" do not count.
func lineAssignsGradleSetting(line, key, expectedValue string) bool {
	idx := strings.Index(line, key)
	if idx == -1 {
		return false
	}

	// Skip mentions where the key is a substring of a longer
	// identifier (e.g. "myPreserveFileTimestamps").
	if idx > 0 && isIdentChar(line[idx-1]) {
		return false
	}

	rest := strings.TrimSpace(line[idx+len(key):])
	// Strip the "= " / "=" / leading whitespace before the value.
	rest = strings.TrimLeft(rest, "= \t")

	if !strings.HasPrefix(rest, expectedValue) {
		return false
	}

	// Boundary check on the value too — "trueX" must not pass.
	tail := rest[len(expectedValue):]

	return tail == "" || !isIdentChar(tail[0])
}

func isIdentChar(b byte) bool {
	return (b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') ||
		b == '_'
}

func failGradleMissing(dir, reason string, out io.Writer, annot output.Annotator, strict bool) {
	reproducibilityFinding(annot, strict, "Gradle reproducibility not configured in %s — explicit-settings policy failed; effective build behavior was not evaluated (%s)", displayDir(dir), reason)

	_, _ = fmt.Fprintf(out, "%s %s: %s\n", clicolor.Cross(out), displayDir(dir), reason)
	_, _ = fmt.Fprintf(out, "  Fix: add to build.gradle{,.kts}:\n")
	_, _ = fmt.Fprintf(out, "    tasks.withType(AbstractArchiveTask).configureEach {\n")
	_, _ = fmt.Fprintf(out, "        preserveFileTimestamps = false\n")
	_, _ = fmt.Fprintf(out, "        reproducibleFileOrder = true\n")
	_, _ = fmt.Fprintf(out, "    }\n")
	_, _ = fmt.Fprintf(out, "  (Kotlin DSL: isPreserveFileTimestamps = false / isReproducibleFileOrder = true)\n")
	_, _ = fmt.Fprintln(out, "  Gradle 9+ standard archive tasks default to reproducible output; convention plugins may also configure it.")
	_, _ = fmt.Fprintln(out, "  This policy reads this script only. APK/AAB packaging and byte reproducibility require separate rebuild checks.")
	_, _ = fmt.Fprintf(out, "  Reference: https://docs.gradle.org/current/userguide/working_with_files.html#sec:reproducible_archives\n")
}
