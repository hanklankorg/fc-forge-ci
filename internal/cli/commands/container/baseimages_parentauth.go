// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/cosign"
	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appbaseimages "github.com/diggsweden/forge-ci/internal/app/baseimages"
	"github.com/diggsweden/forge-ci/internal/cli/regflags"
	"github.com/diggsweden/forge-ci/internal/cliio"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func baseImagesAuthenticateParentCmd() *cli.Command {
	return &cli.Command{
		Name:  "authenticate-parent",
		Usage: "authenticate one exact published base and print its {ref,input_id} parent projection",
		Description: `Requires explicit registry auth and exactly one independently approved
base-inputs-json entry: flavor, base_input_id and optional immediate parent_base.
Verifies the final tag's signature, CycloneDX and SLSA evidence with the pinned
public key and expected source/workflow, and requires its digest to equal
expected-ref. A missing base is a refusal, never a build request. Prints one
JSON {ref,input_id} only on success. The output is comparison data, not a portable
authority token or approval of a child plan. Does not build, sign or promote.`,
		Flags: append(baseImagesCommonFlags(),
			regflags.AuthFile(regflags.AuthFileOpts{Usage: "required explicit registry auth file; bounded private snapshot used for all reads"}),
			&cli.StringFlag{Name: "expected-ref", Required: true, Usage: "exact independently approved digest-pinned parent reference"},
			&cli.StringFlag{Name: flagBaseInputsJSON, Required: true, Usage: "one expected flavor/base_input_id and optional immediate parent_base {ref,input_id}; not producer-declared authority"},
		),
		Action: authenticateBaseParent,
	}
}

func authenticateBaseParent(ctx context.Context, cmd *cli.Command) error {
	common, err := baseImagesCommonFromCmd(cmd, false, true)
	if err != nil {
		return err
	}

	if cmd.String(flagAuthFile) == "" || !domaincontainer.ValidDigestPinnedRef(cmd.String("expected-ref")) {
		return fmt.Errorf("parent authentication requires --auth-file and a digest-pinned --expected-ref: %w", errs.ErrUsage)
	}
	// A parent has one finite input expectation, not an arbitrary graph document.
	if len(cmd.String(flagBaseInputsJSON)) > 64<<10 {
		return fmt.Errorf("parent authentication input exceeds 64 KiB: %w", errs.ErrMalformedInput)
	}

	inputs, err := parseBaseInputs(cmd.String(flagBaseInputsJSON))
	if err != nil {
		return err
	}

	if len(inputs) != 1 {
		return fmt.Errorf("parent authentication requires exactly one expected base input: %w", errs.ErrUsage)
	}

	key, err := readVerifiedCosignPublicKey("parent authentication", common.PublicKeyPath, common.PublicKeySHA256)
	if err != nil {
		return err
	}
	// Read once through the existing bounded regular-file boundary. Both adapters
	// consume this private snapshot, never ambient credentials or a reopened input.
	auth, err := cliio.ReadRegularFile(cmd.String(flagAuthFile))
	if err != nil {
		return fmt.Errorf("parent authentication registry auth input: %w", err)
	}

	return withBaseImagesDockerConfig(func(authFile string) error {
		if err := os.WriteFile(authFile, auth, 0o600); err != nil {
			return err
		}

		keyFile := filepath.Join(filepath.Dir(authFile), "cosign.pub")
		if err := os.WriteFile(keyFile, key, 0o600); err != nil {
			return err
		}

		verifier := cosign.NewIsolated("DOCKER_CONFIG")
		verifier.AuthFile = authFile

		parent, err := appbaseimages.AuthenticateBaseParent(ctx, ociregistry.WithAuthFile(authFile), verifier,
			appbaseimages.BaseParentAuthentication{ExpectedRef: cmd.String("expected-ref"), Verify: appbaseimages.BaseImageVerifyExistingInput{
				Authenticated: true, Flavors: []string{inputs[0].Flavor}, BaseInputs: inputs,
				ExpectedRepository: common.ExpectedRepository, ExpectedSource: common.ExpectedSource,
				ExpectedWorkflow: common.ExpectedWorkflow, CosignPublicKey: keyFile,
			}})
		if err != nil {
			return err
		}

		return json.NewEncoder(cmd.Writer).Encode(parent)
	})
}

func baseImagesSelectParentCmd() *cli.Command {
	return &cli.Command{
		Name:  "select-parent",
		Usage: "print one flavor's {ref,input_id} from a verified parent projection map",
		Description: `Reads --parent-bases-file, the parent_bases_json object that validate
--auth-file wrote, and prints the {ref,input_id} of --flavor. A flavor the map
does not hold was not found or not verified and is a refusal. The map is the
verifying job's; this command authenticates nothing.`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "parent-bases-file", Required: true, Usage: "file holding the parent_bases_json object of base-images validate --auth-file"},
			&cli.StringFlag{Name: "flavor", Required: true, Usage: "flavor of the parent"},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			body, err := cliio.ReadRegularFile(cmd.String("parent-bases-file"))
			if err != nil {
				return err
			}

			parents := map[string]appbaseimages.BaseImageParent{}
			if err := json.Unmarshal(body, &parents); err != nil {
				return fmt.Errorf("invalid parent projection map: %w", errs.ErrMalformedInput)
			}

			parent, verified := parents[cmd.String("flavor")]
			if !verified || !domaincontainer.ValidDigestPinnedRef(parent.Ref) || parent.InputID == "" {
				return fmt.Errorf("no verified parent base of flavor %q: %w", cmd.String("flavor"), errs.ErrValidation)
			}

			encoded, err := json.Marshal(parent)
			if err != nil {
				return err
			}

			_, err = fmt.Fprintln(os.Stdout, string(encoded))

			return err
		},
	}
}
