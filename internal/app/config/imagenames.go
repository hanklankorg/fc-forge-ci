// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package config

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/docslink"
	"github.com/diggsweden/forge-ci/internal/domain/ci"
	"github.com/diggsweden/forge-ci/internal/domain/config"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/output"
)

// imageNameGuide is the page that shows how a Containerfile names its images.
const imageNameGuide = "ecosystems.md#name-the-registry-of-an-image"

// WarnContainerfileShortImageNames warns about each Containerfile of a
// declared container that names an image without its registry, in FROM, in
// COPY --from= or in a RUN --mount. The registry such a name is pulled from
// is decided by the registries.conf of the runner that builds, so the image
// builds on one forge and fails, or pulls another image, on the next.
//
// It is advice and returns no error, so it cannot fail a job. A Containerfile
// that several containers share is reported once.
func WarnContainerfileShortImageNames(ctx context.Context, out io.Writer, annot output.Annotator, summary ci.SummarySink, in EmitConfigPlanInput) {
	var reported []string

	eachContainerfile(out, in, func(_ *config.Config, _ config.Container, file, body string) {
		if slices.Contains(reported, file) {
			return
		}

		reported = append(reported, file)

		if names := domaincontainer.ContainerfileShortImageNames(body); len(names) > 0 {
			warnShortImageNames(ctx, out, annot, summary, file, names)
		}
	})
}

// warnShortImageNames reports one Containerfile: an annotation on the first
// short name, a block in the job log and a line in the job summary. A name
// that cannot be judged is named in the log alone, as not checked.
func warnShortImageNames(
	ctx context.Context, out io.Writer, annot output.Annotator, summary ci.SummarySink,
	file string, names []domaincontainer.ShortImageName,
) {
	var block strings.Builder

	for _, name := range names {
		if name.Unjudged {
			fmt.Fprintf(&block, "%s line %d: `%s` not checked for a registry: it has a build argument with no default in the file\n", file, name.Line, name.Image)
		}
	}

	names = slices.DeleteFunc(slices.Clone(names), func(name domaincontainer.ShortImageName) bool { return name.Unjudged })
	if len(names) == 0 {
		_, _ = io.WriteString(out, block.String())

		return
	}

	first := names[0]

	annot.WarningAt(
		output.Annotation{File: file, Line: first.Line, Title: "The image is named without its registry"},
		"%s line %d names the image `%s` without a registry. The runner's registries.conf decides where it is pulled from, so the build fails, or pulls another image, on another runner. Write the full name, `%s` for the Docker Hub image; %s",
		file, first.Line, first.Resolved, first.DockerHubName(), docslink.See(imageNameGuide),
	)

	fmt.Fprintf(&block, "\n%s names an image without its registry\n", file)

	for _, name := range names {
		fmt.Fprintf(&block, "  line %d: %s\n    for the Docker Hub image write %s\n", name.Line, name.Resolved, name.DockerHubName())
	}

	block.WriteString("  A name without a registry is looked up in the registries the runner's\n")
	block.WriteString("  registries.conf lists. One runner pulls it from Docker Hub, another has no\n")
	block.WriteString("  list and fails the build, and a third pulls an image of that name from\n")
	block.WriteString("  another registry. A digest does not help where the name does not resolve.\n")
	fmt.Fprintf(&block, "  Write the registry in the name: %s\n\n", docslink.URL(imageNameGuide))

	_, _ = io.WriteString(out, block.String())

	if summary == nil {
		return
	}

	_ = summary.Append(ctx, fmt.Sprintf(
		"> [!WARNING]\n> `%s` line %d names the image `%s` without a registry, so the runner decides where it is pulled from. Write the full name, `%s` for the Docker Hub image. [Name the registry of an image](%s).\n\n",
		file, first.Line, first.Resolved, first.DockerHubName(), docslink.URL(imageNameGuide),
	))
}
