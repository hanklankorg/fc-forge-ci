// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package kaniko builds container images with the kaniko executor, for a
// runner pool whose job containers may not create user namespaces: rootless
// Buildah cannot run there, kaniko can. It runs the Containerfile's steps as
// the job container's own root, in the job container's own filesystem, and
// snapshots the difference. Nothing privileged, no daemon, no mounts.
//
// That design is also its limits, and this adapter states them instead of
// working around them:
//
//   - It has no local image store. Every build ends in an OCI layout, which the
//     ggcr adapter pushes by digest as it does for Buildah; `--mode load` is
//     refused. `--mode local` builds the target to a layout in this process's
//     own directory and extracts the image's flattened filesystem into
//     --output-dir through internal/archive, which refuses what Buildah's
//     export would write but a filesystem export must not: absolute or
//     escaping symlinks, hardlinks, devices and other special files.
//   - It replaces the job container's root filesystem with the image's, and
//     with --cleanup, which every run passes, deletes it after the build, so
//     a second build in the same job starts from its own base image, not from
//     the first one's filesystem. Only mounts and the ignored paths survive:
//     the kaniko job image keeps what later steps need under /kaniko, which
//     kaniko never touches, and this adapter protects the layout, the context
//     and the registry credentials with --ignore-path. /tmp does not
//     survive, so the kaniko job image sets TMPDIR to /kaniko/tmp, which
//     kaniko leaves alone, for a job that builds more than once.
//   - Build secrets are refused: kaniko keeps them recoverable by later RUN
//     steps of the same build.
//   - A verified parent is not imported into a store but copied into kaniko's
//     base-image cache, which the next build reads offline instead of the
//     registry (LoadRegistryInput).
//
// The multiarch build-arch tool expects a build into a local tag followed by an
// export. Here the build is recorded under that tag and performed when the
// export names its layout, so one kaniko run produces the layout directly.
package kaniko

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/diggsweden/forge-ci/internal/archive"
	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
	"github.com/diggsweden/forge-ci/internal/safeexec"
)

// DefaultExecutor is where the kaniko image, and the kaniko job image built
// from it, install the executor.
const DefaultExecutor = "/kaniko/executor"

// Log markers the live image-plan oracle counts to tell a real build from a
// reused architecture cache. Referencing them here keeps the oracle and the
// producer in one place: a wording change fails to compile instead of
// failing a lab run.
const (
	// LayoutBuildNoticeSuffix follows the local image name when a build is
	// staged until its layout is exported.
	LayoutBuildNoticeSuffix = " builds when its layout is exported"
	// ExecutionNotice is printed once per executed kaniko build.
	ExecutionNotice = "kaniko replaces this container's root filesystem with the image's and deletes it after the build; mounts, the layout, the context and /kaniko are kept."
)

// flagIgnorePath keeps a path through kaniko's replacement of the root
// filesystem.
const flagIgnorePath = "--ignore-path"

// Adapter wraps the kaniko executor. Executor is overridable for tests and
// for an installation that places the binary elsewhere.
type Adapter struct {
	Executor string
	// Filesystem flattens a built layout for --mode local.
	Filesystem LayoutFilesystem

	// euid is the effective uid check; tests substitute it.
	euid func() int
	// staged holds build-arch requests between BuildSignerImage and the
	// export that gives them their layout directory.
	staged map[string]container.SignerImageBuildToolRequest
	// baseCache holds the verified parents LoadRegistryInput copied for the
	// next build, which consumes and removes it.
	baseCache string
}

// LayoutFilesystem reads the effective filesystem of a verified OCI layout as
// one tar stream, layers applied in order and whiteouts honoured.
type LayoutFilesystem interface {
	ExportLayoutFilesystem(ctx context.Context, directory string) (io.ReadCloser, error)
}

