// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package buildah shells out to the daemonless `buildah` binary to build
// container images, the forge-neutral, action-free replacement for
// docker/setup-buildx-action + docker/build-push-action. forge-ci builds
// one native platform per invocation (the multi-arch index is assembled later
// by the ggcr `container manifest` adapter), which is exactly buildah's sweet
// spot — no QEMU, no BuildKit daemon, no GitHub-marketplace action.
//
// This adapter builds and exports local OCI layouts. Registry uploads belong to
// ociregistry. The push-by-digest path builds into an OCI layout
// here and hands it to the ggcr ociregistry adapter, which writes it tagless by
// digest — buildah cannot push without creating a tag, and a per-arch tag could
// not later be removed without deleting the manifest the multi-arch index
// references. Splitting build (buildah) from publish (ggcr) keeps each tool to
// what it does cleanly.
//
// buildah reads the same {"auths":…} credentials written by `container login`
// ($REGISTRY_AUTH_FILE / $DOCKER_CONFIG), so authentication is already
// forge-neutral and shared with cosign/skopeo/podman.
package buildah

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/safeexec"
)

// Adapter wraps the buildah binary. Bin is overridable for tests; Global holds
// buildah GLOBAL flags (placed before the subcommand) such as
// --storage-driver/--root, used by hermetic integration tests and by operators
// who need a non-default storage backend.
type Adapter struct {
	Bin    string   // empty → "buildah"
	Global []string // global flags before the subcommand (e.g. --storage-driver vfs)
}

// New returns an Adapter using the system buildah with default storage.
func New() *Adapter { return &Adapter{} }

// Build runs the load or local-export build described by req. Load builds into
// local container storage under req.ImageRef (smoke-test flows); local exports
// a stage's filesystem to req.OutputDir (binary extraction). Neither pushes —
// the push-by-digest path goes through BuildToLayout + the ggcr pusher.
func (a *Adapter) Build(ctx context.Context, req container.BuildRequest, out io.Writer) error { //nolint:varnamelen // idiomatic short name (io conventions).
	if err := req.Validate(); err != nil {
		return err
	}

	switch req.Mode {
	case container.BuildModeLoad:
		return a.runBuildWithEnv(ctx, out, registryAuthEnv(req.AuthFile), req.SourceDateEpoch, a.buildArgs(req, "-t", req.ImageRef)...)
	case container.BuildModeLocal:
		return a.runBuildWithEnv(ctx, out, registryAuthEnv(req.AuthFile), req.SourceDateEpoch, a.buildArgs(req, "--output", "type=local,dest="+req.OutputDir)...)
	case container.BuildModePushByDigest:
		return fmt.Errorf("push-by-digest builds go through BuildToLayout, not Build: %w", errs.ErrUsage)
	default:
		return fmt.Errorf("unknown build output mode %q: %w", req.Mode, errs.ErrUsage)
	}
}

// BuildToLayout builds the image and exports it to an OCI image layout in
// layoutDir, ready for a tagless digest push by the ggcr adapter. It builds to
// an image ID (no tag), exports it, and removes the staged image from storage.
func (a *Adapter) BuildToLayout(ctx context.Context, req container.BuildRequest, layoutDir string, out io.Writer) error { //nolint:varnamelen // idiomatic short name (io conventions).
	if err := req.Validate(); err != nil {
		return err
	}

	iidFile, err := os.CreateTemp("", "forge-ci-iid-*")
	if err != nil {
		return fmt.Errorf("create image-id file: %w", err)
	}

	iidPath := iidFile.Name()
	_ = iidFile.Close()

	defer func() { _ = os.Remove(iidPath) }()

	if err = a.runBuildWithEnv(ctx, out, registryAuthEnv(req.AuthFile), req.SourceDateEpoch, a.buildArgs(req, "--iidfile", iidPath)...); err != nil {
		return err
	}

	idBytes, err := os.ReadFile(iidPath) //nolint:gosec // path is our own mktemp.
	if err != nil {
		return fmt.Errorf("read image id: %w", err)
	}

	imageID := strings.TrimSpace(string(idBytes))

	// Best-effort removal of the staged image from local storage once exported.
	defer func() { _ = a.run(context.WithoutCancel(ctx), io.Discard, a.global("rmi", "--force", imageID)...) }()

	return a.run(ctx, out, a.global("push", imageID, "oci:"+layoutDir+":image")...)
}

