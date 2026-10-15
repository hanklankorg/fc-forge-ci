// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/cosign"
	"github.com/diggsweden/forge-ci/internal/adapters/git"
	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/cli/planfile"
	"github.com/diggsweden/forge-ci/internal/cli/signflags"
	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provenance"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
)

// planScopeProvenance is the plan-file scope of `release provenance` in
// $FORGE_CI_PLAN (flag > plan > env > default).
const planScopeProvenance = "release provenance"

// flagExternalParametersJSON is the generic externalParameters-extras
// flag: a JSON object merged into buildDefinition.externalParameters
// with every engine-computed key reserved.
const flagExternalParametersJSON = "external-parameters-json"

// provenanceCmd generates an in-toto/SLSA-v1.0 provenance statement from a
// checksums file, optionally signing the output with cosign sign-blob.
//
// Generic provenance uses the same runner-attested source and identity as
// `container attest`, without a forge attestation API. The forgejo-actions
// profile preserves forgejo-ci's shipped provider-derived source/builder shape.
func provenanceCmd() *cli.Command {
	return &cli.Command{
		Name:  "provenance",
		Usage: "generate an in-toto/SLSA-v1.0 provenance statement from a checksums file",
		Description: `Reads GoReleaser-format checksums and emits an in-toto Statement
(SLSA Provenance v1.0). With no method or key configured, the output is unsigned.
Use --method sigstore for keyless signing, or --method kms with --key for
key-backed signing. A key without a method also selects kms. Signing uses
cosign sign-blob and requires --output to name a file, not stdout; the signature
bundle defaults to <output>.bundle (override with --bundle).

Generic context (repository, ref, commit, builder and run) comes from the
active runner's attested environment, independently of --provider. Unknown
local identity is not inferred; generation requires a CI source and builder.
The forgejo-actions profile preserves provider-derived legacy source/builder
identity. Build timestamps come from flags or $SOURCE_DATE_EPOCH.

--ref-name and --source-sha state the source ref and commit when the job runs
on another ref than the one it attests, such as a release request that signs
the release tag. They replace the environment's values for both profiles; the
repository, the invocation and the generic builder remain the runner's own.
The forgejo-actions builder id names the workflow at the source ref, so it
follows --ref-name.
Every flag may also be fed from the $FORGE_CI_PLAN plan file under the
"release provenance" scope (flag > plan > env > default).

With --checksums-from-manifest the checksums come from the release file
manifest instead of --checksums-file: the manifest's single checksums file is
validated against the release files, as "release sign" does, before the
statement is written. A checksums file that is also named, by the flag or its
variable, is refused.

EXAMPLES:
   # Subjects from a named checksums file, signed keyless
   forge-ci release provenance --checksums-file checksums.sha256 --method sigstore --output provenance.json

   # Subjects from the release file manifest's validated checksums file
   forge-ci release provenance --checksums-from-manifest --manifest dist/release-files.json \
     --output dist/slsa-provenance.intoto.json --method kms --key env://COSIGN_KEY`,
		Flags: append(
			[]cli.Flag{
				&cli.StringFlag{Name: flagChecksumsFile, Value: cliio.StdSentinel, Sources: planfile.Vars(planScopeProvenance, flagChecksumsFile, "CHECKSUMS_FILE"), Usage: "GoReleaser checksums file (\"-\" reads stdin)"},
				&cli.BoolFlag{Name: flagChecksumsFromManifest, Sources: planfile.Vars(planScopeProvenance, flagChecksumsFromManifest), Usage: "use and validate the single checksums file from --manifest; conflicts with a named --checksums-file"},
				&cli.StringFlag{Name: flagManifest, Value: apprelease.DefaultReleaseFilesManifest, Sources: planfile.Vars(planScopeProvenance, flagManifest, "RELEASE_FILES_MANIFEST"), Usage: "release file manifest used by --checksums-from-manifest; a manifest named here must exist"},
				&cli.StringFlag{Name: flagDistDir, Value: defaultDistDir, Sources: planfile.Vars(planScopeProvenance, flagDistDir), Usage: "dist directory used with --manifest"},
				&cli.StringFlag{Name: "go-sum", Value: "go.sum", Sources: planfile.Vars(planScopeProvenance, "go-sum", "GO_SUM_FILE"), Usage: "go.sum for resolved module deps (empty string to skip)"},
				&cli.StringFlag{Name: "profile", Value: provenanceProfileGenericName, Sources: planfile.Vars(planScopeProvenance, "profile"), Usage: "provenance profile: generic or forgejo-actions"},
				&cli.StringFlag{Name: "workflow", Sources: planfile.Vars(planScopeProvenance, "workflow", "FORGEJO_WORKFLOW"), Usage: "workflow filename/path, relative with no '..' step or control character: the forgejo-actions profile's workflow object; the generic profile records it verbatim as externalParameters.workflow"},
				&cli.StringFlag{Name: flagRefName, Sources: planfile.Vars(planScopeProvenance, flagRefName), Usage: "source ref name recorded in the statement, such as the release tag (default: the ref name of the run)"},
				&cli.StringFlag{Name: flagSourceSHA, Sources: planfile.Vars(planScopeProvenance, flagSourceSHA), Usage: "source commit recorded as the provenance gitCommit (default: the commit of the run)"},
				&cli.StringFlag{Name: "started-on", Sources: planfile.Vars(planScopeProvenance, "started-on"), Usage: "RFC3339 build timestamp (default: $SOURCE_DATE_EPOCH)"},
				&cli.StringFlag{Name: "started-on-commit", Sources: planfile.Vars(planScopeProvenance, "started-on-commit"), Usage: "commit/ref whose commit timestamp becomes the RFC3339 build timestamp"},
				&cli.StringFlag{Name: flagExternalParametersJSON, Sources: planfile.Vars(planScopeProvenance, flagExternalParametersJSON, "SLSA_EXTERNAL_PARAMETERS_JSON"), Usage: "JSON object of extra buildDefinition.externalParameters merged into the predicate; already-present (engine-computed) keys are reserved, a collision fails. Generic successor to the per-field lineage flags (--base-ref/--base-digest/--base-input-id)"},
				&cli.StringFlag{Name: flagOutput, Value: cliio.StdSentinel, Sources: planfile.Vars(planScopeProvenance, flagOutput), Usage: "output file (\"-\" for stdout)"},
				&cli.StringFlag{Name: "bundle", Sources: planfile.Vars(planScopeProvenance, "bundle"), Usage: "signature bundle output path (default: <output>.bundle)"},
			},
			signflags.Cosign(signflags.CosignOpts{
				MethodNote: "With no method or key configured, generates only.",
				KeyNote:    "With no method configured, a key selects kms.",
				PlanScope:  planScopeProvenance,
			})...,
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error { //nolint:varnamelen // idiomatic short name (testing/http/io conventions).
				profile, err := parseProvenanceProfile(cmd.String("profile"))
				if err != nil {
					return err
				}

				// Parse the declared extras up front so a malformed
				// document fails before any context resolution or output.
				extraParams, err := provenance.ParseExternalParametersJSON(cmd.String(flagExternalParametersJSON))
				if err != nil {
					return fmt.Errorf("provenance: --%s: %w", flagExternalParametersJSON, err)
				}

				in, err := provenanceContext(ctx, cmd, d, profile)
				if err != nil {
					return err
				}

				startedOn, err := resolveStartedOn(ctx, git.New(), cmd.String("started-on"), cmd.String("started-on-commit"))
				if err != nil {
					return err
				}

				checksums, err := readProvenanceChecksums(cmd)
				if err != nil {
					return err
				}

				in.Checksums = bytes.NewReader(checksums)
				in.GoSum = openGoSum(cmd.String("go-sum"))
				in.StartedOn = startedOn
				in.ExternalParameters = extraParams

				out, err := apprelease.GenerateProvenance(in)
				if err != nil {
					return err
				}

				output := cmd.String(flagOutput)
				if err := cliio.WriteFile(output, out, 0o644); err != nil { //nolint:gosec // provenance JSON is public attestation material, not a secret.
					return err
				}

				return signProvenance(ctx, signProvenanceInput{
					output:     output,
					method:     cmd.String("method"),
					keyRef:     cmd.String("key"),
					oidcIssuer: cmd.String("oidc-issuer"),
					bundle:     cmd.String("bundle"),
					endpoints:  signflags.ReadEndpoints(cmd),
				})
			})
		},
	}
}

