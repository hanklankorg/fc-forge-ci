// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/diggsweden/forge-ci/internal/archive"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/diggsweden/forge-ci/internal/domain/sbom"
	"github.com/diggsweden/forge-ci/internal/domain/version"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// SBOMBundleInput drives `forge-ci release sbom-bundle`.
type SBOMBundleInput struct {
	ProjectName   string
	Version       string
	WorkingDir    string // default: cwd; root for *-sbom.{spdx,cyclonedx}.json globs
	SBOMDir       string // default: ./sbom-artifacts; analyzed-container SBOMs are flattened (no path)
	SignArtifacts bool   // when true (and signer != nil) emit <bundle>.asc
	AssemblyFile  string // when set, bundle exactly the staged SBOM inputs
}

// SBOMBundleResult reports what CreateSBOMBundle wrote.
type SBOMBundleResult struct {
	BundleName string // empty when no SBOMs were found
	EntryCount int
	Signed     bool
}

// CreateSBOMBundle bundles every *-sbom.{spdx,cyclonedx}.json in the working
// directory plus every analyzed-container SBOM under SBOMDir into a single
// gzip-compressed tar. Returns BundleName="" when nothing was found. signer is
// consulted only when in.SignArtifacts is set and signer is non-nil.
func CreateSBOMBundle(ctx context.Context, signer Signer, in SBOMBundleInput, out io.Writer) (*SBOMBundleResult, error) {
	if err := checkSBOMBundleSigner(signer, in.SignArtifacts); err != nil {
		return nil, err
	}

	if in.AssemblyFile != "" {
		return createSBOMBundleFromAssembly(ctx, signer, in, out)
	}

	in, ver := withSBOMBundleDefaults(in)

	wdMatches, containerMatches, err := discoverSBOMs(in.WorkingDir, in.SBOMDir, in.ProjectName)
	if err != nil {
		return nil, err
	}

	if len(wdMatches)+len(containerMatches) == 0 {
		_, _ = fmt.Fprintln(out, "No SBOMs found, skipping bundle creation")

		return &SBOMBundleResult{}, nil
	}

	bundleName := sbom.BundleName(in.ProjectName, ver)

	_, _ = fmt.Fprintln(out, "Creating SBOM bundle with all 3 layers")

	count, err := writeSBOMBundle(bundleName, in.WorkingDir, wdMatches, containerMatches, out)
	if err != nil {
		return nil, err
	}

	res := &SBOMBundleResult{BundleName: bundleName, EntryCount: count}

	if in.SignArtifacts {
		if err := signSBOMBundle(ctx, signer, bundleName, out); err != nil {
			return res, err
		}

		res.Signed = true
	}

	_, _ = fmt.Fprintf(out, "Created SBOM bundle: %s\n", bundleName)

	return res, nil
}

func createSBOMBundleFromAssembly(ctx context.Context, signer Signer, in SBOMBundleInput, out io.Writer) (*SBOMBundleResult, error) {
	asm, err := readAssembly(in.AssemblyFile)
	if err != nil {
		return nil, err
	}

	if len(asm.SBOMs) == 0 {
		_, _ = fmt.Fprintln(out, "No SBOMs found in release assembly, skipping bundle creation")

		return &SBOMBundleResult{}, nil
	}

	bundleName := asm.SBOMBundleFile
	if bundleName == "" {
		ver := in.Version
		if ver == "" {
			ver = "unknown"
		}

		bundleName = sbom.BundleName(in.ProjectName, version.StripVPrefix(ver))
	}

	if mkdirErr := os.MkdirAll(filepath.Dir(bundleName), 0o755); mkdirErr != nil { //nolint:gosec // public release staging dir.
		return nil, fmt.Errorf("create SBOM bundle dir for %q: %w", bundleName, mkdirErr)
	}

	_, _ = fmt.Fprintln(out, "Creating SBOM bundle from release assembly")

	count, err := writeAssemblySBOMBundle(bundleName, asm.SBOMs, out)
	if err != nil {
		return nil, err
	}

	res := &SBOMBundleResult{BundleName: bundleName, EntryCount: count}
	if in.SignArtifacts {
		if err := signSBOMBundle(ctx, signer, bundleName, out); err != nil {
			return res, err
		}

		res.Signed = true
	}

	_, _ = fmt.Fprintf(out, "Created SBOM bundle: %s\n", bundleName)

	return res, nil
}

// discoverSBOMs lists SBOMs in the working dir (keeps relative path) and
// in the container SBOM dir (flattened later when written into the bundle).
// First return is working-dir matches; second is container matches.
//
// When projectName is set, only SBOMs named "<projectName>-*" are taken.
// The bundle is published and signed under the project's name, so an
// unscoped glob would sweep any *-sbom.*.json another tool left in the
// working directory into the release. Matching on the basename prefix
// rather than splicing projectName into the glob keeps a name carrying
// glob metacharacters inert.
func discoverSBOMs(workingDir, sbomDir, projectName string) ([]string, []string, error) {
	wdMatches, err := globAll(workingDir, domainrelease.SBOMFilePatterns)
	if err != nil {
		return nil, nil, err
	}

	containerMatches, err := globAll(sbomDir, []string{domainrelease.AnalyzedContainerSBOMPattern})
	if err != nil {
		return nil, nil, err
	}

	return scopeToProject(wdMatches, projectName), scopeToProject(containerMatches, projectName), nil
}

// scopeToProject keeps the matches whose basename belongs to projectName.
// An empty projectName keeps every match (the caller asked for no scope).
func scopeToProject(matches []string, projectName string) []string {
	if projectName == "" {
		return matches
	}

	kept := matches[:0]

	for _, match := range matches {
		if strings.HasPrefix(filepath.Base(match), projectName+"-") {
			kept = append(kept, match)
		}
	}

	return kept
}

