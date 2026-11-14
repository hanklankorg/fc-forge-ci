// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

func TestNanolinterFlavour_FollowsProjectType(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		projectType projecttype.Type
		want        string
	}{
		{projecttype.Go, "go"},
		{projecttype.Maven, "java"},
		{projecttype.Gradle, "kotlin"},
		{projecttype.GradleAndroid, "kotlin"},
		{projecttype.NPM, "javascript-typescript"},
		{projecttype.Cargo, "rust"},
		{projecttype.XcodeIOS, "swift"},
		{projecttype.Python, "core"},
		{projecttype.Meta, "core"},
		{projecttype.Unknown, "core"},
		{"", "core"},
	} {
		if got := pipeline.NanolinterFlavour(tc.projectType); got != tc.want {
			t.Errorf("NanolinterFlavour(%q) = %q, want %q", tc.projectType, got, tc.want)
		}
	}
}

// The lint job receives the checkout and the run context, so it runs only in
// an image pinned by digest: a tag, the flavour's own included, is refused
// with the flavour as the hint.
func TestNanolinterImage_AdmitsOnlyADigestPin(t *testing.T) {
	t.Parallel()

	pinned := "mirror.example/nanolinter:java@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if got, err := pipeline.NanolinterImage(" "+pinned+"\n", projecttype.Go); err != nil || got != pinned {
		t.Errorf("pinned = %q, %v; want %q", got, err, pinned)
	}

	for _, refused := range []string{"", "   ", "ghcr.io/itiquette/nanolinter:rust", "ghcr.io/itiquette/nanolinter:rust@sha256:abc", "a b@sha256:" + strings.Repeat("0", 64)} {
		_, err := pipeline.NanolinterImage(refused, projecttype.Cargo)
		if !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), "ghcr.io/itiquette/nanolinter:rust@sha256:<digest>") {
			t.Errorf("NanolinterImage(%q) = %v, want a usage error naming the rust flavour", refused, err)
		}
	}
}
