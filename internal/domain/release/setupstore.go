// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

// SecretStoreHint says how a person stores one release credential on a forge:
// the CLI command where the forge has one, otherwise where its web settings
// keep it. forge is the setup-report spelling: github, forgejo or gitlab. The
// value is never part of the hint; each command reads it from stdin or a
// prompt, so it stays out of shell history.
func SecretStoreHint(forge, name string) string {
	switch forge {
	case "github":
		return "gh secret set " + name + " < <file holding the value>"
	case "forgejo":
		return "repository (or organization) Settings → Actions → Secrets → Add secret " + name
	case "gitlab":
		return "glab variable set " + name + " --protected --masked (or Settings → CI/CD → Variables); match the environment scope above"
	default:
		return "store " + name + " as a CI secret of the repository"
	}
}
