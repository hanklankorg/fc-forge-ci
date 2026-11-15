// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package sbom

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/cosign"
	"github.com/diggsweden/forge-ci/internal/adapters/git"
	"github.com/diggsweden/forge-ci/internal/adapters/maven"
	"github.com/diggsweden/forge-ci/internal/adapters/syft"
	appconfig "github.com/diggsweden/forge-ci/internal/app/config"
	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	appsbom "github.com/diggsweden/forge-ci/internal/app/sbom"
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/cli/signflags"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
)

// assembleCmd wires `forge-ci sbom assemble` — assemble a project's CISA
// SBOM layer set. It HARVESTS the language-native build BOM (e.g. the
// cyclonedx-gomod `bom.json` your build step emits) and SYFT-SCANS built
// artifacts/containers, then normalises everything into the canonical
// per-layer filenames (optionally bundling them into a release tar.gz).
//
// It runs AFTER the build: the build layer copies a pre-produced BOM rather
// than generating one, because the language's own dependency tooling resolves
// a more accurate graph than a filesystem scan. The scan layers need a built
// artifact (`analyzed-artifact`) or an image (`analyzed-container`).
func assembleCmd() *cli.Command {
	return &cli.Command{
		Name:  "assemble",
		Usage: "assemble a project's CISA SBOM layer set (harvest the build BOM + syft-scan artifacts/containers)",
		Description: `Assembles the requested CISA layers into canonical SBOM filenames. Runs after
the build: the 'build' layer harvests the language-native BOM your build emits
(cyclonedx-gomod / build-go.yml, cyclonedx-maven-plugin, …); the scan layers
syft-scan a built artifact or a container image.

EXAMPLES:
   # Assemble every layer for the auto-detected project (the default)
   forge-ci sbom assemble

   # Just the build layer (harvested from the pre-produced bom.json) for npm
   forge-ci sbom assemble --project-type=npm --layers=build

   # Build + analyzed-container for a Go service (--container-image is
   # required whenever the analyzed-container layer is requested)
   forge-ci sbom assemble --project-type=go \
       --layers=build,analyzed-container \
       --container-image=ghcr.io/examplescope/myapp:v1.2.3

   # The build SBOMs of the release's container-first Cargo artifacts, from
   # their lockfiles, into dist/sboms/ for release files declare --sbom
   forge-ci sbom assemble --artifacts-config .forge-ci/artifacts.yml \
       --project-type=cargo --version "${RELEASE_TAG}" --output-dir dist/sboms

With --artifacts-config, writes the build SBOM of every container-first
artifact of --project-type (go or cargo) the declaration plans one for, as
GitHub's sbom-go.yml and sbom-cargo.yml do, on a runner with that toolchain:
each named <artifact>-<version>-<sha>-build-sbom.cyclonedx.json in
--output-dir. The release refuses an SBOM bundle without them.`,
		Flags: append([]cli.Flag{
			&cli.StringFlag{Name: "project-type", Value: string(projecttype.Auto), Usage: "ecosystem driving the syft scan (auto/maven/gradle/npm/go/cargo/…)"},
			&cli.StringFlag{Name: "layers", Value: "all", Usage: "comma/space/newline-separated CISA layers to assemble: all | build | analyzed-artifact | analyzed-container (\"all\" = every layer)"},
			&cli.StringFlag{Name: "version", Usage: "release version embedded in the SBOM filenames"},
			&cli.StringFlag{Name: "name", Usage: "project slug used as the SBOM filename prefix"},
			&cli.StringFlag{Name: flagWorkingDir, Value: ".", Usage: "directory syft scans"},
			&cli.StringFlag{Name: "container-image", Usage: "container image to analyze (required for the analyzed-container layer)"},
			// Plan mode (per-artifact matrix; was `sbom generate artifacts`).
			&cli.StringFlag{Name: "plan", Sources: cli.EnvVars("CONFIG_PLAN_JSON"), Usage: "typed config-plan JSON: assemble one layer set per artifact in the plan (from \"config parse-artifacts\")"},
			&cli.StringFlag{Name: "acquisition-dir", Sources: cli.EnvVars("ACQUISITION_DIR"), Usage: "plan mode: use only this fresh producer tree for build BOMs, artifact scans, and generated outputs; requires --version"},
			&cli.StringFlag{Name: "sboms", Value: "all", Sources: cli.EnvVars("SBOMS"), Usage: "plan mode: sboms enum (\"all\", \"build,source\", …) selecting which layers to produce per artifact"},
			// Container-first mode: the build SBOMs forge-ci writes itself.
			&cli.StringFlag{Name: "artifacts-config", Usage: "container-first mode: the release's artifacts.yml; writes the build SBOM of each container-first artifact of --project-type it plans one for"},
			&cli.StringFlag{Name: "output-dir", Usage: "container-first mode: directory the build SBOMs are written to"},
			// Multi-artifact container mode (was `sbom generate container`).
			&cli.StringFlag{Name: "image-name", Sources: cli.EnvVars("IMAGE_NAME"), Usage: "container mode: base image name without tag/digest; with --image-digest forms the syft target"},
			&cli.StringFlag{Name: "image-digest", Sources: cli.EnvVars("IMAGE_DIGEST"), Usage: "container mode: sha256:… digest pinning the exact image manifest"},
			&cli.StringFlag{Name: "artifact-types", Sources: cli.EnvVars("ARTIFACT_TYPES"), Usage: "container mode: ecosystems embedded in the multi-artifact container SBOM"},
			&cli.StringFlag{Name: "platform", Sources: cli.EnvVars("PLATFORM"), Usage: "container mode: platform the digest is the image of, os/arch[/variant]; named in the SBOM filenames"},
			&cli.StringFlag{Name: "ref-name", Sources: cienv.RefName(), Usage: "container mode: git ref name (leading \"v\" stripped) used in the SBOM filename"},
			&cli.StringFlag{Name: "repository", Sources: cienv.Repository(), Usage: "container mode: \"owner/repo\" used to derive the project slug"},
			// Signing (any mode): cosign-sign each assembled SBOM into a .bundle.
			&cli.BoolFlag{Name: "sign", Sources: cli.EnvVars("SIGN_SBOMS"), Usage: "cosign-sign each assembled SBOM (produces <file>.bundle); reuses the \"release sign\" signing path"},
		}, assembleSignFlags()...),
		// One verb: assemble (one of three modes), then optionally sign. The
		// signer is built FIRST so a bad --sign-method fails fast, before any
		// assembly work.
		Action: func(ctx context.Context, cmd *cli.Command) error {
			var signer appsbom.FileSigner

			if cmd.Bool("sign") {
				built, err := buildSBOMSigner(cmd)
				if err != nil {
					return err
				}

				signer = built
			}

			if err := runAssembleMode(ctx, cmd); err != nil {
				return err
			}

			if signer == nil {
				return nil
			}

			return appsbom.SignAssembled(ctx, signer, cmd.String(flagWorkingDir), os.Stderr)
		},
	}
}

