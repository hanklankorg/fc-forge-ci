// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/diggsweden/forge-ci/internal/archive"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	domainpublish "github.com/diggsweden/forge-ci/internal/domain/publish"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// NPMPublishOps is the streaming npm-adapter surface the forge-packages npm
// publish needs (distinct from the capturing NPMOps used by the validators).
type NPMPublishOps interface {
	RunInherit(ctx context.Context, dir string, stdout, stderr io.Writer, args ...string) error
}

// ForgePackagesNPMPublishInput drives ForgePackagesNPMPublish.
type ForgePackagesNPMPublishInput struct {
	// WorkingDir is the directory holding the packed *.tgz tarball.
	WorkingDir string
	// Tag is required: an explicit release-<version> or release-{version}.
	Tag string
}

// ForgePackagesNPMPublish is the npm sibling of ForgePackagesDeploy: it
// publishes the packed tarball to the active forge's npm registry (GitHub
// Packages, the GitLab npm registry, or Forgejo packages). The per-forge
// registry + token come from the provider role; this use case writes a
// credentialed .npmrc to a 0600 temp file (token never on argv) and runs
// `npm publish <tarball> --userconfig <npmrc>`.
func ForgePackagesNPMPublish(ctx context.Context, ops NPMPublishOps, resolver provider.ForgeNPMRegistryResolver, stdout, stderr io.Writer, in ForgePackagesNPMPublishInput) error {
	if err := domainpublish.ValidateNPMTag(in.Tag); err != nil {
		return err
	}

	dir := in.WorkingDir
	if dir == "" {
		dir = "."
	}

	tarball, err := findSingleTarball(dir)
	if err != nil {
		return err
	}

	meta, tag, err := prepareNPMPublication(tarball, in.Tag, stderr)
	if err != nil {
		return err
	}

	reg, err := resolver.ResolveForgeNPMRegistry()
	if err != nil {
		return err
	}

	if meta.PublishConfig.Registry != "" && strings.TrimRight(meta.PublishConfig.Registry, "/") != strings.TrimRight(reg.Registry, "/") {
		return fmt.Errorf("packed publishConfig.registry conflicts with the selected registry: %w", errs.ErrInvalidConfig)
	}

	npmrcPath, cleanup, err := writeTempSecretFile("forge-ci-npmrc-*", reg.RenderNPMRC())
	if err != nil {
		return err
	}

	defer cleanup()

	if err := checkNPMTagBinding(ctx, ops, dir, userConfig{npmrcPath, reg.Registry}, meta, tag); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stdout, "Publishing %s@%s with tag %s to %s\n", meta.Name, meta.Version, tag, reg.Registry)

	// "./<name>", not the dir-joined path findSingleTarball returned, and not a
	// bare name either. npm runs with dir as its working directory, so the joined
	// path resolved to dir/dir/<name> and did not exist — at which point npm stops
	// treating the argument as a file and parses it as a package *spec*, where
	// "pkg/thing.tgz" is GitHub shorthand for <user>/<repo>. So `publish
	// forge-packages deploy --project-type npm` ended up running
	//
	//	git ls-remote ssh://git@github.com/pkg/<name>.tgz.git
	//
	// which fails for everyone, and reaches out to github.com from a job that
	// asked to publish to its own forge. The leading "./" is what makes npm treat
	// it as a path unconditionally, so no filename can ever be read as a spec.
	return ops.RunInherit(ctx, dir, stdout, stderr, "publish", "./"+filepath.Base(tarball), "--userconfig", npmrcPath, "--registry", reg.Registry, "--tag", tag, "--ignore-scripts")
}

func prepareNPMPublication(tarball, setting string, stderr io.Writer) (npmPackedMetadata, string, error) {
	meta, err := packedNPMMetadata(tarball)
	if err != nil {
		return meta, "", err
	}

	tag, err := domainpublish.NPMVersionTag(meta.Version, setting)
	if err != nil {
		return meta, "", err
	}

	canonical, err := domainpublish.NPMRegistryVersion(meta.Version)
	if err != nil {
		return meta, "", err
	}

	if canonical != meta.Version {
		_, _ = fmt.Fprintf(stderr, "Warning: npm removes build metadata from packed version %s; publication identity is %s and tag %s\n", meta.Version, canonical, tag)
		meta.Version = canonical
	}

	if meta.PublishConfig.Tag != "" && meta.PublishConfig.Tag != tag {
		return meta, "", fmt.Errorf("packed publishConfig.tag conflicts with required version-specific tag %q: %w", tag, errs.ErrInvalidConfig)
	}

	return meta, tag, nil
}