// CommandExists reports whether name is available in PATH.
func (a *Adapter) CommandExists(name string) bool {
	_, err := exec.LookPath(name)

	return err == nil
}

// InfoDriver returns buildah's selected graph driver for the supplied env.
func (a *Adapter) InfoDriver(ctx context.Context, env []string) (string, error) {
	out, err := a.outputWithEnv(ctx, env, "info", "--format", "{{.store.GraphDriverName}}")
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(out), nil
}

// InfoJSON returns buildah info for diagnostics.
func (a *Adapter) InfoJSON(ctx context.Context, env []string) ([]byte, error) {
	out, err := a.outputBytesWithEnv(ctx, env, "info")
	if err != nil {
		return nil, err
	}

	return out, nil
}

// ProbeBuild builds and removes a tiny scratch image to prove storage works.
func (a *Adapter) ProbeBuild(ctx context.Context, env []string, probeDir, image string, out io.Writer) error {
	args := append(a.global("bud"), chrootIsolation()...)
	args = append(args, "--pull=never", "--tag", image, probeDir)

	if err := a.runWithEnv(ctx, out, env, args...); err != nil {
		return err
	}

	return a.RemoveImage(ctx, env, image, out)
}

// RemoveImage removes an image from local Buildah storage.
func (a *Adapter) RemoveImage(ctx context.Context, env []string, image string, out io.Writer) error {
	return a.runWithEnv(ctx, out, env, a.global("rmi", image)...)
}

// chrootIsolation returns the flags of a build that runs inside the job's own
// container: chroot isolation, and the network stated as the host's.
//
// Under chroot isolation a RUN step shares the caller's network namespace;
// buildah creates none. With the network left to its default, buildah 1.28
// (Debian 12) nevertheless writes the step's /etc/resolv.conf as for a private
// namespace: loopback nameservers are dropped, and when none remain the file
// names public resolvers (8.8.8.8 and its siblings) instead. A job container
// whose only resolver is a loopback one (Docker's 127.0.0.11 on a user-defined
// network) then builds with resolvers the job itself never asks, and fails
// where only the job's resolver is reachable. Buildah 1.39 (Debian 13) keeps
// the job's file by default. --network host says what chroot isolation does,
// so both keep it. It grants nothing: the step was already in the job's
// network namespace.
func chrootIsolation() []string {
	return append([]string{"--isolation", "chroot"}, hostNetwork()...)
}

// hostNetwork runs RUN steps in the job's network namespace, so they resolve
// names the way the job does.
//
// A consumer build keeps buildah's isolation, and on a hosted GitHub runner
// that is rootless with a private slirp4netns or pasta namespace. Its
// resolv.conf keeps slirp's forwarder, which answers A but not AAAA, and the
// host's uplink resolver, which is unreachable from the namespace. glibc
// settles for the A answer; musl fails the lookup, so every apk step of an
// Alpine image failed with "DNS: transient error". On the host network a RUN
// step reaches what the job's own steps reach, which includes the host's
// loopback services and ports a slirp namespace would hide: the build is
// trusted like the job, as on every other build path here.
func hostNetwork() []string {
	return []string{"--network", "host"}
}

// buildArgs assembles `buildah build` argv from the request. modeArgs are the
// mode-specific flags (-t / --output / --iidfile); the build context is last.
func (a *Adapter) buildArgs(req container.BuildRequest, modeArgs ...string) []string {
	args := append(a.global("build"), hostNetwork()...)
	if req.AuthFile != "" {
		args = append(args, "--authfile", req.AuthFile)
	}

	if req.Containerfile != "" {
		args = append(args, "-f", req.Containerfile)
	}

	if req.Platform != "" {
		args = append(args, "--platform", req.Platform)
	}

	if req.Target != "" {
		args = append(args, "--target", req.Target)
	}

	for _, buildArg := range req.BuildArgs {
		args = append(args, "--build-arg", buildArg)
	}

	for _, s := range req.Secrets {
		args = append(args, "--secret", s)
	}

	for _, l := range req.Labels {
		args = append(args, "--label", l)
	}

	// Normalize image-config and layer timestamps. Matching digests also require
	// matching labels, base images and deterministic build output. --timestamp is
	// supported by older Buildah versions that lack --source-date-epoch.
	if req.SourceDateEpoch != "" {
		args = append(args, "--timestamp", req.SourceDateEpoch)
	}

	args = append(args, cacheArgs(req)...)
	args = append(args, modeArgs...)
	args = append(args, req.Context)

	return args
}

