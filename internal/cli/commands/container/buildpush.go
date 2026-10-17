// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/buildah"
	"github.com/diggsweden/forge-ci/internal/adapters/git"
	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	"github.com/diggsweden/forge-ci/internal/adapters/trivy"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/cli/planfile"
	"github.com/diggsweden/forge-ci/internal/cli/regflags"
	"github.com/diggsweden/forge-ci/internal/cli/secretenv"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// planScopeBuildPush is the plan-file scope of `container
// build-push-oci-image` in $FORGE_CI_PLAN (flag > plan > env > default).
const planScopeBuildPush = "container build-push-oci-image"

const tlsVerifyDisabled = "false"

func buildPushOCIImageCmd() *cli.Command {
	return &cli.Command{
		Name:  "build-push-oci-image",
		Usage: "build a multi-arch image with buildah and publish its OCI layout",
		Description: `Builds every platform from a JSON plan into one local manifest,
exports one OCI index, and publishes it through the registry adapter. All
platforms build on this one host, so this is only for Containerfiles without
RUN steps for the target platform (for example COPY-only images over
prebuilt per-architecture artifacts, or RUN only in FROM --platform=$BUILDPLATFORM
stages). An image whose target stages have RUN steps would run them under CPU
emulation for every non-native platform; build it with container
multiarch-image, one native job per architecture, instead. With
--scan-output-template, every exported platform must pass Trivy before publication.
Emits image, verified digest and platforms outputs; platforms is the comma-separated
list that was built, in plan order. This is the forge-ci implementation of the
public reusable-workflow build-push-oci-image contract. Every flag may also be
fed from the $FORGE_CI_PLAN plan file under the "container build-push-oci-image"
scope (flag > plan > env > default).`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagTag, Sources: planfile.Vars(planScopeBuildPush, flagTag, "IMAGE_TAG"), Usage: "image tag to build and push"},
			&cli.StringFlag{Name: flagContainerfile, Sources: planfile.Vars(planScopeBuildPush, flagContainerfile, "CONTAINER_FILE"), Usage: "path to the Containerfile/Dockerfile"},
			&cli.StringFlag{Name: "builds-json", Sources: planfile.Vars(planScopeBuildPush, "builds-json", "BUILDS_JSON"), Usage: `JSON array of per-platform builds: [{"platform":"linux/amd64","build-args":["KEY=VALUE"]}]`},
			&cli.StringFlag{Name: flagImage, Sources: planfile.Vars(planScopeBuildPush, flagImage, "IMAGE_NAME"), Usage: "image base (registry/owner/name); defaults to server host + lowercased repository"},
			&cli.StringFlag{Name: flagContext, Value: ".", Sources: planfile.Vars(planScopeBuildPush, flagContext, "BUILD_CONTEXT"), Usage: "build context directory"},
			&cli.StringFlag{Name: flagTitle, Sources: planfile.Vars(planScopeBuildPush, "title", "OCI_LABEL_TITLE"), Usage: "org.opencontainers.image.title"},
			&cli.StringFlag{Name: "description", Sources: planfile.Vars(planScopeBuildPush, "description", "OCI_LABEL_DESCRIPTION"), Usage: "org.opencontainers.image.description"},
			&cli.StringFlag{Name: "licenses", Sources: planfile.Vars(planScopeBuildPush, "licenses", "OCI_LABEL_LICENSES"), Usage: "org.opencontainers.image.licenses"},
			&cli.StringFlag{Name: "vendor", Sources: planfile.Vars(planScopeBuildPush, "vendor", "OCI_LABEL_VENDOR"), Usage: "org.opencontainers.image.vendor"},
			&cli.StringFlag{Name: "authors", Sources: planfile.Vars(planScopeBuildPush, "authors", "OCI_LABEL_AUTHORS"), Usage: "org.opencontainers.image.authors"},
			&cli.StringFlag{Name: "documentation", Sources: planfile.Vars(planScopeBuildPush, "documentation", "OCI_LABEL_DOCUMENTATION"), Usage: "org.opencontainers.image.documentation; defaults to <source>#readme"},
			&cli.StringFlag{Name: flagRefName, Sources: planfile.Vars(planScopeBuildPush, flagRefName, "OCI_LABEL_REF_NAME"), Usage: "org.opencontainers.image.ref.name; defaults to --tag"},
			&cli.StringFlag{Name: flagVersion, Sources: planfile.Vars(planScopeBuildPush, flagVersion, "OCI_LABEL_VERSION"), Usage: "org.opencontainers.image.version; defaults to --tag"},
			&cli.StringFlag{Name: flagRevision, Sources: planfile.Vars(planScopeBuildPush, flagRevision, "OCI_LABEL_REVISION"), Usage: "org.opencontainers.image.revision; defaults to git rev-parse HEAD"},
			regflags.AuthFile(regflags.AuthFileOpts{Usage: "registry auth file for builds, export and OCI publication", PlanScope: planScopeBuildPush}),
			&cli.StringFlag{Name: "scan-output-template", Sources: planfile.Vars(planScopeBuildPush, "scan-output-template", "SCAN_OUTPUT_TEMPLATE"), Usage: "scan every exported platform before publication; Trivy JSON path must contain {arch} or {platform}"},
			regflags.TLSVerify(regflags.TLSVerifyOpts{Env: "BUILD_PUSH_TLS_VERIFY", PlanScope: planScopeBuildPush}),
			&cli.StringFlag{Name: flagServerURL, Sources: planfile.Chain(planScopeBuildPush, flagServerURL, cienv.ServerURL()), Usage: "forge server URL used to derive defaults and source labels"},
			&cli.StringFlag{Name: flagRepository, Sources: planfile.Chain(planScopeBuildPush, flagRepository, cienv.Repository()), Usage: "owner/repo used to derive defaults and source labels"},
			&cli.IntFlag{Name: flagRetryAttempts, Value: 3, Sources: planfile.Vars(planScopeBuildPush, flagRetryAttempts, "MANIFEST_PUSH_RETRY_ATTEMPTS"), Usage: "manifest push attempts"},
			&cli.IntFlag{Name: flagRetryDelaySeconds, Value: 15, Sources: planfile.Vars(planScopeBuildPush, flagRetryDelaySeconds, "MANIFEST_PUSH_RETRY_DELAY_SECONDS"), Usage: "base delay between manifest push retry attempts"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			template := cmd.String("scan-output-template")
			if template != "" && !strings.Contains(template, "{arch}") && !strings.Contains(template, "{platform}") {
				return fmt.Errorf("scan-output-template must contain {arch} or {platform}: %w", errs.ErrUsage)
			}

			return deps.FromCmd(ctx, cmd, func(dep *deps.Deps) error {
				auth := regflags.Resolve(cmd)
				registry := ociregistry.WithAuthFileAndTLS(auth.AuthFile, auth.TLSVerify != tlsVerifyDisabled)

				var checkLayout func(context.Context, string, []string) error
				if template := cmd.String("scan-output-template"); template != "" {
					checkLayout = func(scanContext context.Context, layout string, platforms []string) error {
						return appcontainer.ImageEvidence(scanContext, nil, registry, trivy.New(secretenv.Names()...), nil, os.Stderr, os.Stderr,
							appcontainer.ImageEvidenceInput{ScanLayout: layout, Platforms: platforms, TrivyOutputTemplate: template})
					}
				}

				_, err := appcontainer.BuildPushOCIImage(ctx, buildah.New(), registry, git.New(), checkLayout, dep.OutputSink, os.Stderr, appcontainer.BuildPushOCIImageInput{
					Tag:           cmd.String(flagTag),
					Containerfile: cmd.String(flagContainerfile),
					BuildsJSON:    cmd.String("builds-json"),
					Image:         cmd.String(flagImage),
					Context:       cmd.String(flagContext),
					OCILabels: domaincontainer.OCILabels{
						Title:         cmd.String("title"),
						Description:   cmd.String("description"),
						Licenses:      cmd.String("licenses"),
						Vendor:        cmd.String("vendor"),
						Authors:       cmd.String("authors"),
						Documentation: cmd.String("documentation"),
						RefName:       cmd.String(flagRefName),
						Version:       cmd.String(flagVersion),
						Revision:      cmd.String(flagRevision),
					},
					AuthFile:      auth.AuthFile,
					TLSVerify:     auth.TLSVerify,
					ServerURL:     cmd.String(flagServerURL),
					Repository:    cmd.String(flagRepository),
					RetryAttempts: cmd.Int(flagRetryAttempts),
					RetryDelay:    time.Duration(cmd.Int(flagRetryDelaySeconds)) * time.Second,
				})

				return err
			})
		},
	}
}
