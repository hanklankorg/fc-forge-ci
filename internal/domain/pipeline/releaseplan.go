// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/build"
	"github.com/diggsweden/forge-ci/internal/domain/config"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/diggsweden/forge-ci/internal/domain/runners"
	"github.com/diggsweden/forge-ci/internal/listval"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// ReleasePlanVersion is the current release plan contract version.
const ReleasePlanVersion = 1

// ArtifactTransferPlanVersion is the current artifact-transfer plan contract
// version. It is shared by production and dev release flows.
const ArtifactTransferPlanVersion = 1

// ReleasePlanInput contains workflow inputs plus the parsed config plan.
type ReleasePlanInput struct {
	RepositoryVisibility            string
	ConfigPlan                      ConfigPlan
	Branch                          string
	RefName                         string
	FilePattern                     string
	ReleasePolicy                   string
	ReleasePublisher                string
	ReleaseRequireAllowlistedSigner bool
	ReleaseDraft                    bool
	ReleaseSBOMs                    string
	ReleaseSignArtifacts            bool
	ChangelogCreator                string
	ChangelogSkipVersionBump        bool
	// ReleasePlatforms is RELEASE_PLATFORMS, the subset of each container's
	// declared platforms this run builds. Empty plans every declared platform.
	ReleasePlatforms string
	// Runners is the runner inputs the forge layer passed. The plan is
	// refused when a platform it builds, or its Xcode jobs, has none; nil, a
	// layer that passes none, is not checked.
	Runners *runners.Inputs
}

// ReleasePlan is the top-level typed contract for production release setup.
type ReleasePlan struct {
	Version           int                  `json:"version"`
	Context           ReleaseContext       `json:"context"`
	Policy            ReleasePolicy        `json:"policy"`
	Stages            ReleaseStagePlans    `json:"stages"`
	ArtifactTransfers ArtifactTransferPlan `json:"artifact_transfers"`
}

// ReleaseContext records workflow context used to derive release policy.
type ReleaseContext struct {
	Branch           string `json:"branch"`
	RefName          string `json:"ref_name"`
	FilePattern      string `json:"file_pattern,omitempty"`
	ReleasePolicy    string `json:"release_policy,omitempty"`
	ReleasePublisher string `json:"release_publisher,omitempty"`
	ChangelogCreator string `json:"changelog_creator,omitempty"`
}

// ReleasePolicy is the release policy envelope carried by the typed plan.
type ReleasePolicy struct {
	SignArtifacts            bool                 `json:"sign_artifacts"`
	RequireAllowlistedSigner bool                 `json:"require_allowlisted_signer"`
	RunVersionBump           bool                 `json:"run_version_bump"`
	CreateRelease            bool                 `json:"create_release"`
	CreateDraftRelease       bool                 `json:"create_draft_release"`
	SBOMs                    string               `json:"sboms"`
	MakeLatest               bool                 `json:"make_latest"`
	Prerelease               bool                 `json:"prerelease"`
	HasContainers            bool                 `json:"has_containers"`
	SBOMConflict             *ReleaseSBOMConflict `json:"sbom_conflict,omitempty"`
}

// ReleaseSBOMConflict records a non-empty release/pipeline SBOM mismatch.
type ReleaseSBOMConflict struct {
	ReleaseSBOMs  string `json:"release_sboms"`
	PipelineSBOMs string `json:"pipeline_sboms"`
}

// ReleaseStagePlans contains the stage-specific plans emitted separately.
type ReleaseStagePlans struct {
	Prepare ReleasePrepareStagePlan `json:"prepare"`
	Build   ReleaseBuildStagePlan   `json:"build"`
	Publish ReleasePublishStagePlan `json:"publish"`
}

// ArtifactTransferPlan lists exact CI artifacts that downstream jobs download
// before SBOM generation or release attachment.
type ArtifactTransferPlan struct {
	Version int                `json:"version"`
	Items   []ArtifactTransfer `json:"items"`
}

