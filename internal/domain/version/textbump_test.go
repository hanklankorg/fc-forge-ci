// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version_test

import (
	"errors"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/domain/version"
)

// The text bump is one function for the writer and the release check, so
// both agree on every byte, a retry included.
func TestTextBump_IsTheEnginesVersionFileWrite(t *testing.T) {
	t.Parallel()

	for name, scenario := range map[string]struct {
		kind   projecttype.Type
		body   string
		exists bool
		want   string
		writes bool
	}{
		"gradle":                 {projecttype.Gradle, "a=b\nversion=1.0.0\n", true, "a=b\nversion=1.2.3\n", true},
		"android":                {projecttype.GradleAndroid, "versionName=1.0.0\nversionCode=7\n", true, "versionName=1.2.3\nversionCode=8\n", true},
		"android retry":          {projecttype.GradleAndroid, "versionName=1.2.3\nversionCode=8\n", true, "versionName=1.2.3\nversionCode=8\n", false},
		"xcode":                  {projecttype.XcodeIOS, "MARKETING_VERSION = 1.0.0\n", true, "MARKETING_VERSION = 1.2.3\n", true},
		"xcode creates the file": {projecttype.XcodeIOS, "", false, "MARKETING_VERSION = 1.2.3\n", true},
	} {
		got, err := version.TextBump(scenario.kind, scenario.body, scenario.exists, "1.2.3")
		if err != nil || got.Body != scenario.want || got.Writes != scenario.writes {
			t.Errorf("%s: TextBump() = %+v, %v; want %q, writes %t", name, got, err, scenario.want, scenario.writes)
		}
	}

	if _, err := version.TextBump(projecttype.Gradle, "", false, "1.2.3"); !errors.Is(err, errs.ErrMissingInput) {
		t.Errorf("a missing Gradle file: %v", err)
	}

	if _, err := version.TextBump(projecttype.Maven, "", true, "1.2.3"); !errors.Is(err, errs.ErrUsage) {
		t.Errorf("a native type: %v", err)
	}
}