// build is what one kaniko run needs, from either entry point.
type build struct {
	AuthFile      string
	Context       string
	Containerfile string
	Target        string
	Platform      string
	BuildArgs     []string
	Labels        []string
	Reproducible  bool
	CacheRepo     string
	CachePush     bool
	// BaseCache is the base-image cache of verified parents, or empty.
	BaseCache string
}

// Build exports a stage's filesystem (--mode local) and refuses the load mode,
// which needs a local image store.
func (a *Adapter) Build(ctx context.Context, req container.BuildRequest, out io.Writer) error {
	if err := req.Validate(); err != nil {
		return err
	}

	if req.Mode != container.BuildModeLocal {
		return fmt.Errorf("kaniko has no local image store; --mode %s needs Buildah's, select --image-builder buildah: %w", req.Mode, errs.ErrUsage)
	}

	return a.exportFilesystem(ctx, req, out)
}

// BuildToLayout builds the image into layoutDir, ready for the ggcr digest push.
func (a *Adapter) BuildToLayout(ctx context.Context, req container.BuildRequest, layoutDir string, out io.Writer) error {
	if err := req.Validate(); err != nil {
		return err
	}

	if len(req.Secrets) > 0 {
		return fmt.Errorf("build secrets are not supported with the kaniko builder: kaniko keeps a secret recoverable by later RUN steps of the build; select --image-builder buildah: %w", errs.ErrUsage)
	}

	return a.run(ctx, build{
		AuthFile:      req.AuthFile,
		Context:       req.Context,
		Containerfile: req.Containerfile,
		Target:        req.Target,
		Platform:      req.Platform,
		BuildArgs:     req.BuildArgs,
		Labels:        req.Labels,
		Reproducible:  req.SourceDateEpoch != "",
		CacheRepo:     req.CacheRepo,
		CachePush:     req.CachePush,
	}, layoutDir, out)
}

// BuildSignerImage records one architecture build under its local tag. There
// is no local store to build into; ExportLocalImageToLayout runs the build
// when it names the layout directory.
func (a *Adapter) BuildSignerImage(_ context.Context, req container.SignerImageBuildToolRequest, out io.Writer) error {
	if req.LocalImage == "" {
		return fmt.Errorf("kaniko build-arch needs the local tag as the handle of its staged build: %w", errs.ErrUsage)
	}

	if err := a.preflight(); err != nil {
		return err
	}

	if a.staged == nil {
		a.staged = make(map[string]container.SignerImageBuildToolRequest)
	}

	a.staged[req.LocalImage] = req
	_, _ = fmt.Fprintf(out, "kaniko: %s"+LayoutBuildNoticeSuffix+"\n", req.LocalImage)

	return nil
}

// ExportLocalImageToLayout performs the build recorded under imageRef, straight
// into layoutDir.
func (a *Adapter) ExportLocalImageToLayout(ctx context.Context, imageRef, layoutDir string, out io.Writer) error {
	req, ok := a.staged[imageRef]
	if !ok {
		return fmt.Errorf("no kaniko build is staged under %s: %w", imageRef, errs.ErrUsage)
	}

	delete(a.staged, imageRef)

	labels := []string{
		"org.opencontainers.image.source=" + req.SourceURL,
		"org.opencontainers.image.revision=" + req.Revision,
	}
	if req.Title != "" {
		labels = append(labels, "org.opencontainers.image.title="+req.Title)
	}

	labels = append(labels, req.Labels...)

	return a.run(ctx, build{
		AuthFile:      req.AuthFile,
		Context:       req.Context,
		Containerfile: req.Containerfile,
		Target:        req.Target,
		Platform:      req.Platform,
		BuildArgs:     req.BuildArgs,
		Labels:        labels,
		Reproducible:  true,
	}, layoutDir, out)
}

