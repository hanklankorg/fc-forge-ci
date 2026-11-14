// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package provider

// RunnerKeyword is the forge's own word for the runner field of a job, which
// its layers' runner inputs are named after: runner-tags on GitLab, runs-on
// on Forgejo and GitHub, and "" for a local run, which chooses no runner.
func (p ForgeAPI) RunnerKeyword() string {
	switch p {
	case ForgeGitLab:
		return "runner-tags"
	case ForgeForgejo, ForgeGitHub:
		return "runs-on"
	case ForgeLocal:
		return ""
	default:
		return ""
	}
}

// EmptyRunnerListIsUntagged reports whether an empty runner list chooses a
// runner on this forge. GitLab runs an untagged job on any runner that takes
// untagged jobs; on Forgejo and GitHub an empty label list selects none.
func (p ForgeAPI) EmptyRunnerListIsUntagged() bool {
	return p == ForgeGitLab
}

// FixesSignerRunner reports whether the forge's layer fixes the runner of
// its signing jobs, so it has no signer runner input: GitHub's signer jobs
// run on a hosted literal that no caller input or variable changes.
func (p ForgeAPI) FixesSignerRunner() bool {
	return p == ForgeGitHub
}
