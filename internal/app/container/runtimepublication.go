// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/ci"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// RuntimePublicationInput binds a product roster file to source and CI coordinates.
type RuntimePublicationInput struct {
	RosterFile, PlatformMatrix, Namespace, SourceSHA, CheckedOutSHA, RunID, Ref string
}

// PlanRuntimePublication owns bounded source reads, strict decoding and output
// publication. It reaches no provider or registry and builds no executable.
func PlanRuntimePublication(ctx context.Context, sink ci.OutputSink, in RuntimePublicationInput) error {
	plan, err := loadRuntimePublicationPlan(ctx, in)
	if err != nil {
		return err
	}

	builds, err := json.Marshal(struct {
		Include []domaincontainer.RuntimeBuildLeg `json:"include"`
	}{Include: plan.Builds})
	if err != nil {
		return err
	}

	imagesJSON, err := json.Marshal(struct {
		Include []domaincontainer.RuntimeImage `json:"include"`
	}{Include: plan.Images})
	if err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	for _, output := range []struct{ key, value string }{
		{"build-matrix", string(builds)}, {"image-matrix", string(imagesJSON)}, {"candidate-tag", plan.CandidateTag},
	} {
		if err := sink.Set(ctx, output.key, output.value); err != nil {
			return err
		}
	}

	return nil
}

type runtimeTargetProjection struct {
	domaincontainer.RuntimeImage

	Repository string                   `json:"repository"`
	Tag        string                   `json:"tag"`
	Candidates []runtimeTargetCandidate `json:"candidates,omitempty"`
}

type runtimeTargetCandidate struct {
	Arch string `json:"arch"`
	Ref  string `json:"ref"`
}

// PlanRuntimeTarget projects one admitted target for build/assembly adapters.
// Metadata, when requested, uses the same native validator as collection.
// Nothing is emitted until the full roster and selected metadata pass.
func PlanRuntimeTarget(ctx context.Context, out io.Writer, in RuntimePublicationInput, target, arch, metadataRoot string) error {
	plan, err := loadRuntimePublicationPlan(ctx, in)
	if err != nil {
		return err
	}

	for _, image := range plan.Images {
		if image.Target != target {
			continue
		}

		if arch != "" {
			if !slices.Contains(image.Archs, arch) {
				return fmt.Errorf("runtime target does not select architecture %q: %w", arch, errs.ErrValidation)
			}

			image.Archs = []string{arch}
		}

		projection := runtimeTargetProjection{RuntimeImage: image, Repository: in.Namespace + "/" + image.Name, Tag: plan.CandidateTag}
		if metadataRoot != "" {
			for _, selected := range image.Archs {
				ref, err := signerArchRef(metadataRoot, selected, projection.Repository, "runtime-"+target, projection.Repository+":"+projection.Tag+"-"+selected)
				if err != nil {
					return err
				}

				projection.Candidates = append(projection.Candidates, runtimeTargetCandidate{Arch: selected, Ref: ref})
			}
		}

		if err := ctx.Err(); err != nil {
			return err
		}

		return json.NewEncoder(out).Encode(projection)
	}

	return fmt.Errorf("unknown or unselected runtime target %q: %w", target, errs.ErrValidation)
}

func loadRuntimePublicationPlan(ctx context.Context, in RuntimePublicationInput) (domaincontainer.RuntimePublicationPlan, error) {
	if err := ctx.Err(); err != nil {
		return domaincontainer.RuntimePublicationPlan{}, err
	}

	body, err := cliio.ReadRegularFile(in.RosterFile)
	if err != nil {
		return domaincontainer.RuntimePublicationPlan{}, err
	}

	var images []domaincontainer.RuntimeImage
	if err := decodeRuntimeJSON(body, &images); err != nil {
		return domaincontainer.RuntimePublicationPlan{}, err
	}

	var platforms []PlatformLeg
	if err := decodeRuntimeJSON([]byte(in.PlatformMatrix), &platforms); err != nil {
		return domaincontainer.RuntimePublicationPlan{}, err
	}

	archs := make([]string, 0, len(platforms))
	for _, platform := range platforms {
		if platform.Platform != "linux/"+platform.Suffix {
			return domaincontainer.RuntimePublicationPlan{}, fmt.Errorf("runtime platform and architecture do not agree: %w", errs.ErrInvalidConfig)
		}

		archs = append(archs, platform.Suffix)
	}

	return domaincontainer.PlanRuntimePublication(domaincontainer.RuntimePublicationInput{
		Images: images, Architectures: archs, Namespace: in.Namespace, SourceSHA: in.SourceSHA,
		CheckedOutSHA: in.CheckedOutSHA, RunID: in.RunID, Ref: in.Ref,
	})
}

func decodeRuntimeJSON(body []byte, into any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(into); err != nil {
		return fmt.Errorf("invalid runtime publication JSON: %w", errs.ErrInvalidConfig)
	}

	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("runtime publication JSON must contain exactly one value: %w", errs.ErrInvalidConfig)
	}

	return nil
}

func runtimeSourceManifest(in SignerImageAssembleInput) (SignerImageAssembleInput, string) {
	source := strings.TrimSuffix(in.ServerURL, "/") + "/" + in.Repository
	in.RequiredLabels = append(slices.Clone(in.RequiredLabels),
		"org.opencontainers.image.source="+source,
		"org.opencontainers.image.revision="+in.SourceSHA)

	return in, source
}
