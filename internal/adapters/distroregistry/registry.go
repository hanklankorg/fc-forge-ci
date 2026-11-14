// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package distroregistry implements the Debian, RPM and Alpine package
// registry protocols of Forgejo: one PUT of the package file, and a read of
// what the registry then serves at the package's place. The forge adapter
// resolves the registry's root and token; it does not implement this
// protocol.
//
// The upload follows no redirect. A read follows one, without the token and
// to HTTPS alone: an instance that serves packages from object storage
// (SERVE_DIRECT) answers every repository file and package download with a
// redirect to a signed address there.
//
// The read is the one a package manager makes. A Debian or RPM repository
// lists the SHA-256 of each package in the index the forge signs (Packages,
// primary.xml), so the package is found there by digest. An Alpine index
// lists no digest of the file, so the file is downloaded by the name,
// version and architecture the package states, and hashed.
package distroregistry

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/diggsweden/forge-ci/internal/adapters/httpretry"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// Client accepts an injected transport for controlled registry tests.
type Client struct{ HTTP *http.Client }

// requestTimeout bounds the wait for the registry: the idle window of a
// transfer, since a package is bounded by progress, not wall time.
const requestTimeout = 2 * time.Minute

// plainHTTP is the scheme only a loopback registry may have.
const plainHTTP = "http"

// indexLimit bounds a repository index read back from the registry.
const indexLimit = 256 << 20

// Upload sends the package file exactly once and reports whether the
// registry created it. A 409 is the registry holding that package version
// at that place already: it is reported, not an error, and Holds decides
// whether the held file is this one. No query parameter is sent, so the
// registry is never asked to sign or change the file.
func (c Client) Upload(ctx context.Context, registry provider.ForgeDistroRegistry, pkg provider.DistroPackage, body io.Reader) (bool, error) {
	var steps []string

	switch pkg.Format {
	case provider.DistroPackageDebian:
		steps = append([]string{"debian", "pool"}, append(pkg.Place, "upload")...)
	case provider.DistroPackageRPM:
		steps = append([]string{"rpm"}, append(strings.Split(pkg.Place[0], "/"), "upload")...)
	case provider.DistroPackageAlpine:
		steps = append([]string{"alpine"}, pkg.Place...)
	default:
		return false, fmt.Errorf("no registry for package format %q: %w", pkg.Format, errs.ErrUnsupported)
	}

	transferCtx, transfer := httpretry.StartTransfer(ctx, requestTimeout)
	defer transfer.Stop()

	response, err := c.do(transferCtx, http.MethodPut, registry, steps, body, pkg.Size)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}

		if stalled := transfer.Err(err); errors.Is(stalled, httpretry.ErrStalled) {
			return false, fmt.Errorf("distro package upload: %w", stalled)
		}

		return false, err
	}

	defer drain(response)

	switch response.StatusCode {
	case http.StatusCreated:
		return true, nil
	case http.StatusConflict:
		return false, nil
	default:
		return false, statusError("upload", response)
	}
}

// Listing reports whether the registry serves a file with the package's
// SHA-256 at the package's place. The Debian and RPM indexes are searched for
// the digest; the Alpine file is found by the package's name, so a file
// there with another digest is told apart from none.
func (c Client) Listing(ctx context.Context, registry provider.ForgeDistroRegistry, pkg provider.DistroPackage) (provider.DistroListing, error) {
	var (
		held bool
		err  error
	)

	switch pkg.Format {
	case provider.DistroPackageDebian:
		held, err = c.debianHolds(ctx, registry, pkg)
	case provider.DistroPackageRPM:
		held, err = c.rpmHolds(ctx, registry, pkg)
	case provider.DistroPackageAlpine:
		return c.alpineListing(ctx, registry, pkg)
	default:
		return provider.DistroNotListed, fmt.Errorf("no registry for package format %q: %w", pkg.Format, errs.ErrUnsupported)
	}

	if err != nil || !held {
		return provider.DistroNotListed, err
	}

	return provider.DistroListed, nil
}