// ArtifactTransferKind classifies why a transfer is needed.
type ArtifactTransferKind string

// Recognised ArtifactTransferKind values.
const (
	ArtifactTransferBuildArtifact         ArtifactTransferKind = "build_artifact"
	ArtifactTransferBuildSBOM             ArtifactTransferKind = "build_sbom"
	ArtifactTransferAnalyzedContainerSBOM ArtifactTransferKind = "analyzed_container_sbom"
	ArtifactTransferExtractedBinaries     ArtifactTransferKind = "extracted_binaries"
)

// ArtifactTransfer is one explicit artifact download operation. Name is used
// as-is; NameTemplate uses {run_id} replacement by transfer executors for
// artifacts whose upload name contains the current run id.
type ArtifactTransfer struct {
	Kind         ArtifactTransferKind `json:"kind"`
	Name         string               `json:"name,omitempty"`
	NameTemplate string               `json:"name_template,omitempty"`
	Path         string               `json:"path"`
	Required     bool                 `json:"required"`
}

// ValidateArtifactTransferItem validates the static transfer contract before a
// provider-specific downloader resolves templates or performs I/O.
func ValidateArtifactTransferItem(item ArtifactTransfer) error {
	if !IsArtifactTransferKind(item.Kind) {
		return fmt.Errorf("artifact transfer kind %q is invalid: %w", item.Kind, errs.ErrInvalidConfig)
	}

	name := strings.TrimSpace(item.Name)

	tmpl := strings.TrimSpace(item.NameTemplate)
	switch {
	case name == "" && tmpl == "":
		return fmt.Errorf("artifact transfer missing name/name_template: %w", errs.ErrInvalidConfig)
	case name != "" && tmpl != "":
		return fmt.Errorf("artifact transfer must set exactly one of name/name_template: %w", errs.ErrInvalidConfig)
	}

	if strings.ContainsAny(item.Name, "\t\n\r") {
		return fmt.Errorf("artifact transfer name must be a single-line value: %w", errs.ErrInvalidConfig)
	}

	if strings.ContainsAny(item.NameTemplate, "\t\n\r") {
		return fmt.Errorf("artifact transfer name_template must be a single-line value: %w", errs.ErrInvalidConfig)
	}

	if strings.TrimSpace(item.Path) == "" {
		return fmt.Errorf("artifact transfer %q has empty path: %w", cmp.Or(name, tmpl, "artifact"), errs.ErrInvalidConfig)
	}

	// The path decides where a downloaded artifact is unpacked. It is
	// re-validated here rather than trusted, because the plan crosses a
	// process boundary as JSON and this is the field that reaches the
	// filesystem.
	if !pathsafe.Relative(item.Path) {
		return fmt.Errorf("artifact transfer %q has an unsafe path %q: %w", cmp.Or(name, tmpl, "artifact"), item.Path, errs.ErrInvalidConfig)
	}

	return nil
}

// ParseArtifactTransferPlan decodes and fully validates a transfer plan.
//
// Validating here, once, rather than per item as each is acted on, is what
// makes a refused plan mean nothing happened: DownloadArtifacts used to
// validate inside its download loop, so a plan whose second item was invalid
// had already fetched the first. Both consumers of the plan now share this
// entry point, which also stops them disagreeing about what a valid item is --
// only one of them checked the path.
func ParseArtifactTransferPlan(raw string) (ArtifactTransferPlan, error) {
	// A transfer plan is produced and consumed by the same release, like a
	// stage plan, and it names the files a download lands on disk: an unknown
	// or repeated member is a stale shape, never something to read past.
	var plan ArtifactTransferPlan
	if err := DecodeStagePlan(raw, "artifact-transfer-plan-json", &plan); err != nil {
		return ArtifactTransferPlan{}, err
	}

	if plan.Version != ArtifactTransferPlanVersion {
		return ArtifactTransferPlan{}, fmt.Errorf("artifact transfer plan has unsupported version %d: %w", plan.Version, errs.ErrInvalidConfig)
	}

	for _, item := range plan.Items {
		if err := ValidateArtifactTransferItem(item); err != nil {
			return ArtifactTransferPlan{}, err
		}
	}

	return plan, nil
}

