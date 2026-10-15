// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package toolchain

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/retry"
)

const defaultMiseBaseURL = "https://github.com/jdx/mise/releases/download"

const (
	releaseDownloadAttempts   = 4
	releaseDownloadRetryDelay = 2 * time.Second
	// The caps bound a download and its extraction; the archive's pinned
	// SHA-256 has already been checked when the binary is extracted. They
	// must admit the pinned release: mise 2026.6.11 ships a 33 MB archive
	// holding a 95 MB binary, which the earlier 64 MiB binary cap refused.
	maxReleaseArchiveBytes = 64 << 20
	maxReleaseBinaryBytes  = 256 << 20
)

// releaseBinary names one executable inside a tool's pinned release archive:
// the tool as messages call it, the archive member that is the executable,
// and the file name it is installed under.
type releaseBinary struct {
	tool    string
	member  string
	install string
}

// miseBinary is the executable of a mise release archive.
func miseBinary() releaseBinary {
	return releaseBinary{tool: "mise", member: "mise/bin/mise", install: "mise"}
}

// InstallMiseInput drives InstallMise.
type InstallMiseInput struct {
	Version          string
	LinuxX64SHA256   string
	LinuxARM64SHA256 string
	DestDir          string
	BaseURL          string
}

// InstallMise downloads a pinned mise release archive, verifies its SHA-256, and installs the mise binary.
func InstallMise(ctx context.Context, client *http.Client, out io.Writer, in InstallMiseInput) (string, error) {
	if err := validateInstallMiseInput(in); err != nil {
		return "", err
	}

	arch, expectedSHA, err := miseArchiveArchAndSHA(in)
	if err != nil {
		return "", err
	}

	archive, address, err := miseArchiveURL(in, arch)
	if err != nil {
		return "", err
	}

	if out != nil {
		_, _ = fmt.Fprintf(out, "Installing mise %s (%s)...\n", in.Version, arch)
	}

	body, err := downloadReleaseArchive(ctx, client, address)
	if err != nil {
		return "", err
	}

	if err = verifyArchiveSHA256(archive, body, expectedSHA); err != nil {
		return "", err
	}

	destDir, err := resolveMiseDestDir(in.DestDir)
	if err != nil {
		return "", err
	}

	installPath, err := installBinaryFromArchive(body, miseBinary(), destDir)
	if err != nil {
		return "", err
	}

	if out != nil {
		_, _ = fmt.Fprintf(out, "mise %s installed to %s\n", in.Version, installPath)
	}

	return installPath, nil
}

// Build and validate the effective URL without constructing or sending a request.
func miseArchiveURL(in InstallMiseInput, arch string) (string, string, error) {
	baseURL := defaultString(in.BaseURL, defaultMiseBaseURL)
	if err := validateReleaseBaseURL("mise", baseURL); err != nil {
		return "", "", err
	}

	archive := fmt.Sprintf("mise-v%s-linux-%s-musl.tar.gz", in.Version, arch)

	return archive, releaseArchiveURL(baseURL, in.Version, archive), nil
}

// releaseArchiveURL is where a release of the GitHub download layout keeps an
// archive: <base>/v<version>/<archive>.
func releaseArchiveURL(baseURL, version, archive string) string {
	return strings.TrimRight(baseURL, "/") + "/v" + version + "/" + archive
}

// validateReleaseBaseURL holds a tool's release base URL to an absolute
// HTTP(S) directory prefix.
func validateReleaseBaseURL(tool, baseURL string) error {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("invalid %s download URL: %w: %w", tool, err, errs.ErrUsage)
	}

	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("%s download URL must be absolute HTTP(S): %w", tool, errs.ErrUsage)
	}

	// The address is quoted in every download error, so credentials in it
	// would end up in the job log; the pinned SHA-256 is the trust anchor, not
	// an authenticated source.
	if parsed.User != nil {
		return fmt.Errorf("%s base URL must not carry credentials: %w", tool, errs.ErrUsage)
	}

	if strings.Contains(baseURL, "#") {
		return fmt.Errorf("%s base URL must be a directory prefix without a fragment: %w", tool, errs.ErrUsage)
	}

	if parsed.RawQuery != "" || parsed.ForceQuery {
		return fmt.Errorf("%s base URL must be a directory prefix without a query: %w", tool, errs.ErrUsage)
	}

	return nil
}

