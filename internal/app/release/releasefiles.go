// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provenance"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// Release file manifest defaults shared by the `release files` commands.
const (
	// DefaultReleaseFilesManifest is the default release file manifest path.
	DefaultReleaseFilesManifest   = "dist/release-files.json"
	defaultReleaseDistDir         = "dist"
	releaseFilesVersion           = 1
	releaseFilesSectionAssets     = "assets"
	releaseFilesSectionChecksums  = "checksums"
	releaseFilesSectionSBOMs      = "sboms"
	releaseFilesSectionEvidence   = "evidence"
	releaseFilesSectionProvenance = "provenance"

	// ProvenanceStatementName is the file name of the SLSA provenance
	// statement the engine writes into the dist directory.
	ProvenanceStatementName = "slsa-provenance.intoto.json"
)

// releaseSignature is one kind of signature file a signing verb writes next
// to a release file.
type releaseSignature struct{ suffix, source string }

// releaseSignatures are the signature files the signing verbs write: a cosign
// bundle, and the detached signature of a distro package. Finalization
// publishes them for declared files, and nothing else decides what a signature
// file is.
func releaseSignatures() []releaseSignature {
	return []releaseSignature{
		{suffix: ".bundle", source: "signature_bundle"},
		{suffix: ".sig", source: "package_signature"},
	}
}

// FileEntry is one classified release file in the manifest.
type FileEntry struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Source string `json:"source"`
}

// FileAssets is the `release files collect` output: the versioned
// list of publishable release assets.
type FileAssets struct {
	Version int         `json:"version"`
	Assets  []FileEntry `json:"assets"`
}

// FileManifest is the versioned release file manifest written by
// WriteReleaseFileManifest and consumed by the sign/publish steps.
type FileManifest struct {
	Version    int         `json:"version"`
	Assets     []FileEntry `json:"assets"`
	Checksums  []FileEntry `json:"checksums"`
	SBOMs      []FileEntry `json:"sboms"`
	Evidence   []FileEntry `json:"evidence"`
	Provenance []FileEntry `json:"provenance"`
}

// FilesInput is the shared dist-dir/manifest pair driving the
// `release files` commands.
type FilesInput struct {
	DistDir      string
	ManifestFile string
	// ManifestExplicit says the caller named ManifestFile instead of taking
	// the default path. A named manifest has to exist: only an absent default
	// manifest selects GoReleaser discovery.
	ManifestExplicit bool
}

// ValidateReleaseFileManifestInput drives ValidateReleaseFileManifest.
type ValidateReleaseFileManifestInput struct {
	FilesInput
	// Unsigned holds the manifest and its dist to the state a producer hands
	// over, before any signing verb has run.
	Unsigned bool
}

// WriteReleaseFileManifestInput drives WriteReleaseFileManifest.
type WriteReleaseFileManifestInput struct {
	FilesInput
	OutputFile     string
	AssetsJSONFile string
}

// ImportGoReleaserFileManifestInput selects additional evidence to sign alongside
// the GoReleaser assets. Import happens before signing creates provenance.
type ImportGoReleaserFileManifestInput struct {
	FilesInput
	EvidenceFiles []string
}

// ListReleaseFilesInput drives ListReleaseFiles.
type ListReleaseFilesInput struct {
	FilesInput
	Section string
}

// ValidateReleaseChecksumsInput drives ValidateReleaseChecksums.
type ValidateReleaseChecksumsInput struct {
	FilesInput
	ChecksumsFile string
}

// CollectReleaseAssets discovers the publishable release assets from
// GoReleaser metadata and signed sidecars under the dist directory.
func CollectReleaseAssets(in FilesInput) (*FileAssets, error) {
	ctx := releaseFilesContext(in)

	assets, err := collectReleaseAssetEntries(ctx)
	if err != nil {
		return nil, err
	}

	return &FileAssets{Version: releaseFilesVersion, Assets: assets}, nil
}

// WriteReleaseFileManifest writes (and re-validates) the versioned release
// file manifest, collecting assets when no pre-collected JSON is supplied.
func WriteReleaseFileManifest(in WriteReleaseFileManifestInput) (*FileManifest, error) {
	ctx := releaseFilesContext(in.FilesInput)

	output := strings.TrimSpace(in.OutputFile)
	if output == "" {
		output = ctx.manifestFile
	}

	manifest, err := prepareReleaseFileManifest(ctx, in.AssetsJSONFile)
	if err != nil {
		return nil, err
	}

	return writeReleaseFileManifest(ctx, manifest, output)
}

func writeReleaseFileManifest(ctx releaseFilesCtx, manifest *FileManifest, output string) (*FileManifest, error) {
	if validationErr := validateReleaseFileManifest(ctx, manifest); validationErr != nil {
		return nil, validationErr
	}

	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal release file manifest: %w", err)
	}

	body = append(body, '\n')

	if output != cliio.StdSentinel {
		if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil { //nolint:gosec,mnd // release metadata under workspace.
			return nil, fmt.Errorf("create release file manifest directory: %w", err)
		}
	}

	if err := cliio.WriteFile(output, body, 0o644); err != nil { //nolint:gosec // public release metadata.
		return nil, fmt.Errorf("write release file manifest %s: %w", output, err)
	}

	return manifest, nil
}

