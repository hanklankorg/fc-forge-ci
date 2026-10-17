// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package consumerinit

import (
	"fmt"
	"path"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// The forge decides where a consumer's files go and which placeholders an
// example uses. This file is that table, one case per forge, and the only
// file of the package that branches on forge identity (see the archguard
// allowlist).

// ResolveForge decides the consumer's forge: github.com is GitHub; any other
// host must say what it is in the defaults file.
func ResolveForge(host string, defaults Defaults) (provider.ForgeAPI, error) {
	if host == "github.com" {
		return provider.ForgeGitHub, nil
	}

	switch defaults.Forge {
	case provider.ForgeForgejo.String():
		return provider.ForgeForgejo, nil
	case provider.ForgeGitLab.String():
		return provider.ForgeGitLab, nil
	case "":
		return "", fmt.Errorf("%s is not github.com and its %s names no forge: the mirror's operator adds `forge: forgejo` or `forge: gitlab`: %w",
			host, DefaultsFile, errs.ErrInvalidConfig)
	default:
		return "", fmt.Errorf("%s: forge %q is not forgejo or gitlab: %w", DefaultsFile, defaults.Forge, errs.ErrInvalidConfig)
	}
}

// Plan lists the files a forge takes from an example. Entries are the
// file names the example holds in its forge directory.
func Plan(forge provider.ForgeAPI, example string, forgeEntries []string) []FileCopy {
	plan := []FileCopy{{From: path.Join(example, "nanolinter.toml"), To: "nanolinter.toml"}}

	switch forge {
	case provider.ForgeGitHub:
		plan = append(plan, FileCopy{From: path.Join(example, "artifacts.yml"), To: ".forge-ci/artifacts.yml"})
		for _, name := range forgeEntries {
			plan = append(plan, FileCopy{From: path.Join(example, "github", name), To: path.Join(".github/workflows", name)})
		}
	case provider.ForgeGitLab:
		plan = append(plan, FileCopy{From: path.Join(example, "gitlab", ".gitlab-ci.yml"), To: ".gitlab-ci.yml"})
	case provider.ForgeForgejo:
		for _, name := range forgeEntries {
			plan = append(plan, FileCopy{From: path.Join(example, "forgejo", name), To: path.Join(".forgejo/workflows", name)})
		}
	case provider.ForgeLocal:
	}

	return plan
}

// ForgeDir is the directory of an example that holds a forge's files.
func ForgeDir(forge provider.ForgeAPI) string {
	switch forge {
	case provider.ForgeGitHub:
		return "github"
	case provider.ForgeGitLab:
		return "gitlab"
	default:
		return "forgejo"
	}
}

// Replacements returns the substitutions for a forge, longest placeholder
// first, so a composite such as the signer image is replaced before its
// parts. A value the operator did not give is left out: its placeholder
// stays, and Remaining reports it.
func Replacements(forge provider.ForgeAPI, example string, src Source, defaults Defaults) []Replacement {
	var replacements []Replacement

	add := func(old, value string) {
		if value != "" {
			replacements = append(replacements, Replacement{Old: old, New: value})
		}
	}

	switch forge {
	case provider.ForgeGitHub:
		add("diggsweden/forge-ci", src.Repository)
		add("v0.90.0", src.Tag)
	case provider.ForgeForgejo:
		add("FORGE_HOST/OWNER/forge-ci-signer@sha256:PIN_SIGNER_IMAGE_DIGEST", defaults.SignerImage)
		add("PIN_COSIGN_PUBLIC_KEY_SHA256", defaults.CosignPublicKeySHA256)
		add("PIN_FORGE_CI_SHA", src.Commit)
		add("OWNER/forge-ci", src.Repository)
		add("FORGE_HOST", src.Host)
	case provider.ForgeGitLab:
		flavour := FlavourOf(example)
		add("ghcr.io/itiquette/nanolinter:"+flavour+"@sha256:PIN_DIGEST", defaults.NanolinterImages[flavour])
		add("registry.example.com/team/check-tools@sha256:PIN_IMAGE_DIGEST", defaults.CheckImage)
		add("gitlab.example.com/team/ci/forge-ci", src.Host+"/"+src.Repository)
		add("team/ci/forge-ci", src.Repository)
		add("@0.90.0", "@"+strings.TrimPrefix(src.Tag, "v"))
		add("PIN_FORGE_CI_SHA", src.Commit)
	case provider.ForgeLocal:
	}

	return replacements
}