// runAssembleMode dispatches the assemble mode by the flags/env present, in
// precedence order: --artifacts-config → the container-first build SBOMs;
// --plan → per-artifact matrix; --image-name/-digest →
// multi-artifact container; otherwise → single-project assembly.
func runAssembleMode(ctx context.Context, cmd *cli.Command) error {
	// This verb can hold a signing key for --sign; syft runs with only the
	// gate tool environment, so neither it nor any other credential reaches it.
	scanner := syft.New()

	switch {
	case cmd.String("artifacts-config") != "":
		plans, err := appconfig.LoadReleaseStagePlans(appconfig.EmitConfigPlanInput{Path: cmd.String("artifacts-config")}, deps.Annotator(cmd), false)
		if err != nil {
			return err
		}

		_, err = appsbom.ContainerFirstBuildSBOMs(ctx, git.New(), buildSBOMGenerator{}, os.Stderr, os.Stderr, appsbom.ContainerFirstBuildSBOMsInput{
			Artifacts:   plans.ContainerFirstBuildSBOMs,
			ProjectType: projecttype.Type(cmd.String("project-type")),
			Version:     cmd.String("version"),
			OutputDir:   cmd.String("output-dir"),
		})

		return err
	case cmd.String("plan") != "":
		return appsbom.GenerateArtifacts(ctx, scanner, maven.New(), git.New(), os.Stderr, os.Stderr, appsbom.GenerateArtifactsInput{
			ConfigPlanJSON: cmd.String("plan"),
			SBOMs:          cmd.String("sboms"),
			Version:        cmd.String("version"),
			WorkingDir:     cmd.String(flagWorkingDir),
			AcquisitionDir: cmd.String("acquisition-dir"),
		})
	case cmd.String("image-name") != "" || cmd.String("image-digest") != "":
		return appsbom.GenerateContainer(ctx, scanner, maven.New(), git.New(), os.Stderr, os.Stderr, appsbom.GenerateContainerInput{
			ArtifactTypes: cmd.String("artifact-types"),
			RefName:       cmd.String("ref-name"),
			Repo:          cmd.String("repository"),
			ImageName:     cmd.String("image-name"),
			ImageDigest:   cmd.String("image-digest"),
			Platform:      cmd.String("platform"),
		})
	default:
		return appsbom.Generate(ctx, scanner, maven.New(), git.New(), buildSBOMGenerator{}, os.Stderr, os.Stderr, appsbom.GenerateInput{
			ProjectType:    cmd.String("project-type"),
			Layers:         cmd.String("layers"),
			Version:        cmd.String("version"),
			Name:           cmd.String("name"),
			WorkingDir:     cmd.String(flagWorkingDir),
			ContainerImage: cmd.String("container-image"),
		})
	}
}