// ImportGoReleaserFileManifest writes the unsigned file set from GoReleaser
// metadata and explicit evidence. It refuses to overwrite an existing manifest;
// WriteReleaseFileManifest finalizes that file set after signing.
func ImportGoReleaserFileManifest(in ImportGoReleaserFileManifestInput) (*FileManifest, error) {
	ctx := releaseFilesContext(in.FilesInput)
	if _, err := os.Lstat(ctx.manifestFile); err == nil {
		return nil, fmt.Errorf("release file manifest already exists: %s: %w", ctx.manifestFile, errs.ErrValidation)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stat release file manifest %s: %w", ctx.manifestFile, err)
	}

	artifacts, err := readGoReleaserArtifacts(ctx)
	if err != nil {
		return nil, err
	}

	collector := &releaseFileCollector{}
	if addErr := addGoReleaserAssetEntries(ctx, collector, artifacts); addErr != nil {
		return nil, addErr
	}

	evidence, err := addEvidenceEntries(ctx, collector, in.EvidenceFiles)
	if err != nil {
		return nil, err
	}

	manifest, err := buildGoReleaserFileManifest(ctx, collector.entries)
	if err != nil {
		return nil, err
	}

	manifest.Evidence = evidence

	if err := validateUnsignedReleaseFiles(ctx, manifest); err != nil {
		return nil, err
	}

	return writeReleaseFileManifest(ctx, manifest, ctx.manifestFile)
}

// addEvidenceEntries adds the named evidence files to the collected assets.
// Evidence is an additional file: it cannot reclassify a collected asset.
func addEvidenceEntries(ctx releaseFilesCtx, collector *releaseFileCollector, paths []string) ([]FileEntry, error) {
	evidence, err := entriesFromPaths(ctx, releaseFilesSectionEvidence, paths)
	if err != nil {
		return nil, err
	}

	for _, entry := range evidence {
		if _, exists := collector.paths[entry.Path]; exists {
			return nil, fmt.Errorf("evidence must be an additional release file: %s: %w", entry.Path, errs.ErrValidation)
		}

		if addErr := collector.add(ctx, entry.Path, entry.Source); addErr != nil {
			return nil, addErr
		}
	}

	return evidence, nil
}

func prepareReleaseFileManifest(ctx releaseFilesCtx, assetsJSONFile string) (*FileManifest, error) {
	existingValue, preserve, err := releaseFileManifestForRewrite(ctx, assetsJSONFile)
	if err != nil {
		return nil, err
	}

	if preserve {
		return finalizeReleaseFileManifest(ctx, &existingValue)
	}

	assets, err := resolveManifestAssets(ctx, assetsJSONFile)
	if err != nil {
		return nil, err
	}

	return buildGoReleaserFileManifest(ctx, assets)
}

// releaseFileManifestForRewrite returns an existing explicit manifest as the
// classification source for finalization. An assets JSON input remains an
// explicit replacement and preserves the original fresh-write behavior.
func releaseFileManifestForRewrite(ctx releaseFilesCtx, assetsJSONFile string) (FileManifest, bool, error) {
	if assetsJSONFile != "" {
		return FileManifest{}, false, nil
	}

	manifest, err := ctx.validManifest()
	if err != nil || manifest == nil {
		return FileManifest{}, false, err
	}

	return *manifest, true, nil
}

// resolveManifestAssets returns the pre-collected assets from assetsJSONFile
// when supplied and collects them from the dist directory otherwise.
func resolveManifestAssets(ctx releaseFilesCtx, assetsJSONFile string) ([]FileEntry, error) {
	if assetsJSONFile != "" {
		return readReleaseAssetsJSON(assetsJSONFile)
	}

	return collectReleaseAssetEntries(ctx)
}

// ValidateReleaseFileManifest reads the release file manifest and checks
// every structural invariant (version, entry shape, classification). With
// Unsigned it also refuses a manifest or a dist that already carries a
// signature or a provenance statement.
func ValidateReleaseFileManifest(in ValidateReleaseFileManifestInput) error {
	ctx := releaseFilesContext(in.FilesInput)

	manifest, err := readReleaseFileManifest(ctx.manifestFile)
	if err != nil {
		return err
	}

	if err := validateReleaseFileManifest(ctx, manifest); err != nil {
		return err
	}

	if in.Unsigned {
		return validateUnsignedReleaseFiles(ctx, manifest)
	}

	return nil
}

// ListReleaseFiles prints the release file paths for one manifest section,
// falling back to on-disk discovery when no manifest exists yet.
func ListReleaseFiles(in ListReleaseFilesInput) ([]string, error) {
	ctx := releaseFilesContext(in.FilesInput)

	section := strings.TrimSpace(in.Section)
	if section == "" {
		section = releaseFilesSectionAssets
	}

	manifest, err := ctx.validManifest()
	if err != nil {
		return nil, err
	}

	if manifest == nil {
		return discoverReleaseFilesSection(ctx, section)
	}

	entries, err := manifestSection(manifest, section)
	if err != nil {
		return nil, err
	}

	return entryPaths(entries), nil
}

// PublishableReleaseFiles returns the release file paths `release publish`
// uploads. A manifest has to be finalized: a producer's unsigned manifest
// names evidence without the bundle that covers it and no provenance
// statement. Without a manifest the assets are discovered, as ListReleaseFiles
// does.
func PublishableReleaseFiles(in FilesInput) ([]string, error) {
	ctx := releaseFilesContext(in)

	manifest, err := ctx.validManifest()
	if err != nil {
		return nil, err
	}

	if manifest == nil {
		return discoverAssetSectionPaths(ctx)
	}

	if err := requireFinalizedManifest(ctx, manifest); err != nil {
		return nil, err
	}

	return entryPaths(manifest.Assets), nil
}

