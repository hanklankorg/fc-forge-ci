// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package config

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/docslink"
	"github.com/diggsweden/forge-ci/internal/domain/ci"
	"github.com/diggsweden/forge-ci/internal/domain/config"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

// rebuildGuide is the page that shows how a Containerfile copies the built
// artifact.
const rebuildGuide = "ecosystems.md#use-the-built-artifact-in-the-image"

// WarnContainerfileRebuilds warns about each declared container that is built
// from an artifact (`from:`) while its Containerfile runs that artifact's
// build again. Such an image holds a file made during the image build, not
// the artifact forge-ci built, tested, described in the SBOM and released.
//
// It is advice and returns no error, so it cannot fail a job.
func WarnContainerfileRebuilds(ctx context.Context, out io.Writer, annot output.Annotator, summary ci.SummarySink, in EmitConfigPlanInput) {
	eachContainerfile(out, in, func(cfg *config.Config, declared config.Container, file, body string) {
		prebuilt := prebuiltTypes(cfg.Artifacts, declared.From)

		rebuilds := slices.DeleteFunc(domaincontainer.ContainerfileRebuilds(body), func(r domaincontainer.Rebuild) bool {
			return !slices.Contains(prebuilt, r.Ecosystem)
		})
		if len(rebuilds) > 0 {
			warnRebuild(ctx, out, annot, summary, declared, file, rebuilds)
		}
	})
}

// eachContainerfile calls visit for every declared container whose
// Containerfile can be read, with the file's path and content. The
// Containerfiles are read from the working directory, the root of the
// checkout. A declaration or a Containerfile that cannot be read is left to
// the plan and the image build, which refuse it with their own message.
func eachContainerfile(out io.Writer, in EmitConfigPlanInput, visit func(cfg *config.Config, declared config.Container, file, body string)) {
	data, err := readArtifactsFile(in)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}

	var cfg *config.Config
	if err == nil {
		cfg, err = config.Parse(data)
	}

	if err != nil {
		_, _ = fmt.Fprintf(out, "Containerfiles not checked against %s: %v\n", in.Path, err)

		return
	}

	fsys := in.FS
	if fsys == nil {
		root, rootErr := os.OpenRoot(".")
		if rootErr != nil {
			return
		}
		defer func() { _ = root.Close() }()

		fsys = root.FS()
	}

	for _, declared := range cfg.Containers {
		file := path.Clean(filepath.ToSlash(cmp.Or(declared.ContainerFile, config.DefaultContainerFile)))

		if body, readErr := fs.ReadFile(fsys, file); readErr == nil {
			visit(cfg, declared, file, string(body))
		}
	}
}

// prebuiltTypes lists the project types of the artifacts a container is built
// from and that forge-ci builds before the image. A container-first Go or
// Cargo artifact is not one: its image build is the build.
func prebuiltTypes(artifacts []config.Artifact, from []string) []projecttype.Type {
	var types []projecttype.Type

	for _, artifact := range artifacts {
		if !slices.Contains(from, artifact.Name) ||
			config.GoArtifactBuildMode(artifact) == config.GoBuildModeContainerFirst ||
			config.CargoArtifactBuildMode(artifact) == config.CargoBuildModeContainerFirst {
			continue
		}

		types = append(types, artifact.ProjectType)
	}

	return types
}

// warnRebuild reports one container: an annotation on the first build
// command, a block in the job log and a line in the job summary.
func warnRebuild(
	ctx context.Context, out io.Writer, annot output.Annotator, summary ci.SummarySink,
	declared config.Container, file string, rebuilds []domaincontainer.Rebuild,
) {
	first := rebuilds[slices.IndexFunc(rebuilds, func(r domaincontainer.Rebuild) bool { return !r.FetchOnly })]

	annot.WarningAt(
		output.Annotation{File: file, Line: first.Line, Title: "The image rebuilds what forge-ci built"},
		"container %q: %s line %d runs `%s`. The image will hold a file built again during the image build, not the artifact forge-ci built, tested, described in the SBOM and released. Copy the built artifact instead; %s",
		declared.Name, file, first.Line, first.Command, docslink.See(rebuildGuide),
	)

	var block strings.Builder

	fmt.Fprintf(&block, "\nThe image of container %q rebuilds what forge-ci built\n", declared.Name)
	fmt.Fprintf(&block, "  Built from (from:): %s\n", strings.Join(declared.From, ", "))

	for _, rebuild := range rebuilds {
		fmt.Fprintf(&block, "  %s line %d: %s\n", file, rebuild.Line, rebuild.Command)
	}

	block.WriteString("  The image will hold a file built again during the image build. It is not the\n")
	block.WriteString("  artifact forge-ci built, tested, described in the SBOM and released.\n")
	block.WriteString("  Every image build also downloads the build tool and the dependencies, so it\n")
	block.WriteString("  fails when the package registry cannot be reached.\n")
	fmt.Fprintf(&block, "  Copy the built artifact into the image instead: %s\n\n", docslink.URL(rebuildGuide))

	_, _ = io.WriteString(out, block.String())

	if summary == nil {
		return
	}

	_ = summary.Append(ctx, fmt.Sprintf(
		"> [!WARNING]\n> Container `%s`: `%s` line %d runs `%s`, so the image holds a rebuilt file and not the artifact forge-ci built, tested, described in the SBOM and released. [Use the built artifact in the image](%s).\n\n",
		declared.Name, file, first.Line, first.Command, docslink.URL(rebuildGuide),
	))
}