// IsArtifactTransferKind reports whether kind is a known transfer kind.
func IsArtifactTransferKind(kind ArtifactTransferKind) bool {
	switch kind {
	case ArtifactTransferBuildArtifact, ArtifactTransferBuildSBOM, ArtifactTransferAnalyzedContainerSBOM, ArtifactTransferExtractedBinaries:
		return true
	default:
		return false
	}
}

// ReleasePrepareStagePlan describes release prepare-stage targets.
type ReleasePrepareStagePlan struct {
	Version     int                   `json:"version"`
	Stage       string                `json:"stage"`
	FilePattern string                `json:"file_pattern,omitempty"`
	Targets     ReleasePrepareTargets `json:"targets"`
}

// ReleasePrepareTargets are the release prepare-stage jobs.
type ReleasePrepareTargets struct {
	VersionBump TargetPlan[PlannedArtifact] `json:"version_bump"`
}

// ReleaseBuildStagePlan describes release build-stage targets.
type ReleaseBuildStagePlan struct {
	Version int                 `json:"version"`
	Stage   string              `json:"stage"`
	Targets ReleaseBuildTargets `json:"targets"`
}

// ReleaseBuildTargets are the artifact-first build jobs in release build stage.
type ReleaseBuildTargets struct {
	Maven         TargetPlan[PlannedArtifact] `json:"maven"`
	NPM           TargetPlan[PlannedArtifact] `json:"npm"`
	Gradle        TargetPlan[PlannedArtifact] `json:"gradle"`
	GradleAndroid TargetPlan[PlannedArtifact] `json:"gradle_android"`
	XcodeIOS      TargetPlan[PlannedArtifact] `json:"xcode_ios"`
	Go            TargetPlan[PlannedArtifact] `json:"go"`
	Cargo         TargetPlan[PlannedArtifact] `json:"cargo"`
}

// ReleasePublishStagePlan describes release publish-stage targets.
type ReleasePublishStagePlan struct {
	Version int                   `json:"version"`
	Stage   string                `json:"stage"`
	Targets ReleasePublishTargets `json:"targets"`
}

// ReleasePublishTargets are the release publish-stage jobs.
type ReleasePublishTargets struct {
	ForgePackages       TargetPlan[PlannedArtifact]  `json:"forge_packages"`
	MavenCentral        TargetPlan[PlannedArtifact]  `json:"maven_central"`
	GooglePlay          TargetPlan[PlannedArtifact]  `json:"google_play"`
	XcodeIOS            TargetPlan[PlannedArtifact]  `json:"xcode_ios"`
	Containers          TargetPlan[PlannedContainer] `json:"containers"`
	CargoContainerFirst TargetPlan[PlannedArtifact]  `json:"cargo_container_first"`
	GoContainerFirst    TargetPlan[PlannedArtifact]  `json:"go_container_first"`
}

