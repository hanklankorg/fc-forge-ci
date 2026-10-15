// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

// SelfRuntimeRepository is the repository allowed to publish the self-runtime
// CLI channel when no other repository is configured.
const SelfRuntimeRepository = "diggsweden/forge-ci"

// SelfRuntimeBranch is the branch whose builds feed the channel when no other
// branch is configured. It matches the installer's default pre-release signer.
const SelfRuntimeBranch = "main"

// SelfRuntimeChannelTag is the one mutable CLI tag owned by the self-runtime
// workflow.
const SelfRuntimeChannelTag = "v0.90.0-pre"

// SelfRuntimePolicy names the one repository and branch allowed to publish the
// rolling channel. The zero value is the upstream repository and branch; a
// fork sets both to its own.
type SelfRuntimePolicy struct {
	Repository string
	Branch     string
}

// AllowedRepository returns the repository the policy trusts.
func (p SelfRuntimePolicy) AllowedRepository() string {
	if p.Repository == "" {
		return SelfRuntimeRepository
	}

	return p.Repository
}

// SourceRefAllowed reports whether ref is the trusted branch, the one both the
// rolling-channel producer and the installer's Sigstore identity policy name.
func (p SelfRuntimePolicy) SourceRefAllowed(ref string) bool {
	branch := p.Branch
	if branch == "" {
		branch = SelfRuntimeBranch
	}

	return ref == "refs/heads/"+branch
}
