// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package toolchain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// InstallChangelogRendererInput drives `toolchain install-changelog-renderer`.
type InstallChangelogRendererInput struct {
	// Backend is git-chglog or git-cliff.
	Backend string
	// Version is the renderer release, MAJOR.MINOR.PATCH without a v prefix.
	Version string
	// LinuxX64SHA256 and LinuxARM64SHA256 pin that release's Linux archives.
	LinuxX64SHA256   string
	LinuxARM64SHA256 string
	// BaseURL overrides the renderer's release download prefix, for a mirror.
	BaseURL string
	// BinHome is where the renderer is installed; empty is ~/.local/bin.
	BinHome string
	// PathFile is the runner path file that receives BinHome.
	PathFile string
}

// changelogRenderer is one renderer the verb can install: where its releases
// are, and how a release names its Linux archive and the executable in it.
type changelogRenderer struct {
	bin     string
	baseURL string
	// archive returns the archive name and the executable's member path for
	// a version and a Go architecture.
	archive func(version, goarch string) (string, string)
}

// The renderers, by backend name, which is also the name of the executable.
const (
	gitChglogBin = "git-chglog"
	gitCliffBin  = "git-cliff"
)

// changelogRenderers is the closed set of renderers. The verb downloads from
// no other project, whatever the caller passes as backend.
func changelogRenderers() map[string]changelogRenderer {
	return map[string]changelogRenderer{
		gitChglogBin: {
			bin:     gitChglogBin,
			baseURL: "https://github.com/git-chglog/git-chglog/releases/download",
			archive: func(version, goarch string) (string, string) {
				return fmt.Sprintf("git-chglog_%s_linux_%s.tar.gz", version, goarch), gitChglogBin
			},
		},
		gitCliffBin: {
			bin:     gitCliffBin,
			baseURL: "https://github.com/orhun/git-cliff/releases/download",
			archive: func(version, goarch string) (string, string) {
				machine := map[string]string{goarchAMD64: "x86_64", goarchARM64: "aarch64"}[goarch]

				return fmt.Sprintf("git-cliff-%s-%s-unknown-linux-musl.tar.gz", version, machine),
					fmt.Sprintf("git-cliff-%s/git-cliff", version)
			},
		},
	}
}

// InstallChangelogRenderer downloads the pinned release archive of the
// selected changelog renderer, verifies its SHA-256, installs the executable
// into the bin home and appends that directory to the runner path file.
//
// The download is one archive from the renderer's release URL. No tool
// manager runs, no forge API is called and no token is read: the pinned
// checksum is the trust anchor. Every local refusal precedes the download; a
// failure after it may leave the installed executable or the path entry.
func InstallChangelogRenderer(ctx context.Context, client *http.Client, out io.Writer, in InstallChangelogRendererInput) error {
	renderer, ok := changelogRenderers()[in.Backend]
	if !ok {
		return fmt.Errorf("install-changelog-renderer: backend must be git-chglog or git-cliff: %s: %w", in.Backend, errs.ErrValidation)
	}

	expectedSHA, err := changelogRendererPin(in)
	if err != nil {
		return err
	}

	baseURL := defaultString(in.BaseURL, renderer.baseURL)
	if err = validateReleaseBaseURL(renderer.bin, baseURL); err != nil {
		return err
	}

	binHome, pathFile, err := preflightChangelogRendererPaths(in, renderer.bin)
	if err != nil {
		return err
	}

	archive, member := renderer.archive(in.Version, runtime.GOARCH)

	if out != nil {
		_, _ = fmt.Fprintf(out, "Installing %s %s (%s)...\n", renderer.bin, in.Version, runtime.GOARCH)
	}

	body, err := downloadReleaseArchive(ctx, client, releaseArchiveURL(baseURL, in.Version, archive))
	if err != nil {
		return err
	}

	if err = verifyArchiveSHA256(archive, body, expectedSHA); err != nil {
		return err
	}

	installed, err := installBinaryFromArchive(body, releaseBinary{tool: renderer.bin, member: member, install: renderer.bin}, binHome)
	if err != nil {
		return err
	}

	if err = appendPathFile(pathFile, binHome); err != nil {
		return err
	}

	return printToolVersion(ctx, out, installed)
}

// changelogRendererPin validates the version and both checksums and returns
// the one for this machine. Both are required whatever the machine is: a pin
// file with half a pair is refused where it is read, not on the other
// architecture.
func changelogRendererPin(in InstallChangelogRendererInput) (string, error) {
	if !exactDownloadVersion(in.Version) {
		return "", fmt.Errorf("install-changelog-renderer: version must be an exact MAJOR.MINOR.PATCH without a v prefix: %w", errs.ErrUsage)
	}

	for label, value := range map[string]string{"linux-x64": in.LinuxX64SHA256, "linux-arm64": in.LinuxARM64SHA256} {
		if !domaincontainer.ValidSHA256Hex(value) {
			return "", fmt.Errorf("install-changelog-renderer: %s SHA-256 pin must be 64 lowercase hex characters: %w", label, errs.ErrUsage)
		}
	}

	return runnerArchPin(in.LinuxX64SHA256, in.LinuxARM64SHA256)
}

// preflightChangelogRendererPaths resolves the bin home and the path file and
// refuses, before anything is downloaded, a destination the install could not
// replace by rename: anything at the executable's name that is neither a
// regular file nor a link.
func preflightChangelogRendererPaths(in InstallChangelogRendererInput, bin string) (string, string, error) {
	if in.PathFile == "" {
		return "", "", fmt.Errorf("install-changelog-renderer: path-file is required: %w", errs.ErrUsage)
	}

	binHome, err := resolveBinHome(in.BinHome)
	if err != nil {
		return "", "", err
	}

	binHome, err = resolveMiseLocalPath(binHome, true)
	if err != nil {
		return "", "", err
	}

	pathFile, err := resolveMiseLocalPath(in.PathFile, false)
	if err != nil {
		return "", "", err
	}

	destination := filepath.Join(binHome, bin)
	if pathFile == destination {
		return "", "", fmt.Errorf("install-changelog-renderer: the path file is the renderer destination %s: %w", destination, errs.ErrValidation)
	}

	if err = preflightMiseLocalPaths([]string{binHome}, []string{pathFile}); err != nil {
		return "", "", err
	}

	if err = replaceableDestination(destination); err != nil {
		return "", "", err
	}

	return binHome, pathFile, nil
}

// replaceableDestination admits a name that is free, a regular file or a
// link: what a rename replaces without following anything.
func replaceableDestination(destination string) error {
	info, err := os.Lstat(destination)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("inspect renderer destination %s: %w", destination, err)
	}

	if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("renderer destination %s must be a regular file or a link: %w", destination, errs.ErrValidation)
	}

	return nil
}