// NewReleasePlan builds the production release setup contract.
func NewReleasePlan(in ReleasePlanInput) (ReleasePlan, error) {
	if err := ValidateConfigPlan(in.ConfigPlan); err != nil {
		return ReleasePlan{}, err
	}

	// The images plan builds only the selected platforms, so the publish stage
	// must expect exactly those platforms' per-architecture artifacts.
	narrowed, err := selectReleasePlatforms(in.ConfigPlan, in.ReleasePlatforms)
	if err != nil {
		return ReleasePlan{}, err
	}

	in.ConfigPlan = narrowed

	if err := RefuseDistroPackages(narrowed.DistroPackages, "GitHub", "GitHub Packages has no Debian, RPM or Alpine registry"); err != nil {
		return ReleasePlan{}, err
	}

	if err := in.Runners.Admit(releaseRunnerNeeds(narrowed)); err != nil {
		return ReleasePlan{}, err
	}

	if err := domainrelease.ValidateTransparencyConsent(in.RepositoryVisibility, in.ConfigPlan.Sign.Transparency, in.ConfigPlan.Sign.TransparencyExplicit, in.ReleaseSignArtifacts); err != nil {
		return ReleasePlan{}, err
	}

	policy, err := resolveReleasePolicy(releasePolicyInputs{
		ReleasePolicy:    in.ReleasePolicy,
		ReleasePublisher: in.ReleasePublisher,
		// A Central release is permanent. Its destination requires a signed,
		// allowlisted request even when no artifact opted into the general gate.
		ReleaseRequireAllowlistedSigner: in.ReleaseRequireAllowlistedSigner || len(in.ConfigPlan.Artifacts.MavenCentral) > 0,
		ReleaseDraft:                    in.ReleaseDraft,
		ReleaseSBOMs:                    in.ReleaseSBOMs,
		ReleaseSignArtifacts:            in.ReleaseSignArtifacts,
		ChangelogCreator:                in.ChangelogCreator,
		ChangelogSkipVersionBump:        in.ChangelogSkipVersionBump,
		RefName:                         in.RefName,
		PipelineSBOMs:                   in.ConfigPlan.PipelineSBOMs,
		AnyRequireAuthorization:         in.ConfigPlan.AnyRequireAuthorization,
		HasContainers:                   in.ConfigPlan.Containers.HasContainers,
	})
	if err != nil {
		return ReleasePlan{}, err
	}

	if policy.Prerelease {
		if err := validatePrereleasePublications(in.ConfigPlan, in.RefName); err != nil {
			return ReleasePlan{}, err
		}
	}

	if signingErr := validateContainerSigning(in.ConfigPlan, policy); signingErr != nil {
		return ReleasePlan{}, signingErr
	}

	buildSBOM, err := hasSBOMLayer(policy.SBOMs, config.SBOMLayerBuild)
	if err != nil {
		return ReleasePlan{}, err
	}

	transfers := newReleaseArtifactTransferPlan(in.ConfigPlan, policy)
	if err := validateConcreteTransferNames(transfers); err != nil {
		return ReleasePlan{}, err
	}

	return ReleasePlan{
		Version: ReleasePlanVersion,
		Context: ReleaseContext{
			Branch:           in.Branch,
			RefName:          in.RefName,
			FilePattern:      in.FilePattern,
			ReleasePolicy:    in.ReleasePolicy,
			ReleasePublisher: in.ReleasePublisher,
			ChangelogCreator: in.ChangelogCreator,
		},
		Policy: policy,
		Stages: ReleaseStagePlans{
			Prepare: newReleasePrepareStagePlan(in.ConfigPlan, policy, in.FilePattern),
			Build:   newReleaseBuildStagePlan(in.ConfigPlan),
			Publish: newReleasePublishStagePlan(in.ConfigPlan, buildSBOM),
		},
		ArtifactTransfers: transfers,
	}, nil
}

func validateContainerSigning(plan ConfigPlan, policy ReleasePolicy) error {
	for _, container := range plan.Containers.All {
		if container.EnableSLSA && (!policy.SignArtifacts || !plan.Sign.SignsContainers) {
			return fmt.Errorf(
				"container %q enables pushed SLSA provenance but signing is not enabled with a cosign-capable method; enable release artifact signing with sign.method sigstore or kms, or set enable-slsa: false: %w",
				container.Name, errs.ErrInvalidConfig,
			)
		}
	}

	return nil
}

