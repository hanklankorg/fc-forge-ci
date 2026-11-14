// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage

import (
	"cmp"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	appversion "github.com/diggsweden/forge-ci/internal/app/version"
	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

const (
	// sbomToolVersionFlag pins the build CLI's SBOM tool for JVM and npm builds.
	sbomToolVersionFlag = "--sbom-tool-version"
	// runTestsFlag keeps tests enabled for JVM and npm snapshot builds.
	runTestsFlag = "--skip-tests=false"
)

var (
	errBuildRunnerRequired  = errors.New("snapshot artifact build requires the existing build CLI runner")
	errPreexistingTarballs  = errors.New("snapshot npm producer refuses preexisting tarballs")
	errUnsupportedBuildType = errors.New("unsupported snapshot build type")
)

// ArtifactBuildRunner invokes this engine's existing build command, never a
// shell or a consumer-supplied executable. Providers only supply toolchains.
type ArtifactBuildRunner interface {
	Run(ctx context.Context, request ArtifactBuildRequest) error
}

// ArtifactBuildRequest is one build CLI invocation: its working directory,
// arguments and the allowlisted environment.
type ArtifactBuildRequest struct {
	Dir       string
	Args, Env []string
}

// BuildArtifactInput selects one planned artifact, its producer output root,
// an optional prebuilt artifact directory and the parent environment.
type BuildArtifactInput struct {
	Name, OutputDir, FromArtifact string
	Environment                   []string
}

// BuildArtifact builds (or adopts, with FromArtifact) one planned artifact at
// the pinned source and prepares its producer directory.
func BuildArtifact(ctx context.Context, ops appversion.GenerateSnapshotOps, runner ArtifactBuildRunner, plan snapshotimage.Plan, in BuildArtifactInput) error {
	if err := CheckSource(ctx, ops, plan.SourceSHA); err != nil {
		return err
	}

	planned, err := plan.Artifact(in.Name)
	if err != nil {
		return err
	}

	output := filepath.Join(cmp.Or(in.OutputDir, snapshotimage.DefaultProducerDir), planned.Name)
	if err := newOutput(output, plan); err != nil {
		return err
	}

	if in.FromArtifact == "" {
		if err := runArtifactBuild(ctx, ops, runner, plan, planned, in.Environment); err != nil {
			return err
		}
	}

	return prepareProducer(plan, planned, in.FromArtifact, output)
}

// runArtifactBuild runs the build CLI for one artifact with an isolated HOME
// and re-pins the source afterwards.
func runArtifactBuild(ctx context.Context, ops appversion.GenerateSnapshotOps, runner ArtifactBuildRunner, plan snapshotimage.Plan, planned pipeline.PlannedArtifact, environment []string) error {
	if runner == nil {
		return errBuildRunnerRequired
	}

	if planned.ProjectType == projecttype.NPM {
		tarballs, err := npmTarballs(planned.WorkingDirectory)
		if err != nil {
			return err
		}

		if len(tarballs) > 0 {
			return errPreexistingTarballs
		}
	}

	request, err := artifactBuildRequest(plan, planned, environment)
	if err != nil {
		return err
	}

	home, err := os.MkdirTemp("", "forge-ci-snapshot-build-home-")
	if err != nil {
		return err
	}

	defer func() { _ = os.RemoveAll(home) }()

	request.Env = append(request.Env, "HOME="+home)
	if err := runner.Run(ctx, request); err != nil {
		return err
	}

	return CheckSource(ctx, ops, plan.SourceSHA)
}

// BuildSBOM reports whether the build of the producer or file output called
// name writes its Build SBOM: what the build CLI is told with --build-sbom. A
// job that brings the generator itself asks this before the build.
func BuildSBOM(plan snapshotimage.Plan, name string) (bool, error) {
	planned, err := plan.Artifact(name)
	if err != nil {
		if planned, err = plan.File(name); err != nil {
			return false, err
		}
	}

	return buildsSBOM(planned), nil
}

func buildsSBOM(planned pipeline.PlannedArtifact) bool {
	return slices.Contains(planned.EffectiveSBOMs, config.SBOMLayerBuild)
}

func artifactBuildRequest(plan snapshotimage.Plan, planned pipeline.PlannedArtifact, parent []string) (ArtifactBuildRequest, error) {
	root, err := pathsafe.OpenRoot(planned.WorkingDirectory)
	if err != nil {
		return ArtifactBuildRequest{}, err
	}

	_ = root.Close()

	dir, err := filepath.Abs(planned.WorkingDirectory)
	if err != nil {
		return ArtifactBuildRequest{}, err
	}

	args := []string{"build", string(planned.ProjectType), "run", "--working-dir", ".", "--build-sbom=" + strconv.FormatBool(buildsSBOM(planned))}

	typeArgs, private, err := projectBuildArgs(plan, planned)
	if err != nil {
		return ArtifactBuildRequest{}, err
	}

	args = append(args, typeArgs...)

	if planned.ProjectType == projecttype.NPM || planned.ProjectType == projecttype.Maven || planned.ProjectType == projecttype.Gradle {
		args = append(args, "--private-dependencies="+strconv.FormatBool(private))
	}
	// An allowlist removes release/registry/signing credentials AND event-based
	// CLI policy overrides. Only explicitly requested read-only dependency auth
	// crosses this boundary; the child also applies its existing auth isolation.
	allowed := []string{"PATH", "TMPDIR", "TEMP", "TMP", "GOROOT", "GOTOOLCHAIN", "GOPATH", "GOPROXY", "GOSUMDB", "GOPRIVATE", "GONOSUMDB", "GONOPROXY", "GOCACHE", "GOMODCACHE", "RUSTUP_HOME", "CARGO_HOME", "CC", "CXX", "CGO_ENABLED", "SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS", "JAVA_HOME"}
	// Image-provisioned Mise shims need the installed-tool location and the
	// image's config selection together. Losing these when HOME is replaced
	// selects consumer config (and can attempt acquisition) instead of the
	// preprovisioned tools. Do not admit trust overrides, credentials, arbitrary
	// MISE_* or XDG config: only these nonsecret image runtime selectors.
	allowed = append(allowed, "MISE_DATA_DIR", "MISE_CONFIG_DIR", "MISE_OVERRIDE_CONFIG_FILENAMES")
	if private {
		allowed = append(allowed, "DEPENDENCY_TOKEN", "DEPENDENCY_USERNAME")
	}

	env := []string{}

	for _, entry := range parent {
		key, _, ok := strings.Cut(entry, "=")
		if ok && slices.Contains(allowed, key) {
			env = append(env, entry)
		}
	}

	env = append(env, "SOURCE_DATE_EPOCH="+plan.SourceDateEpoch, "FORGE_CI_VERSION="+plan.ArtifactVersion(), "VERSION="+plan.ArtifactVersion(), "FORGE_CI_COMMIT="+plan.SourceSHA)

	return ArtifactBuildRequest{Dir: dir, Args: args, Env: env}, nil
}

// projectBuildArgs returns the project-type-specific build CLI arguments and
// whether the build may use private dependency credentials.
func projectBuildArgs(plan snapshotimage.Plan, planned pipeline.PlannedArtifact) ([]string, bool, error) {
	switch planned.ProjectType {
	case projecttype.Go:
		goConfig := planned.Go
		if goConfig == nil {
			goConfig = &config.GoConfig{}
		}

		return []string{"--artifact-name", planned.Name, "--binary-name", goConfig.BinaryName, "--version", plan.ArtifactVersion(), "--ref-name", plan.SourceBranch, "--commit", plan.SourceSHA, "--platforms", cmp.Or(goConfig.Platforms, strings.Join(plan.ArtifactPlatforms(planned.Name), ",")), "--build-tags", goConfig.BuildTags, "--ldflags", goConfig.LDFlags, "--main-package", cmp.Or(goConfig.MainPackage, "."), "--skip-tests=" + strconv.FormatBool(goConfig.SkipTests)}, false, nil
	case projecttype.Cargo:
		cargoConfig := planned.Cargo
		if cargoConfig == nil {
			cargoConfig = &config.CargoConfig{}
		}

		return []string{"--artifact-name", planned.Name, "--binary-name", cargoConfig.BinaryName, "--version", plan.ArtifactVersion(), "--ref-name", plan.SourceBranch, "--commit", plan.SourceSHA, "--platforms", cmp.Or(cargoConfig.Platforms, strings.Join(plan.ArtifactPlatforms(planned.Name), ",")), "--skip-tests=" + strconv.FormatBool(cargoConfig.SkipTests)}, false, nil
	case projecttype.Maven:
		return mavenBuildArgs(planned)
	case projecttype.Gradle:
		gradleConfig := planned.Gradle
		if gradleConfig == nil {
			gradleConfig = &config.GradleConfig{}
		}

		return []string{"--tasks", cmp.Or(gradleConfig.GradleTasks, "build"), runTestsFlag, sbomToolVersionFlag, "3.2.1", "--java-version", gradleConfig.JavaVersion}, gradleConfig.PrivateDependencies, nil
	case projecttype.NPM:
		npmConfig := planned.NPM
		if npmConfig == nil {
			npmConfig = &config.NPMConfig{}
		}

		return []string{"--dependency-registry", npmConfig.DependencyRegistry, "--script", "build", runTestsFlag, sbomToolVersionFlag, "6.0.0", "--node-version", npmConfig.NodeVersion}, npmConfig.PrivateDependencies, nil
	default:
		return nil, false, errUnsupportedBuildType
	}
}

func mavenBuildArgs(planned pipeline.PlannedArtifact) ([]string, bool, error) {
	mavenConfig := planned.Maven
	if mavenConfig == nil {
		mavenConfig = &config.MavenConfig{}
	}

	settings := mavenConfig.SettingsPath
	if settings != "" {
		var err error

		settings, err = filepath.Abs(settings)
		if err != nil {
			return nil, false, err
		}
	}

	buildType := "app"
	if planned.BuildType == config.BuildTypeLibrary {
		buildType = "lib"
	}

	return []string{"--build-type", buildType, "--profile", cmp.Or(mavenConfig.MavenProfile, mavenConfig.Profile), "--settings-path", settings, "--cli-opts", "-B -ntp", runTestsFlag, sbomToolVersionFlag, "2.9.3", "--java-version", mavenConfig.JavaVersion}, mavenConfig.PrivateDependencies, nil
}
