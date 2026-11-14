// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version

import (
	"fmt"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

// TextBumpResult is what the engine writes to the version file of a project
// type without a native version tool.
type TextBumpResult struct {
	Body string
	// Writes is false for a retry that would change nothing: an Android
	// project already at the version keeps its versionCode.
	Writes bool
	// Created is true when an absent Xcode version file is written anew.
	Created bool
	// Property says whether the version property was rewritten or added.
	Property UpdatePropertyResult
	// VersionCode is the Android versionCode change.
	VersionCode IncrementVersionCodeResult
}

// TextBump is the engine's deterministic version bump of a Gradle, Gradle
// Android or Xcode version file: body is the file's content, exists whether
// it exists. The version is written as given. The release commit's job
// writes these bytes, and the release check recomputes them from the parent.
func TextBump(kind projecttype.Type, body string, exists bool, version string) (TextBumpResult, error) {
	if !exists && kind != projecttype.XcodeIOS {
		return TextBumpResult{}, fmt.Errorf("the %s version file does not exist: %w", kind, errs.ErrMissingInput)
	}

	switch kind {
	case projecttype.Gradle:
		out, res := UpdateGradleJVMVersion(body, version)

		return TextBumpResult{Body: out, Writes: true, Property: res}, nil
	case projecttype.GradleAndroid:
		// Properties interpret backslashes as escapes, including line continuation.
		out, res := UpdateOrAddProperty(body, "versionName", strings.ReplaceAll(version, `\`, `\\`), "=")
		code := IncrementVersionCode(out)
		// Setting the requested version again is a retry, not a new Android release.
		if out == body && !code.Added {
			return TextBumpResult{Body: body, Property: res}, nil
		}

		return TextBumpResult{Body: code.Body, Writes: true, Property: res, VersionCode: code}, nil
	case projecttype.XcodeIOS:
		if !exists {
			return TextBumpResult{Body: "MARKETING_VERSION = " + version + "\n", Writes: true, Created: true, Property: UpdatePropertyAdded}, nil
		}

		out, res := UpdateXcodeMarketingVersion(body, version)

		return TextBumpResult{Body: out, Writes: true, Property: res}, nil
	default:
		return TextBumpResult{}, fmt.Errorf("%s has no text version bump: %w", kind, errs.ErrUsage)
	}
}

// TextBumpTarget is the type a project's version file is bumped as and that
// file, relative to the project, for a type the engine bumps as text; ok is
// false for any other type. A Gradle Android library is bumped as a Gradle
// project. Empty names select the defaults, gradle.properties and
// versions.xcconfig.
func TextBumpTarget(kind projecttype.Type, library bool, gradleVersionFile, xcconfigFile string) (projecttype.Type, string, bool) {
	if library && kind == projecttype.GradleAndroid {
		kind = projecttype.Gradle
	}

	switch kind {
	case projecttype.Gradle, projecttype.GradleAndroid:
		if gradleVersionFile == "" {
			gradleVersionFile = "gradle.properties"
		}

		return kind, gradleVersionFile, true
	case projecttype.XcodeIOS:
		if xcconfigFile == "" {
			xcconfigFile = "versions.xcconfig"
		}

		return kind, xcconfigFile, true
	default:
		return kind, "", false
	}
}