// provenanceContext resolves the source and the build identity of the
// statement. Generic provenance uses the same forge-neutral source, builder and
// invocation identity as containers. The forgejo-actions profile takes the
// source from the selected provider; GenerateProvenance names its builder.
// --ref-name and --source-sha replace the ref and commit of either; the
// invocation is always the runner's own.
func provenanceContext(ctx context.Context, cmd *cli.Command, d *deps.Deps, profile apprelease.ProvenanceProfile) (apprelease.ProvenanceInput, error) { //nolint:varnamelen // idiomatic short name (testing/http/io conventions).
	legacy := profile == apprelease.ProvenanceProfileForgejoActions
	repoURL, ref, sha := cienv.ProvenanceSource()

	if legacy {
		// Preserve the explicitly selected compatibility profile;
		// target metadata must not feed the generic trust identity.
		evt, err := d.Provider.ResolveContext(ctx)
		if err != nil {
			return apprelease.ProvenanceInput{}, err
		}

		repoURL, ref, sha = evt.RepoURL, evt.RefName, evt.SHA
	} else if repoURL == "" {
		return apprelease.ProvenanceInput{}, fmt.Errorf("provenance: runner repository URL is unknown: %w", errs.ErrMissingInput)
	}

	// A flag that is set never falls back to the environment, even when it
	// is empty: GenerateProvenance refuses an empty or malformed ref name or
	// commit.
	if cmd.IsSet(flagRefName) {
		ref = cmd.String(flagRefName)
	}

	if cmd.IsSet(flagSourceSHA) {
		sha = cmd.String(flagSourceSHA)
	}

	return apprelease.ProvenanceInput{
		RepositoryURL: repoURL,
		Ref:           ref,
		SHA:           sha,
		BuilderID:     cienv.ProvenanceBuilderID(),
		InvocationID:  cienv.ProvenanceInvocationID(),
		Profile:       profile,
		Workflow:      cmd.String("workflow"),
	}, nil
}