// assembleSignFlags is the shared cosign flag set (--method, --key,
// --oidc-issuer and the self-hosted Sigstore endpoints) that every signing
// verb declares, so SIGN_FULCIO_URL and friends reach --sign here too. The
// spellings this verb used before, --sign-method and --sign-key, stay as
// aliases.
func assembleSignFlags() []cli.Flag {
	flags := signflags.Cosign(signflags.CosignOpts{
		MethodNote: "Applies with --sign; sigstore when omitted.",
		KeyNote:    "Applies with --sign.",
	})

	for _, flag := range flags {
		stringFlag, ok := flag.(*cli.StringFlag)
		if !ok {
			continue
		}

		switch stringFlag.Name {
		case "method":
			stringFlag.Aliases = append(stringFlag.Aliases, "sign-method")
		case "key":
			stringFlag.Aliases = append(stringFlag.Aliases, "sign-key")
		}
	}

	return flags
}

// buildSBOMSigner constructs the cosign signer used by --sign, reusing the same
// CosignSigner path as `release sign`. Sigstore (keyless) resolves the detected
// forge's OIDC issuer when none is given; KMS takes an explicit --key.
func buildSBOMSigner(cmd *cli.Command) (appsbom.FileSigner, error) {
	var method domainrelease.SignMethod

	switch methodName := cmd.String("method"); methodName {
	case "sigstore", "":
		method = domainrelease.SignMethodSigstore
	case "kms":
		method = domainrelease.SignMethodKMS
	default:
		return nil, fmt.Errorf("--method must be \"sigstore\" or \"kms\", got %q: %w", methodName, errs.ErrUsage)
	}

	// Only an issuer the operator named: see buildSigstoreSigner in release.
	oidcIssuer := cmd.String("oidc-issuer")

	endpoints := signflags.ReadEndpoints(cmd)

	return apprelease.NewCosignSigner(cosign.ForKeyRef(cmd.String("key")), apprelease.CosignSignerInput{
		Method:          method,
		KeyRef:          cmd.String("key"),
		OIDCIssuer:      oidcIssuer,
		FulcioURL:       endpoints.FulcioURL,
		RekorURL:        endpoints.RekorURL,
		TrustedRootPath: endpoints.TrustedRootPath,
	}, os.Stderr)
}