// resolveMiseDestDir defaults an empty destination to ~/.local/bin.
func resolveMiseDestDir(destDir string) (string, error) {
	if destDir != "" {
		return destDir, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}

	return filepath.Join(home, ".local", "bin"), nil
}

func validateInstallMiseInput(in InstallMiseInput) error {
	if !exactDownloadVersion(in.Version) {
		return fmt.Errorf("mise version must be an exact MAJOR.MINOR.PATCH without a v prefix: %w", errs.ErrUsage)
	}

	if in.LinuxX64SHA256 == "" || in.LinuxARM64SHA256 == "" {
		return fmt.Errorf("both linux x64 and arm64 SHA-256 pins are required: %w", errs.ErrUsage)
	}

	for label, value := range map[string]string{"linux-x64": in.LinuxX64SHA256, "linux-arm64": in.LinuxARM64SHA256} {
		if !domaincontainer.ValidSHA256Hex(value) {
			return fmt.Errorf("%s SHA-256 pin must be 64 lowercase hex characters: %w", label, errs.ErrUsage)
		}
	}

	return nil
}

func miseArchiveArchAndSHA(in InstallMiseInput) (string, string, error) {
	pin, err := runnerArchPin(in.LinuxX64SHA256, in.LinuxARM64SHA256)
	if err != nil {
		return "", "", err
	}

	if runtime.GOARCH == goarchARM64 {
		return goarchARM64, pin, nil
	}

	return "x64", pin, nil
}

// The two architectures a pinned Linux release archive is selected for.
const (
	goarchAMD64 = "amd64"
	goarchARM64 = "arm64"
)

// runnerArchPin returns the pin of the architecture this process runs on.
func runnerArchPin(linuxX64, linuxARM64 string) (string, error) {
	switch runtime.GOARCH {
	case goarchAMD64:
		return linuxX64, nil
	case goarchARM64:
		return linuxARM64, nil
	default:
		return "", fmt.Errorf("unsupported runner architecture: %s: %w", runtime.GOARCH, errs.ErrUnsupported)
	}
}

// downloadReleaseArchive fetches a pinned release archive whole, retrying a
// failed transfer and a status the server may recover from.
func downloadReleaseArchive(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}

	return retry.Do(ctx, nil, releaseDownloadAttempts, releaseDownloadRetryDelay, func() ([]byte, error) {
		body, retryable, err := downloadReleaseArchiveOnce(ctx, client, url)
		if err != nil && !retryable {
			return nil, retry.Permanent(err)
		}

		return body, err
	}, retry.WithBackoff(retry.Constant))
}

func downloadReleaseArchiveOnce(ctx context.Context, client *http.Client, url string) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, fmt.Errorf("create request %s: %w", url, err)
	}

	resp, err := client.Do(req)
	if err != nil {
		//nolint:errorlint // transport error is intentionally flattened: wrapping it would let a
		// mid-download cancellation match context.Canceled and change the exit-code classification.
		return nil, true, fmt.Errorf("download %s: %v: %w", url, err, errs.ErrDependencyUnavailable)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, shouldRetryReleaseHTTPStatus(resp.StatusCode), fmt.Errorf("download %s: HTTP %d: %w", url, resp.StatusCode, errs.ErrDependencyUnavailable)
	}

	if resp.ContentLength > maxReleaseArchiveBytes {
		return nil, false, fmt.Errorf("download %s: archive exceeds %d bytes: %w", url, maxReleaseArchiveBytes, errs.ErrMalformedInput)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReleaseArchiveBytes+1))
	if err != nil {
		return nil, true, fmt.Errorf("read %s: %v: %w", url, err, errs.ErrDependencyUnavailable) //nolint:errorlint // keep the transport text, classify as unavailable.
	}

	if len(body) > maxReleaseArchiveBytes {
		return nil, false, fmt.Errorf("download %s: archive exceeds %d bytes: %w", url, maxReleaseArchiveBytes, errs.ErrMalformedInput)
	}

	return body, false, nil
}

func shouldRetryReleaseHTTPStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

func verifyArchiveSHA256(name string, body []byte, expected string) error {
	sum := sha256.Sum256(body)

	actual := hex.EncodeToString(sum[:])
	if actual != expected {
		return fmt.Errorf("sha256 mismatch for %s: expected %s, got %s: %w", name, expected, actual, errs.ErrValidation)
	}

	return nil
}

// installBinaryFromArchive extracts one executable of a verified tar.gz
// release archive into destDir and returns its path.
func installBinaryFromArchive(body []byte, binary releaseBinary, destDir string) (string, error) {
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("read %s archive gzip: %w: %w", binary.tool, err, errs.ErrMalformedInput)
	}

	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return "", fmt.Errorf("read %s archive tar: %w: %w", binary.tool, err, errs.ErrMalformedInput)
		}

		if header.Name != binary.member {
			continue
		}

		// tar.Reader normalizes the legacy TypeRegA to TypeReg on read.
		if header.Typeflag != tar.TypeReg {
			return "", fmt.Errorf("%s is not a regular file in archive: %w", binary.member, errs.ErrMalformedInput)
		}

		if header.Size < 0 || header.Size > maxReleaseBinaryBytes {
			return "", fmt.Errorf("%s exceeds %d bytes: %w", binary.member, maxReleaseBinaryBytes, errs.ErrMalformedInput)
		}

		return writeReleaseBinary(tr, header.Size, binary, destDir)
	}

	return "", fmt.Errorf("%s binary not found at %s in archive: %w", binary.tool, binary.member, errs.ErrMalformedInput)
}

// writeReleaseBinary writes the archive entry into destDir under the binary's
// install name and marks it executable. The file is written beside its
// destination and renamed over it, so a link already at that name is replaced
// and never written through.
func writeReleaseBinary(tr *tar.Reader, size int64, binary releaseBinary, destDir string) (string, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil { //nolint:gosec // tool install dir read by later CI steps.
		return "", fmt.Errorf("create install directory %s: %w", destDir, err)
	}

	root, err := os.OpenRoot(destDir)
	if err != nil {
		return "", fmt.Errorf("open install directory %s: %w", destDir, err)
	}
	defer func() { _ = root.Close() }()

	installPath := filepath.Join(destDir, binary.install)

	out, err := os.CreateTemp(destDir, "."+binary.install+"-*") //nolint:gosec // destination is caller-selected and opened as an os.Root above.
	if err != nil {
		return "", fmt.Errorf("create temporary %s binary in %s: %w", binary.tool, destDir, err)
	}

	tempName := filepath.Base(out.Name())

	complete := false
	defer func() {
		if !complete {
			_ = out.Close()
			_ = root.Remove(tempName)
		}
	}()

	if _, err := io.CopyN(out, tr, size); err != nil {
		return "", fmt.Errorf("write %s: %w", installPath, err)
	}

	if err := out.Close(); err != nil {
		return "", fmt.Errorf("close %s: %w", installPath, err)
	}

	if err := root.Chmod(tempName, 0o755); err != nil { //nolint:gosec // the tool must be executable by the runner.
		return "", fmt.Errorf("set executable mode on %s: %w", installPath, err)
	}

	if err := root.Rename(tempName, binary.install); err != nil {
		return "", fmt.Errorf("install %s: %w", installPath, err)
	}

	complete = true

	return installPath, nil
}

func defaultString(value, fallback string) string {
	if value != "" {
		return value
	}

	return fallback
}
