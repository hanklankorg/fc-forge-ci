// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	appartifact "github.com/diggsweden/forge-ci/internal/app/artifact"
	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/ci"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provenance"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// RuntimeSmokeLeg binds a native smoke job to candidate bytes and a source CLI pin.
type RuntimeSmokeLeg struct {
	Target   string `json:"target"`
	Arch     string `json:"arch"`
	Ref      string `json:"ref"`
	Checksum string `json:"checksum"`
}

// CollectRuntimePublication admits the complete plan and CLI pins before any
// artifact download. Existing multiarch-image metadata validation binds every
// candidate to its repository, architecture and run tag. Only a complete set
// is emitted; this operation never contacts a registry or runs candidate code.
func CollectRuntimePublication(ctx context.Context, downloader provider.RunArtifactDownloader, sink ci.OutputSink, workspace string, in RuntimePublicationInput) (retErr error) {
	plan, err := loadRuntimePublicationPlan(ctx, in)
	if err != nil {
		return err
	}

	checksums := make(map[string]string)
	for _, leg := range plan.Builds {
		if _, ok := checksums[leg.Arch]; ok {
			continue
		}

		checksum, readErr := runtimeCLIChecksum(workspace, leg.Arch)
		if readErr != nil {
			return readErr
		}

		checksums[leg.Arch] = checksum
	}

	work, err := os.MkdirTemp(workspace, ".runtime-metadata-*")
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, os.RemoveAll(work)) }()

	rows := make([]RuntimeSmokeLeg, 0, len(plan.Builds))
	for _, leg := range plan.Builds {
		name := "runtime-" + leg.Target
		artifactName := name + "-arch-" + leg.Arch

		if _, err := appartifact.Download(ctx, downloader, nil, nil, provider.RunArtifactDownload{
			Name: artifactName, Dir: filepath.Join(work, artifactName),
		}); err != nil {
			return err
		}

		repository := in.Namespace + "/" + leg.Name

		ref, err := signerArchRef(work, leg.Arch, repository, name, repository+":"+plan.CandidateTag+"-"+leg.Arch)
		if err != nil {
			return err
		}

		rows = append(rows, RuntimeSmokeLeg{Target: leg.Target, Arch: leg.Arch, Ref: ref, Checksum: checksums[leg.Arch]})
	}

	body, err := json.Marshal(struct {
		Include []RuntimeSmokeLeg `json:"include"`
	}{Include: rows})
	if err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	return sink.Set(ctx, "smoke-matrix", string(body))
}

func runtimeCLIChecksum(workspace, arch string) (string, error) {
	body, err := cliio.ReadRegularFile(filepath.Join(workspace, ".engine/bin/forge-ci-linux-"+arch+".sha256"))
	if err != nil {
		return "", err
	}

	subjects, err := provenance.ParseChecksums(bytes.NewReader(body))
	if err != nil {
		return "", err
	}

	if len(subjects) != 1 || subjects[0].Name != "bin/forge-ci" {
		return "", fmt.Errorf("runtime CLI checksum must name exactly bin/forge-ci: %w", errs.ErrValidation)
	}

	return subjects[0].SHA256, nil
}