// requireFinalizedManifest refuses a manifest finalization has not written.
// Finalization declares the provenance statement with its bundle and the
// bundle of every evidence file; those are the published files no checksum
// line covers.
func requireFinalizedManifest(ctx releaseFilesCtx, manifest *FileManifest) error {
	const hint = "run `release files manifest` after signing"

	if len(manifest.Provenance) == 0 {
		return fmt.Errorf("release file manifest %s is not finalized: it declares no provenance statement; %s: %w", ctx.manifestFile, hint, errs.ErrValidation)
	}

	declared := make(map[string]struct{}, len(manifest.Assets))
	for _, asset := range manifest.Assets {
		declared[filepath.Clean(asset.Path)] = struct{}{}
	}

	for _, signed := range append([]string{ctx.provenancePath()}, entryPaths(manifest.Evidence)...) {
		bundle := filepath.Clean(signed) + ".bundle"
		if _, ok := declared[bundle]; !ok {
			return fmt.Errorf("release file manifest %s is not finalized: it declares %s without its signature bundle %s; %s: %w", ctx.manifestFile, signed, bundle, hint, errs.ErrValidation)
		}
	}

	return nil
}

// discoverReleaseFilesSection lists one release files section by on-disk
// discovery when no manifest has been written yet.
func discoverReleaseFilesSection(ctx releaseFilesCtx, section string) ([]string, error) {
	switch section {
	case releaseFilesSectionAssets:
		return discoverAssetSectionPaths(ctx)
	case releaseFilesSectionChecksums:
		return discoverChecksumSectionPaths(ctx)
	case releaseFilesSectionSBOMs:
		return discoverSBOMSectionPaths(ctx)
	case releaseFilesSectionEvidence:
		return nil, nil
	case releaseFilesSectionProvenance:
		return discoverProvenanceSectionPaths(ctx), nil
	default:
		return nil, fmt.Errorf("release files section must be one of assets, checksums, sboms, evidence, provenance (got %q): %w", section, errs.ErrUsage)
	}
}

// discoverAssetSectionPaths lists the public release asset paths found on disk.
func discoverAssetSectionPaths(ctx releaseFilesCtx) ([]string, error) {
	assets, err := collectReleaseAssetEntries(ctx)
	if err != nil {
		return nil, err
	}

	return entryPaths(assets), nil
}

// discoverChecksumSectionPaths lists the single on-disk checksums file.
func discoverChecksumSectionPaths(ctx releaseFilesCtx) ([]string, error) {
	checksum, err := discoverSingleChecksum(ctx)
	if err != nil {
		return nil, err
	}

	return []string{checksum}, nil
}

// discoverSBOMSectionPaths lists the dist SBOM paths found on disk.
func discoverSBOMSectionPaths(ctx releaseFilesCtx) ([]string, error) {
	sboms, err := discoverDistSBOMEntries(ctx)
	if err != nil {
		return nil, err
	}

	return entryPaths(sboms), nil
}

// discoverProvenanceSectionPaths lists the provenance envelope when present.
func discoverProvenanceSectionPaths(ctx releaseFilesCtx) []string {
	if !regularReleaseFile(ctx.provenancePath()) {
		return nil
	}

	return []string{ctx.provenancePath()}
}

// FindReleaseChecksumFile returns the single release checksums file, from the
// manifest when available and by dist-dir discovery otherwise.
func FindReleaseChecksumFile(in FilesInput) (string, error) {
	ctx := releaseFilesContext(in)

	manifest, err := ctx.validManifest()
	if err != nil {
		return "", err
	}

	if manifest == nil {
		return discoverSingleChecksum(ctx)
	}

	// A valid manifest holds exactly one checksums file.
	return manifest.Checksums[0].Path, nil
}

// ValidateReleaseChecksums checks that the checksums file names every public
// release asset exactly once, nothing else, and records each file's actual
// SHA-256 digest.
func ValidateReleaseChecksums(in ValidateReleaseChecksumsInput) error {
	_, _, err := validatedReleaseChecksums(in.FilesInput, in.ChecksumsFile)

	return err
}

// validatedReleaseChecksums is the one validation behind the checksums verbs
// and --checksums-from-manifest. The file is read once, through the opener
// that refuses a link, and the returned content is the bytes that were
// validated. An empty checksumFile selects the release's single checksums file.
func validatedReleaseChecksums(in FilesInput, checksumFile string) (string, []byte, error) {
	ctx := releaseFilesContext(in)

	checksumFile = strings.TrimSpace(checksumFile)
	if checksumFile == "" {
		var err error

		checksumFile, err = FindReleaseChecksumFile(in)
		if err != nil {
			return "", nil, err
		}
	}

	body, err := readReleaseFile(checksumFile)
	if err != nil {
		return "", nil, fmt.Errorf("checksum file: %w", err)
	}

	subjects, err := provenance.ParseChecksums(bytes.NewReader(body))
	if err != nil {
		return "", nil, err
	}

	subjectPaths, err := checksumSubjectPaths(ctx)
	if err != nil {
		return "", nil, err
	}

	if err := checkChecksumSubjects(subjects, sortedChecksumSubjectNames(subjectPaths)); err != nil {
		return "", nil, err
	}

	if err := verifyReleaseChecksumDigests(subjects, subjectPaths); err != nil {
		return "", nil, err
	}

	return checksumFile, body, nil
}

// ReadValidatedReleaseChecksums returns the content of the release's single
// checksums file as it was validated against the release files. It is the
// read behind `release provenance --checksums-from-manifest`.
func ReadValidatedReleaseChecksums(in FilesInput, explicitChecksumsFile string) ([]byte, error) {
	_, body, err := manifestReleaseChecksums(in, explicitChecksumsFile)

	return body, err
}

// manifestReleaseChecksums is --checksums-from-manifest on every command that
// offers it: the manifest names the checksums file, so a checksums file the
// caller also named is refused, whatever it names, and the manifest's own is
// validated against the release files.
func manifestReleaseChecksums(in FilesInput, explicitChecksumsFile string) (string, []byte, error) {
	if explicitChecksumsFile != "" {
		return "", nil, fmt.Errorf("--checksums-from-manifest conflicts with the explicit checksums file %q: %w", explicitChecksumsFile, errs.ErrInvalidConfig)
	}

	return validatedReleaseChecksums(in, "")
}

