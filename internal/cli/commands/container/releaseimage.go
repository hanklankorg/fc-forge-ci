// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/cosign"
	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/cli/regflags"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provenance"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// releaseImagesVerifyCmd is the `container release-images verify` verb: it
// re-checks one already-produced, digest-pinned release image at the trust
// boundary. It lives under release-images (the public boundary) rather than in
// a singleton release-image group.
func releaseImagesVerifyCmd() *cli.Command {
	return &cli.Command{
		Name:  subCmdValidate,
		Usage: "validate an existing digest-pinned release image before reusing it",
		Description: `Checks Cosign signature, CycloneDX SBOM attestation, and SLSA
provenance fields for a digest-pinned release image. With --allow-reattest, a
provenance mismatch may still return status "reattestable" when the image is
signed, SBOM-attested, and its OCI release labels match the expected identity.
The command prints one status token to stdout on success: verified or
reattestable.`,
		Flags: append(baseImagesPublicKeyFlags(),
			&cli.StringFlag{Name: flagRef, Required: true, Sources: cli.EnvVars("IMAGE_REF"), Usage: "digest-pinned image ref to verify"},
			&cli.StringFlag{Name: "expected-tag", Required: true, Sources: cli.EnvVars("RELEASE_TAG"), Usage: "release tag expected in SLSA workflow externalParameters.workflow.ref"},
			&cli.StringFlag{Name: "expected-commit", Required: true, Sources: cli.EnvVars("RELEASE_SHA"), Usage: "source commit expected in SLSA resolvedDependencies[].digest.gitCommit"},
			&cli.StringFlag{Name: flagExpectedSource, Required: true, Sources: cli.EnvVars("EXPECTED_SOURCE"), Usage: "source repository URL expected in SLSA workflow externalParameters.workflow.repository"},
			&cli.StringFlag{Name: flagExpectedWorkflow, Required: true, Sources: cli.EnvVars("EXPECTED_WORKFLOW"), Usage: "workflow path expected in SLSA workflow externalParameters.workflow.path"},
			&cli.StringFlag{Name: "expected-base-ref", Sources: cli.EnvVars("EXPECTED_BASE_REF"), Usage: "optional digest-pinned base ref expected in SLSA base lineage"},
			&cli.StringFlag{Name: "expected-base-input-id", Sources: cli.EnvVars("EXPECTED_BASE_INPUT_ID"), Usage: "optional sha256 base input ID expected in SLSA base lineage"},
			&cli.BoolFlag{Name: "allow-reattest", Sources: cli.EnvVars("ALLOW_REATTEST"), Usage: "allow signed/SBOM-attested images with matching OCI identity labels to be re-attested"},
			&cli.StringFlag{Name: "identity-version", Usage: "OCI org.opencontainers.image.version expected for --allow-reattest; defaults to --expected-tag"},
			&cli.StringFlag{Name: "identity-ref-name", Usage: "OCI org.opencontainers.image.ref.name expected for --allow-reattest; defaults to --expected-tag"},
			&cli.StringFlag{Name: "identity-source", Usage: "OCI org.opencontainers.image.source expected for --allow-reattest; defaults to --expected-source"},
			regflags.AuthFile(regflags.AuthFileOpts{Usage: "Docker/containers auth config for registry label reads and cosign verification"}),
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if err := validateReleaseImageCLIInput(cmd); err != nil {
				return err
			}

			registry := ociregistry.WithAuthFile(cmd.String(flagAuthFile))

			return deps.FromCmd(ctx, cmd, func(dep *deps.Deps) error {
				return withReleaseImageDockerConfig(cmd.String(flagAuthFile), func() error {
					result, err := appcontainer.VerifyExistingReleaseImage(ctx,
						cosign.NewIsolated("DOCKER_CONFIG"),
						registry,
						os.Stderr,
						releaseImageVerifyExistingInputFromCmd(cmd),
					)
					if err != nil {
						return err
					}

					_, _ = fmt.Fprintln(os.Stdout, result.Status)

					return dep.OutputSink.Set(ctx, "status", result.Status)
				})
			})
		},
	}
}

func validateReleaseImageCLIInput(cmd *cli.Command) error {
	if err := validateReleaseImagePublicKey(cmd.String("cosign-public-key-path"), cmd.String("cosign-public-key-sha256")); err != nil {
		return err
	}

	if workflow := cmd.String(flagExpectedWorkflow); !provenance.ValidWorkflowPath(workflow) {
		return fmt.Errorf("release image verify: unsafe expected-workflow path: %s: %w", workflow, errs.ErrUsage)
	}

	return nil
}

func validateReleaseImagePublicKey(path, want string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("release image verify: cosign-public-key-path is required: %w", errs.ErrUsage)
	}

	// The same relative-to-the-checkout rule the base-image verbs apply to this
	// flag; the release verb used to accept an absolute or climbing path.
	if !pathsafe.Relative(path) {
		return fmt.Errorf("release image verify: unsafe Cosign public key path: %s: %w", path, errs.ErrUsage)
	}

	want = strings.TrimSpace(want)
	if want == "" {
		return nil
	}

	return verifyCosignPublicKeyDigest("release image verify", path, want)
}

func releaseImageVerifyExistingInputFromCmd(cmd *cli.Command) appcontainer.ReleaseImageVerifyExistingInput {
	return appcontainer.ReleaseImageVerifyExistingInput{
		Ref:                     cmd.String(flagRef),
		CosignPublicKey:         cmd.String("cosign-public-key-path"),
		ExpectedTag:             cmd.String("expected-tag"),
		ExpectedCommit:          cmd.String("expected-commit"),
		ExpectedSource:          cmd.String(flagExpectedSource),
		ExpectedWorkflow:        cmd.String(flagExpectedWorkflow),
		ExpectedBaseRef:         cmd.String("expected-base-ref"),
		ExpectedBaseInputID:     cmd.String("expected-base-input-id"),
		AllowReattest:           cmd.Bool("allow-reattest"),
		ExpectedIdentityVersion: cmd.String("identity-version"),
		ExpectedIdentityRefName: cmd.String("identity-ref-name"),
		ExpectedIdentitySource:  cmd.String("identity-source"),
	}
}

func withReleaseImageDockerConfig(authFile string, fn func() error) error {
	authFile = strings.TrimSpace(authFile)
	if authFile == "" {
		return fn()
	}

	dir, err := os.MkdirTemp(runnerTempDir(), "forge-ci-release-image-docker-config-*")
	if err != nil {
		return fmt.Errorf("release image verify: create Docker config dir: %w", err)
	}

	defer func() { _ = os.RemoveAll(dir) }()

	if chmodErr := os.Chmod(dir, 0o700); chmodErr != nil { //nolint:gosec // 0o700: a directory needs the owner execute bit; it stays owner-only.
		return fmt.Errorf("release image verify: chmod Docker config dir: %w", chmodErr)
	}

	body, err := os.ReadFile(authFile) //nolint:gosec // caller-selected registry auth file.
	if err != nil {
		return fmt.Errorf("release image verify: read auth file %s: %w", authFile, err)
	}

	if err := os.WriteFile(filepath.Join(dir, "config.json"), body, 0o600); err != nil { //nolint:gosec // path is inside the fresh MkdirTemp dir created above.
		return fmt.Errorf("release image verify: write Docker config for cosign: %w", err)
	}

	restoreDockerConfig, setErr := cienv.SetTemporarily("DOCKER_CONFIG", dir)
	if setErr != nil {
		return fmt.Errorf("release image verify: %w", setErr)
	}

	defer restoreDockerConfig()

	return fn()
}