// debianHolds reads the distribution's Release file for the Packages
// indexes of the component, one for each architecture, and looks for the
// digest in them.
func (c Client) debianHolds(ctx context.Context, registry provider.ForgeDistroRegistry, pkg provider.DistroPackage) (bool, error) {
	distribution, component := pkg.Place[0], pkg.Place[1]
	dists := []string{"debian", "dists", distribution}

	release, found, err := c.get(ctx, registry, append(dists, "Release"))
	if err != nil || !found {
		return false, err
	}

	defer drain(release)

	indexes := map[string]bool{}
	lines := bufio.NewScanner(io.LimitReader(release.Body, indexLimit))

	for lines.Scan() {
		// " <hash> <size> <component>/binary-<architecture>/Packages"
		fields := strings.Fields(lines.Text())
		if len(fields) != 3 {
			continue
		}

		steps := strings.Split(fields[2], "/")
		if len(steps) == 3 && steps[0] == component && strings.HasPrefix(steps[1], "binary-") && steps[2] == "Packages" {
			indexes[steps[1]] = true
		}
	}

	if err := lines.Err(); err != nil {
		return false, fmt.Errorf("read the Debian Release file: %w", errs.ErrMalformedInput)
	}

	for architecture := range indexes {
		held, err := c.debianIndexHolds(ctx, registry, append(dists, component, architecture, "Packages"), pkg.SHA256)
		if err != nil || held {
			return held, err
		}
	}

	return false, nil
}

func (c Client) debianIndexHolds(ctx context.Context, registry provider.ForgeDistroRegistry, steps []string, digest string) (bool, error) {
	index, found, err := c.get(ctx, registry, steps)
	if err != nil || !found {
		return false, err
	}

	defer drain(index)

	lines := bufio.NewScanner(io.LimitReader(index.Body, indexLimit))
	lines.Buffer(nil, 1<<20)

	for lines.Scan() {
		if value, isDigest := strings.CutPrefix(lines.Text(), "SHA256: "); isDigest && strings.TrimSpace(value) == digest {
			return true, nil
		}
	}

	if err := lines.Err(); err != nil {
		return false, fmt.Errorf("read the Debian Packages index: %w", errs.ErrMalformedInput)
	}

	return false, nil
}

// rpmHolds reads the group's repomd.xml for its primary index and looks for
// the digest among the package checksums there.
func (c Client) rpmHolds(ctx context.Context, registry provider.ForgeDistroRegistry, pkg provider.DistroPackage) (bool, error) {
	group := append([]string{"rpm"}, strings.Split(pkg.Place[0], "/")...)

	repomd, found, err := c.get(ctx, registry, append(group, "repodata", "repomd.xml"))
	if err != nil || !found {
		return false, err
	}

	defer drain(repomd)

	var metadata struct {
		Data []struct {
			Type     string `xml:"type,attr"`
			Location struct {
				Href string `xml:"href,attr"`
			} `xml:"location"`
		} `xml:"data"`
	}

	if err := xml.NewDecoder(io.LimitReader(repomd.Body, 1<<20)).Decode(&metadata); err != nil {
		return false, fmt.Errorf("read the RPM repomd.xml: %w", errs.ErrMalformedInput)
	}

	for _, data := range metadata.Data {
		if data.Type != "primary" {
			continue
		}

		// The index is a file of repodata/, by the registry's own word: any
		// other path is not followed.
		name, isIndex := strings.CutPrefix(data.Location.Href, "repodata/")
		if !isIndex || !strings.HasSuffix(name, ".xml.gz") || strings.Contains(name, "/") {
			return false, fmt.Errorf("the RPM repomd.xml names a primary index outside repodata/: %w", errs.ErrMalformedInput)
		}

		return c.rpmIndexHolds(ctx, registry, append(group, "repodata", name), pkg.SHA256)
	}

	return false, fmt.Errorf("the RPM repomd.xml names no primary index: %w", errs.ErrMalformedInput)
}