// readReleaseFile reads a release file through the opener that refuses a link.
func readReleaseFile(path string) ([]byte, error) {
	file, err := openReleaseFile(path)
	if err != nil {
		return nil, err
	}

	defer func() { _ = file.Close() }()

	body, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read release file %s: %w", path, err)
	}

	return body, nil
}

// checkChecksumSubjects verifies that the parsed subjects name every allowed
// release asset exactly once and nothing else.
func checkChecksumSubjects(subjects []provenance.Subject, allowed []string) error {
	subjectCounts := make(map[string]int, len(subjects))
	for _, subject := range subjects {
		subjectCounts[subject.Name]++
	}

	duplicates := sortedKeysMatching(subjectCounts, func(_ string, count int) bool { return count > 1 })
	if len(duplicates) > 0 {
		return fmt.Errorf("checksum file contains duplicate subjects: %v: %w", duplicates, errs.ErrValidation)
	}

	subjectNames := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		subjectNames = append(subjectNames, subject.Name)
	}

	unknown := namesNotIn(subjectNames, allowed)
	if len(unknown) > 0 {
		return fmt.Errorf("checksum file references files that are not public release assets: %v: %w", unknown, errs.ErrValidation)
	}

	missing := namesNotIn(allowed, subjectNames)
	if len(missing) > 0 {
		return fmt.Errorf("checksum file is missing public release assets: %v: %w", missing, errs.ErrValidation)
	}

	return nil
}

func verifyReleaseChecksumDigests(subjects []provenance.Subject, subjectPaths map[string]string) error {
	for _, subject := range subjects {
		path := subjectPaths[subject.Name]

		actual, err := sha256File(path)
		if err != nil {
			return fmt.Errorf("checksum release asset %s: %w", subject.Name, err)
		}

		if actual != subject.SHA256 {
			return fmt.Errorf("checksum digest mismatch for %s: recorded %s, actual %s: %w", subject.Name, subject.SHA256, actual, errs.ErrValidation)
		}
	}

	return nil
}

func sortedChecksumSubjectNames(subjectPaths map[string]string) []string {
	names := make([]string, 0, len(subjectPaths))
	for name := range subjectPaths {
		names = append(names, name)
	}

	slices.Sort(names)

	return names
}

// namesNotIn returns the sorted unique values that do not appear in others.
func namesNotIn(values, others []string) []string {
	otherSet := stringSet(others)

	var out []string

	for _, value := range values {
		if _, ok := otherSet[value]; !ok {
			out = append(out, value)
		}
	}

	return uniqueSorted(out)
}

type releaseFilesCtx struct {
	distDir          string
	manifestFile     string
	manifestExplicit bool
}

func releaseFilesContext(in FilesInput) releaseFilesCtx {
	distDir := strings.TrimSpace(in.DistDir)
	if distDir == "" {
		distDir = defaultReleaseDistDir
	}

	manifest := strings.TrimSpace(in.ManifestFile)
	if manifest == "" {
		manifest = DefaultReleaseFilesManifest
	}

	return releaseFilesCtx{distDir: filepath.Clean(distDir), manifestFile: manifest, manifestExplicit: in.ManifestExplicit}
}

// validManifest returns the validated release file manifest, or nil when
// there is none and the caller falls back to GoReleaser discovery. Only a
// default manifest path that does not exist is "none": a named manifest has to
// exist, and one that exists but cannot be read or validated is an error.
func (c releaseFilesCtx) validManifest() (*FileManifest, error) {
	if _, err := os.Lstat(c.manifestFile); errors.Is(err, fs.ErrNotExist) {
		if c.manifestExplicit {
			return nil, fmt.Errorf("release file manifest does not exist: %s: %w: %w", c.manifestFile, err, errs.ErrMissingInput)
		}

		return nil, nil //nolint:nilnil // no manifest is a state, not a failure: the caller discovers instead.
	}

	manifest, err := readReleaseFileManifest(c.manifestFile)
	if err != nil {
		return nil, err
	}

	if err := validateReleaseFileManifest(c, manifest); err != nil {
		return nil, err
	}

	return manifest, nil
}

func (c releaseFilesCtx) artifactsJSONPath() string {
	return filepath.Join(c.distDir, "artifacts.json")
}

func (c releaseFilesCtx) provenancePath() string {
	return filepath.Join(c.distDir, ProvenanceStatementName)
}

func collectReleaseAssetEntries(ctx releaseFilesCtx) ([]FileEntry, error) {
	artifacts, err := readGoReleaserArtifacts(ctx)
	if err != nil {
		return nil, err
	}

	collector := &releaseFileCollector{}

	if err := addGoReleaserAssetEntries(ctx, collector, artifacts); err != nil {
		return nil, err
	}

	if err := addProvenanceAssetEntries(ctx, collector); err != nil {
		return nil, err
	}

	if err := addSignatureBundleEntries(ctx, collector); err != nil {
		return nil, err
	}

	if err := addPackageSignatureEntries(ctx, collector); err != nil {
		return nil, err
	}

	if len(collector.entries) == 0 {
		return nil, fmt.Errorf("no release assets found in %s: %w", ctx.artifactsJSONPath(), errs.ErrMissingInput)
	}

	return collector.entries, nil
}

