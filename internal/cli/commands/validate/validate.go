// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package validate wires `forge-ci validate ...` subcommands using
// urfave/cli v3.
//
// Reusable preconditions live here; checks of an already-produced artifact also
// live under its owning command group. The generated CLI reference owns syntax.
package validate

import (
	"slices"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/cli/cmdmeta"
)

// New returns the `validate` subgroup command tree.
//
// Verb convention across the CLI:
//
//   - validate  — a fail-fast PRECONDITION gate run before you act. Most
//     checks are local (semver format, ref-type, namespace, secret presence),
//     but some legitimately reach out read-only (`auth token` hits the platform
//     API, `tag commit` checks remote reachability). The defining trait is
//     "gate the next step," not "offline." These live here in the reusable
//     `validate` group.
//   - artifact checks — RECOMPUTE or CROSS-CHECK an already-produced or remote
//     thing against expected truth (e.g. `release validate-dist` recomputes
//     the dist digest, `release validate-tag` re-checks the remote,
//     `container ledger validate-digests` cross-checks registry digests). These live
//     under the command that owns the produced artifact, not here.
//
// Several validate subcommands read "verify …" in their Usage (signature checks,
// remote-reachability checks); that wording is intentional where the check
// cross-checks something rather than asserting a local fact.
func New() *cli.Command {
	return &cli.Command{
		Name:  "validate",
		Usage: "precondition, policy and signature checks for release workflows",
		Description: `Validation gates the next operation. Some checks are local; others
make read-only provider calls, such as "validate auth token".

Checks of produced artifacts also live under their owning groups:
"release validate-tag" rechecks a remote release tag, "release validate-dist"
recomputes a distribution digest, and "container ledger validate-digests"
compares recorded digests with the registry.

"report status" renders collected outcomes; it is not a validation gate.`,
		Commands: slices.Concat(
			cmdmeta.WithCategory("Release pre-flight gates",
				prerequisitesCmd(), releaseProviderCmd(), eventContextCmd(), signingPrivacyCmd(), isolationCmd(), jobGraphCmd(), pinReachabilityCmd(), tagGroup(), workflowGroup(), changelogCmd()),
			cmdmeta.WithCategory("Auth & secrets", authGroup()),
			cmdmeta.WithCategory("Signature verification", artifactSignatureCmd(), containerSignatureCmd(), signingKeyCmd()),
			cmdmeta.WithCategory("Ecosystem checks", cargoCmd(), jvmReproducibilityCmd()),
		),
	}
}
