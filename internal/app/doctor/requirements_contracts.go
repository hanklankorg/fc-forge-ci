// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	domaingit "github.com/diggsweden/forge-ci/internal/domain/git"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/release"
	"gopkg.in/yaml.v3"
)

// inspect reports one call. jobs are the calling workflow's, which a Forgejo
// signing call's artifacts-config may name a prepare job of; nil for GitLab.
func (report *RequirementsReport) inspect(root, repo, commit, forge, file string, ref, inputs, secrets, jobs *yaml.Node) {
	where := fmt.Sprintf("%q:%d", file, ref.Line)
	raw := setupScalar(ref)
	// Ordinary build/check components do not make a release inventory incomplete.
	// Unknown release-like calls still need manual review; never infer their rules.
	if !setupReleaseCandidate(raw, forge) {
		return
	}

	name, ok := setupContract(raw, forge, repo)
	if !ok {
		report.unknown(where + ": unsupported entry point or revision; requirements were not inferred")

		return
	}

	_, revision, _ := strings.Cut(raw, "@")

	contract := "reference guidance from this CLI; pinned contract unverified"
	if domaingit.ValidCommitSHA(commit) && commit == revision {
		contract = "revision-matched reference rules; remote readiness not checked"
	} else {
		report.unknown(where + ": pinned contract not verified; the rows listed for it are this CLI's reference guidance, not authoritative requirements for that revision")
	}

	call := SetupCall{Location: where, Forge: forge, Workflow: name, Revision: revision, Contract: contract, Requirements: []SetupItem{}, Settings: []SetupSetting{}}
	if inputs != nil && inputs.Kind != yaml.MappingNode {
		report.unknown(where + ": inputs must be a literal mapping; requirements were not inferred")

		return
	}

	var requirements []release.SetupRequirement

	if forge == setupGitHub {
		if setupScalar(yamlChild(inputs, "forge-ci-binary-ref")) != revision {
			call.Contract = "reference guidance from this CLI; runtime engine override unverified"

			report.unknown(where + ": forge-ci-binary-ref is absent or names another engine than this CLI's revision; its contract is not verified")
		}

		requirements = report.githubRequirements(root, inputs, &call)
	} else {
		files := report.releaseFileSigning(root, setupPreparedArtifactsConfig(inputs, jobs), &call)
		prepare := name == setupPrepareWorkflow || forge == setupGitLab

		images := release.SetupConditional // Sign/publish discovers images in a runtime ledger.
		if name == setupPrepareWorkflow {
			images = setupChoice(inputs, "publish-container-images", true)
		}

		if forge == setupGitLab {
			images = gitlabRegistryState(inputs)
		}

		requirements = release.KeyReleaseSetup(forge, prepare, setupChoice(inputs, "sign-distro-packages", true), files, images)
		report.keySettings(&call, inputs, prepare)
	}

	report.mapRequirements(&call, requirements, secrets)

	report.Calls = append(report.Calls, call)
}

func (report *RequirementsReport) setting(call *SetupCall, inputs *yaml.Node, name, purpose string) {
	source, ok := setupSource(yamlChild(inputs, name), "vars")

	call.Settings = append(call.Settings, SetupSetting{Name: name, Source: source, Purpose: purpose})
	if !ok {
		report.unknown(call.Location + ": public input " + name + " is absent or unresolved")
	}
}

func (report *RequirementsReport) keySettings(call *SetupCall, inputs *yaml.Node, prepare bool) {
	report.setting(call, inputs, "signer-image", "Reviewed digest-pinned image; publication and pull access are not checked")

	if call.Forge == setupForgejo {
		report.setting(call, inputs, "runs-on", "JSON array of trusted runner labels")
		report.setting(call, inputs, "forge-ci-sha", "Exact helper checkout revision; must match the workflow pin")
		report.forgejoRepositorySetting(call, inputs)

		if setupScalar(yamlChild(inputs, "forge-ci-sha")) != call.Revision {
			call.Contract = "reference guidance from this CLI; runtime helper revision unverified"

			report.unknown(call.Location + ": forge-ci-sha must match the literal uses revision")
		}
	} else {
		for _, name := range []string{"forge-ci-repository", "server-url", "registry", "consumer-project", "publication-project", "publication-image-repository", "job-image", "request-allowed-signers"} {
			report.setting(call, inputs, name, "Reviewed GitLab release input; see docs/gitlab.md")
		}

		call.Settings = append(call.Settings,
			SetupSetting{"COSIGN_PUBLIC_KEY", "project variable, scope *", "Public PEM matching the reviewed digest"},
			SetupSetting{"RELEASE_REGISTRY_USER", "protected project variable, scope release/registry/*", "Publication registry identity; needed with RELEASE_REGISTRY_TOKEN only when release-images or base-stages publishes images"},
		)
	}

	if prepare {
		report.setting(call, inputs, "bot-name", "Author name for the signed release commit")
		report.setting(call, inputs, "bot-email", "Author email for the signed release commit")
		report.setting(call, inputs, "release-git-host-key-fingerprint", "Independently verified SSH server fingerprint")
	}

	if !prepare || call.Forge == setupGitLab {
		report.setting(call, inputs, "cosign-public-key-sha256", "Reviewed literal SHA-256; do not obtain the trust pin from a variable")

		if strings.Contains(setupScalar(yamlChild(inputs, "cosign-public-key-sha256")), "$") {
			report.unknown(call.Location + ": Cosign trust digest must be a reviewed literal, not a variable or expression")
		}

		if call.Forge == setupForgejo {
			report.setting(call, inputs, "cosign-public-key", "Public PEM matching the reviewed digest")
			call.Settings = append(call.Settings, SetupSetting{"registry-user", "input or RELEASE_REGISTRY_USER variable; forge owner only for the forge's own registry", "External registries require an explicit account; image destination comes from image-repository / runtime ledger"})
		}
	}
}

