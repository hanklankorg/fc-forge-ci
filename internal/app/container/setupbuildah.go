// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/diggsweden/forge-ci/internal/domain/ci"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

const (
	defaultBuildahRunnerTemp = "/tmp"
	probeImage               = "localhost/forge-ci-storage-probe:latest"

	// Command/package names of the runtime dependencies installed by setup.
	buildahBinary       = "buildah"
	fuseOverlayfsBinary = "fuse-overlayfs"
	netavarkBinary      = "netavark"
	aardvarkDNSBinary   = "aardvark-dns"
)

var aptPackageRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]*$`)

// BuildahSetupTool is the Buildah/system surface needed by SetupBuildah.
type BuildahSetupTool interface {
	CommandExists(name string) bool
	InfoDriver(ctx context.Context, env []string) (string, error)
	InfoJSON(ctx context.Context, env []string) ([]byte, error)
	ProbeBuild(ctx context.Context, env []string, probeDir, image string, out io.Writer) error
	RemoveImage(ctx context.Context, env []string, image string, out io.Writer) error
}

// PackageInstaller installs Debian packages selected by SetupBuildah.
type PackageInstaller interface {
	Install(ctx context.Context, packages []string, out io.Writer) error
}

// SetupBuildahInput drives `container setup-buildah`.
type SetupBuildahInput struct {
	ExtraPackages   string
	InstallPackages bool
	ProbeBuild      bool
	PrintStore      bool
	WriteSummary    bool
	StorageConf     string
	StorageRoot     string
	TmpDir          string
	RunnerTemp      string
	EnvFile         string
}

// SetupBuildahResult is the selected storage configuration.
type SetupBuildahResult struct {
	Driver      string
	StorageConf string
	StorageRoot string
	TmpDir      string
}

// SetupBuildah installs missing Buildah runtime packages when requested and
// configures job-local container storage. It prefers overlay/fuse-overlayfs only
// after buildah info and an optional layered build probe succeed, then falls back
// to vfs. The selected storage paths are emitted for later CI steps through the
// runner env file and the OutputSink.
func SetupBuildah(
	ctx context.Context,
	tool BuildahSetupTool,
	installer PackageInstaller,
	sink ci.OutputSink,
	summary ci.SummarySink,
	out io.Writer,
	in SetupBuildahInput,
) (*SetupBuildahResult, error) {
	if tool == nil {
		return nil, fmt.Errorf("buildah setup tool is required: %w", errs.ErrUsage)
	}

	packages, err := setupBuildahPackages(in.ExtraPackages)
	if err != nil {
		return nil, err
	}

	paths, err := setupBuildahPaths(in)
	if err != nil {
		return nil, err
	}

	if in.InstallPackages {
		if err = installBuildahPackages(ctx, tool, installer, packages, out); err != nil {
			return nil, err
		}
	}

	if !tool.CommandExists(buildahBinary) {
		return nil, fmt.Errorf("buildah is not installed: %w", errs.ErrDependencyUnavailable)
	}

	driver, err := configureBuildahStorage(ctx, tool, out, paths, in.ProbeBuild)
	if err != nil {
		return nil, err
	}

	printFilesystemDiagnostics(out, []string{"/tmp", "/var/tmp", paths.StorageRoot, paths.TmpDir})

	result := &SetupBuildahResult{
		Driver:      driver,
		StorageConf: paths.StorageConf,
		StorageRoot: paths.StorageRoot,
		TmpDir:      paths.TmpDir,
	}

	if err := emitBuildahSetup(ctx, sink, summary, out, result, in.EnvFile, in.WriteSummary); err != nil {
		return nil, err
	}

	if in.PrintStore {
		printBuildahStore(ctx, tool, out, paths)
	}

	return result, nil
}

type buildahSetupPaths struct {
	RunnerTemp  string
	StorageConf string
	StorageRoot string
	TmpDir      string
	ProbeDir    string
}

func setupBuildahPackages(extra string) ([]string, error) {
	// With --no-install-recommends, Buildah's network backend and its DNS
	// helper must be explicit, even when the build uses vfs storage.
	packages := []string{buildahBinary, fuseOverlayfsBinary, netavarkBinary, aardvarkDNSBinary}
	seen := make(map[string]bool, len(packages))
	for _, name := range packages {
		seen[name] = true
	}

	for packageName := range strings.FieldsSeq(strings.ReplaceAll(extra, "\n", " ")) {
		if !aptPackageRE.MatchString(packageName) {
			return nil, fmt.Errorf("unsafe apt package name: %s: %w", packageName, errs.ErrUsage)
		}

		if !seen[packageName] {
			packages = append(packages, packageName)
			seen[packageName] = true
		}
	}

	return packages, nil
}

func installBuildahPackages(ctx context.Context, tool BuildahSetupTool, installer PackageInstaller, packages []string, out io.Writer) error {
	if !tool.CommandExists("apt-get") {
		return fmt.Errorf("apt-get is required when install-packages is true: %w", errs.ErrDependencyUnavailable)
	}

	if installer == nil {
		return fmt.Errorf("package installer is required when install-packages is true: %w", errs.ErrUsage)
	}

	missing := missingBuildahPackages(tool, packages)
	if len(missing) == 0 {
		return nil
	}

	return installer.Install(ctx, missing, out)
}

func missingBuildahPackages(tool BuildahSetupTool, packages []string) []string {
	var missing []string

	for _, packageName := range packages {
		commandName := buildahPackageCommand(packageName)
		if commandName == "" || !tool.CommandExists(commandName) {
			missing = append(missing, packageName)
		}
	}

	return missing
}

func buildahPackageCommand(packageName string) string {
	switch packageName {
	case buildahBinary, fuseOverlayfsBinary, netavarkBinary, aardvarkDNSBinary, "curl", "jq", "podman", "skopeo", "unzip":
		return packageName
	case "gettext":
		return "envsubst"
	default:
		return ""
	}
}

// setupBuildahPaths places Buildah's storage. By default it creates a fresh
// private directory under runner-temp and keeps everything there, so a shared
// parent such as /tmp -- which is what a Forgejo job container reports as
// RUNNER_TEMP -- is safe: the directory is new, unique and 0700, and the
// storage reset only removes what it made. Explicit storage paths are reset in
// place, so they must sit below a job-specific runner-temp, never shared
// system temp. The env file is the runner's own command file, which Forgejo
// and Gitea keep outside any temp directory, so it is held to what appending
// to it needs: a regular file, not a link.
func setupBuildahPaths(in SetupBuildahInput) (buildahSetupPaths, error) {
	runnerTemp, err := filepath.Abs(defaultString(in.RunnerTemp, defaultBuildahRunnerTemp))
	if err != nil {
		return buildahSetupPaths{}, err
	}

	parent, err := pathsafe.OpenRoot(runnerTemp)
	if err != nil {
		return buildahSetupPaths{}, err
	}

	_ = parent.Close()

	if err = checkBuildahEnvFile(in.EnvFile); err != nil {
		return buildahSetupPaths{}, err
	}

	runnerTemp, err = buildahStorageBase(in, runnerTemp)
	if err != nil {
		return buildahSetupPaths{}, err
	}

	root, err := pathsafe.OpenRoot(runnerTemp)
	if err != nil {
		return buildahSetupPaths{}, err
	}

	defer func() { _ = root.Close() }()

	paths := buildahSetupPaths{
		RunnerTemp:  runnerTemp,
		StorageConf: defaultString(in.StorageConf, filepath.Join(runnerTemp, "containers-storage.conf")),
		StorageRoot: defaultString(in.StorageRoot, filepath.Join(runnerTemp, "containers-storage")),
		TmpDir:      defaultString(in.TmpDir, filepath.Join(runnerTemp, "container-tmp")),
		ProbeDir:    filepath.Join(runnerTemp, "container-storage-probe"),
	}
	for _, path := range []*string{&paths.StorageRoot, &paths.StorageConf, &paths.TmpDir} {
		*path, err = filepath.Abs(*path)
		if err != nil {
			return buildahSetupPaths{}, err
		}
	}

	for _, field := range []struct {
		path      string
		directory bool
	}{{paths.StorageRoot, true}, {paths.ProbeDir, true}, {paths.TmpDir, true}, {paths.StorageConf, false}} {
		if pathErr := validateBuildahJobPath(root, runnerTemp, field.path, field.directory); pathErr != nil {
			return buildahSetupPaths{}, pathErr
		}
	}

	return paths, nil
}

// checkBuildahEnvFile refuses, before any effect, an env file that exists and
// is not a regular file. appendBuildahEnvFile checks again where it opens it.
func checkBuildahEnvFile(path string) error {
	if path == "" {
		return nil
	}

	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	if !info.Mode().IsRegular() {
		return fmt.Errorf("buildah env file must be regular: %w", errs.ErrUsage)
	}

	return nil
}

// buildahStorageBase is the directory Buildah's storage goes under: a new
// private directory below runnerTemp, or runnerTemp itself when the caller
// names storage paths, which then must not be shared system temp.
func buildahStorageBase(in SetupBuildahInput, runnerTemp string) (string, error) {
	if in.StorageConf == "" && in.StorageRoot == "" && in.TmpDir == "" {
		private, err := os.MkdirTemp(runnerTemp, "forge-ci-buildah-")
		if err != nil {
			return "", fmt.Errorf("create a private buildah directory under runner-temp: %w", err)
		}

		return private, nil
	}

	if runnerTemp == string(filepath.Separator) || runnerTemp == defaultBuildahRunnerTemp || runnerTemp == "/var/tmp" {
		return "", fmt.Errorf("explicit buildah paths need a job-specific runner-temp, not shared system temp: %w", errs.ErrUsage)
	}

	return runnerTemp, nil
}

//nolint:cyclop // containment, config serialization and each existing path component are independently validated.
func validateBuildahJobPath(root *os.Root, runnerTemp, path string, directory bool) error {
	if path == "" {
		return nil
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}

	rel, err := filepath.Rel(runnerTemp, abs)
	if err != nil || rel == "." || !pathsafe.Relative(rel) || strings.ContainsAny(path, "\\\t\r\n\"") {
		return fmt.Errorf("buildah paths must be below runner-temp and safe for storage config: %w", errs.ErrUsage)
	}

	parts := strings.Split(rel, string(filepath.Separator))
	for index := range parts {
		info, statErr := root.Lstat(filepath.Join(parts[:index+1]...))
		if errors.Is(statErr, os.ErrNotExist) {
			break
		}

		if statErr != nil {
			return statErr
		}

		wantDir := directory || index < len(parts)-1
		if info.Mode()&os.ModeSymlink != 0 || (wantDir && !info.IsDir()) || (!wantDir && !info.Mode().IsRegular()) {
			return fmt.Errorf("buildah paths must not contain symlinks or unexpected file types: %w", errs.ErrUsage)
		}
	}

	return nil
}

func configureBuildahStorage(ctx context.Context, tool BuildahSetupTool, out io.Writer, paths buildahSetupPaths, probe bool) (string, error) {
	if tool.CommandExists(fuseOverlayfsBinary) {
		if err := writeBuildahStorage(ctx, tool, out, paths, "overlay", probe); err == nil {
			return "overlay", nil
		} else if out != nil {
			_, _ = fmt.Fprintf(out, "overlay/fuse-overlayfs unavailable; falling back to vfs: %v\n", err)
		}
	} else if out != nil {
		_, _ = fmt.Fprintln(out, "fuse-overlayfs missing; falling back to vfs")
	}

	if err := writeBuildahStorage(ctx, tool, out, paths, "vfs", probe); err != nil {
		return "", withUserNamespaceHint(err)
	}

	return "vfs", nil
}

// userNamespaceRefusal is what Buildah prints when the job's container may not
// create a user namespace: a pool that shares its kernel between tenants
// denies them, and rootless Buildah cannot run without one.
const userNamespaceRefusal = "unshare(CLONE_NEWUSER): Operation not permitted"

// withUserNamespaceHint names the runner, not the storage driver, as the
// cause when every driver failed the same way, and the variables that move
// the job or change its builder.
func withUserNamespaceHint(err error) error {
	if err == nil || !strings.Contains(err.Error(), userNamespaceRefusal) {
		return err
	}

	return fmt.Errorf("%w\nthis runner denies user namespaces, which rootless Buildah needs; no storage driver can help. Run container-building jobs on a runner that permits them: CI_RUNNER_LABEL_CONTAINER selects it, and `forge-ci platform request-runner` can lend one; or build without them with CI_IMAGE_BUILDER=kaniko (docs/runners.md)", err)
}

func writeBuildahStorage(ctx context.Context, tool BuildahSetupTool, out io.Writer, paths buildahSetupPaths, driver string, probe bool) error {
	if err := resetBuildahStorage(paths); err != nil {
		return err
	}

	if err := writeBuildahStorageConf(paths, driver); err != nil {
		return err
	}

	env := buildahStorageEnv(paths)

	actualDriver, err := tool.InfoDriver(ctx, env)
	if err != nil {
		return err
	}

	if actualDriver != driver {
		return fmt.Errorf("buildah selected %s storage after %s config: %w", actualDriver, driver, errs.ErrValidation)
	}

	if probe {
		return runBuildahProbe(ctx, tool, env, paths.ProbeDir, out)
	}

	return nil
}

func resetBuildahStorage(paths buildahSetupPaths) error {
	root, err := pathsafe.OpenRoot(paths.RunnerTemp)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	storage, err := filepath.Rel(paths.RunnerTemp, paths.StorageRoot)
	if err != nil {
		return err
	}

	probe, err := filepath.Rel(paths.RunnerTemp, paths.ProbeDir)
	if err != nil {
		return err
	}

	if err := root.RemoveAll(storage); err != nil {
		return fmt.Errorf("reset container storage root: %w", err)
	}

	if err := root.RemoveAll(probe); err != nil {
		return fmt.Errorf("reset container storage probe dir: %w", err)
	}

	for _, dir := range []string{paths.StorageRoot, paths.ProbeDir, paths.TmpDir, filepath.Dir(paths.StorageConf)} {
		rel, err := filepath.Rel(paths.RunnerTemp, dir)
		if err != nil {
			return err
		}

		if err := root.MkdirAll(rel, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}

	return nil
}

func writeBuildahStorageConf(paths buildahSetupPaths, driver string) error {
	var body string

	switch driver {
	case "overlay":
		body = fmt.Sprintf("[storage]\ndriver = \"overlay\"\nrunroot = \"%s/run\"\ngraphroot = \"%s/graph\"\n\n[storage.options.overlay]\nmount_program = \"/usr/bin/fuse-overlayfs\"\n", paths.StorageRoot, paths.StorageRoot)
	case "vfs":
		body = fmt.Sprintf("[storage]\ndriver = \"vfs\"\nrunroot = \"%s/run-vfs\"\ngraphroot = \"%s/graph-vfs\"\n", paths.StorageRoot, paths.StorageRoot)
	default:
		return fmt.Errorf("unsupported storage driver: %s: %w", driver, errs.ErrUsage)
	}

	root, err := pathsafe.OpenRoot(filepath.Dir(paths.StorageConf))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	return root.WriteFile(filepath.Base(paths.StorageConf), []byte(body), 0o644)
}

func runBuildahProbe(ctx context.Context, tool BuildahSetupTool, env []string, probeDir string, out io.Writer) error {
	root, err := pathsafe.OpenRoot(probeDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	if err := root.WriteFile("probe.txt", []byte("probe\n"), 0o644); err != nil {
		return err
	}

	// Two stages, and both earn their place.
	//
	// The probe used to be "FROM scratch, COPY into /", which is the one build
	// that cannot fail the way real builds fail. A scratch image owns nothing,
	// so no directory has to be created inside an existing layer and nothing
	// has to be copied up. On a nested runner -- container storage sitting on
	// the container's own overlay filesystem, where overlay cannot stack on
	// overlay -- that probe passed while every real build failed, and the
	// failure surfaced much later as "mkdir /usr/local: operation not
	// permitted" from inside Buildah's copier, naming neither the storage
	// driver nor the nesting.
	//
	// The first stage creates a directory, which the old probe never did. The
	// second writes into a directory its base image already owns, which needs a
	// copy-up and is what real Containerfiles do constantly. Both stay offline:
	// the second stage's base is the first, so this pulls nothing and needs no
	// registry.
	probeContainerfile := "FROM scratch AS owner\n" +
		"COPY probe.txt /owned/probe.txt\n" +
		"\n" +
		"FROM owner\n" +
		"COPY probe.txt /owned/second.txt\n"
	if err := root.WriteFile("Containerfile", []byte(probeContainerfile), 0o644); err != nil {
		return err
	}

	_ = tool.RemoveImage(context.WithoutCancel(ctx), env, probeImage, io.Discard)

	return tool.ProbeBuild(ctx, env, probeDir, probeImage, out)
}

func buildahStorageEnv(paths buildahSetupPaths) []string {
	return []string{
		"CONTAINERS_STORAGE_CONF=" + paths.StorageConf,
		"TMPDIR=" + paths.TmpDir,
	}
}

func emitBuildahSetup(ctx context.Context, sink ci.OutputSink, summary ci.SummarySink, out io.Writer, result *SetupBuildahResult, envFile string, writeSummary bool) error {
	if envFile != "" {
		if err := appendBuildahEnvFile(envFile, result); err != nil {
			return err
		}
	}

	if err := emitBuildahSetupOutputs(ctx, sink, result); err != nil {
		return err
	}

	if out != nil {
		_, _ = fmt.Fprintf(out, "Container storage driver: %s\n", result.Driver)
		_, _ = fmt.Fprintf(out, "Container storage config: %s\n", result.StorageConf)
		_, _ = fmt.Fprintf(out, "Container storage root: %s\n", result.StorageRoot)
		_, _ = fmt.Fprintf(out, "Container temp dir: %s\n", result.TmpDir)
	}

	if writeSummary && summary != nil {
		body := fmt.Sprintf("### Container storage\n\n* Driver: `%s`\n* Config: `%s`\n* Graph root: `%s`\n", result.Driver, result.StorageConf, result.StorageRoot)
		if err := summary.Append(ctx, body); err != nil {
			return err
		}
	}

	return nil
}

// emitBuildahSetupOutputs publishes the selected storage configuration to the
// CI output sink.
func emitBuildahSetupOutputs(ctx context.Context, sink ci.OutputSink, result *SetupBuildahResult) error {
	if sink == nil {
		return nil
	}

	if err := sink.Set(ctx, "driver", result.Driver); err != nil {
		return err
	}

	if err := sink.Set(ctx, "storage-conf", result.StorageConf); err != nil {
		return err
	}

	return sink.Set(ctx, "storage-root", result.StorageRoot)
}

func appendBuildahEnvFile(path string, result *SetupBuildahResult) error {
	root, err := pathsafe.MkdirRoot(filepath.Dir(path), 0o755)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	base := filepath.Base(path)

	info, err := root.Lstat(base)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("buildah env file must be regular: %w", errs.ErrUsage)
	}

	file, err := root.OpenFile(base, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}

	defer func() { _ = file.Close() }()

	_, err = fmt.Fprintf(file, "CONTAINERS_STORAGE_CONF=%s\nTMPDIR=%s\n", result.StorageConf, result.TmpDir)

	return err
}

func printBuildahStore(ctx context.Context, tool BuildahSetupTool, out io.Writer, paths buildahSetupPaths) {
	if out == nil {
		return
	}

	body, err := tool.InfoJSON(ctx, buildahStorageEnv(paths))
	if err != nil {
		_, _ = fmt.Fprintf(out, "buildah info unavailable: %v\n", err)

		return
	}

	var info map[string]any
	if err := json.Unmarshal(body, &info); err == nil {
		if store, ok := info["store"]; ok {
			if pretty, marshalErr := json.MarshalIndent(store, "", "  "); marshalErr == nil {
				_, _ = fmt.Fprintln(out, string(pretty))

				return
			}
		}
	}

	_, _ = out.Write(body)
}

func printFilesystemDiagnostics(out io.Writer, paths []string) {
	if out == nil {
		return
	}

	seen := map[string]bool{}
	_, _ = fmt.Fprintln(out, "Filesystem capacity diagnostics:")

	for _, path := range paths {
		if seen[path] {
			continue
		}

		seen[path] = true
		printFilesystemStat(out, path, false)
	}

	seen = map[string]bool{}
	_, _ = fmt.Fprintln(out, "Filesystem inode diagnostics:")

	for _, path := range paths {
		if seen[path] {
			continue
		}

		seen[path] = true
		printFilesystemStat(out, path, true)
	}
}

func printFilesystemStat(out io.Writer, path string, inodes bool) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		_, _ = fmt.Fprintf(out, "  %s: unavailable (%v)\n", path, err)

		return
	}

	if inodes {
		_, _ = fmt.Fprintf(out, "  %s: free=%d total=%d\n", path, stat.Ffree, stat.Files)

		return
	}

	_, _ = fmt.Fprintf(out, "  %s: avail=%d total=%d\n", path, stat.Bavail*uint64(stat.Bsize), stat.Blocks*uint64(stat.Bsize)) //nolint:gosec // Bsize is a positive filesystem block size; conversion cannot overflow.
}

func defaultString(value, fallback string) string {
	if value != "" {
		return value
	}

	return fallback
}
