// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package consumerinit

import (
	"fmt"
	"path"
	"slices"
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

// Release paths init reads from a Forge CI release, besides DefaultsFile.
const (
	ExamplesDir  = "examples"
	ChangelogDir = "templates/gitcliff"
)

// changelogConfigs are the git-cliff configurations the Forgejo and GitLab
// releases read from the consumer by default, .forge-ci/gitcliff/<name>.
var changelogConfigs = []string{"keepachangelog.toml", "minimal.toml"}

// Plan lists the files a forge takes from a release. Entries are the file
// names the example holds in its forge directory; From is a path in the
// release. GitHub's release brings its own changelog configuration; the
// Forgejo and GitLab ones read the consumer's, so init writes the release's.
func Plan(forge provider.ForgeAPI, example string, forgeEntries []string) []FileCopy {
	// Linter configuration belongs to the consumer, not CI initialization.
	var plan []FileCopy

	dir := path.Join(ExamplesDir, example)

	switch forge {
	case provider.ForgeGitHub:
		plan = append(plan, FileCopy{From: path.Join(dir, "artifacts.yml"), To: ".forge-ci/artifacts.yml"})
		for _, name := range forgeEntries {
			plan = append(plan, FileCopy{From: path.Join(dir, "github", name), To: path.Join(".github/workflows", name)})
		}
	case provider.ForgeGitLab:
		plan = append(plan, FileCopy{From: path.Join(dir, "gitlab", ".gitlab-ci.yml"), To: ".gitlab-ci.yml"})
		plan = append(plan, changelogCopies()...)
	case provider.ForgeForgejo:
		// The directory also holds optional variants (snapshot.yml, an
		// alternative release-goreleaser.yml) and files the README asks to
		// copy elsewhere. Init writes only the workflows it owns: two release
		// workflows would both run on one release-request tag.
		for _, name := range forgeEntries {
			if slices.Contains(forgejoWorkflows, name) {
				plan = append(plan, FileCopy{From: path.Join(dir, "forgejo", name), To: path.Join(".forgejo/workflows", name)})
			}
		}

		plan = append(plan, changelogCopies()...)
	case provider.ForgeLocal:
	}

	return plan
}

// forgejoWorkflows are the Forgejo workflows init writes.
var forgejoWorkflows = []string{"pullrequest.yml", "release.yml"}

// OptionalWorkflows lists the workflows of an example's forge directory that
// init does not write, for the report to name: a person copies one by hand
// when they want it.
func OptionalWorkflows(forge provider.ForgeAPI, forgeEntries []string) []string {
	if forge != provider.ForgeForgejo {
		return nil
	}

	var optional []string

	for _, name := range forgeEntries {
		if path.Ext(name) == ".yml" && !slices.Contains(forgejoWorkflows, name) {
			optional = append(optional, name)
		}
	}

	return optional
}

func changelogCopies() []FileCopy {
	copies := make([]FileCopy, 0, len(changelogConfigs))
	for _, name := range changelogConfigs {
		copies = append(copies, FileCopy{From: path.Join(ChangelogDir, name), To: path.Join(".forge-ci/gitcliff", name), KeepExisting: true})
	}

	return copies
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

// runtimeImagePlaceholders are the GitLab examples' build images, which a
// mirror's runtime-images fill by toolchain family.
var runtimeImagePlaceholders = []struct{ family, placeholder string }{
	{"java", "ghcr.io/diggsweden/forge-ci-runtime-java-25@sha256:PIN_JAVA_IMAGE_DIGEST"},
	{"node", "ghcr.io/diggsweden/forge-ci-runtime-node-24@sha256:PIN_NODE_IMAGE_DIGEST"},
	{"go", "ghcr.io/diggsweden/forge-ci-runtime-go-1.27@sha256:PIN_GO_IMAGE_DIGEST"},
	{"rust", "ghcr.io/diggsweden/forge-ci-runtime-rust-stable@sha256:PIN_RUST_IMAGE_DIGEST"},
	{"android", "ghcr.io/diggsweden/forge-ci-runtime-android-35@sha256:PIN_ANDROID_IMAGE_DIGEST"},
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

		for _, runtime := range runtimeImagePlaceholders {
			add(runtime.placeholder, defaults.RuntimeImages[runtime.family])
		}

		add("server-url: https://gitlab.example.com", "server-url: https://"+src.Host)
		add("gitlab.example.com/team/ci/forge-ci", src.Host+"/"+src.Repository)
		add("team/ci/forge-ci", src.Repository)
		add("@0.90.0", "@"+strings.TrimPrefix(src.Tag, "v"))
		add("PIN_FORGE_CI_SHA", src.Commit)
	case provider.ForgeLocal:
	}

	return replacements
}