// VersionBumpRuns is the version-bump rule every forge's release shares: the
// release commit bumps the planned projects when the release renders the
// changelog that commit carries, unless the caller skips the bump.
func VersionBumpRuns(rendersChangelog, skipBump bool) bool {
	return rendersChangelog && !skipBump
}

// NewPolicyReleaseStagePlans returns the prepare and build stage plans of a
// declaration for a release whose policy, not artifacts.yml, decides signing
// and publication: the GitLab release component's and Forgejo's
// prepare-release.yml. The stages are the ones NewReleasePlan derives from the
// same declaration. Both releases render their changelog with either renderer
// and have no skip, so they bump.
func NewPolicyReleaseStagePlans(configPlan ConfigPlan) (ReleasePrepareStagePlan, ReleaseBuildStagePlan, error) {
	if err := ValidateConfigPlan(configPlan); err != nil {
		return ReleasePrepareStagePlan{}, ReleaseBuildStagePlan{}, err
	}

	policy := ReleasePolicy{RunVersionBump: VersionBumpRuns(true, false)}

	return newReleasePrepareStagePlan(configPlan, policy, ""), newReleaseBuildStagePlan(configPlan), nil
}

// ReleaseSigningWarnings says how the forge's release signs when the
// declaration asks for signing that release does not use, and is empty when
// it signs as declared. It warns about a sign.method, a sign.files-method or
// a sign.key the release does not use, naming the key that signs instead,
// and about any sign.transparency, which the release's transparency-log
// input decides instead. One declaration often serves several forges, so
// these are warnings, never a refusal. sign is the block as written: a
// declaration without a method is signed as the release signs by default,
// and has no warning.
func ReleaseSigningWarnings(sign config.SignConfig, forge provider.ForgeAPI) []string {
	methods := provider.ReleaseSignMethods(forge)
	if methods == nil {
		return nil
	}

	var warnings []string
	if warning := releaseKeyWarning(sign, forge, methods); warning != "" {
		warnings = append(warnings, warning)
	}

	if sign.Transparency != "" {
		warnings = append(warnings, fmt.Sprintf(
			"sign.transparency %s is not used by the %s release; its transparency-log input decides whether Cosign signatures are written to the public transparency log, and is none by default",
			sign.Transparency, forge))
	}

	return warnings
}

// releaseKeyWarning names the key that signs the release files when the
// declaration asks for a method or key the release does not use.
func releaseKeyWarning(sign config.SignConfig, forge provider.ForgeAPI, methods []domainrelease.SignMethod) string {
	const cosignKey = "the Cosign key (RELEASE_COSIGN_PRIVATE_KEY)"

	signedWith := "release files are signed with " + cosignKey
	if sign.DeclaresGPG() && slices.Contains(methods, domainrelease.SignMethodGPG) {
		signedWith = "release files are signed with GPG (RELEASE_GPG_PRIVATE_KEY), and the provenance, evidence and images with " + cosignKey
	}

	if sign.Method != "" && !slices.Contains(methods, sign.Method) {
		return fmt.Sprintf("sign.method %s is not available on the %s release; %s", sign.Method, forge, signedWith)
	}

	if sign.FilesMethod != "" && !slices.Contains(methods, sign.FilesMethod) {
		return fmt.Sprintf("sign.files-method %s is not available on the %s release; %s", sign.FilesMethod, forge, signedWith)
	}

	if sign.Key != "" && sign.Key != config.CosignKeyEnvRef {
		return fmt.Sprintf("sign.key %q is not used by the %s release; %s, sign.key %s",
			sign.Key, forge, signedWith, config.CosignKeyEnvRef)
	}

	return ""
}

// RefuseDistroPackages refuses the distro-packages block for a release whose
// forge has no registry to publish the packages to, before anything is
// built: the release would otherwise publish without the packages the
// declaration asks for. reason says what the forge lacks.
func RefuseDistroPackages(declared *config.DistroPackages, forge, reason string) error {
	if declared == nil {
		return nil
	}

	return fmt.Errorf(
		"distro-packages is published by the Forgejo release only; %s, so the %s release cannot publish .deb, .rpm and .apk files to a registry: remove distro-packages (the packages stay release files): %w",
		reason, forge, errs.ErrUnsupported)
}