func addGoReleaserAssetEntries(ctx releaseFilesCtx, collector *releaseFileCollector, artifacts []goReleaserArtifact) error {
	for _, artifact := range artifacts {
		if publishableGoReleaserAsset(artifact.Path, artifact.Type) {
			if err := collector.add(ctx, artifact.Path, "goreleaser"); err != nil {
				return err
			}
		} else {
			_, _ = fmt.Fprintf(os.Stderr, "Skipping non-release GoReleaser artifact: %s\n", artifact.Path)
		}
	}

	return nil
}

func addProvenanceAssetEntries(ctx releaseFilesCtx, collector *releaseFileCollector) error {
	if err := collector.add(ctx, ctx.provenancePath(), releaseFilesSectionProvenance); err != nil {
		return err
	}

	return collector.add(ctx, ctx.provenancePath()+".bundle", "provenance_bundle")
}

func addSignatureBundleEntries(ctx releaseFilesCtx, collector *releaseFileCollector) error {
	checksumFile, err := discoverOptionalChecksum(ctx)
	if err != nil {
		return err
	}

	if checksumFile != "" && regularReleaseFile(checksumFile+".bundle") {
		if addErr := collector.add(ctx, checksumFile+".bundle", "signature_bundle"); addErr != nil {
			return addErr
		}
	}

	sboms, err := discoverDistSBOMEntries(ctx)
	if err != nil {
		return err
	}

	for _, sbom := range sboms {
		bundle := sbom.Path + ".bundle"
		if regularReleaseFile(bundle) {
			if err := collector.add(ctx, bundle, "signature_bundle"); err != nil {
				return err
			}
		}
	}

	return nil
}

func addPackageSignatureEntries(ctx releaseFilesCtx, collector *releaseFileCollector) error {
	entries, err := os.ReadDir(ctx.distDir)
	if err != nil {
		return fmt.Errorf("read release package signatures in %s: %w", ctx.distDir, err)
	}

	for _, suffix := range []string{".deb.sig", ".rpm.sig", ".apk.sig"} {
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), suffix) {
				continue
			}

			if err := collector.add(ctx, filepath.Join(ctx.distDir, entry.Name()), "package_signature"); err != nil {
				return err
			}
		}
	}

	return nil
}

func buildGoReleaserFileManifest(ctx releaseFilesCtx, assets []FileEntry) (*FileManifest, error) {
	checksumFile, err := discoverSingleChecksum(ctx)
	if err != nil {
		return nil, err
	}

	checksums, err := entriesFromPaths(ctx, "checksum", []string{checksumFile})
	if err != nil {
		return nil, err
	}

	sboms, err := discoverDistSBOMEntries(ctx)
	if err != nil {
		return nil, err
	}

	var provenanceEntries []FileEntry
	if regularReleaseFile(ctx.provenancePath()) {
		provenanceEntries, err = entriesFromPaths(ctx, releaseFilesSectionProvenance, []string{ctx.provenancePath()})
		if err != nil {
			return nil, err
		}
	}

	manifest := &FileManifest{
		Version:    releaseFilesVersion,
		Assets:     assets,
		Checksums:  checksums,
		SBOMs:      sboms,
		Evidence:   []FileEntry{},
		Provenance: provenanceEntries,
	}

	return manifest, nil
}

// finalizeReleaseFileManifest uses the producer's declared file set, independent
// of its build tool. Only the provenance statement and signatures of declared
// files are added. An evidence file is no checksum subject, so it is published
// only with its own signature bundle.
func finalizeReleaseFileManifest(ctx releaseFilesCtx, existing *FileManifest) (*FileManifest, error) {
	collector := &releaseFileCollector{}

	for _, entry := range existing.Assets {
		if err := collector.add(ctx, entry.Path, entry.Source); err != nil {
			return nil, err
		}
	}

	if err := addProvenanceAssetEntries(ctx, collector); err != nil {
		return nil, err
	}

	if err := addDeclaredSignatureEntries(ctx, collector, existing); err != nil {
		return nil, err
	}

	provenanceEntries, err := entriesFromPaths(ctx, releaseFilesSectionProvenance, []string{ctx.provenancePath()})
	if err != nil {
		return nil, err
	}

	return &FileManifest{
		Version:    releaseFilesVersion,
		Assets:     collector.entries,
		Checksums:  existing.Checksums,
		SBOMs:      existing.SBOMs,
		Evidence:   existing.Evidence,
		Provenance: mergeManifestEntries(existing.Provenance, provenanceEntries),
	}, nil
}

// addDeclaredSignatureEntries adds the signature files found next to the
// declared assets, and requires the bundle of every evidence file.
func addDeclaredSignatureEntries(ctx releaseFilesCtx, collector *releaseFileCollector, existing *FileManifest) error {
	for _, entry := range existing.Assets {
		for _, signature := range releaseSignatures() {
			path := entry.Path + signature.suffix
			if regularReleaseFile(path) {
				if err := collector.add(ctx, path, signature.source); err != nil {
					return err
				}
			}
		}
	}

	for _, entry := range existing.Evidence {
		if _, signed := collector.paths[entry.Path+".bundle"]; !signed {
			return fmt.Errorf("evidence file %s has no signature bundle %s.bundle; sign the evidence section before finalizing: %w", entry.Path, entry.Path, errs.ErrValidation)
		}
	}

	return nil
}

func mergeManifestEntries(existing, generated []FileEntry) []FileEntry {
	merged := make([]FileEntry, 0, len(existing)+len(generated))
	seen := make(map[string]struct{}, len(existing)+len(generated))

	for _, entries := range [][]FileEntry{existing, generated} {
		for _, entry := range entries {
			if _, ok := seen[entry.Path]; ok {
				continue
			}

			seen[entry.Path] = struct{}{}
			merged = append(merged, entry)
		}
	}

	return merged
}

type goReleaserArtifact struct {
	Path string `json:"path"`
	Type string `json:"type"`
}

