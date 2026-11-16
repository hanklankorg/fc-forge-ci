// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline

import (
	"fmt"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// Matrix is a workflow matrix of include rows, which a workflow reads as
// strategy.matrix: ${{ fromJSON(<output>) }}.
//
// Forgejo appends a row's values to the name of a job whose matrix comes
// from another job's output, whatever name the job sets, so a row holds only
// the short scalar values that tell its job apart. The job reads the rest
// of its row by them, from an output keyed by name.
type Matrix[Row any] struct {
	Include []Row `json:"include"`
}

// BuildRow is one native build's matrix row: its image and architecture.
type BuildRow struct {
	Name string `json:"name"`
	Arch string `json:"arch"`
}

// ImageRow is one image's matrix row: its name.
type ImageRow struct {
	Name string `json:"name"`
}

// BuildMatrix is the plan's builds as matrix rows, in plan order.
func (plan ReleaseImagesPlan) BuildMatrix() Matrix[BuildRow] {
	rows := make([]BuildRow, 0, len(plan.Builds))
	for _, build := range plan.Builds {
		rows = append(rows, BuildRow{Name: build.Name, Arch: build.Arch})
	}

	return Matrix[BuildRow]{Include: rows}
}

// ImageMatrix is the plan's images as matrix rows, in plan order.
func (plan ReleaseImagesPlan) ImageMatrix() Matrix[ImageRow] {
	rows := make([]ImageRow, 0, len(plan.Images))
	for _, image := range plan.Images {
		rows = append(rows, ImageRow{Name: image.Name})
	}

	return Matrix[ImageRow]{Include: rows}
}

// BuildsByName is the plan's builds keyed by image name and then by
// architecture, the keys of a BuildMatrix row. Two builds of one image and
// architecture are refused: one row would read the other's build. Forgejo
// looks an object's key up regardless of case, so two image names, or two
// architectures of one image, that differ only by case are refused too.
func (plan ReleaseImagesPlan) BuildsByName() (map[string]map[string]ReleaseImageBuild, error) {
	builds := map[string]map[string]ReleaseImageBuild{}
	names := map[string]string{}
	archs := map[string]string{}

	for _, build := range plan.Builds {
		if _, taken := builds[build.Name][build.Arch]; taken {
			return nil, fmt.Errorf("the plan builds image %s for %s twice: %w", build.Name, build.Arch, errs.ErrInvalidConfig)
		}

		if err := admitRowKey(names, build.Name, "image"); err != nil {
			return nil, err
		}

		if err := admitRowKey(archs, build.Name+"/"+build.Arch, "image and architecture"); err != nil {
			return nil, err
		}

		if builds[build.Name] == nil {
			builds[build.Name] = map[string]ReleaseImageBuild{}
		}

		builds[build.Name][build.Arch] = build
	}

	return builds, nil
}

// ImagesByName is the plan's images keyed by name, the key of an ImageMatrix
// row. Two images of one name, or of names that differ only by case, are
// refused.
func (plan ReleaseImagesPlan) ImagesByName() (map[string]ReleaseImage, error) {
	images := map[string]ReleaseImage{}
	names := map[string]string{}

	for _, image := range plan.Images {
		if _, taken := images[image.Name]; taken {
			return nil, fmt.Errorf("the plan publishes image %s twice: %w", image.Name, errs.ErrInvalidConfig)
		}

		if err := admitRowKey(names, image.Name, "image"); err != nil {
			return nil, err
		}

		images[image.Name] = image
	}

	return images, nil
}

// admitRowKey records key in seen by its lower-case form, the form Forgejo
// matches an object's key by, and refuses a key that differs from one seen
// before only by case: a row keyed by either would read either's values.
func admitRowKey(seen map[string]string, key, what string) error {
	folded := strings.ToLower(key)

	if earlier, taken := seen[folded]; taken && earlier != key {
		return fmt.Errorf("the plan names %s %s and %s, which differ only by case, and Forgejo reads row keys regardless of case: %w",
			what, earlier, key, errs.ErrInvalidConfig)
	}

	seen[folded] = key

	return nil
}