func (report *RequirementsReport) forgejoRepositorySetting(call *SetupCall, inputs *yaml.Node) {
	node := yamlChild(inputs, "forge-ci-repository")
	if node == nil || (node.Kind == yaml.ScalarNode && node.Value == "") {
		call.Settings = append(call.Settings, SetupSetting{"forge-ci-repository", "vars.FORGE_CI_REPOSITORY (fallback; remote value not checked)", "Helper repository matching the workflow's uses coordinate; required through the input or calling repository/organization variable"})

		return
	}

	report.setting(call, inputs, "forge-ci-repository", "Helper repository matching the workflow's uses coordinate; empty uses vars.FORGE_CI_REPOSITORY")
}

// gitlabRegistryState follows the rules of the GitLab release's registry
// access check: it runs when release-images is true or base-stages names a
// stage, and every job that uses the registry token runs only then.
func gitlabRegistryState(inputs *yaml.Node) release.SetupState {
	images := setupChoice(inputs, "release-images", false)

	bases := release.SetupUnused
	if stages := setupScalar(yamlChild(inputs, "base-stages")); strings.Contains(stages, "$") {
		bases = release.SetupConditional
	} else if stages != "" {
		bases = release.SetupRequired
	}

	switch {
	case images == release.SetupRequired || bases == release.SetupRequired:
		return release.SetupRequired
	case images == release.SetupConditional || bases == release.SetupConditional:
		return release.SetupConditional
	default:
		return release.SetupUnused
	}
}

// setupNeedsArtifactsConfig is how a Forgejo signing call names its
// declaration: the prepare job's artifacts-config output.
var setupNeedsArtifactsConfig = regexp.MustCompile(`^\$\{\{\s*needs\.([A-Za-z0-9_-]+)\.outputs\.artifacts-config\s*\}\}$`)

// setupPreparedArtifactsConfig follows a Forgejo call's artifacts-config,
// when it is a prepare-release.yml job's output, to that job's inputs: the
// output is the input, validated. Any other input, and a GitLab call's, is
// the call's own.
func setupPreparedArtifactsConfig(inputs, jobs *yaml.Node) *yaml.Node {
	match := setupNeedsArtifactsConfig.FindStringSubmatch(setupScalar(yamlChild(inputs, "artifacts-config")))
	if match == nil {
		return inputs
	}

	prepare := yamlChild(jobs, match[1])
	if _, workflow, _ := strings.Cut(setupScalar(yamlChild(prepare, "uses")), "/.forgejo/workflows/"); !strings.HasPrefix(workflow, setupPrepareWorkflow+"@") {
		return inputs
	}

	return yamlChild(prepare, "with")
}

// setupDefaultArtifactsConfig is the declaration a release reads when the
// call names none.
const setupDefaultArtifactsConfig = ".forge-ci/artifacts.yml"

// releaseFileSigning is the GPG demand of the declaration a Forgejo or GitLab
// release signs its checksums and SBOMs by: sign.method gpg or
// sign.files-method gpg needs the GPG key, its passphrase and its
// fingerprint. Any other declaration, and none, signs them with the Cosign
// key.
func (report *RequirementsReport) releaseFileSigning(root string, inputs *yaml.Node, call *SetupCall) release.SetupState {
	path := setupScalar(yamlChild(inputs, "artifacts-config"))
	if path == "" {
		path = setupDefaultArtifactsConfig
	}

	// Only repository-local literal paths; never evaluate a workflow expression.
	if !filepath.IsLocal(path) || strings.Contains(path, "$") {
		report.unknown(call.Location + ": artifacts-config is not a literal repository-local path; GPG release file signing remains conditional")

		return release.SetupConditional
	}

	if _, err := os.Lstat(filepath.Join(root, path)); os.IsNotExist(err) {
		return release.SetupUnused
	}

	if report.read(root, path) == nil {
		return release.SetupConditional
	}

	cfg, checks, parsed := loadDoctorConfig(root, filepath.Join(root, path), false)
	if !parsed || CountFailures(checks) > 0 || cfg == nil {
		report.unknown(call.Location + ": artifact configuration could not be resolved; GPG release file signing remains conditional")

		return release.SetupConditional
	}

	return setupState(cfg.Sign.DeclaresGPG())
}