// LoadRegistryInput makes the verified OCI layout in directory the base image
// a FROM pinned to digest starts from. Kaniko looks such a FROM up in its
// base-image cache before any registry, as <cache-dir>/<digest>: an OCI layout
// holding the one image, the format `kaniko warmer` writes. The layout is
// copied there, so the build reads this process's own copy, and the returned
// reference names it; no registry is contacted and no credential is read.
// A parent that cannot be loaded fails its build, and the parents loaded for
// it are discarded.
func (a *Adapter) LoadRegistryInput(_ context.Context, directory, digest string, _ io.Writer) (_ string, err error) {
	if !container.ValidDigest(digest) {
		return "", fmt.Errorf("registry input digest %q is not a sha256 digest: %w", digest, errs.ErrValidation)
	}

	defer func() {
		if err != nil {
			a.DiscardRegistryInputs()
		}
	}()

	if a.baseCache == "" {
		// Under TMPDIR, which the kaniko job image keeps under /kaniko.
		var dir string
		if dir, err = os.MkdirTemp("", "forge-ci-kaniko-base-*"); err != nil {
			return "", fmt.Errorf("create the kaniko base-image cache: %w", err)
		}

		a.baseCache = dir
		if a.baseCache, err = filepath.Abs(dir); err != nil {
			_ = os.RemoveAll(dir)

			return "", fmt.Errorf("resolve the kaniko base-image cache: %w", err)
		}
	}

	// The cache is this process's own: an entry is the copy of a parent with
	// that digest, loaded already.
	entry := filepath.Join(a.baseCache, digest)

	_, err = os.Lstat(entry)

	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		if err = copyLayout(directory, entry); err != nil {
			return "", fmt.Errorf("copy the verified parent into the kaniko base-image cache: %w", err)
		}
	default:
		return "", fmt.Errorf("inspect the kaniko base-image cache: %w", err)
	}

	return container.RegistryInputRepository + "@" + digest, nil
}

// DiscardRegistryInputs removes the parents loaded for the next build. A
// build discards its own when it ends; a caller discards those of a build
// that never ran.
func (a *Adapter) DiscardRegistryInputs() {
	if a.baseCache != "" {
		_ = os.RemoveAll(a.baseCache)
		a.baseCache = ""
	}
}

// exportFilesystem builds the target into a private layout, which the build
// keeps through --ignore-path, then extracts its flattened filesystem into
// req.OutputDir. Image layers are untrusted: the extraction never writes
// outside the output directory and installs nothing until every entry passed.
func (a *Adapter) exportFilesystem(ctx context.Context, req container.BuildRequest, out io.Writer) error {
	if a.Filesystem == nil {
		return fmt.Errorf("kaniko --mode local needs a layout filesystem reader: %w", errs.ErrPrerequisite)
	}

	// Resolved before kaniko runs: the job's root filesystem is replaced
	// during the build, a path named now still means the same place after.
	outputDir, err := filepath.Abs(req.OutputDir)
	if err != nil {
		return fmt.Errorf("resolve output directory: %w", err)
	}

	layoutDir, err := os.MkdirTemp("", "forge-ci-kaniko-export-*")
	if err != nil {
		return fmt.Errorf("create oci layout dir: %w", err)
	}

	defer func() { _ = os.RemoveAll(layoutDir) }()

	if err := a.BuildToLayout(ctx, req, layoutDir, out); err != nil {
		return err
	}

	stream, err := a.Filesystem.ExportLayoutFilesystem(ctx, layoutDir)
	if err != nil {
		return fmt.Errorf("read the built filesystem: %w", err)
	}

	defer func() { _ = stream.Close() }()

	if err := archive.ExtractTar(stream, outputDir); err != nil {
		return fmt.Errorf("export the built filesystem to %s: %w", outputDir, err)
	}

	return nil
}

func (a *Adapter) executor() string {
	if a.Executor != "" {
		return a.Executor
	}

	return DefaultExecutor
}

func (a *Adapter) effectiveUID() int {
	if a.euid != nil {
		return a.euid()
	}

	return os.Geteuid()
}

