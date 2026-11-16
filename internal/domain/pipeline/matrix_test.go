// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline_test

import (
	"errors"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
)

// One row reads one build: a plan that builds an image and architecture
// twice, or publishes an image twice, has no row-keyed form, nor does one
// whose keys differ only by case.
func TestReleaseImagesPlanByNameRefusesARowKeyTwice(t *testing.T) {
	t.Parallel()

	build := pipeline.ReleaseImageBuild{Name: "app", Arch: "amd64"}
	image := pipeline.ReleaseImage{Name: "app"}

	for _, tc := range []struct {
		name string
		plan pipeline.ReleaseImagesPlan
		err  bool
	}{
		{"one of each", pipeline.ReleaseImagesPlan{Builds: []pipeline.ReleaseImageBuild{build, {Name: "app", Arch: "arm64"}}, Images: []pipeline.ReleaseImage{image}}, false},
		{"a build twice", pipeline.ReleaseImagesPlan{Builds: []pipeline.ReleaseImageBuild{build, build}, Images: []pipeline.ReleaseImage{image}}, true},
		{"an image twice", pipeline.ReleaseImagesPlan{Builds: []pipeline.ReleaseImageBuild{build}, Images: []pipeline.ReleaseImage{image, image}}, true},
		// Forgejo reads an object's keys regardless of case.
		{"a build's image by case", pipeline.ReleaseImagesPlan{Builds: []pipeline.ReleaseImageBuild{build, {Name: "App", Arch: "arm64"}}, Images: []pipeline.ReleaseImage{image}}, true},
		{"a build's architecture by case", pipeline.ReleaseImagesPlan{Builds: []pipeline.ReleaseImageBuild{build, {Name: "app", Arch: "AMD64"}}, Images: []pipeline.ReleaseImage{image}}, true},
		{"an image by case", pipeline.ReleaseImagesPlan{Builds: []pipeline.ReleaseImageBuild{build}, Images: []pipeline.ReleaseImage{image, {Name: "APP"}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			builds, buildErr := tc.plan.BuildsByName()
			images, imageErr := tc.plan.ImagesByName()

			err := errors.Join(buildErr, imageErr)
			if (err != nil) != tc.err || (err != nil && !errors.Is(err, errs.ErrInvalidConfig)) {
				t.Fatalf("err = %v, want refusal %t", err, tc.err)
			}

			if !tc.err && (len(builds["app"]) != 2 || images["app"].Name != "app") {
				t.Fatalf("builds %v, images %v", builds, images)
			}
		})
	}
}
