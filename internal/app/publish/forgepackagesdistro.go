// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// DistroPackageRegistry implements the Debian, RPM and Alpine registry
// protocols; the registry's location and token come from the forge's
// resolver role.
type DistroPackageRegistry interface {
	// Upload sends the package once. created is false when the registry
	// refused it because it holds that package version at that place.
	Upload(ctx context.Context, registry provider.ForgeDistroRegistry, pkg provider.DistroPackage, body io.Reader) (created bool, err error)
	// Listing reports whether the registry serves a file with the package's
	// SHA-256 at the package's place.
	Listing(ctx context.Context, registry provider.ForgeDistroRegistry, pkg provider.DistroPackage) (provider.DistroListing, error)
}

// defaultListingWait is how long a package the registry refused as held is
// awaited in what the registry serves: another run that published it has
// its upload answered after the indexes are rebuilt, and this run's refusal
// can come before that.
const defaultListingWait = 30 * time.Second

// ForgePackagesDistroInput drives ForgePackagesDistroPublish.
type ForgePackagesDistroInput struct {
	// Files are the release's files, as its manifest names them.
	Files []string
	// Places is the resolved distro-packages block.
	Places config.DistroPackages
	// ListingWait replaces defaultListingWait when it is set.
	ListingWait time.Duration
}

// ForgePackagesDistroAdmit checks, without a registry or a token, that every
// package among the release's files can be published, and counts them. The
// signing job runs it before anything is signed or published, so a release
// is not public when a package of it is found unpublishable.
func ForgePackagesDistroAdmit(in ForgePackagesDistroInput) (int, error) {
	packages, err := distroPackages(in)

	return len(packages), err
}

// ForgePackagesDistroPublish publishes the release's .deb, .rpm and .apk
// files to the forge's Debian, RPM and Alpine registries. Each file is sent
// as it is; the registry is then read back, and has to serve that file's
// SHA-256 at its place. So a package an earlier run published is left as it
// is when its content is this file's, the same version with other content
// is refused, and so is a registry that stores other bytes than it was sent.
// Nothing is replaced or deleted.
func ForgePackagesDistroPublish(ctx context.Context, client DistroPackageRegistry, resolver provider.ForgeDistroRegistryResolver, out io.Writer, in ForgePackagesDistroInput) error {
	packages, err := distroPackages(in)
	if err != nil {
		return err
	}

	registry, err := resolver.ResolveForgeDistroRegistry()
	if err != nil {
		return err
	}

	if registry.Token == "" {
		return fmt.Errorf("a token that may write the owner's packages is required to publish distro packages: %w", errs.ErrPermissionDenied)
	}

	wait := in.ListingWait
	if wait <= 0 {
		wait = defaultListingWait
	}

	for index, pkg := range packages {
		published, publishErr := publishDistroPackage(ctx, client, registry, pkg, wait)
		if publishErr != nil {
			return fmt.Errorf("publication stopped after %d/%d packages; nothing was replaced, and a rerun leaves the published ones as they are: %w", index, len(packages), publishErr)
		}

		_, _ = fmt.Fprintf(out, "%s %s (sha256:%s) at %s %s\n", published, pkg.Path, pkg.SHA256, pkg.Format, strings.Join(pkg.Place, "/"))
	}

	return nil
}

func publishDistroPackage(ctx context.Context, client DistroPackageRegistry, registry provider.ForgeDistroRegistry, pkg provider.DistroPackage, wait time.Duration) (string, error) {
	file, err := os.Open(pkg.Path)
	if err != nil {
		return "", err
	}

	created, err := client.Upload(ctx, registry, pkg, file)
	_ = file.Close()

	if err != nil {
		return "", fmt.Errorf("publish %s: %w", pkg.Path, err)
	}

	// The registry answers a created package after its indexes are rebuilt,
	// so that one is read once. A refused one may be another run's, whose
	// rebuild is not done: it is awaited.
	if created {
		wait = 0
	}

	listing, err := awaitDistroListing(ctx, client, registry, pkg, wait)
	if err != nil {
		return "", fmt.Errorf("read the registry back for %s: %w", pkg.Path, err)
	}

	place := string(pkg.Format) + " " + strings.Join(pkg.Place, "/")

	switch {
	case listing == provider.DistroListed && created:
		return "Published", nil
	case listing == provider.DistroListed:
		return "Already published with this content:", nil
	case created:
		return "", fmt.Errorf("the registry accepted %s at %s but does not serve the release's file (sha256:%s): a registry that signs or rewrites a package on upload publishes other bytes than the release signed; the stored package was not removed: %w",
			pkg.Path, place, pkg.SHA256, errs.ErrValidation)
	case listing == provider.DistroOtherContent:
		return "", fmt.Errorf("the registry already holds the package version of %s at %s with other content than the release's file (sha256:%s); a published version is never replaced: %w",
			pkg.Path, place, pkg.SHA256, errs.ErrValidation)
	default:
		return "", fmt.Errorf("the registry refused %s as a package version it already holds at %s, and after %s does not list the release's file (sha256:%s): either another run published it and the registry does not list it yet, and a rerun succeeds, or the version is there with other content, and a rerun fails the same way; nothing was replaced: %w",
			pkg.Path, place, wait, pkg.SHA256, errs.ErrValidation)
	}
}