func (c Client) rpmIndexHolds(ctx context.Context, registry provider.ForgeDistroRegistry, steps []string, digest string) (bool, error) {
	index, found, err := c.get(ctx, registry, steps)
	if err != nil || !found {
		return false, err
	}

	defer drain(index)

	malformed := fmt.Errorf("read the RPM primary index: %w", errs.ErrMalformedInput)

	plain, err := gzip.NewReader(io.LimitReader(index.Body, indexLimit))
	if err != nil {
		return false, malformed
	}

	decoder := xml.NewDecoder(io.LimitReader(plain, indexLimit))

	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return false, nil
		}

		if err != nil {
			return false, malformed
		}

		start, isStart := token.(xml.StartElement)
		if !isStart || start.Name.Local != "checksum" {
			continue
		}

		var checksum struct {
			Type  string `xml:"type,attr"`
			Value string `xml:",chardata"`
		}

		if err := decoder.DecodeElement(&checksum, &start); err != nil {
			return false, malformed
		}

		if checksum.Type == "sha256" && strings.TrimSpace(checksum.Value) == digest {
			return true, nil
		}
	}
}

// alpineListing downloads the file the registry serves under the package's
// architecture, name and version, and compares its digest.
func (c Client) alpineListing(ctx context.Context, registry provider.ForgeDistroRegistry, pkg provider.DistroPackage) (provider.DistroListing, error) {
	if pkg.Name == "" || pkg.Version == "" || pkg.Architecture == "" {
		return provider.DistroNotListed, fmt.Errorf("an Alpine package is read back by its name, version and architecture: %w", errs.ErrUsage)
	}

	steps := append([]string{"alpine"}, pkg.Place...)

	transferCtx, transfer := httpretry.StartTransfer(ctx, requestTimeout)
	defer transfer.Stop()

	file, found, err := c.get(transferCtx, registry, append(steps, pkg.Architecture, pkg.Name+"-"+pkg.Version+".apk"))
	if err != nil || !found {
		return provider.DistroNotListed, err
	}

	defer drain(file)

	digest := sha256.New()

	// One byte past the package's size is already another file.
	if _, err := io.Copy(digest, io.LimitReader(transfer.Reader(file.Body), pkg.Size+1)); err != nil {
		return provider.DistroNotListed, fmt.Errorf("download the Alpine package to compare it: %w", errs.RequestFailure(err))
	}

	if hex.EncodeToString(digest.Sum(nil)) != pkg.SHA256 {
		return provider.DistroOtherContent, nil
	}

	return provider.DistroListed, nil
}

// get reads one file of the registry. found is false for a 404: a place
// nothing was published to yet has no index. The registry may answer with
// one redirect to where it keeps the file.
func (c Client) get(ctx context.Context, registry provider.ForgeDistroRegistry, steps []string) (*http.Response, bool, error) {
	response, err := c.do(ctx, http.MethodGet, registry, steps, nil, 0)
	if err != nil {
		return nil, false, err
	}

	if location := response.Header.Get("Location"); location != "" && response.StatusCode >= 300 && response.StatusCode < 400 {
		drain(response)

		if response, err = c.follow(ctx, response.Request.URL, location); err != nil {
			return nil, false, err
		}
	}

	switch response.StatusCode {
	case http.StatusOK:
		return response, true, nil
	case http.StatusNotFound:
		drain(response)

		return nil, false, nil
	default:
		defer drain(response)

		return nil, false, statusError("read", response)
	}
}