// UndeclaredReleaseStagePlans are the stage plans of a release without a
// declaration: no project to bump and none to build.
func UndeclaredReleaseStagePlans() (ReleasePrepareStagePlan, ReleaseBuildStagePlan) {
	return newReleasePrepareStagePlan(ConfigPlan{}, ReleasePolicy{}, ""), newReleaseBuildStagePlan(ConfigPlan{})
}

// newReleasePrepareStagePlan builds the standalone release prepare-stage plan.
func newReleasePrepareStagePlan(configPlan ConfigPlan, policy ReleasePolicy, filePattern string) ReleasePrepareStagePlan {
	return ReleasePrepareStagePlan{
		Version:     ReleasePlanVersion,
		Stage:       "prepare",
		FilePattern: filePattern,
		Targets: ReleasePrepareTargets{
			VersionBump: targetPlan(configPlan.Artifacts.All, len(configPlan.Artifacts.All) > 0 && policy.RunVersionBump),
		},
	}
}

// newReleaseBuildStagePlan builds the standalone release build-stage plan.
func newReleaseBuildStagePlan(configPlan ConfigPlan) ReleaseBuildStagePlan {
	artifacts := configPlan.Artifacts

	return ReleaseBuildStagePlan{
		Version: ReleasePlanVersion,
		Stage:   "build",
		Targets: ReleaseBuildTargets{
			Maven:         newTargetPlan(artifacts.Maven),
			NPM:           newTargetPlan(artifacts.NPM),
			Gradle:        newTargetPlan(artifacts.Gradle),
			GradleAndroid: newTargetPlan(artifacts.GradleAndroid),
			XcodeIOS:      newTargetPlan(artifacts.XcodeIOS),
			Go:            newTargetPlan(artifacts.GoArtifactFirst),
			Cargo:         newTargetPlan(artifacts.CargoArtifactFirst),
		},
	}
}

// VersionFamily is a toolchain family whose native version tool prepares a
// share of a release's version bump in a key-free job of its own, in the
// family's runtime image, and the project type that tool bumps.
type VersionFamily struct {
	Family      string           `json:"family"`
	ProjectType projecttype.Type `json:"project-type"`
}

// versionFamilies are the families with a native version tool, in the order
// every forge lists them. The other types' version files are text, which the
// release commit's job writes itself.
var versionFamilies = []VersionFamily{
	{Family: "java", ProjectType: projecttype.Maven},
	{Family: "node", ProjectType: projecttype.NPM},
	{Family: "rust", ProjectType: projecttype.Cargo},
}

// VersionFamilies are the families whose native version tools the planned
// bump runs, each once, in family order; none when the plan bumps nothing.
func (p ReleasePrepareStagePlan) VersionFamilies() []VersionFamily {
	if !p.Targets.VersionBump.Runs {
		return nil
	}

	var out []VersionFamily

	for _, family := range versionFamilies {
		if slices.ContainsFunc(p.Targets.VersionBump.Items, func(item PlannedArtifact) bool {
			return item.ProjectType == family.ProjectType
		}) {
			out = append(out, family)
		}
	}

	return out
}