// awaitDistroListing reads the registry until it lists the package, serves
// other content in its place, or wait is over.
func awaitDistroListing(ctx context.Context, client DistroPackageRegistry, registry provider.ForgeDistroRegistry, pkg provider.DistroPackage, wait time.Duration) (provider.DistroListing, error) {
	deadline := time.Now().Add(wait)

	for {
		listing, err := client.Listing(ctx, registry, pkg)
		if err != nil || listing != provider.DistroNotListed || !time.Now().Before(deadline) {
			return listing, err
		}

		select {
		case <-ctx.Done():
			return listing, ctx.Err()
		case <-time.After(wait / 10):
		}
	}
}

// distroPackages describes every package among the release's files before
// anything is sent, so an unpublishable file stops the release's packages
// together and not after some of them are public.
func distroPackages(in ForgePackagesDistroInput) ([]provider.DistroPackage, error) {
	var packages []provider.DistroPackage

	for _, path := range in.Files {
		pkg := provider.DistroPackage{Path: path}

		switch filepath.Ext(path) {
		case ".deb":
			pkg.Format, pkg.Place = provider.DistroPackageDebian, []string{in.Places.Debian.Distribution, in.Places.Debian.Component}
		case ".rpm":
			pkg.Format, pkg.Place = provider.DistroPackageRPM, []string{in.Places.RPM.Group}
		case ".apk":
			pkg.Format, pkg.Place = provider.DistroPackageAlpine, []string{in.Places.Alpine.Branch, in.Places.Alpine.Repository}
		default:
			continue
		}

		if slices.Contains(pkg.Place, "") {
			return nil, fmt.Errorf("distro-packages names no %s place for %s: %w", pkg.Format, path, errs.ErrInvalidConfig)
		}

		digest, size, err := pathsafe.HashRegularFile(path)
		if err != nil {
			return nil, fmt.Errorf("distro package %s: %w", path, err)
		}

		pkg.SHA256, pkg.Size = hex.EncodeToString(digest), size

		if pkg.Format == provider.DistroPackageAlpine {
			if err := readAlpineIdentity(&pkg); err != nil {
				return nil, err
			}
		}

		packages = append(packages, pkg)
	}

	if len(packages) == 0 {
		return nil, fmt.Errorf("distro-packages is declared, but the release has no .deb, .rpm or .apk file: %w", errs.ErrMissingInput)
	}

	return packages, nil
}

// alpineInfoLimit bounds what is read of a package to find its .PKGINFO,
// which the format puts in the first kilobytes.
const alpineInfoLimit = 4 << 20

// readAlpineIdentity reads the name, version and architecture an Alpine
// package states in its .PKGINFO: the registry files the package under
// them, so they are where it is read back. A noarch package is refused: the
// registry files it under architectures of its own choosing.
func readAlpineIdentity(pkg *provider.DistroPackage) error {
	file, err := os.Open(pkg.Path)
	if err != nil {
		return err
	}

	defer func() { _ = file.Close() }()

	// Every .apk release file is taken for an Alpine package; an Android
	// package, which shares the suffix, ends here.
	malformed := fmt.Errorf("%s is no Alpine package with a readable .PKGINFO (pkgname, pkgver, arch); an Android .apk cannot be released beside distro-packages: %w", pkg.Path, errs.ErrMalformedInput)

	// An apk is gzip streams of one tar: the signature, the control segment
	// that holds .PKGINFO, then the data.
	stream, err := gzip.NewReader(bufio.NewReader(file))
	if err != nil {
		return malformed
	}

	archive := tar.NewReader(io.LimitReader(stream, alpineInfoLimit))

	for {
		header, nextErr := archive.Next()
		if nextErr != nil {
			return malformed
		}

		if header.Name == ".PKGINFO" {
			break
		}
	}

	fields := map[string]string{}
	lines := bufio.NewScanner(archive)

	for lines.Scan() {
		if key, value, found := strings.Cut(lines.Text(), " = "); found {
			fields[key] = value
		}
	}

	pkg.Name, pkg.Version, pkg.Architecture = fields["pkgname"], fields["pkgver"], fields["arch"]

	for _, value := range []string{pkg.Name, pkg.Version, pkg.Architecture} {
		if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\ \t\r") {
			return errors.Join(malformed, lines.Err())
		}
	}

	if pkg.Architecture == "noarch" {
		return fmt.Errorf("alpine package %s is noarch, which the registry files under architectures of its own choosing; publish a package for each architecture: %w", pkg.Path, errs.ErrUnsupported)
	}

	return nil
}
