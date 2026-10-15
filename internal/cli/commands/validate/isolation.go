// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	appvalidate "github.com/diggsweden/forge-ci/internal/app/validate"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
)

// isolationCmd wires `forge-ci validate isolation` — selected static
// release-isolation checks over a consumer's workflow.
func isolationCmd() *cli.Command {
	return &cli.Command{
		Name:  "isolation",
		Usage: "check static release isolation (build job has no signing secrets; checkouts don't persist credentials)",
		Description: `Static release-isolation checks: the artifact-producing build job must have no
access to signing secrets (those belong only to the separately-trusted signing
job), and every actions/checkout step must set persist-credentials: false.

When --sign-job is set, additional Forgejo release-signing invariants are
checked: prepare release identity outputs, signer secret enumeration, dist-digest
handoff wiring, prepare-secret-before-checkout ordering, and cache-free
setup-toolchain use. --single-pin-subject additionally enforces that every
matching cross-repo reference in the workflow directory uses one commit pin.
--single-pin-input names the input through which the subject's reusable
workflows receive that commit a second time (the revision they check their
helper files out at): every job in the workflow directory that calls one of them
must then pass it as a literal commit, and it counts as a pin. The name is never
derived from the subject, so a renamed repository or fork cannot switch the
check off.

--literal-input names an input of the subject's reusable workflows that a
caller must write out, as NAME or NAME=SHAPE: every job in the workflow
directory that calls one of them and passes that input must pass a literal,
never an expression such as ${{ vars.X }}, so the value changes only by a
reviewed commit. SHAPE also holds the literal to a form: sha256 (64 lowercase
hex characters), image-digest (an image reference pinned by digest), or
enum:VALUE/VALUE (one of the listed values, for example enum:none/public). A
job that does not pass the input is left to the called workflow.

EXAMPLE:
   forge-ci validate isolation --workflow .github/workflows/release.yml \
     --build-job build --signing-secret RELEASE_GPG_PRIVATE_KEY --signing-secret COSIGN_PRIVATE_KEY`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagWorkflow, Required: true, Usage: "path to the workflow file to check"},
			&cli.StringFlag{Name: "build-job", Value: "build", Usage: "the artifact-producing job that must not see signing secrets"},
			&cli.StringFlag{Name: "sign-job", Usage: "cross-repo signing workflow call-site job; enables Forgejo release-signing channel checks"},
			&cli.StringFlag{Name: "prepare-job", Usage: "job that emits release-tag/release-sha and may check signing-secret presence before checkout"},
			&cli.StringFlag{Name: "dist-digest-output", Usage: "build job output carrying the dist-digest passed to the signer"},
			&cli.StringFlag{Name: "single-pin-subject", Usage: "subject whose @<sha> refs must agree across the workflow directory (for example, example/provider-workflows)"},
			&cli.StringFlag{Name: "single-pin-input", Usage: "input of the subject's reusable workflows that repeats their commit pin (for example, forge-ci-sha); needs --single-pin-subject"},
			&cli.StringFlag{Name: "contract-root", Usage: "trusted checkout of --single-pin-subject at the caller's pin; reject undeclared action/workflow inputs against its YAML, without fetching anything"},
			&cli.StringSliceFlag{Name: "literal-input", Usage: "input of the subject's reusable workflows that a caller must pass as a literal, as NAME or NAME=SHAPE with SHAPE sha256, image-digest or enum:VALUE/VALUE (repeatable); needs --single-pin-subject"},
			&cli.StringSliceFlag{
				Name:  "signing-secret",
				Usage: "signing secret name forbidden in the build job (repeatable)",
				Value: []string{"RELEASE_GPG_PRIVATE_KEY", "RELEASE_GPG_PASSPHRASE", "COSIGN_PRIVATE_KEY", "COSIGN_PASSWORD"},
			},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			return appvalidate.Isolation(os.Stderr, deps.Annotator(cmd), appvalidate.IsolationInput{
				Workflow:         cmd.String(flagWorkflow),
				BuildJob:         cmd.String("build-job"),
				SignJob:          cmd.String("sign-job"),
				PrepareJob:       cmd.String("prepare-job"),
				DistDigestOutput: cmd.String("dist-digest-output"),
				SinglePinSubject: cmd.String("single-pin-subject"),
				SinglePinInput:   cmd.String("single-pin-input"),
				ContractRoot:     cmd.String("contract-root"),
				LiteralInputs:    cmd.StringSlice("literal-input"),
				SigningSecrets:   cmd.StringSlice("signing-secret"),
			})
		},
	}
}