type npmPackedMetadata struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	PublishConfig struct {
		Tag      string `json:"tag"`
		Registry string `json:"registry"`
	} `json:"publishConfig"`
}

//nolint:gochecknoglobals // immutable package-spec grammar, not a registry query expression.
var npmPackageNamePattern = regexp.MustCompile(`^(?:@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$`)

func packedNPMMetadata(tarball string) (npmPackedMetadata, error) {
	var meta npmPackedMetadata

	body, err := archive.ReadTarGzipMember(tarball, "package/package.json", 1<<20)
	if err != nil {
		return meta, err
	}

	if err := json.Unmarshal(body, &meta); err != nil {
		return meta, fmt.Errorf("packed package.json is invalid: %w", errs.ErrMalformedInput)
	}

	if len(meta.Name) > 214 || !npmPackageNamePattern.MatchString(meta.Name) {
		return meta, fmt.Errorf("packed npm package name is invalid: %w", errs.ErrInvalidConfig)
	}

	return meta, nil
}

type userConfig struct{ path, registry string }

func checkNPMTagBinding(ctx context.Context, ops NPMPublishOps, dir string, config userConfig, meta npmPackedMetadata, tag string) error {
	var stdout, stderr npmRegistryOutput

	// Read package-wide bindings: view implicitly selects latest, which need not
	// exist. Selecting the proposed version could hide a conflicting tag on 404.
	err := ops.RunInherit(ctx, dir, &stdout, &stderr, "dist-tag", "ls", meta.Name, "--userconfig", config.path, "--registry", config.registry)
	if err != nil {
		if isNPMNotFound(stderr.String()) || npmHasNoDistTags(stderr.String(), meta.Name) {
			return nil
		}

		return fmt.Errorf("read npm distribution tags before publishing: %w", err)
	}

	tags := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		name, version, ok := strings.Cut(line, ": ")
		_, duplicate := tags[name]
		_, versionErr := domainpublish.NPMRegistryVersion(version)
		if !ok || name == "" || strings.ContainsAny(name, " \t\r:") || duplicate || versionErr != nil {
			return fmt.Errorf("npm registry returned invalid distribution tags: %w", errs.ErrMalformedInput)
		}
		tags[name] = version
	}

	if version, exists := tags[tag]; exists && version != meta.Version {
		return fmt.Errorf("npm tag %q already names version %q; refusing to move it to %q: %w", tag, version, meta.Version, errs.ErrValidation)
	}

	return nil
}

// Some registries return an empty tag map for a new package rather than 404.
// npm reports that specific successful registry response as a command error.
func npmHasNoDistTags(stderr, name string) bool {
	for _, line := range strings.Split(stderr, "\n") {
		if strings.TrimSpace(line) == "npm error No dist-tags found for "+name {
			return true
		}
	}
	return false
}

type npmRegistryOutput struct{ buffer bytes.Buffer }

func (b *npmRegistryOutput) Write(p []byte) (int, error) {
	if len(p) > (1<<20)-b.buffer.Len() {
		return 0, fmt.Errorf("npm registry response exceeds 1 MiB: %w", errs.ErrMalformedInput)
	}

	return b.buffer.Write(p)
}

func (b *npmRegistryOutput) Bytes() []byte  { return b.buffer.Bytes() }
func (b *npmRegistryOutput) String() string { return b.buffer.String() }

// findSingleTarball returns the single *.tgz in dir (the npm-pack output),
// erroring when none or more than one is present so the publish target is
// unambiguous.
func findSingleTarball(dir string) (string, error) {
	root, err := pathsafe.OpenRoot(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("npm package directory is missing: %w", errs.ErrMissingInput)
	}

	if err != nil {
		return "", err
	}

	defer func() { _ = root.Close() }()

	matches, err := fs.Glob(root.FS(), "*.tgz")
	if err != nil {
		return "", fmt.Errorf("scan %q for npm tarball: %w", dir, err)
	}

	switch len(matches) {
	case 1:
		info, statErr := root.Lstat(matches[0])
		if statErr != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("npm tarball must be a confined regular file: %w", errs.ErrValidation)
		}

		return filepath.Join(dir, matches[0]), nil
	case 0:
		return "", fmt.Errorf("no *.tgz tarball in %q (run `npm pack` first): %w", dir, errs.ErrMissingInput)
	default:
		return "", fmt.Errorf("%d *.tgz tarballs in %q (expected exactly one): %w", len(matches), dir, errs.ErrValidation)
	}
}