func readGoReleaserArtifacts(ctx releaseFilesCtx) ([]goReleaserArtifact, error) {
	body, err := os.ReadFile(ctx.artifactsJSONPath()) //nolint:gosec // release metadata path under workspace.
	if err != nil {
		return nil, fmt.Errorf("read GoReleaser artifacts %s: %w", ctx.artifactsJSONPath(), err)
	}

	var artifacts []goReleaserArtifact
	if err := json.Unmarshal(body, &artifacts); err != nil {
		return nil, fmt.Errorf("parse GoReleaser artifacts %s: %w: %w", ctx.artifactsJSONPath(), err, errs.ErrInvalidConfig)
	}

	return artifacts, nil
}

func publishableGoReleaserAsset(path, artifactType string) bool {
	if artifactType == "Binary" || artifactType == "binary" {
		return false
	}

	for _, suffix := range []string{
		"checksums.txt", ".tar.gz", ".tgz", ".zip", ".tar.xz", ".tar.bz2",
		".deb", ".rpm", ".apk", ".sbom.json",
	} {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}

	return false
}

type releaseFileCollector struct {
	entries []FileEntry
	paths   map[string]struct{}
	names   map[string]string
}

func (c *releaseFileCollector) add(ctx releaseFilesCtx, path, source string) error { //nolint:varnamelen // collector receiver.
	if err := validateReleaseDistAsset(ctx, path); err != nil {
		return err
	}

	if c.paths == nil {
		c.paths = map[string]struct{}{}
		c.names = map[string]string{}
	}

	if _, ok := c.paths[path]; ok {
		return nil
	}

	name := filepath.Base(path)
	if existing, ok := c.names[name]; ok {
		return fmt.Errorf("duplicate release asset basename: %s (first: %s, second: %s): %w", name, existing, path, errs.ErrValidation)
	}

	c.paths[path] = struct{}{}
	c.names[name] = path
	c.entries = append(c.entries, FileEntry{Path: path, Name: name, Source: source})

	return nil
}

func discoverSingleChecksum(ctx releaseFilesCtx) (string, error) {
	checksumFile, err := discoverOptionalChecksum(ctx)
	if err != nil {
		return "", err
	}

	if checksumFile == "" {
		return "", fmt.Errorf("expected exactly one checksums file in %s, found 0: %w", ctx.distDir, errs.ErrValidation)
	}

	return checksumFile, nil
}

// Asset collection permits a checksum that has not been produced yet, but
// ambiguity and filesystem walk failures are never an optional absence.
func discoverOptionalChecksum(ctx releaseFilesCtx) (string, error) {
	var files []string

	if err := filepath.WalkDir(ctx.distDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil
		}

		if strings.HasSuffix(filepath.Base(path), "checksums.txt") {
			files = append(files, path)
		}

		return nil
	}); err != nil {
		return "", fmt.Errorf("discover checksums under %s: %w", ctx.distDir, err)
	}

	slices.Sort(files)

	if len(files) > 1 {
		return "", fmt.Errorf("expected exactly one checksums file in %s, found %d: %w", ctx.distDir, len(files), errs.ErrValidation)
	}

	if len(files) == 0 {
		return "", nil
	}

	return files[0], nil
}

func discoverDistSBOMEntries(ctx releaseFilesCtx) ([]FileEntry, error) {
	artifacts, err := readGoReleaserArtifacts(ctx)
	if err != nil {
		return nil, err
	}

	paths := []string{}

	for _, artifact := range artifacts {
		if publishableGoReleaserAsset(artifact.Path, artifact.Type) && strings.HasSuffix(artifact.Path, ".sbom.json") {
			paths = append(paths, artifact.Path)
		}
	}

	return entriesFromPaths(ctx, "sbom", paths)
}

func entriesFromPaths(ctx releaseFilesCtx, source string, paths []string) ([]FileEntry, error) {
	entries := make([]FileEntry, 0, len(paths))
	for _, path := range paths {
		if err := validateReleaseDistAsset(ctx, path); err != nil {
			return nil, err
		}

		entries = append(entries, FileEntry{Path: path, Name: filepath.Base(path), Source: source})
	}

	return entries, nil
}

func readReleaseAssetsJSON(path string) ([]FileEntry, error) {
	body, err := cliio.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read release assets JSON %s: %w", path, err)
	}

	var object FileAssets
	if err := json.Unmarshal(body, &object); err == nil && object.Assets != nil {
		return object.Assets, nil
	}

	var assets []FileEntry
	if err := json.Unmarshal(body, &assets); err != nil {
		return nil, fmt.Errorf("parse release assets JSON %s: %w: %w", path, err, errs.ErrInvalidConfig)
	}

	return assets, nil
}

func readReleaseFileManifest(path string) (*FileManifest, error) {
	// Read through the descriptor the link-refusing opener checked, so the file
	// read is the file admitted. The opener's error keeps its class: a link
	// is a refused file, and one that cannot be read is not a missing one.
	file, err := openReleaseFile(path)
	if err != nil {
		return nil, fmt.Errorf("release file manifest %s: %w", path, err)
	}

	defer func() { _ = file.Close() }()

	body, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read release file manifest %s: %w", path, err)
	}

	var manifest FileManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, fmt.Errorf("parse release file manifest %s: %w: %w", path, err, errs.ErrInvalidConfig)
	}

	return &manifest, nil
}