// cacheArgs returns the forge-neutral registry layer-cache flags (replacing the
// GitHub-specific type=gha). The cache repository is always imported
// (best-effort) with --layers; CachePush additionally exports it (trusted push
// only). No cache repository → nil.
func cacheArgs(req container.BuildRequest) []string {
	ref := req.CacheRepo
	if ref == "" {
		return nil
	}

	args := []string{"--layers", "--cache-from", ref}
	if req.CachePush {
		args = append(args, "--cache-to", ref)
	}

	return args
}

// global returns a fresh argv beginning with buildah's global flags followed by
// sub, so callers never alias the shared Global slice.
func (a *Adapter) global(sub ...string) []string {
	args := append([]string{}, a.Global...)

	return append(args, sub...)
}

// run invokes buildah, streaming combined output to w so the build log lands in
// CI as it happens. On failure the captured tail is folded into the classified
// error (redacted, defending against a build that echoes secret material).
func (a *Adapter) run(ctx context.Context, w io.Writer, args ...string) error {
	return a.runWithEnv(ctx, w, nil, args...)
}

func (a *Adapter) runWithEnv(ctx context.Context, w io.Writer, env []string, args ...string) error {
	return a.runWithCommandEnv(ctx, w, append(os.Environ(), env...), args...)
}

// runBuildWithEnv keeps --timestamp authoritative. Newer Buildah defaults
// --source-date-epoch from SOURCE_DATE_EPOCH and rejects it alongside
// --timestamp, even when both values agree. Keep --timestamp's layer and config
// normalization and older-version support; without it, leave Buildah's defaults
// alone. Explicit build arguments are passed through unchanged.
func (a *Adapter) runBuildWithEnv(ctx context.Context, w io.Writer, env []string, timestamp string, args ...string) error {
	env = append(os.Environ(), env...)
	if timestamp != "" {
		env = safeexec.EnvWithout(env, "SOURCE_DATE_EPOCH")
	}

	return a.runWithCommandEnv(ctx, w, env, args...)
}

func (a *Adapter) runWithCommandEnv(ctx context.Context, w io.Writer, env []string, args ...string) error {
	cmd := safeexec.Command(ctx, a.bin(), args...)
	cmd.Stdout = w

	cmd.Stderr = w
	cmd.Env = env

	if err := cmd.Run(); err != nil {
		return safeexec.WrapError(err, a.bin(), safeexec.FirstArg(args))
	}

	return nil
}

func (a *Adapter) outputWithEnv(ctx context.Context, env []string, args ...string) (string, error) {
	out, err := a.outputBytesWithEnv(ctx, env, args...)

	return string(out), err
}

func (a *Adapter) outputBytesWithEnv(ctx context.Context, env []string, args ...string) ([]byte, error) {
	cmd := safeexec.Command(ctx, a.bin(), args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}

	// Output, not CombinedOutput: callers parse this. InfoDriver reads it as
	// the graph-driver name and compares it to the configured one, and buildah
	// writes warnings to stderr while exiting 0 -- a "WARN[0000] Failed to
	// decode the keys ..." line from a system storage.conf would be returned
	// as the driver name and fail that comparison with a nonsense message.
	// Stderr still reaches the operator: it travels on the ExitError that
	// WrapError classifies.
	out, err := cmd.Output()
	if err != nil {
		return nil, safeexec.WrapError(err, a.bin(), safeexec.FirstArg(args))
	}

	return out, nil
}

func (a *Adapter) bin() string {
	if a.Bin != "" {
		return a.Bin
	}

	return "buildah"
}