// preflight names the two things the pool must give kaniko before anything is
// extracted over the filesystem.
func (a *Adapter) preflight() error {
	info, err := os.Stat(a.executor())
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return fmt.Errorf("kaniko executor is not at %s: run this job in the kaniko job image (containers/kaniko-job), which CI_IMAGE_BUILDER_JOB_IMAGE names, or set --kaniko-executor: %w", a.executor(), errs.ErrPrerequisite)
	}

	if a.effectiveUID() != 0 {
		return fmt.Errorf("kaniko runs the Containerfile's steps as this container's root and needs uid 0, got %d: %w", a.effectiveUID(), errs.ErrUsage)
	}

	return nil
}

func (a *Adapter) run(ctx context.Context, spec build, layoutDir string, out io.Writer) error {
	// The loaded parents are this build's: the next starts without them,
	// whether this one fails or not.
	spec.BaseCache = a.baseCache
	defer a.DiscardRegistryInputs()

	if err := a.preflight(); err != nil {
		return err
	}

	args, cleanup, env, err := a.arguments(spec, layoutDir)
	defer cleanup()

	if err != nil {
		return err
	}

	// The push after this build runs in this process, after kaniko has
	// replaced /etc/ssl. Go loads the system roots once; load them now.
	_, _ = x509.SystemCertPool()

	_, _ = fmt.Fprintln(out, ExecutionNotice)

	// One writer for both streams keeps them in the order kaniko wrote them.
	// What it said is kept beside the log: a registry kaniko could not reach
	// ends the build as an unavailable dependency (errs.KanikoReport).
	var said bytes.Buffer

	heard := io.MultiWriter(out, &said)

	cmd := safeexec.Command(ctx, a.executor(), args...)
	cmd.Stdout = heard
	cmd.Stderr = heard

	cmd.Env = append(safeexec.ImageBuilderEnv(os.Environ()), env...)

	err = cmd.Run()
	if err == nil {
		return nil
	}

	if _, ok := errors.AsType[*exec.ExitError](err); ok {
		report := errs.KanikoReport(said.String())
		if spec.BaseCache != "" && asksForRegistryInput(report) {
			// No registry serves the loaded parent: asking again finds the
			// same cache without it.
			return fmt.Errorf("kaniko build: %w: the verified parent was not found in the base-image cache %s, and kaniko asked a registry for it: %s: %w",
				err, spec.BaseCache, strings.TrimSpace(report), errs.ErrPrerequisite)
		}

		return safeexec.ExitFailure(err, "kaniko", "build", report)
	}

	return safeexec.WrapError(err, "kaniko", "build")
}

// arguments assembles the executor's argv for one layout build, with the
// registry credentials and a .containerignore staged where kaniko reads them.
// Every path kaniko must not destroy is named absolutely and ignored. The
// cleanup removes what was staged, also when an error is returned.
func (a *Adapter) arguments(spec build, layoutDir string) ([]string, func(), []string, error) {
	var staged []string

	cleanup := func() {
		for _, dir := range staged {
			_ = os.RemoveAll(dir)
		}
	}

	containerfile, stageDir, err := stageIgnoreFile(spec)
	if err != nil {
		return nil, cleanup, nil, err
	}

	if stageDir != "" {
		staged = append(staged, stageDir)
	}

	args, err := layoutArguments(spec, containerfile, layoutDir)
	if err != nil {
		return nil, cleanup, nil, err
	}

	if stageDir != "" {
		args = append(args, flagIgnorePath, stageDir)
	}

	args = append(args, imageArguments(spec)...)

	var env []string

	if spec.BaseCache != "" {
		args = append(args, baseCacheArguments(spec.BaseCache)...)
		// --reproducible would rewrite the parent's layers as well; the child
		// carries the verified parent's layers byte for byte, as with Buildah.
		env = append(env, "FF_KANIKO_REPRODUCIBLE_PRESERVE_BASE_LAYERS=1")
	}

	if spec.AuthFile == "" {
		return args, cleanup, env, nil
	}

	configDir, err := stageDockerConfig(spec.AuthFile)
	if err != nil {
		return nil, cleanup, nil, err
	}

	staged = append(staged, configDir)
	args = append(args, flagIgnorePath, configDir)

	return args, cleanup, append(env, "DOCKER_CONFIG="+configDir), nil
}

