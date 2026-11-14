// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package baseimages

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"strings"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
)

// PublishChildBaseInput reuses the root publisher's staging/evidence ports, but
// carries a separate caller-verified immediate parent expectation. The producer
// does not authenticate that parent; protected workflow callers must do so first.
type PublishChildBaseInput struct {
	PublishRootBaseInput

	ParentBase BaseImageParent
}

// PublishChildBase publishes the unsigned candidates of one cold child base,
// or of several that are siblings on the one parent. No recursive graph, warm
// cache, final-tag write or signing authority is added.
func PublishChildBase(ctx context.Context, registry appcontainer.StagedSignerRegistry, out io.Writer, in PublishChildBaseInput) error {
	images, entries, err := admitChildBasePublication(in.PublishRootBaseInput, in.ParentBase)
	if err != nil {
		return err
	}

	return publishBaseStage(ctx, registry, out, in.PublishRootBaseInput, images, entries)
}

// ValidateChildBasePlan admits the entire architecture partition and requires
// the same immediate parent in caller facts, build arguments and image lineage.
// Ref/ID comparison is not signature verification or producer authentication.
func ValidateChildBasePlan(plan pipeline.ReleaseImagesPlan, repository string, parent BaseImageParent) error {
	_, _, err := admitChildBasePublication(PublishRootBaseInput{Plan: plan, ExpectedRepository: repository}, parent)

	return err
}

// admitChildBasePublication holds every image of the stage to the one parent:
// each is a sibling, with that parent in its lineage, as the one registry
// input of each of its builds and in the build argument that carries it.
func admitChildBasePublication(in PublishRootBaseInput, parent BaseImageParent) ([]pipeline.ReleaseImage, []imageledger.Entry, error) {
	images, entries, err := admitBaseStagePublication(in)
	if err != nil {
		return nil, nil, err
	}

	for idx, image := range images {
		if err := admitChildBaseParent(in.Plan.Builds, image, entries[idx], parent); err != nil {
			return nil, nil, err
		}
	}

	return images, entries, nil
}

func admitChildBaseParent(builds []pipeline.ReleaseImageBuild, image pipeline.ReleaseImage, entry imageledger.Entry, parent BaseImageParent) error {
	if _, err := baseInputByFlavor([]BaseInput{{Flavor: image.Flavor, BaseInputID: image.BaseInputID, ParentBase: &parent}}); err != nil {
		return err
	}

	if parent.InputID == image.BaseInputID {
		return fmt.Errorf("child base requires a distinct parent input ID: %w", errs.ErrInvalidConfig)
	}

	expected := map[string]any{"ref": parent.Ref, "input_id": parent.InputID}
	if !reflect.DeepEqual(entry.Provenance["parent_base"], expected) {
		return fmt.Errorf("child base lineage must match the explicit parent projection exactly: %w", errs.ErrInvalidConfig)
	}

	argument := ""

	for _, build := range builds {
		if build.Name != image.Name {
			continue
		}

		if len(build.RegistryInputs) != 1 {
			return fmt.Errorf("child base requires exactly one prepared parent per architecture: %w", errs.ErrInvalidConfig)
		}

		for key, ref := range build.RegistryInputs {
			if ref != parent.Ref || (argument != "" && argument != key) {
				return fmt.Errorf("child base registry input differs from its parent projection: %w", errs.ErrInvalidConfig)
			}

			argument = key
		}

		matches := 0

		for line := range strings.SplitSeq(build.BuildArgs, "\n") {
			key, value, _ := strings.Cut(line, "=")
			if key == argument {
				if value != parent.Ref {
					return fmt.Errorf("child base parent build argument conflicts: %w", errs.ErrInvalidConfig)
				}

				matches++
			}
		}

		if matches != 1 {
			return fmt.Errorf("child base requires one exact parent build argument: %w", errs.ErrInvalidConfig)
		}
	}

	return nil
}