// writeSBOMBundle writes wdMatches (path relative to workingDir) and
// containerMatches (basename only) into bundleName.
func writeSBOMBundle(bundleName, workingDir string, wdMatches, containerMatches []string, out io.Writer) (int, error) {
	return writeBundleAtomic(bundleName, func(bundle *archive.TarGzipWriter) (int, error) {
		count := 0

		for _, m := range wdMatches {
			rel, err := filepath.Rel(workingDir, m)
			if err != nil {
				rel = filepath.Base(m)
			}

			if err := addToBundle(bundle, m, rel); err != nil {
				return 0, err
			}

			_, _ = fmt.Fprintf(out, "  Added: %s\n", rel)

			count++
		}

		for _, m := range containerMatches {
			base := filepath.Base(m)
			if err := addToBundle(bundle, m, base); err != nil {
				return 0, err
			}

			_, _ = fmt.Fprintf(out, "  Added: %s\n", base)

			count++
		}

		return count, nil
	})
}

func writeAssemblySBOMBundle(bundleName string, entries []domainrelease.AssemblyFile, out io.Writer) (int, error) {
	return writeBundleAtomic(bundleName, func(bundle *archive.TarGzipWriter) (int, error) {
		count := 0

		for _, entry := range entries {
			if !regularFileExists(entry.Path) {
				return 0, fmt.Errorf("assembly SBOM input %q is missing or not a regular file: %w", entry.Path, errs.ErrMissingInput)
			}

			name := entry.Name
			if name == "" {
				name = filepath.Base(entry.Path)
			}

			if err := addToBundle(bundle, entry.Path, name); err != nil {
				return 0, err
			}

			_, _ = fmt.Fprintf(out, "  Added: %s\n", name)

			count++
		}

		return count, nil
	})
}

// writeBundleAtomic builds the bundle in a temporary file next to bundleName
// and renames it into place only after fill and both closes succeed, so a
// failed run never leaves a partial bundle behind for a later upload glob to
// publish.
func writeBundleAtomic(bundleName string, fill func(*archive.TarGzipWriter) (int, error)) (int, error) {
	tmp, err := os.CreateTemp(filepath.Dir(bundleName), "."+filepath.Base(bundleName)+".*.tmp")
	if err != nil {
		return 0, fmt.Errorf("create %q: %w", bundleName, err)
	}

	fail := func(err error) (int, error) {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())

		return 0, err
	}

	bundle := archive.NewTarGzipWriter(tmp)

	count, err := fill(bundle)
	if err != nil {
		return fail(err)
	}

	if err := bundle.Close(); err != nil {
		return fail(fmt.Errorf("close SBOM bundle: %w", err))
	}

	// CreateTemp creates 0600; the bundle is a published release asset.
	if err := tmp.Chmod(0o644); err != nil {
		return fail(fmt.Errorf("chmod %q: %w", bundleName, err))
	}

	if err := tmp.Close(); err != nil {
		return fail(fmt.Errorf("close SBOM bundle file: %w", err))
	}

	if err := os.Rename(tmp.Name(), bundleName); err != nil {
		return fail(fmt.Errorf("move SBOM bundle into %q: %w", bundleName, err))
	}

	return count, nil
}

// withSBOMBundleDefaults fills the discovery directories and returns the version
// used in the bundle name, without a v prefix.
func withSBOMBundleDefaults(in SBOMBundleInput) (SBOMBundleInput, string) {
	if in.WorkingDir == "" {
		in.WorkingDir = "."
	}

	if in.SBOMDir == "" {
		in.SBOMDir = domainrelease.DefaultSBOMArtifactsDir
	}

	ver := in.Version
	if ver == "" {
		ver = "unknown"
	}

	return in, version.StripVPrefix(ver)
}

// checkSBOMBundleSigner checks a signing request before any SBOM is read or the
// bundle written, so a missing or misconfigured signer leaves no unsigned
// bundle behind.
func checkSBOMBundleSigner(signer Signer, sign bool) error {
	if !sign {
		return nil
	}

	if signer == nil {
		return fmt.Errorf("sbom-bundle: signing requested but no signer is configured: %w", errs.ErrUsage)
	}

	_, err := signerExtensions(signer)

	return err
}

func signSBOMBundle(ctx context.Context, signer Signer, bundleName string, out io.Writer) error {
	if err := signFileWithSidecars(ctx, signer, bundleName); err != nil {
		return fmt.Errorf("sign sbom bundle: %w", err)
	}

	_, _ = fmt.Fprintf(out, "Signed SBOM bundle: %s\n", bundleName)

	return nil
}

func globAll(dir string, patterns []string) ([]string, error) {
	root, err := pathsafe.OpenRoot(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	defer func() { _ = root.Close() }()

	var out []string

	for _, p := range patterns {
		matches, err := fs.Glob(root.FS(), p)
		if err != nil {
			return nil, fmt.Errorf("glob %q: %w", p, err)
		}

		for _, name := range matches {
			path := filepath.Join(dir, filepath.FromSlash(name))

			info, err := root.Lstat(filepath.FromSlash(name))
			if err != nil {
				return nil, err
			}

			if info.IsDir() {
				continue
			}

			if err := validateReleasePathComponents(path); err != nil {
				return nil, err
			}

			out = append(out, path)
		}
	}

	return out, nil
}

func addToBundle(bundle *archive.TarGzipWriter, srcPath, archivePath string) error {
	file, err := openReleaseFile(srcPath)
	if err != nil {
		return fmt.Errorf("open %q: %w", srcPath, err)
	}

	defer func() { _ = file.Close() }()

	return bundle.AddFile(archivePath, file)
}