// baseCacheArguments point kaniko at the verified parents in dir. Kaniko
// looks a FROM up in its base-image cache only with --cache=true, which
// without a cache repository needs --no-push-cache. Its layer cache stays
// off: no RUN, WORKDIR, COPY or ADD layer is looked up or stored.
func baseCacheArguments(dir string) []string {
	return []string{"--cache-dir", dir, flagIgnorePath, dir, "--cache=true", "--no-push-cache", "--cache-run-layers=false", "--cache-copy-layers=false"}
}

// asksForRegistryInput reports whether kaniko's report names the registry
// of container.RegistryInputRepository, which serves nothing: kaniko asks it
// for a loaded parent it did not find in its base-image cache, and reports
// the address it could not reach, as "https://localhost/v2/".
func asksForRegistryInput(report string) bool {
	host, _, _ := strings.Cut(container.RegistryInputRepository, "/")

	return strings.Contains(report, "//"+host+"/")
}

// copyLayout copies the directories and regular files of the layout in
// source to the new directory destination, and refuses anything else.
func copyLayout(source, destination string) error {
	root, err := pathsafe.OpenRoot(source)
	if err != nil {
		return err
	}

	defer func() { _ = root.Close() }()

	return fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		target := filepath.Join(destination, filepath.FromSlash(name))

		switch {
		case entry.IsDir():
			return os.Mkdir(target, 0o700)
		case entry.Type().IsRegular():
			return copyLayoutFile(root, name, target)
		default:
			return fmt.Errorf("layout entry %s is not a regular file: %w", name, errs.ErrValidation)
		}
	})
}

func copyLayoutFile(root *os.Root, name, target string) error {
	in, err := root.Open(name)
	if err != nil {
		return err
	}

	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // target is under this process's own cache directory.
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()

		return err
	}

	return out.Close()
}

// layoutArguments names where the build reads and writes, absolutely, and
// keeps those paths out of kaniko's way.
func layoutArguments(spec build, containerfile, layoutDir string) ([]string, error) {
	contextDir, err := filepath.Abs(spec.Context)
	if err != nil {
		return nil, fmt.Errorf("resolve build context: %w", err)
	}

	layout, err := filepath.Abs(layoutDir)
	if err != nil {
		return nil, fmt.Errorf("resolve layout directory: %w", err)
	}

	args := []string{
		"--context", "dir://" + contextDir,
		"--no-push",
		// The next build of this job must not start on this one's root
		// filesystem: delete it once the layout is written.
		"--cleanup",
		// Content changes with unchanged mtime/size must still reach the image.
		"--snapshot-mode=full",
		// A base image is fetched again when the registry cannot be reached.
		"--image-download-retry", strconv.Itoa(container.RegistryRetries),
		"--oci-layout-path", layout,
		flagIgnorePath, layout,
		flagIgnorePath, contextDir,
	}

	if containerfile != "" {
		containerfile, err := filepath.Abs(containerfile)
		if err != nil {
			return nil, fmt.Errorf("resolve containerfile: %w", err)
		}

		args = append(args, "--dockerfile", containerfile)
	}

	return args, nil
}