// readProvenanceChecksums returns the checksums the statement's subjects are
// parsed from: the --checksums-file content, or with --checksums-from-manifest
// the release manifest's single checksums file as it was validated against the
// release files.
func readProvenanceChecksums(cmd *cli.Command) ([]byte, error) {
	if !cmd.Bool(flagChecksumsFromManifest) {
		return cliio.ReadFile(cmd.String(flagChecksumsFile))
	}

	return apprelease.ReadValidatedReleaseChecksums(releaseFilesInput(cmd), explicitValue(cmd, flagChecksumsFile))
}

// signProvenanceInput carries the signing decision for the generated
// provenance statement.
type signProvenanceInput struct {
	output     string
	method     string // "" | sigstore | kms
	keyRef     string
	oidcIssuer string
	bundle     string
	// endpoints are the self-hosted Sigstore overrides signflags.Cosign
	// declares (--fulcio-url / --rekor-url / --trusted-root). They were
	// declared here and read nowhere, so a self-hosted operator's provenance
	// was silently signed against the public CA and log.
	endpoints signflags.Endpoints
}

// signProvenance signs the written provenance statement with cosign
// sign-blob, emitting a portable Sigstore bundle (verifiable on any forge
// with `cosign verify-blob`). It is the forge-neutral replacement for
// GitHub's attest-build-provenance:
//
//   - --method=sigstore → keyless OIDC (the workflow's identity).
//   - --method=kms      → cosign --key (KMS URI / file / env://VAR).
//   - neither method nor key → generate-only (no signature).
//
// For an env://VAR key the cosign subprocess is isolated to that key var
// (+ COSIGN_PASSWORD) so no forge token, GPG key, or registry credential
// is visible to it — the forgejo-ci secret-family-isolation guarantee.
// Keyless and KMS/file keys are not isolated (those backends need their
// own OIDC / cloud env).
func signProvenance(ctx context.Context, in signProvenanceInput) error {
	if in.method == "" && in.keyRef == "" {
		return nil // generate-only
	}

	if in.output == cliio.StdSentinel {
		return fmt.Errorf("provenance: --output must be a file when signing (cosign signs a path): %w", errs.ErrUsage)
	}

	bundle := in.bundle
	if bundle == "" {
		bundle = in.output + ".bundle"
	}

	blob, err := provenanceSignBlobInput(in, bundle)
	if err != nil {
		return err
	}

	return cosign.ForKeyRef(in.keyRef).SignBlob(ctx, blob, os.Stderr)
}