// newReleasePublishStagePlan builds the standalone release publish-stage plan.
func newReleasePublishStagePlan(configPlan ConfigPlan, buildSBOM bool) ReleasePublishStagePlan {
	artifacts := configPlan.Artifacts
	cargoContainerFirst := []PlannedArtifact{}
	goContainerFirst := []PlannedArtifact{}

	if buildSBOM {
		// Container-first cargo / go only — artifact-first variants
		// emit their Build SBOMs inline in build-cargo.yml / build-go.yml
		// at the build stage. Listing them here would double-emit the
		// same bom.json under two upload-artifact paths.
		cargoContainerFirst = filterArtifactsBySBOMLayer(artifacts.CargoContainerFirst, config.SBOMLayerBuild)
		goContainerFirst = filterArtifactsBySBOMLayer(artifacts.GoContainerFirst, config.SBOMLayerBuild)
	}

	return ReleasePublishStagePlan{
		Version: ReleasePlanVersion,
		Stage:   "publish",
		Targets: ReleasePublishTargets{
			ForgePackages:       newTargetPlan(artifacts.ForgePackages),
			MavenCentral:        newTargetPlan(artifacts.MavenCentral),
			GooglePlay:          newTargetPlan(artifacts.GooglePlay),
			XcodeIOS:            newTargetPlan(artifacts.XcodeIOS),
			Containers:          targetPlan(configPlan.Containers.All, configPlan.Containers.HasContainers),
			CargoContainerFirst: newTargetPlan(cargoContainerFirst),
			GoContainerFirst:    newTargetPlan(goContainerFirst),
		},
	}
}

// androidReleaseAPKName is the upload that holds the release APK of an
// Android application, which a release carries beside the AAB so that the
// application can be installed from the release itself. Empty for a library
// and for a build without the release build type.
func androidReleaseAPKName(artifact PlannedArtifact) string {
	if artifact.ProjectType != projecttype.GradleAndroid || artifact.BuildType == config.BuildTypeLibrary {
		return ""
	}

	declared := config.Artifact{BuildType: artifact.BuildType, GradleAndroid: artifact.GradleAndroid}

	buildTypes, err := build.ParseAndroidBuildTypes(declared.AndroidBuildTypes())
	if err != nil || !buildTypes.Release {
		return ""
	}

	names, err := build.ResolveAndroidArtifactNames(build.AndroidArtifactNamesInput{Override: artifact.Name})
	if err != nil {
		return ""
	}

	return names.ReleaseName
}

// newReleaseArtifactTransferPlan builds the release-create artifact download
// plan without wildcard artifact names.
func newReleaseArtifactTransferPlan(configPlan ConfigPlan, policy ReleasePolicy) ArtifactTransferPlan {
	items := make([]ArtifactTransfer, 0)

	for _, artifact := range configPlan.Artifacts.All {
		if artifact.BuildArtifactName != "" {
			items = append(items, ArtifactTransfer{
				Kind:     ArtifactTransferBuildArtifact,
				Name:     artifact.BuildArtifactName,
				Path:     "./release-artifacts/", //nolint:goconst // test fixture / generic identifier — extracting would explode setup boilerplate.
				Required: true,
			})
		}

		if name := androidReleaseAPKName(artifact); name != "" {
			items = append(items, ArtifactTransfer{
				Kind:     ArtifactTransferBuildArtifact,
				Name:     name,
				Path:     "./release-artifacts/",
				Required: true,
			})
		}

		if policyIncludesSBOMLayer(policy.SBOMs, config.SBOMLayerBuild) && artifact.BuildSBOMArtifactName != "" && artifactIncludesSBOMLayer(artifact, config.SBOMLayerBuild) {
			items = append(items, ArtifactTransfer{
				Kind:     ArtifactTransferBuildSBOM,
				Name:     artifact.BuildSBOMArtifactName,
				Path:     "./release-artifacts/",
				Required: true,
			})
		}
	}

	if policyIncludesSBOMLayer(policy.SBOMs, config.SBOMLayerAnalyzedContainer) {
		for _, container := range configPlan.Containers.All {
			if !container.EnableAnalyzedContainerSBOM {
				continue
			}

			for _, suffix := range transferPlatformSuffixes(container.Platforms) {
				items = append(items, ArtifactTransfer{
					Kind:         ArtifactTransferAnalyzedContainerSBOM,
					NameTemplate: fmt.Sprintf("analyzed-container-sbom-{run_id}-%s-%s", transferContainerName(container.Name), suffix),
					Path:         "./sbom-artifacts/",
					Required:     true,
				})
			}
		}
	}

	for _, container := range configPlan.Containers.All {
		if container.ExtractBinaryTarget == "" && len(container.ExtractBinaryPaths) == 0 {
			continue
		}

		for _, suffix := range transferPlatformSuffixes(container.Platforms) {
			items = append(items, ArtifactTransfer{
				Kind:     ArtifactTransferExtractedBinaries,
				Name:     fmt.Sprintf("%s-binaries-%s", transferContainerName(container.Name), suffix),
				Path:     "./release-artifacts/binaries/",
				Required: false,
			})
		}
	}

	return ArtifactTransferPlan{Version: ArtifactTransferPlanVersion, Items: items}
}