// stageIgnoreFile returns the Containerfile to give kaniko so that it reads the
// ignore file container.IgnoreFile selects, and the directory it staged, if
// any, for removal after the build.
//
// Kaniko reads <dockerfile>.dockerignore, else the context's .dockerignore,
// and has no flag naming another file (DockerignorePath in kaniko's
// pkg/util/fs_util.go; --ignore-path is about snapshots). It knows no
// .containerignore. That covers the .dockerignore half of the order unaided;
// for a .containerignore, the Containerfile is copied into a directory of this
// process's own beside that file under the <dockerfile>.dockerignore name
// kaniko reads first. The checkout is left as it is.
func stageIgnoreFile(spec build) (string, string, error) {
	containerfile := spec.Containerfile
	if containerfile == "" {
		// Kaniko's own default, which then becomes explicit.
		containerfile = filepath.Join(spec.Context, "Dockerfile")
	}

	ignoreFile := container.IgnoreFile(containerfile, spec.Context)
	if !strings.HasSuffix(ignoreFile, ".containerignore") {
		return spec.Containerfile, "", nil
	}

	dir, err := os.MkdirTemp("", "forge-ci-kaniko-containerfile-*")
	if err != nil {
		return "", "", fmt.Errorf("stage the containerfile for kaniko: %w", err)
	}

	staged := filepath.Join(dir, filepath.Base(containerfile))
	for src, dst := range map[string]string{containerfile: staged, ignoreFile: staged + ".dockerignore"} {
		if err := copyFile(src, dst); err != nil {
			_ = os.RemoveAll(dir)

			return "", "", fmt.Errorf("stage the containerfile for kaniko: %w", err)
		}
	}

	return staged, dir, nil
}

func copyFile(src, dst string) error {
	body, err := os.ReadFile(src) //nolint:gosec // the build's own Containerfile and ignore file.
	if err != nil {
		return err
	}

	return os.WriteFile(dst, body, 0o600) //nolint:gosec // dst is in this process's own temp directory.
}

// imageArguments shapes the image: platform, stage, arguments, labels,
// timestamps and the layer cache.
func imageArguments(spec build) []string {
	var args []string

	if spec.Platform != "" {
		args = append(args, "--custom-platform", spec.Platform)
	}

	if spec.Target != "" {
		args = append(args, "--target", spec.Target)
	}

	for _, buildArg := range spec.BuildArgs {
		args = append(args, "--build-arg", buildArg)
	}

	for _, label := range spec.Labels {
		args = append(args, "--label", label)
	}

	// Kaniko has no epoch to set; it zeroes the timestamps. The digest differs
	// from a Buildah build of the same tree, and matches itself.
	if spec.Reproducible {
		args = append(args, "--reproducible")
	}

	if spec.CacheRepo != "" {
		// Kaniko recommends this for lower cache-compression memory pressure.
		// Keep full content snapshots: redo/time can miss same-metadata edits.
		args = append(args, "--compressed-caching=false", "--cache=true", "--cache-repo", spec.CacheRepo)
		if !spec.CachePush {
			args = append(args, "--no-push-cache")
		}
	}

	return args
}

// stageDockerConfig copies the layer's auth file, the {"auths":…} document
// buildah, podman and skopeo share, to the config.json kaniko reads from
// $DOCKER_CONFIG. The directory is the process's own and is removed after the
// build.
func stageDockerConfig(authFile string) (string, error) {
	body, err := os.ReadFile(authFile) //nolint:gosec // the caller's explicit credentials file.
	if err != nil {
		return "", fmt.Errorf("read registry auth file: %w", err)
	}

	if !strings.Contains(string(body), `"auths"`) {
		return "", fmt.Errorf("registry auth file %s is not an {\"auths\":…} document: %w", authFile, errs.ErrInvalidConfig)
	}

	dir, err := os.MkdirTemp("", "forge-ci-kaniko-docker-*")
	if err != nil {
		return "", fmt.Errorf("stage registry credentials for kaniko: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "config.json"), body, 0o600); err != nil { //nolint:gosec // dir is this process's own temp directory.
		_ = os.RemoveAll(dir)

		return "", fmt.Errorf("stage registry credentials for kaniko: %w", err)
	}

	return dir, nil
}