// provenanceSignBlobInput maps the method/key decision onto a cosign
// SignBlobInput. A bare --key with no --method is treated as kms
// (cosign --key) for backward compatibility. gpg is rejected — OpenPGP
// signs differently and is not a cosign blob backend. The Sigstore
// endpoints ride along for keyless signing and, as the `release sign`
// signer already enforces, --fulcio-url, --rekor-url and --trusted-root are
// refused for kms.
func provenanceSignBlobInput(in signProvenanceInput, bundle string) (cosign.SignBlobInput, error) {
	blob := cosign.SignBlobInput{Artifact: in.output, BundlePath: bundle}

	// A bare --key with no --method means KMS/key signing.
	raw := in.method
	if raw == "" && in.keyRef != "" {
		raw = string(domainrelease.SignMethodKMS)
	}

	method, err := domainrelease.ParseSignMethod(raw)
	if err != nil {
		return blob, fmt.Errorf("provenance: --method %q is not supported (use sigstore or kms): %w", in.method, errs.ErrUsage)
	}

	switch method {
	case domainrelease.SignMethodSigstore:
		blob.Keyless = true
		blob.OIDCIssuer = in.oidcIssuer
		blob.FulcioURL = in.endpoints.FulcioURL
		blob.RekorURL = in.endpoints.RekorURL
		blob.TrustedRootPath = in.endpoints.TrustedRootPath
	case domainrelease.SignMethodKMS:
		if in.keyRef == "" {
			return blob, fmt.Errorf("provenance: --method=kms requires --key: %w", errs.ErrUsage)
		}

		// A key signature involves no certificate authority and cannot name
		// its own log, so the trust root has nothing to vouch for either.
		// Refusing it, as `release sign` and `container sign` do, beats
		// accepting a flag that would change nothing.
		if in.endpoints != (signflags.Endpoints{}) {
			return blob, fmt.Errorf("provenance: --fulcio-url, --rekor-url and --trusted-root are forbidden for --method=kms: %w", errs.ErrUsage)
		}

		blob.KeyRef = in.keyRef
		// Passed through, not dropped: the request's own validation refuses an
		// issuer beside a key, as `release sign` does.
		blob.OIDCIssuer = in.oidcIssuer
	default: // gpg or any non-cosign-blob backend
		return blob, fmt.Errorf("provenance: --method %q cannot sign a blob with cosign (use sigstore or kms): %w", in.method, errs.ErrUsage)
	}

	return blob, nil
}

type commitUnixTimer interface {
	CommitUnixTime(ctx context.Context, ref string) (string, error)
}

// resolveStartedOn returns the reproducible build timestamp as RFC3339 UTC.
// Precedence: an explicit --started-on, then a commit timestamp requested with
// --started-on-commit, then $SOURCE_DATE_EPOCH (unix seconds, the repo-wide
// reproducibility convention). One of these is required.
func resolveStartedOn(ctx context.Context, git commitUnixTimer, flagVal, commitRef string) (string, error) {
	if flagVal == "" && commitRef != "" {
		epoch, err := git.CommitUnixTime(ctx, commitRef)
		if err != nil {
			return "", fmt.Errorf("provenance: resolve --started-on-commit %q: %w", commitRef, err)
		}

		secs, err := strconv.ParseInt(strings.TrimSpace(epoch), 10, 64)
		if err != nil {
			return "", fmt.Errorf("provenance: commit timestamp for %q is not unix seconds: %w", commitRef, errs.ErrUsage)
		}

		return time.Unix(secs, 0).UTC().Format(time.RFC3339), nil
	}

	startedOn, err := cienv.StartedOn(flagVal)
	if err != nil {
		return "", fmt.Errorf("provenance: %w", err)
	}

	return startedOn, nil
}

// openGoSum returns a reader over the go.sum at path, or nil when the
// path is empty or the file is absent (module deps are best-effort —
// their absence is not fatal).
func openGoSum(path string) io.Reader {
	if path == "" {
		return nil
	}

	data, err := os.ReadFile(path) //nolint:gosec // go.sum path is an operator-supplied build input, not attacker-controlled.
	if err != nil {
		return nil
	}

	return bytes.NewReader(data)
}

// provenanceProfileGenericName is the CLI name of the generic (default)
// provenance profile.
const provenanceProfileGenericName = "generic"

func parseProvenanceProfile(raw string) (apprelease.ProvenanceProfile, error) {
	switch raw {
	case "", provenanceProfileGenericName:
		return apprelease.ProvenanceProfileGeneric, nil
	case string(apprelease.ProvenanceProfileForgejoActions):
		return apprelease.ProvenanceProfileForgejoActions, nil
	default:
		return "", fmt.Errorf("provenance: unknown --profile %q (use generic or forgejo-actions): %w", raw, errs.ErrUsage)
	}
}