func validateReleaseFileManifest(ctx releaseFilesCtx, manifest *FileManifest) error {
	if manifest.Version != releaseFilesVersion {
		return fmt.Errorf("release file manifest version must be %d: %w", releaseFilesVersion, errs.ErrValidation)
	}

	if err := validateManifestSections(ctx, manifest); err != nil {
		return err
	}

	assetPaths, err := manifestAssetPaths(manifest)
	if err != nil {
		return err
	}

	for _, classified := range [][]FileEntry{manifest.Checksums, manifest.SBOMs, manifest.Evidence, manifest.Provenance} {
		for _, entry := range classified {
			if _, ok := assetPaths[entry.Path]; !ok {
				return fmt.Errorf("manifest classified file %s must also be present in assets: %w", entry.Path, errs.ErrValidation)
			}
		}
	}

	if len(manifest.Checksums) != 1 {
		return fmt.Errorf("release file manifest must contain exactly one checksum file: %w", errs.ErrValidation)
	}

	// The provenance statement is no checksum subject, so the section admits
	// the one file the engine writes and nothing a producer names.
	for _, entry := range manifest.Provenance {
		if filepath.Clean(entry.Path) != ctx.provenancePath() {
			return fmt.Errorf("release file manifest provenance must be %s, got %s: %w", ctx.provenancePath(), entry.Path, errs.ErrValidation)
		}
	}

	return nil
}

// validateUnsignedReleaseFiles holds a manifest and its dist to the state a
// producer hands over. Signing writes the provenance statement and the
// signatures next to the declared files, and finalization publishes what it
// finds there, so neither may exist or be declared before signing starts: a
// file the producer placed would be published as the signer's.
func validateUnsignedReleaseFiles(ctx releaseFilesCtx, manifest *FileManifest) error {
	if len(manifest.Provenance) != 0 {
		return fmt.Errorf("unsigned release file manifest must not declare provenance: %s: %w", manifest.Provenance[0].Path, errs.ErrValidation)
	}

	declared := stringSet(entryPaths(manifest.Assets))

	for _, asset := range manifest.Assets {
		if signatureOfDeclared(asset.Path, declared) {
			return fmt.Errorf("unsigned release file manifest must not declare a signature: %s: %w", asset.Path, errs.ErrValidation)
		}
	}

	signerWritten := []string{ctx.provenancePath()}

	for _, path := range append(entryPaths(manifest.Assets), ctx.provenancePath()) {
		for _, signature := range releaseSignatures() {
			signerWritten = append(signerWritten, path+signature.suffix)
		}
	}

	for _, path := range signerWritten {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("unsigned dist must not hold %s: signing writes it: %w", path, errs.ErrValidation)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("stat %s: %w", path, err)
		}
	}

	return nil
}

// signatureOfDeclared reports whether path is the signature file of another
// declared release file.
func signatureOfDeclared(path string, declared map[string]struct{}) bool {
	for _, signature := range releaseSignatures() {
		signed, found := strings.CutSuffix(path, signature.suffix)
		if _, ok := declared[signed]; found && ok {
			return true
		}
	}

	return false
}

// manifestAssetPaths indexes the manifest assets by path, rejecting duplicate
// basenames that would collide when published as release assets.
func manifestAssetPaths(manifest *FileManifest) (map[string]struct{}, error) {
	assetPaths := map[string]struct{}{}
	assetNames := map[string]string{}

	for _, asset := range manifest.Assets {
		assetPaths[asset.Path] = struct{}{}
		if previous, ok := assetNames[asset.Name]; ok && previous != asset.Path {
			return nil, fmt.Errorf("duplicate release asset basename in manifest: %s: %w", asset.Name, errs.ErrValidation)
		}

		assetNames[asset.Name] = asset.Path
	}

	return assetPaths, nil
}

// validateManifestSections checks every entry, rejects duplicates within one
// section, and refuses the internal release image ledger anywhere.
func validateManifestSections(ctx releaseFilesCtx, manifest *FileManifest) error {
	sections := []struct {
		name    string
		entries []FileEntry
	}{
		{name: "assets", entries: manifest.Assets},
		{name: releaseFilesSectionChecksums, entries: manifest.Checksums},
		{name: releaseFilesSectionSBOMs, entries: manifest.SBOMs},
		{name: releaseFilesSectionEvidence, entries: manifest.Evidence},
		{name: releaseFilesSectionProvenance, entries: manifest.Provenance},
	}

	for _, section := range sections {
		seen := make(map[string]struct{}, len(section.entries))
		for _, entry := range section.entries {
			if _, exists := seen[entry.Path]; exists {
				return fmt.Errorf("release file manifest %s contains duplicate path %s: %w", section.name, entry.Path, errs.ErrValidation)
			}

			seen[entry.Path] = struct{}{}

			if err := validateManifestEntry(ctx, entry); err != nil {
				return err
			}

			if filepath.Clean(entry.Path) == filepath.Join(ctx.distDir, "release-images.json") {
				return fmt.Errorf("release image ledger must not be in the release file manifest: %s: %w", entry.Path, errs.ErrValidation)
			}
		}
	}

	return nil
}

func validateManifestEntry(ctx releaseFilesCtx, entry FileEntry) error {
	if entry.Path == "" || entry.Name == "" || entry.Source == "" {
		return fmt.Errorf("release file manifest entries require non-empty path, name, and source: %w", errs.ErrValidation)
	}

	if entry.Name != filepath.Base(entry.Path) {
		return fmt.Errorf("release file manifest entry name %q must match basename of %q: %w", entry.Name, entry.Path, errs.ErrValidation)
	}

	return validateReleaseDistAsset(ctx, entry.Path)
}

func validateReleaseDistAsset(ctx releaseFilesCtx, path string) error {
	if err := validateReleaseDistPath(ctx, path); err != nil {
		return err
	}

	return validateReleasePathComponents(path)
}