// follow reads the address the registry redirected a read to: over HTTPS,
// without the token or any other header of the registry's request, and no
// further. The answer is the registry's by its own word, given over the
// authenticated connection. Plain HTTP is followed only from a registry
// that is itself plain HTTP, which is a loopback one, to loopback.
func (c Client) follow(ctx context.Context, from *url.URL, location string) (*http.Response, error) {
	target, err := from.Parse(location)
	if err != nil || target.Host == "" || target.User != nil {
		return nil, fmt.Errorf("the distro package registry redirected a read to an invalid address: %w", errs.ErrValidation)
	}

	if target.Scheme != "https" && (target.Scheme != plainHTTP || from.Scheme != plainHTTP || !isLoopback(target.Hostname())) {
		return nil, fmt.Errorf("the distro package registry redirected a read to an address that is not HTTPS: %w", errs.ErrInvalidConfig)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}

	response, err := c.client().Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		return nil, fmt.Errorf("distro package registry storage request failed: %w: %w", httpretry.SafeError(err), errs.RequestFailure(err))
	}

	return response, nil
}

// client follows no redirect by itself: get follows one, by hand.
func (c Client) client() *http.Client {
	client := *httpretry.NewClient(requestTimeout, nil)
	if c.HTTP != nil {
		client = *c.HTTP
	}

	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	return &client
}

// do sends one request to the registry with its token. No redirect is
// followed here, so the token and the package never go to another authority.
func (c Client) do(ctx context.Context, method string, registry provider.ForgeDistroRegistry, steps []string, body io.Reader, size int64) (*http.Response, error) {
	address, err := registryURL(registry.URL, steps)
	if err != nil {
		return nil, err
	}

	if registry.Token == "" {
		return nil, fmt.Errorf("distro package registry token is required: %w", errs.ErrPermissionDenied)
	}

	request, err := http.NewRequestWithContext(ctx, method, address, body)
	if err != nil {
		return nil, err
	}

	if body != nil {
		request.ContentLength = size
		request.Header.Set("Content-Type", "application/octet-stream")
	}

	request.Header.Set("Authorization", "token "+registry.Token)

	response, err := c.client().Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		return nil, fmt.Errorf("distro package registry request failed: %w: %w", httpretry.SafeError(err), errs.RequestFailure(err))
	}

	return response, nil
}

// CheckRegistry refuses a registry address no package or token is sent to:
// one that is not HTTPS, outside loopback, or that carries credentials. It
// makes no request, so a release is refused for it before it is signed.
func CheckRegistry(registry provider.ForgeDistroRegistry) error {
	_, err := registryURL(registry.URL, nil)

	return err
}

func registryURL(registry string, steps []string) (string, error) {
	address, err := url.Parse(registry)
	if err != nil || address.Host == "" || address.User != nil || address.RawQuery != "" || address.Fragment != "" {
		return "", fmt.Errorf("invalid distro package registry URL: %w", errs.ErrInvalidConfig)
	}

	if address.Scheme != "https" && (address.Scheme != plainHTTP || !isLoopback(address.Hostname())) {
		return "", fmt.Errorf("distro package registry requires HTTPS: %w", errs.ErrInvalidConfig)
	}

	for _, step := range steps {
		if step == "" || step == "." || step == ".." || strings.ContainsAny(step, "/\\?#%\r\n") {
			return "", fmt.Errorf("invalid distro package registry path step %q: %w", step, errs.ErrInvalidConfig)
		}
	}

	// The decoded path: URL.String escapes what a step holds.
	address.Path = path.Join(append([]string{address.Path}, steps...)...)
	address.RawPath = ""

	return address.String(), nil
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)

	return host == "localhost" || ip != nil && ip.IsLoopback()
}

func statusError(operation string, response *http.Response) error {
	classification := errs.FromHTTPStatus(response.StatusCode)
	if classification == nil {
		classification = errs.ErrValidation
	}

	return fmt.Errorf("distro package registry %s returned HTTP %d: %w", operation, response.StatusCode, classification)
}

func drain(response *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	_ = response.Body.Close()
}
