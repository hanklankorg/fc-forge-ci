// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/validate"
)

func TestParseTagFormat_EmptyInputUsage(t *testing.T) {
	t.Parallel()

	_, err := validate.ParseTagFormat("")
	if !errors.Is(err, errs.ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}

	if !strings.Contains(err.Error(), "usage: validate tag-format") {
		t.Errorf("err = %v, want it to show the invocation", err)
	}
}

func TestParseTagFormat_StableReleases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		tag   string
		major string
		minor string
		patch string
	}{
		{"v1.0.0", "1", "0", "0"},
		{"v0.0.1", "0", "0", "1"},
		{"v10.20.30", "10", "20", "30"},
		{"v0.1.0", "0", "1", "0"},
		{"v100.200.300", "100", "200", "300"},
	}
	for _, cfg := range cases {
		tf, err := validate.ParseTagFormat(cfg.tag)
		if err != nil {
			t.Errorf("%s: unexpected error %v", cfg.tag, err)

			continue
		}

		if !tf.IsStable() {
			t.Errorf("%s: should be stable, got prerelease %q", cfg.tag, tf.Prerelease)
		}

		if tf.Major != cfg.major || tf.Minor != cfg.minor || tf.Patch != cfg.patch {
			t.Errorf("%s: %s.%s.%s, want %s.%s.%s",
				cfg.tag, tf.Major, tf.Minor, tf.Patch, cfg.major, cfg.minor, cfg.patch)
		}
	}
}

func TestParseTagFormat_StandardPrereleases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		tag        string
		prerelease string
	}{
		{"v2.3.4-beta.1", "beta.1"},
		{"v1.0.0-alpha", "alpha"},
		{"v1.0.0-rc.2", "rc.2"},
		{"v1.0.0-SNAPSHOT", "SNAPSHOT"},
		{"v1.0.0-dev", "dev"},
		{"v1.0.0-alpha.1", "alpha.1"},
		{"v1.0.0-beta", "beta"},
		{"v1.0.0-rc.10", "rc.10"},
	}
	for _, cfg := range cases {
		tf, err := validate.ParseTagFormat(cfg.tag)
		if err != nil {
			t.Errorf("%s: unexpected error %v", cfg.tag, err)

			continue
		}

		if tf.Prerelease != cfg.prerelease {
			t.Errorf("%s: prerelease %q, want %q", cfg.tag, tf.Prerelease, cfg.prerelease)
		}

		if !tf.PrereleaseStandard {
			t.Errorf("%s: PrereleaseStandard should be true for %q", cfg.tag, cfg.prerelease)
		}

		if tf.IsStable() {
			t.Errorf("%s: should NOT be stable", cfg.tag)
		}
	}
}

func TestParseTagFormat_BuildMetadata(t *testing.T) {
	t.Parallel()

	cases := []struct {
		tag        string
		prerelease string
		build      string
	}{
		{"v1.0.0+build.123", "", "build.123"},
		{"v1.0.0-rc.1+sha.abc123", "rc.1", "sha.abc123"},
		{"v2.3.4+meta-only", "", "meta-only"},
	}
	for _, cfg := range cases {
		tf, err := validate.ParseTagFormat(cfg.tag)
		if err != nil {
			t.Errorf("%s: unexpected error %v", cfg.tag, err)

			continue
		}

		if tf.Prerelease != cfg.prerelease {
			t.Errorf("%s: prerelease %q, want %q", cfg.tag, tf.Prerelease, cfg.prerelease)
		}

		if tf.Build != cfg.build {
			t.Errorf("%s: build %q, want %q", cfg.tag, tf.Build, cfg.build)
		}
	}
}

func TestParseTagFormat_NonStandardPrereleasesAcceptedButFlagged(t *testing.T) {
	t.Parallel()

	cases := []string{
		"v1.0.0-custom.123",
		"v1.0.0-preview.1",
		"v1.0.0-foo",
	}
	for _, tag := range cases {
		tf, err := validate.ParseTagFormat(tag)
		if err != nil {
			t.Errorf("%s: should be accepted, got %v", tag, err)

			continue
		}

		if tf.PrereleaseStandard {
			t.Errorf("%s: PrereleaseStandard should be false", tag)
		}
	}
}

func TestParseTagFormat_RejectsBadTags(t *testing.T) {
	t.Parallel()

	cases := []string{
		"1.0.0",    // missing v prefix
		"V1.0.0",   // uppercase V
		"v1.0",     // incomplete
		"v1",       // single digit
		"v1.0.0.0", // four parts
		"vX.Y.Z",   // non-numeric
		"v1a.0.0",  // mixed alphanumeric major
		// Leading zeros are invalid per official SemVer 2.0.0 — the regex
		// catches them at the CI gate. The bash heritage was permissive
		// here; we deliberately tightened.
		"v01.0.0",
		"v1.01.0",
		"v1.0.01",
		// Pre-release with leading-zero numeric identifier is also invalid.
		"v1.0.0-rc.01",
		"release-1.0.0",
		"version-1.0.0",
		"foobar",
		"abc123def",
	}
	for _, tag := range cases {
		_, err := validate.ParseTagFormat(tag)
		// A malformed tag is a rule failure, distinct from the ErrUsage
		// the empty input above earns.
		if !errors.Is(err, errs.ErrValidation) {
			t.Errorf("%s: err = %v, want ErrValidation", tag, err)

			continue
		}

		if !strings.Contains(err.Error(), "invalid tag format") {
			t.Errorf("%s: wrong error %v", tag, err)
		}
	}
}