// validateReleaseDistPath checks the shape of a release file path, whether or
// not the file exists yet: relative, without traversal, under the dist
// directory.
func validateReleaseDistPath(ctx releaseFilesCtx, path string) error {
	if !pathsafe.Relative(path) {
		return fmt.Errorf("unsafe artifact path: %s: %w", path, errs.ErrValidation)
	}

	distPrefix := filepath.ToSlash(ctx.distDir) + "/"
	if !strings.HasPrefix(filepath.ToSlash(path), distPrefix) {
		return fmt.Errorf("artifact path must be under %s/: %s: %w", ctx.distDir, path, errs.ErrValidation)
	}

	return nil
}

func validateReleasePathComponents(path string) error {
	file, err := openReleaseFile(path)
	if err != nil {
		return err
	}

	return file.Close()
}

// openReleaseFile rejects linked parents and leaves, and binds file validation
// to the descriptor used by readers rather than reopening it after a check.
//
//nolint:cyclop // each descriptor/type/identity failure independently refuses the file.
func openReleaseFile(path string) (*os.File, error) {
	if strings.HasSuffix(path, string(filepath.Separator)) {
		return nil, fmt.Errorf("artifact path must name a regular file: %q: %w", path, errs.ErrMissingInput)
	}

	root, err := pathsafe.OpenRoot(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("release file parent for %q is missing: %w: %w", path, err, errs.ErrMissingInput)
	}

	if err != nil {
		return nil, fmt.Errorf("release file parent for %q: %w", path, err)
	}

	defer func() { _ = root.Close() }()

	name := filepath.Base(path)

	info, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("artifact path is missing or unreadable at %s: %w: %w", path, err, errs.ErrMissingInput)
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("artifact path contains symlink component %s: %w", path, errs.ErrValidation)
	}

	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("artifact path is not a regular file: %s: %w", path, errs.ErrMissingInput)
	}

	file, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open release file %q: %w", path, err)
	}

	opened, statErr := file.Stat()

	current, currentErr := root.Lstat(name)
	if statErr != nil || currentErr != nil || !current.Mode().IsRegular() ||
		!os.SameFile(info, opened) || !os.SameFile(current, opened) {
		_ = file.Close()

		return nil, fmt.Errorf("release file %q changed while opening: %w", path, errs.ErrValidation)
	}

	return file, nil
}

func regularReleaseFile(path string) bool {
	return validateReleasePathComponents(path) == nil
}

func manifestSection(manifest *FileManifest, section string) ([]FileEntry, error) {
	switch section {
	case releaseFilesSectionAssets:
		return manifest.Assets, nil
	case releaseFilesSectionChecksums:
		return manifest.Checksums, nil
	case releaseFilesSectionSBOMs:
		return manifest.SBOMs, nil
	case releaseFilesSectionEvidence:
		return manifest.Evidence, nil
	case releaseFilesSectionProvenance:
		return manifest.Provenance, nil
	default:
		return nil, fmt.Errorf("release files section must be one of assets, checksums, sboms, evidence, provenance (got %q): %w", section, errs.ErrUsage)
	}
}

func entryPaths(entries []FileEntry) []string {
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		paths = append(paths, entry.Path)
	}

	return paths
}

func checksumSubjectPaths(ctx releaseFilesCtx) (map[string]string, error) {
	manifest, err := ctx.validManifest()
	if err != nil {
		return nil, err
	}

	if manifest == nil {
		return checksumSubjectPathsFromArtifacts(ctx)
	}

	return manifestChecksumSubjectPaths(manifest), nil
}

// manifestChecksumSubjectPaths returns the declared assets the checksums file
// has to list, by name. Every asset is a subject except the release metadata
// the engine defines: the checksums file itself, the provenance statement, an
// evidence file (finalization requires its own signature bundle instead), and
// the signature file of another declared asset. A name alone exempts nothing.
func manifestChecksumSubjectPaths(manifest *FileManifest) map[string]string {
	declared := stringSet(entryPaths(manifest.Assets))
	metadata := map[string]struct{}{}

	for _, section := range [][]FileEntry{manifest.Checksums, manifest.Provenance, manifest.Evidence} {
		for _, entry := range section {
			metadata[entry.Path] = struct{}{}
		}
	}

	paths := make(map[string]string, len(manifest.Assets))

	for _, asset := range manifest.Assets {
		if _, ok := metadata[asset.Path]; ok || signatureOfDeclared(asset.Path, declared) {
			continue
		}

		paths[asset.Name] = asset.Path
	}

	return paths
}

func checksumSubjectPathsFromArtifacts(ctx releaseFilesCtx) (map[string]string, error) {
	artifacts, err := readGoReleaserArtifacts(ctx)
	if err != nil {
		return nil, err
	}

	paths := make(map[string]string, len(artifacts))

	for _, artifact := range artifacts {
		if !publishableGoReleaserAsset(artifact.Path, artifact.Type) || strings.HasSuffix(artifact.Path, "checksums.txt") {
			continue
		}

		if err := validateReleaseDistAsset(ctx, artifact.Path); err != nil {
			return nil, err
		}

		name := filepath.Base(artifact.Path)
		if existing, ok := paths[name]; ok && existing != artifact.Path {
			return nil, fmt.Errorf("duplicate release asset basename: %s (first: %s, second: %s): %w", name, existing, artifact.Path, errs.ErrValidation)
		}

		paths[name] = artifact.Path
	}

	return paths, nil
}

func stringSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}

	return out
}

func sortedKeysMatching(counts map[string]int, keep func(string, int) bool) []string {
	keys := []string{}

	for key, count := range counts {
		if keep(key, count) {
			keys = append(keys, key)
		}
	}

	slices.Sort(keys)

	return keys
}

func uniqueSorted(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	slices.Sort(values)
	out := values[:0]

	var previous string
	for i, value := range values {
		if i == 0 || value != previous {
			out = append(out, value)
		}

		previous = value
	}

	return out
}
