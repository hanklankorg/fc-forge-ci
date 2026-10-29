// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage

import (
	"cmp"
	"context"
	"fmt"
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

// ArtifactBuildRunner invokes this engine's existing build command, never a
// shell or a consumer-supplied executable. Providers only supply toolchains.
type ArtifactBuildRunner interface {
	Run(context.Context, ArtifactBuildRequest) error
}
type ArtifactBuildRequest struct {
	Dir       string
	Args, Env []string
}
type BuildArtifactInput struct {
	Name, OutputDir, FromArtifact string
	Environment                   []string
}

func BuildArtifact(ctx context.Context, ops appversion.GenerateSnapshotOps, runner ArtifactBuildRunner, p snapshotimage.Plan, in BuildArtifactInput) error {
	if err := CheckSource(ctx, ops, p.SourceSHA); err != nil {
		return err
	}
	a, err := p.Artifact(in.Name)
	if err != nil {
		return err
	}
	output := filepath.Join(cmp.Or(in.OutputDir, snapshotimage.DefaultProducerDir), a.Name)
	if err := newOutput(output, p); err != nil {
		return err
	}
	if in.FromArtifact == "" {
		if runner == nil {
			return fmt.Errorf("snapshot artifact build requires the existing build CLI runner")
		}
		if a.ProjectType == projecttype.NPM {
			tarballs, err := npmTarballs(a.WorkingDirectory)
			if err != nil {
				return err
			}
			if len(tarballs) > 0 {
				return fmt.Errorf("snapshot npm producer refuses preexisting tarballs")
			}
		}
		request, err := artifactBuildRequest(p, a, in.Environment)
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
		if err := CheckSource(ctx, ops, p.SourceSHA); err != nil {
			return err
		}
	}
	return prepareProducer(p, a, in.FromArtifact, output)
}

func artifactBuildRequest(p snapshotimage.Plan, a pipeline.PlannedArtifact, parent []string) (ArtifactBuildRequest, error) {
	root, err := pathsafe.OpenRoot(a.WorkingDirectory)
	if err != nil {
		return ArtifactBuildRequest{}, err
	}
	_ = root.Close()
	dir, err := filepath.Abs(a.WorkingDirectory)
	if err != nil {
		return ArtifactBuildRequest{}, err
	}
	args := []string{"build", string(a.ProjectType), "run", "--working-dir", ".", "--build-sbom=" + strconv.FormatBool(slices.Contains(a.EffectiveSBOMs, config.SBOMLayerBuild))}
	private := false
	switch a.ProjectType {
	case projecttype.Go:
		c := a.Go
		if c == nil {
			c = &config.GoConfig{}
		}
		args = append(args, "--artifact-name", a.Name, "--binary-name", c.BinaryName, "--version", p.ArtifactVersion(), "--ref-name", p.SourceBranch, "--commit", p.SourceSHA, "--platforms", cmp.Or(c.Platforms, strings.Join(p.ArtifactPlatforms(a.Name), ",")), "--build-tags", c.BuildTags, "--ldflags", c.LDFlags, "--main-package", cmp.Or(c.MainPackage, "."), "--skip-tests="+strconv.FormatBool(c.SkipTests))
	case projecttype.Cargo:
		c := a.Cargo
		if c == nil {
			c = &config.CargoConfig{}
		}
		args = append(args, "--artifact-name", a.Name, "--binary-name", c.BinaryName, "--version", p.ArtifactVersion(), "--ref-name", p.SourceBranch, "--commit", p.SourceSHA, "--platforms", cmp.Or(c.Platforms, strings.Join(p.ArtifactPlatforms(a.Name), ",")), "--skip-tests="+strconv.FormatBool(c.SkipTests))
	case projecttype.Maven:
		c := a.Maven
		if c == nil {
			c = &config.MavenConfig{}
		}
		private = c.PrivateDependencies
		settings := c.SettingsPath
		if settings != "" {
			settings, err = filepath.Abs(settings)
			if err != nil {
				return ArtifactBuildRequest{}, err
			}
		}
		buildType := "app"
		if a.BuildType == config.BuildTypeLibrary {
			buildType = "lib"
		}
		args = append(args, "--build-type", buildType, "--profile", cmp.Or(c.MavenProfile, c.Profile), "--settings-path", settings, "--cli-opts", "-B -ntp", "--skip-tests=false", "--sbom-tool-version", "2.9.3", "--java-version", c.JavaVersion)
	case projecttype.Gradle:
		c := a.Gradle
		if c == nil {
			c = &config.GradleConfig{}
		}
		private = c.PrivateDependencies
		args = append(args, "--tasks", cmp.Or(c.GradleTasks, "build"), "--skip-tests=false", "--sbom-tool-version", "3.2.1", "--java-version", c.JavaVersion)
	case projecttype.NPM:
		c := a.NPM
		if c == nil {
			c = &config.NPMConfig{}
		}
		private = c.PrivateDependencies
		args = append(args, "--dependency-registry", c.DependencyRegistry, "--script", "build", "--skip-tests=false", "--sbom-tool-version", "6.0.0", "--node-version", c.NodeVersion)
	default:
		return ArtifactBuildRequest{}, fmt.Errorf("unsupported snapshot build type")
	}
	if a.ProjectType == projecttype.NPM || a.ProjectType == projecttype.Maven || a.ProjectType == projecttype.Gradle {
		args = append(args, "--private-dependencies="+strconv.FormatBool(private))
	}
	// An allowlist removes release/registry/signing credentials AND event-based
	// CLI policy overrides. Only explicitly requested read-only dependency auth
	// crosses this boundary; the child also applies its existing auth isolation.
	allowed := []string{"PATH", "TMPDIR", "TEMP", "TMP", "GOROOT", "GOTOOLCHAIN", "GOPATH", "GOPROXY", "GOSUMDB", "GOPRIVATE", "GONOSUMDB", "GONOPROXY", "GOCACHE", "GOMODCACHE", "RUSTUP_HOME", "CARGO_HOME", "CC", "CXX", "CGO_ENABLED", "SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS", "JAVA_HOME"}
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
	env = append(env, "SOURCE_DATE_EPOCH="+p.SourceDateEpoch, "FORGE_CI_VERSION="+p.ArtifactVersion(), "VERSION="+p.ArtifactVersion(), "FORGE_CI_COMMIT="+p.SourceSHA)
	return ArtifactBuildRequest{Dir: dir, Args: args, Env: env}, nil
}
