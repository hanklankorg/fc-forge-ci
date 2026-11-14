// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

const setupKeyPassphrase = "Selected key's passphrase"

// GitHubReleaseSetup describes the release orchestrator's credential contract.
// Choices are resolved from the existing config plan by the application layer.
func GitHubReleaseSetup(gpg, gpgPublic, ssh, cosign, maven, buildSecrets SetupState) []SetupRequirement {
	return []SetupRequirement{
		{"GITHUB_TOKEN", SetupAutomatic, "Checkout, packages and workflow artifacts", "GitHub job token; caller workflow permissions", "job-local"},
		{"RELEASE_TOKEN", SetupRequired, "Prepare and publish releases", "GitHub fine-grained PAT; repository contents: write and branch/tag permissions", "release preparation/publication"},
		{"RELEASE_SSH_SIGNING_KEY", ssh, "Sign Git objects", "Release-bot SSH identity", "release preparation"},
		{"RELEASE_GPG_PRIVATE_KEY", gpg, "Sign Git objects, artifacts or Maven packages", "Configured GPG identity", "selected signing/publishing jobs"},
		{"RELEASE_GPG_PASSPHRASE", gpg, "Unlock GPG key", setupKeyPassphrase, "selected signing/publishing jobs"},
		{"RELEASE_GPG_PUBLIC_KEY", gpgPublic, "Verify signatures", "Public GPG key through a secret input", "release preflight"},
		{"RELEASE_COSIGN_PRIVATE_KEY", cosign, "Sign with env://COSIGN_KEY", "Organization Cosign identity", "selected signing jobs"},
		{"RELEASE_COSIGN_PASSPHRASE", cosign, "Unlock Cosign key", setupKeyPassphrase, "selected signing jobs"},
		{"MAVEN_CENTRAL_USERNAME", maven, "Publish Maven packages", "Sonatype publishing username", "Maven publication"},
		{"MAVEN_CENTRAL_PASSWORD", maven, "Publish Maven packages", "Sonatype publishing token", "Maven publication"},
		{"FORGE_CI_BUILD_SECRETS_JSON", buildSecrets, "Supply declared container build-secrets as a JSON envelope", "Caller-managed credentials for the services used during the container build", "container build jobs; do not pass values through build arguments"},
	}
}

// SetupState is a requirement, not a claim that a remote value exists.
type SetupState string

// Setup states describe demand only; they do not establish remote availability.
const (
	SetupRequired    SetupState = "required"
	SetupUnused      SetupState = "not-required"
	SetupConditional SetupState = "conditional"
	SetupAutomatic   SetupState = "automatic"
)

// SetupRequirement contains names and instructions only, never secret values.
type SetupRequirement struct {
	Name      string     `json:"name"`
	State     SetupState `json:"state"`
	Purpose   string     `json:"purpose"`
	Authority string     `json:"authority"`
	Scope     string     `json:"scope"`
}

// KeyReleaseSetup describes the key-based Forgejo and GitLab delivery layers.
// The GPG key, its passphrase and its fingerprint sign distro packages and
// the release files of a declaration that says sign.method gpg.
func KeyReleaseSetup(forge string, prepare bool, distro, files, images SetupState) []SetupRequirement {
	gpg := strongerSetup(distro, files)

	// The Forgejo release alone publishes distro packages, with the registry
	// token, when artifacts.yml declares distro-packages.
	signingScope, apiScope, registryScope := "signing jobs", "publication job", "image signing/promotion/cleanup jobs, and the packages job"
	registryPurpose := "Publish/sign/promote images; cleanup rights depend on registry. Publish declared distro-packages"

	if forge == "gitlab" {
		signingScope, apiScope, registryScope = "release/registry/signing", "release/publish", "release/registry/*"
		registryPurpose = "Publish/sign/promote images; cleanup rights depend on registry"
	}

	items := []SetupRequirement{
		{"RELEASE_TOKEN", SetupRequired, "Create releases and upload assets", "Source forge API; repository write access", apiScope},
		{"RELEASE_COSIGN_PRIVATE_KEY", SetupRequired, "Sign release artifacts", "Organization key matching the reviewed Cosign public key", signingScope},
		{"RELEASE_COSIGN_PASSPHRASE", SetupRequired, "Unlock the Cosign key", setupKeyPassphrase, signingScope},
		{"RELEASE_GPG_PRIVATE_KEY", gpg, "Sign distro packages or release files", "Organization OpenPGP signing key", signingScope},
		{"RELEASE_GPG_PASSPHRASE", gpg, "Unlock the GPG key", setupKeyPassphrase, signingScope},
		{"RELEASE_GPG_FINGERPRINT", gpg, "Pin the GPG key (public fingerprint through a secret input)", "Full primary fingerprint", signingScope},
		{"RELEASE_REGISTRY_TOKEN", images, registryPurpose, "Image registry; package read/write access, distinct from source-forge API credentials", registryScope},
	}
	if prepare {
		items = append(items, SetupRequirement{"RELEASE_SSH_SIGNING_KEY", SetupRequired, "Sign and push release commit and tag", "Source-forge bot; repository and branch/tag push access", "release/prepare"})
	}

	return items
}

// strongerSetup is the state of a credential two choices share: required by
// either, conditional on either, or unused by both.
func strongerSetup(a, b SetupState) SetupState {
	switch {
	case a == SetupRequired || b == SetupRequired:
		return SetupRequired
	case a == SetupConditional || b == SetupConditional:
		return SetupConditional
	default:
		return SetupUnused
	}
}
