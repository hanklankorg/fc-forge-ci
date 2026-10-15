// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
)

func TestPlatformPlan_EmitsMatrixAndSuffix(t *testing.T) {
	t.Parallel()
	sink := fakeoutputsink.New(t)

	var out bytes.Buffer

	res, err := appcontainer.PlatformPlan(context.Background(), sink, &out, appcontainer.PlatformPlanInput{
		Platforms: "linux/amd64, linux/arm64",
		Platform:  "linux/arm64",
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := sink.Single("platforms-json"); got != `["linux/amd64","linux/arm64"]` {
		t.Errorf("platforms-json = %q", got)
	}

	if got := sink.Single("suffix"); got != "arm64" {
		t.Errorf("suffix = %q", got)
	}

	if len(res.Platforms) != 2 || res.Suffix != "arm64" {
		t.Errorf("output = %+v", res)
	}
}

func TestPlatformPlan_RejectsUnsupportedPlatform(t *testing.T) {
	t.Parallel()

	// Both the matrix and the single-platform input go through the same
	// check, and a platform this builder cannot produce is a validation
	// failure rather than a bad flag: the value is well-formed, it just
	// names something native builds do not support.
	for name, in := range map[string]appcontainer.PlatformPlanInput{
		"in the matrix":     {Platforms: "linux/amd64, linux/arm/v7"},
		"as the sole build": {Platform: "windows/amd64"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sink := fakeoutputsink.New(t)

			_, err := appcontainer.PlatformPlan(context.Background(), sink, nil, in)
			if !errors.Is(err, errs.ErrValidation) || !strings.Contains(fmt.Sprint(err), "unsupported container platform") {
				t.Errorf("err = %v, want ErrValidation naming the platform", err)
			}

			if got := sink.Keys(); len(got) != 0 {
				t.Errorf("emitted %q for a refused platform set", got)
			}
		})
	}
}

func TestPlatformPlan_DefaultsEmptyPlatforms(t *testing.T) {
	t.Parallel()
	sink := fakeoutputsink.New(t)

	out, err := appcontainer.PlatformPlan(context.Background(), sink, nil, appcontainer.PlatformPlanInput{})
	if err != nil {
		t.Fatal(err)
	}

	if got := sink.Single("platforms-json"); got != `["linux/amd64"]` {
		t.Errorf("platforms-json = %q", got)
	}

	if len(out.Platforms) != 1 || out.Platforms[0] != "linux/amd64" {
		t.Fatalf("platforms = %v", out.Platforms)
	}
}

func TestPlatformPlan_RejectsCommaOnlyPlatforms(t *testing.T) {
	t.Parallel()

	sink := fakeoutputsink.New(t)

	_, err := appcontainer.PlatformPlan(context.Background(), sink, nil, appcontainer.PlatformPlanInput{Platforms: ", ,"})
	if !errors.Is(err, errs.ErrUsage) || !strings.Contains(fmt.Sprint(err), "platforms is empty") {
		t.Errorf("err = %v, want ErrUsage — separators with nothing between them is a caller mistake", err)
	}

	if got := sink.Keys(); len(got) != 0 {
		t.Errorf("emitted %q for a refused platform set", got)
	}
}

func TestPlatformSuffix_DropsLinuxAndKeepsOtherOSes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ in, want string }{
		{"linux/amd64", "amd64"},
		{"linux/arm64", "arm64"},
		{"darwin/arm64", "darwin-arm64"},
		{"", ""},
	} {
		if got := appcontainer.PlatformSuffix(tc.in); got != tc.want {
			t.Errorf("PlatformSuffix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// RELEASE_PLATFORMS narrows the declared platforms; the matrix carries each
// leg's suffix, so a workflow names nothing per architecture.
func TestPlatformPlan_SelectsFromTheDeclaredPlatforms(t *testing.T) {
	t.Parallel()

	for selection, want := range map[string]string{
		"":            `[{"platform":"linux/amd64","suffix":"amd64"},{"platform":"linux/arm64","suffix":"arm64"}]`,
		"linux/amd64": `[{"platform":"linux/amd64","suffix":"amd64"}]`,
	} {
		sink := fakeoutputsink.New(t)

		if _, err := appcontainer.PlatformPlan(t.Context(), sink, nil, appcontainer.PlatformPlanInput{
			Platforms: "linux/amd64,linux/arm64",
			Select:    selection,
		}); err != nil {
			t.Fatalf("select %q: %v", selection, err)
		}

		if got := sink.Single("matrix-json"); got != want {
			t.Errorf("select %q: matrix-json = %s, want %s", selection, got, want)
		}
	}

	sink := fakeoutputsink.New(t)
	if _, err := appcontainer.PlatformPlan(t.Context(), sink, nil, appcontainer.PlatformPlanInput{
		Platforms: "linux/amd64,linux/arm64",
		Select:    "linux/riscv64",
	}); !errors.Is(err, errs.ErrUsage) {
		t.Errorf("an undeclared selection: err = %v, want a usage error", err)
	}
}