// Check the selected transfers, not all potential uploads. Templates remain
// unresolved here; this is not a runtime or provider-normalization guarantee.
func validateConcreteTransferNames(plan ArtifactTransferPlan) error {
	seen := make(map[string]int, len(plan.Items))
	for index, item := range plan.Items {
		if item.Name == "" {
			continue
		}

		if previous, ok := seen[item.Name]; ok {
			return fmt.Errorf("artifact_transfers.items[%d].name has duplicate concrete name %q (also items[%d]): %w", index, item.Name, previous, errs.ErrInvalidConfig)
		}

		seen[item.Name] = index
	}

	return nil
}

func policyIncludesSBOMLayer(value string, layer config.SBOMLayer) bool {
	layers, err := config.ExpandSBOMs(value)
	if err != nil {
		return false
	}

	return slices.Contains(layers, layer)
}

func artifactIncludesSBOMLayer(artifact PlannedArtifact, layer config.SBOMLayer) bool {
	return slices.Contains(artifact.EffectiveSBOMs, layer)
}

// selectReleasePlatforms narrows every container to the platforms a run selects,
// with the same rules as the release images plan. A container that declares no
// platforms keeps its default.
func selectReleasePlatforms(plan ConfigPlan, selection string) (ConfigPlan, error) {
	if selection == "" {
		return plan, nil
	}

	containers := slices.Clone(plan.Containers.All)

	for i := range containers {
		declared := listval.Tokens(containers[i].Platforms)
		if len(declared) == 0 {
			continue
		}

		platforms, err := domaincontainer.SelectPlatforms(declared, selection)
		if err != nil {
			return ConfigPlan{}, fmt.Errorf("container %q: %w", containers[i].Name, err)
		}

		containers[i].Platforms = strings.Join(platforms, ",")
	}

	plan.Containers.All = containers

	return plan, nil
}

// Deduplicate suffixes within one container's expansion, never whole transfer
// records from potentially different producers. Preserve first occurrence order.
func transferPlatformSuffixes(platforms string) []string {
	if strings.TrimSpace(platforms) == "" {
		return []string{"amd64"}
	}

	out := make([]string, 0)
	seen := make(map[string]bool)

	for _, raw := range listval.Tokens(platforms) {
		platform := strings.TrimSpace(raw)
		if platform == "" {
			continue
		}

		platform = strings.TrimPrefix(platform, "linux/")

		platform = strings.ReplaceAll(platform, "/", "-")
		if !seen[platform] {
			seen[platform] = true
			out = append(out, platform)
		}
	}

	if len(out) == 0 {
		return []string{"amd64"}
	}

	return out
}

func transferContainerName(name string) string {
	if name == "" {
		return "container"
	}

	return name
}

func filterArtifactsBySBOMLayer(in []PlannedArtifact, layer config.SBOMLayer) []PlannedArtifact {
	return filterArtifacts(in, func(a PlannedArtifact) bool {
		return slices.Contains(a.EffectiveSBOMs, layer)
	})
}
