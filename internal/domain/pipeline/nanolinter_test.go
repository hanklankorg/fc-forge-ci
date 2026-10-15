// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline_test

import (
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

func TestNanolinterImage_DerivesFlavourFromProjectType(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		projectType projecttype.Type
		want        string
	}{
		{projecttype.Go, "codefloe.com/itiquette/nanolinter:go"},
		{projecttype.Maven, "codefloe.com/itiquette/nanolinter:java"},
		{projecttype.Gradle, "codefloe.com/itiquette/nanolinter:kotlin"},
		{projecttype.GradleAndroid, "codefloe.com/itiquette/nanolinter:kotlin"},
		{projecttype.NPM, "codefloe.com/itiquette/nanolinter:javascript-typescript"},
		{projecttype.Cargo, "codefloe.com/itiquette/nanolinter:rust"},
		{projecttype.XcodeIOS, "codefloe.com/itiquette/nanolinter:swift"},
		{projecttype.Python, "codefloe.com/itiquette/nanolinter:core"},
		{projecttype.Meta, "codefloe.com/itiquette/nanolinter:core"},
		{projecttype.Unknown, "codefloe.com/itiquette/nanolinter:core"},
		{"", "codefloe.com/itiquette/nanolinter:core"},
	} {
		if got := pipeline.NanolinterImage("", tc.projectType); got != tc.want {
			t.Errorf("NanolinterImage(%q) = %q, want %q", tc.projectType, got, tc.want)
		}
	}
}

func TestNanolinterImage_OverrideWins(t *testing.T) {
	t.Parallel()

	pinned := "mirror.example/nanolinter:java@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if got := pipeline.NanolinterImage(" "+pinned+"\n", projecttype.Go); got != pinned {
		t.Errorf("override = %q, want %q", got, pinned)
	}

	if got := pipeline.NanolinterImage("   ", projecttype.Cargo); got != "codefloe.com/itiquette/nanolinter:rust" {
		t.Errorf("blank override = %q, want the derived rust flavour", got)
	}
}
