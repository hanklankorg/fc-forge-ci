// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package provider

// UsesGitLabCIConfig selects the native CI syntax for local configuration checks.
// An explicit forge wins; local inspection infers GitLab only from an
// unambiguous GitLab-only layout, never from an application's remote URL.
func UsesGitLabCIConfig(forge ForgeAPI, hasGitLabConfig, hasActionsWorkflows bool) bool {
	switch forge {
	case ForgeGitLab:
		return true
	case "", ForgeLocal:
		return hasGitLabConfig && !hasActionsWorkflows
	default:
		return false
	}
}
