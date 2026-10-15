// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package security wires `forge-ci security <subgroup> <subcmd>`.
//
// Two subgroups by responsibility:
//
//   - scan   — run Trivy against a container image
//   - report — transform / upload an existing security report (enrich
//     SARIF for GitHub Code Scanning, convert Trivy JSON or SARIF to the
//     GitLab security-report schemas, upload SARIF)
package security

import "github.com/urfave/cli/v3"

// flagImageRef and flagSARIFFile are the flag names the scan and report verbs
// share, declared once so the spelling cannot drift and the package stays
// under goconst's literal budget. The values are part of the CLI contract —
// docs/cli-reference.md is generated from them and a sync test gates any
// change.
const (
	flagImageRef  = "image-ref"
	flagSARIFFile = "sarif-file"
)

// New returns the `security` subgroup command tree.
func New() *cli.Command {
	return &cli.Command{
		Name:  "security",
		Usage: "security scanners and report converters",
		Commands: []*cli.Command{
			scanGroup(),
			reportGroup(),
		},
	}
}