func setupState(required bool) release.SetupState {
	if required {
		return release.SetupRequired
	}

	return release.SetupUnused
}

func (report *RequirementsReport) githubRequirements(root string, inputs *yaml.Node, call *SetupCall) []release.SetupRequirement {
	gpg, gpgPublic, ssh, cosign, maven := release.SetupConditional, release.SetupConditional, release.SetupConditional, release.SetupConditional, release.SetupConditional

	path := setupDefaultArtifactsConfig
	if n := yamlChild(inputs, "artifacts-config"); n != nil {
		path = setupScalar(n)
	}

	call.Settings = append(call.Settings, SetupSetting{"permissions", "caller workflow/job", "contents: write, packages: write, id-token: write, actions: read; these do not grant PAT access"})
	// Only repository-local literal paths; never evaluate a workflow expression.
	if path == "" || !filepath.IsLocal(path) || strings.Contains(path, "$") {
		report.unknown(call.Location + ": artifacts-config is not a literal repository-local path")

		return release.GitHubReleaseSetup(gpg, gpgPublic, ssh, cosign, maven, release.SetupConditional)
	}

	if _, err := os.Lstat(filepath.Join(root, path)); !os.IsNotExist(err) && report.read(root, path) == nil {
		return release.GitHubReleaseSetup(gpg, gpgPublic, ssh, cosign, maven, release.SetupConditional)
	}

	cfg, checks, parsed := loadDoctorConfig(root, filepath.Join(root, path), path == setupDefaultArtifactsConfig)
	if !parsed || CountFailures(checks) > 0 || cfg == nil {
		report.unknown(call.Location + ": artifact configuration could not be resolved; signing/publishing requirements remain conditional")

		return release.GitHubReleaseSetup(gpg, gpgPublic, ssh, cosign, maven, release.SetupConditional)
	}

	return report.githubConfigRequirements(cfg, inputs, call)
}

func (report *RequirementsReport) githubConfigRequirements(cfg *config.Config, inputs *yaml.Node, call *SetupCall) []release.SetupRequirement {
	var gpg, gpgPublic, ssh, cosign, maven release.SetupState

	plan := pipeline.NewConfigPlan(cfg)
	artifactSigning := setupChoice(inputs, "release.signartifacts", true)
	maven = setupState(len(plan.Artifacts.MavenCentral) > 0)
	ssh = setupState(plan.GitSigning.Method == config.GitSignSSH)
	gpgPublic = setupGPGState(plan.GitSigning.ImportsGPGKey, plan.Sign.ImportsGPGKey, artifactSigning)

	gpg = gpgPublic
	if maven == release.SetupRequired {
		gpg = release.SetupRequired
	}

	cosign = setupState(plan.Sign.UsesKeySecret && artifactSigning == release.SetupRequired)
	// Container signing can use the same key independently of release file signing.
	if cosign != release.SetupRequired && plan.Sign.UsesKeySecret && (artifactSigning == release.SetupConditional || len(cfg.Containers) > 0) {
		cosign = release.SetupConditional
	}

	if plan.Sign.Method == release.SignMethodKMS && !plan.Sign.UsesKeySecret {
		report.unknown(call.Location + ": KMS/file/PKCS#11 authentication is operator-specific; consult the signing backend documentation")
	}

	if len(plan.Artifacts.XcodeIOS) > 0 || len(plan.Artifacts.GradleAndroid) > 0 {
		report.unknown(call.Location + ": mobile build/publisher credentials are outside this release setup report; see docs/publishing.md")
	}

	buildSecrets := release.SetupUnused

	for _, container := range cfg.Containers {
		if len(container.BuildSecrets) > 0 {
			buildSecrets = release.SetupRequired

			break
		}
	}

	return release.GitHubReleaseSetup(gpg, gpgPublic, ssh, cosign, maven, buildSecrets)
}

func setupGPGState(gitSigning, artifactUsesGPG bool, artifactSigning release.SetupState) release.SetupState {
	if gitSigning {
		return release.SetupRequired
	}

	if artifactUsesGPG {
		return artifactSigning
	}

	return release.SetupUnused
}

func (report *RequirementsReport) mapRequirements(call *SetupCall, requirements []release.SetupRequirement, secrets *yaml.Node) {
	conditional := false

	for _, item := range requirements {
		source, resolved := setupSource(yamlChild(secrets, item.Name), "secrets")
		if call.Forge == setupGitLab {
			source, resolved = "protected project variable; environment scope must match", true
		}

		if item.State == release.SetupAutomatic {
			source, resolved = "provided by the forge; do not create a replacement secret", true
		}

		call.Requirements = append(call.Requirements, SetupItem{SetupRequirement: item, Source: source})
		if item.State == release.SetupConditional {
			conditional = true
		}

		if item.State != release.SetupUnused && !resolved {
			report.unknown(call.Location + ": " + item.Name + " has no resolvable explicit caller mapping; remote presence is unknown")
		}
	}

	if conditional {
		report.Notes = append(report.Notes, call.Location+": conditional rows depend on workflow choices or runtime artifacts; they do not mean the credential is missing")
	}
}
