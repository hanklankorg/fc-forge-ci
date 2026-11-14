// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/diggsweden/forge-ci/internal/cliio"
	domainartifact "github.com/diggsweden/forge-ci/internal/domain/artifact"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/provenance"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
	domainsbom "github.com/diggsweden/forge-ci/internal/domain/sbom"
	"github.com/diggsweden/forge-ci/internal/domain/version"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

const (
	assemblySourceBuildArtifact         = "build_artifact"
	assemblySourceExtractedBinaries     = "extracted_binaries"
	assemblySourceAttachment            = "attachment"
	assemblySourceGeneratedSBOM         = "generated_sbom"
	assemblySourceAnalyzedContainerSBOM = "analyzed_container_sbom"
)

// AssembleInput drives `forge-ci release assemble`.
type AssembleInput struct {
	ConfigPlanJSON           string
	ArtifactTransferPlanJSON string
	AttachArtifacts          string
	ProjectName              string
	Version                  string
	OutputFile               string
	ReleaseFilesDir          string
	ReleaseArtifactsDir      string
	SBOMDir                  string
	// AcquisitionDir is the fresh producer tree emitted by download-artifacts
	// --isolated. Source checkout contents remain attachment-only in this mode.
	AcquisitionDir string
}

type assemblyCollector struct {
	assetsDir string
	sbomsDir  string
	// releaseFilesDir / releaseArtifactsDir are the assembly-wide directories
	// the SBOM walk prunes against (so a staged copy isn't re-collected from
	// its source tree). Constant for the whole assembly, carried here rather
	// than threaded through every collect* helper.
	releaseFilesDir     string
	releaseArtifactsDir string
	acquisitionDir      string
	assets              []domainrelease.AssemblyFile
	sboms               []domainrelease.AssemblyFile
	assetByName         map[string]domainrelease.AssemblyFile
	sbomByName          map[string]domainrelease.AssemblyFile
	seenSBOMSrc         map[string]struct{}
}

// Assemble stages the final release files and writes the assembly manifest.
// It is deliberately forge-neutral: all provider/network work happens before
// this command via the artifact-transfer plan.
//
//nolint:cyclop // linear assembly pipeline: parse plans → collect assets/SBOMs → stage → emit; the branches are sequential phases, not nested logic.
func Assemble(out io.Writer, in AssembleInput) (*domainrelease.Assembly, error) {
	cfg, err := parseAssemblyConfigPlan(in.ConfigPlanJSON)
	if err != nil {
		return nil, err
	}

	transfer, err := parseAssemblyTransferPlan(in.ArtifactTransferPlanJSON)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(in.ProjectName) == "" {
		return nil, fmt.Errorf("project-name is required: %w", errs.ErrUsage)
	}

	if strings.TrimSpace(in.Version) == "" {
		return nil, fmt.Errorf("version is required: %w", errs.ErrUsage)
	}

	outputFile := defaultString(in.OutputFile, domainrelease.DefaultAssemblyFile)
	releaseFilesDir := defaultString(in.ReleaseFilesDir, domainrelease.DefaultReleaseFilesDir)
	releaseArtifactsDir := defaultString(in.ReleaseArtifactsDir, domainrelease.DefaultReleaseArtifactsDir)

	sbomDir := defaultString(in.SBOMDir, domainrelease.DefaultSBOMArtifactsDir)
	if err := validateAssemblyName(in.ProjectName); err != nil {
		return nil, err
	}

	if err := validateAssemblyName(in.Version); err != nil {
		return nil, err
	}

	if outputFile != cliio.StdSentinel {
		if err := validateReleasePathComponents(outputFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}

	if err := validateRelativeDir(releaseFilesDir, "release-files-dir"); err != nil {
		return nil, err
	}

	if err := validateAssemblySources(cfg, in.AcquisitionDir, releaseArtifactsDir, sbomDir); err != nil {
		return nil, err
	}

	if in.AcquisitionDir != "" {
		releaseArtifactsDir = filepath.Join(in.AcquisitionDir, releaseArtifactsDir)
		for index := range transfer.Items {
			transfer.Items[index].Path = filepath.Join(in.AcquisitionDir, transfer.Items[index].Path)
		}
	}

	assetsDir := filepath.Join(releaseFilesDir, "assets")
	sbomsDir := filepath.Join(releaseFilesDir, "sboms")

	collector := &assemblyCollector{
		assetsDir:           assetsDir,
		sbomsDir:            sbomsDir,
		releaseFilesDir:     releaseFilesDir,
		releaseArtifactsDir: releaseArtifactsDir,
		acquisitionDir:      in.AcquisitionDir,
		assetByName:         make(map[string]domainrelease.AssemblyFile),
		sbomByName:          make(map[string]domainrelease.AssemblyFile),
		seenSBOMSrc:         make(map[string]struct{}),
	}

	if err := collectReleaseAssets(collector, cfg, transfer, releaseArtifactsDir); err != nil {
		return nil, err
	}

	if err := collectAttachmentAssets(collector, in.AttachArtifacts); err != nil {
		return nil, err
	}

	if err := collectAssemblySBOMs(collector, cfg, sbomDir); err != nil {
		return nil, err
	}

	if err := collector.stage(); err != nil {
		return nil, err
	}

	sortAssemblyFiles(collector.assets)
	sortAssemblyFiles(collector.sboms)

	asm := &domainrelease.Assembly{
		Version:      domainrelease.AssemblyVersion,
		Assets:       collector.assets,
		SBOMs:        collector.sboms,
		ChecksumFile: filepath.ToSlash(filepath.Join(releaseFilesDir, domainrelease.ChecksumsFile)),
		SBOMZipFile:  filepath.ToSlash(filepath.Join(assetsDir, domainsbom.ZipName(in.ProjectName, version.StripVPrefix(in.Version)))),
	}

	if err := writeAssembly(outputFile, asm); err != nil {
		return nil, err
	}

	if out != nil {
		_, _ = fmt.Fprintf(out, "Assembled %d release asset(s) and %d SBOM input(s) in %s\n", len(asm.Assets), len(asm.SBOMs), outputFile)
	}

	return asm, nil
}

func validateAssemblySources(cfg pipeline.ConfigPlan, acquisitionDir, artifactsDir, sbomDir string) error {
	sourceDirs := []string{artifactsDir, sbomDir}
	if acquisitionDir == "" {
		sourceDirs = append(sourceDirs, plannedSBOMRoots(cfg)...)
	} else {
		if !filepath.IsAbs(acquisitionDir) {
			return fmt.Errorf("acquisition-dir must be absolute: %w", errs.ErrValidation)
		}

		if err := validateAcquisitionRoot(acquisitionDir); err != nil {
			return err
		}

		root, err := pathsafe.OpenRoot(acquisitionDir)
		if err != nil {
			return err
		}

		_ = root.Close()
	}

	for _, dir := range sourceDirs {
		if !pathsafe.Relative(dir) {
			return fmt.Errorf("assembly source directory must be workspace-relative: %q: %w", dir, errs.ErrValidation)
		}

		if acquisitionDir != "" {
			dir = filepath.Join(acquisitionDir, dir)
		}

		root, err := pathsafe.OpenRoot(dir)
		if err == nil {
			_ = root.Close()
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}

	return nil
}

// stage validates both destinations and source overlap before clearing either.
//
//nolint:cyclop // validates destinations, then recreates and copies the already-collected plan.
func (c *assemblyCollector) stage() error {
	if root, rootErr := pathsafe.OpenRoot(c.releaseFilesDir); rootErr == nil {
		defer func() { _ = root.Close() }()

		for _, name := range []string{"assets", "sboms"} {
			info, statErr := root.Lstat(name)
			if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
				return statErr
			}

			if statErr == nil && !info.IsDir() {
				return fmt.Errorf("assembly staging directory must be real: %w", errs.ErrValidation)
			}
		}
	} else if !errors.Is(rootErr, fs.ErrNotExist) {
		return rootErr
	}

	files := slices.Concat(c.assets, c.sboms)
	for _, file := range files {
		for _, dir := range []string{c.assetsDir, c.sbomsDir} {
			rel, err := filepath.Rel(absOrSelf(dir), absOrSelf(file.SourcePath))
			if err != nil {
				return err
			}

			if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
				return fmt.Errorf("assembly source overlaps staging: %w", errs.ErrValidation)
			}
		}
	}

	if err := recreateDir(c.assetsDir); err != nil {
		return err
	}

	if err := recreateDir(c.sbomsDir); err != nil {
		return err
	}

	for _, file := range files {
		if err := copyRegularFile(file.SourcePath, file.Path); err != nil {
			return err
		}
	}

	return nil
}

func parseAssemblyConfigPlan(raw string) (pipeline.ConfigPlan, error) {
	if strings.TrimSpace(raw) == "" {
		return pipeline.ConfigPlan{}, fmt.Errorf("config-plan-json is required: %w", errs.ErrUsage)
	}

	plan, err := pipeline.DecodeConfigPlan(raw)
	if err != nil {
		return pipeline.ConfigPlan{}, err
	}

	if plan.Version != pipeline.ConfigPlanVersion {
		return pipeline.ConfigPlan{}, fmt.Errorf("config-plan-json has unsupported version %d: %w", plan.Version, errs.ErrInvalidConfig)
	}

	if err := pipeline.ValidateConfigPlan(plan); err != nil {
		return pipeline.ConfigPlan{}, err
	}

	return plan, nil
}

func parseAssemblyTransferPlan(raw string) (pipeline.ArtifactTransferPlan, error) {
	if strings.TrimSpace(raw) == "" {
		return pipeline.ArtifactTransferPlan{}, fmt.Errorf("artifact-transfer-plan-json is required: %w", errs.ErrUsage)
	}

	var plan pipeline.ArtifactTransferPlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		return pipeline.ArtifactTransferPlan{}, fmt.Errorf("parse artifact-transfer-plan-json: %w: %w", err, errs.ErrInvalidConfig)
	}

	if plan.Version != pipeline.ArtifactTransferPlanVersion {
		return pipeline.ArtifactTransferPlan{}, fmt.Errorf("artifact-transfer-plan-json has unsupported version %d: %w", plan.Version, errs.ErrInvalidConfig)
	}

	for _, item := range plan.Items {
		if err := pipeline.ValidateArtifactTransferItem(item); err != nil {
			return pipeline.ArtifactTransferPlan{}, err
		}
	}

	return plan, nil
}

//nolint:varnamelen // one directory walk with sequential skip/classify guards; c is the collector threaded through every collect* helper.
func collectReleaseAssets(c *assemblyCollector, cfg pipeline.ConfigPlan, transfer pipeline.ArtifactTransferPlan, dir string) error {
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}

		return fmt.Errorf("stat release artifacts dir %s: %w", dir, err)
	}

	hasNative := hasNativeArtifactFirst(cfg)
	rootAbs := absOrSelf(dir)

	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			return nil
		}

		if !entry.Type().IsRegular() {
			return nil
		}

		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		if skipAssemblyReleaseFile(path, rel) {
			return nil
		}

		kind := assemblySourceBuildArtifact
		if firstPathSegment(rel) == "binaries" {
			kind = assemblySourceExtractedBinaries
		}

		if kind != assemblySourceExtractedBinaries && !isPlannedReleaseAsset(path, hasNative) {
			return nil
		}

		return c.addAsset(path, kind, transferNameForKind(transfer, kind, rootAbs, path), true)
	})
}

//nolint:varnamelen // c is the assembly collector threaded through every collect* helper; a longer name only adds noise.
func collectAttachmentAssets(c *assemblyCollector, patterns string) error {
	parts := splitAttachPatterns(patterns)
	if len(parts) == 0 {
		return nil
	}

	for _, pattern := range parts {
		if filepath.IsAbs(pattern) {
			return fmt.Errorf("attach-artifacts pattern %q must be relative to the workspace: %w", pattern, errs.ErrValidation)
		}

		if strings.Contains(pattern, "..") {
			return fmt.Errorf("attach-artifacts pattern %q contains \"..\": %w", pattern, errs.ErrValidation)
		}
	}

	entries, err := domainartifact.CollectGlobEntries(parts, true)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if err := c.addAsset(entry.Abs, assemblySourceAttachment, "", true); err != nil {
			return err
		}
	}

	return nil
}

func collectAssemblySBOMs(collector *assemblyCollector, cfg pipeline.ConfigPlan, sbomDir string) error {
	if collector.acquisitionDir != "" {
		// Only acquired members and outputs generated from them, never source
		// fixtures. Visit analyzed-container inputs first to preserve their role.
		if err := walkSBOMRoot(collector, filepath.Join(collector.acquisitionDir, sbomDir), assemblySourceAnalyzedContainerSBOM); err != nil {
			return err
		}

		return walkSBOMRoot(collector, collector.acquisitionDir, assemblySourceGeneratedSBOM)
	}

	roots := plannedSBOMRoots(cfg)
	for _, root := range roots {
		if err := walkSBOMRoot(collector, root, assemblySourceGeneratedSBOM); err != nil {
			return err
		}
	}

	return walkSBOMRoot(collector, sbomDir, assemblySourceAnalyzedContainerSBOM)
}

//nolint:varnamelen // c is the assembly collector threaded through every collect* helper; a longer name only adds noise.
func walkSBOMRoot(c *assemblyCollector, root, kind string) error {
	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}

		return fmt.Errorf("stat SBOM root %s: %w", root, err)
	}

	releaseFilesAbs := absOrSelf(c.releaseFilesDir)

	releaseArtifactsAbs := absOrSelf(c.releaseArtifactsDir)
	if c.acquisitionDir != "" {
		releaseArtifactsAbs = "" // acquired producer SBOMs are legitimate inputs too
	}

	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			if shouldPruneSBOMDir(path, releaseFilesAbs, releaseArtifactsAbs) {
				return filepath.SkipDir
			}

			return nil
		}

		if !entry.Type().IsRegular() || !isAssemblySBOMFile(path) {
			return nil
		}

		return c.addSBOM(path, kind, "", false)
	})
}

func (c *assemblyCollector) addAsset(src, kind, sourceArtifact string, required bool) error {
	name := filepath.Base(src)
	if err := validateAssemblyName(name); err != nil {
		return err
	}

	abs := absOrSelf(src)
	if existing, ok := c.assetByName[name]; ok {
		if absOrSelf(existing.SourcePath) == abs {
			return nil
		}

		return fmt.Errorf("release asset basename %q is produced by both %s and %s: %w", name, existing.SourcePath, src, errs.ErrValidation)
	}

	dst := filepath.Join(c.assetsDir, name)

	input, err := openReleaseFile(src)
	if err != nil {
		return err
	}

	_ = input.Close()

	file := domainrelease.AssemblyFile{
		Path:           filepath.ToSlash(dst),
		Name:           name,
		SourcePath:     filepath.ToSlash(src),
		SourceKind:     kind,
		SourceArtifact: sourceArtifact,
		Required:       required,
	}
	c.assetByName[name] = file
	c.assets = append(c.assets, file)

	return nil
}

func (c *assemblyCollector) addSBOM(src, kind, sourceArtifact string, required bool) error {
	abs := absOrSelf(src)
	if _, ok := c.seenSBOMSrc[abs]; ok {
		return nil
	}

	c.seenSBOMSrc[abs] = struct{}{}

	name := filepath.Base(src)
	if err := validateAssemblyName(name); err != nil {
		return err
	}

	if existing, ok := c.sbomByName[name]; ok {
		return fmt.Errorf("SBOM archive name %q is produced by both %s and %s: %w", name, existing.SourcePath, src, errs.ErrValidation)
	}

	dst := filepath.Join(c.sbomsDir, name)

	input, err := openReleaseFile(src)
	if err != nil {
		return err
	}

	_ = input.Close()

	file := domainrelease.AssemblyFile{
		Path:           filepath.ToSlash(dst),
		Name:           name,
		SourcePath:     filepath.ToSlash(src),
		SourceKind:     kind,
		SourceArtifact: sourceArtifact,
		Required:       required,
	}
	c.sbomByName[name] = file
	c.sboms = append(c.sboms, file)

	return nil
}

func hasNativeArtifactFirst(cfg pipeline.ConfigPlan) bool {
	for _, artifact := range cfg.Artifacts.GoArtifactFirst {
		if artifact.BuildArtifactName != "" {
			return true
		}
	}

	for _, artifact := range cfg.Artifacts.CargoArtifactFirst {
		if artifact.BuildArtifactName != "" {
			return true
		}
	}

	return false
}

func plannedSBOMRoots(cfg pipeline.ConfigPlan) []string {
	seen := map[string]struct{}{}
	roots := make([]string, 0, len(cfg.Artifacts.All)+1)

	for _, artifact := range cfg.Artifacts.All {
		root := artifact.WorkingDirectory
		if root == "" {
			root = "."
		}

		if _, ok := seen[root]; ok {
			continue
		}

		seen[root] = struct{}{}
		roots = append(roots, root)
	}

	if len(roots) == 0 {
		roots = append(roots, ".")
	}

	slices.Sort(roots)

	return roots
}

func skipAssemblyReleaseFile(path, rel string) bool {
	base := filepath.Base(path)
	switch {
	case base == domainrelease.ChecksumsFile:
		return true
	case base == "release-images.json" || strings.HasPrefix(base, "release-images-ledger"):
		return true
	case base == "bom.json":
		return true
	case strings.HasSuffix(base, ".asc") || strings.HasSuffix(base, ".bundle"):
		return true
	case strings.HasPrefix(base, "original-") && strings.HasSuffix(base, ".jar"):
		return true
	case isAssemblySBOMFile(path):
		return true
	case strings.Contains(filepath.ToSlash(rel), "/.git/"):
		return true
	default:
		return false
	}
}

func isPlannedReleaseAsset(path string, hasNative bool) bool {
	base := filepath.Base(path)
	lower := strings.ToLower(base)

	if domainrelease.IsReleaseArtifact(path) {
		return true
	}

	for _, ext := range []string{".apk", ".aab", ".ipa", ".exe"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}

	return hasNative && filepath.Ext(base) == ""
}

func isAssemblySBOMFile(path string) bool {
	base := filepath.Base(path)

	return strings.HasSuffix(base, "-sbom.spdx.json") || strings.HasSuffix(base, "-sbom.cyclonedx.json")
}

func shouldPruneSBOMDir(path, releaseFilesAbs, releaseArtifactsAbs string) bool {
	abs := absOrSelf(path)
	base := filepath.Base(path)

	return base == ".git" || abs == releaseFilesAbs || abs == releaseArtifactsAbs
}

func transferNameForKind(plan pipeline.ArtifactTransferPlan, kind, rootAbs, path string) string {
	for _, item := range plan.Items {
		if string(item.Kind) != kind {
			continue
		}

		itemRoot := absOrSelf(item.Path)
		if rootAbs != "" && itemRoot != rootAbs && !strings.HasPrefix(absOrSelf(path), itemRoot+string(filepath.Separator)) {
			continue
		}

		if item.Name != "" {
			return item.Name
		}

		return item.NameTemplate
	}

	return ""
}

func firstPathSegment(path string) string {
	path = filepath.ToSlash(path)
	if idx := strings.Index(path, "/"); idx >= 0 {
		return path[:idx]
	}

	return path
}

func validateAssemblyName(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("invalid release asset name %q: %w", name, errs.ErrValidation)
	}

	if !utf8.ValidString(name) {
		return fmt.Errorf("release asset name %q must be valid UTF-8: %w", name, errs.ErrValidation)
	}

	if strings.ContainsFunc(name, unicode.IsControl) {
		return fmt.Errorf("release asset name %q must not contain control characters: %w", name, errs.ErrValidation)
	}

	return nil
}

func copyRegularFile(src, dst string) error {
	in, err := openReleaseFile(src)
	if err != nil {
		return fmt.Errorf("open %q: %w", src, err)
	}

	defer func() { _ = in.Close() }()

	root, err := pathsafe.MkdirRoot(filepath.Dir(dst), 0o755)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	out, err := root.OpenFile(filepath.Base(dst), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create %q: %w", dst, err)
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()

		return fmt.Errorf("copy %q -> %q: %w", src, dst, err)
	}

	if err := out.Close(); err != nil {
		return fmt.Errorf("close %q: %w", dst, err)
	}

	return nil
}

func recreateDir(dir string) error {
	if err := validateRelativeDir(dir, "release staging dir"); err != nil {
		return err
	}

	root, err := pathsafe.MkdirRoot(filepath.Dir(dir), 0o755)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	base := filepath.Base(dir)
	if err := root.RemoveAll(base); err != nil {
		return fmt.Errorf("remove %q: %w", dir, err)
	}

	if err := root.Mkdir(base, 0o755); err != nil {
		return fmt.Errorf("mkdir %q: %w", dir, err)
	}

	return nil
}

func validateRelativeDir(dir, label string) error {
	clean := filepath.Clean(dir)
	if clean == "." || clean == string(filepath.Separator) || clean == "" || filepath.IsAbs(clean) {
		return fmt.Errorf("%s %q must be a relative non-root directory: %w", label, dir, errs.ErrValidation)
	}

	for part := range strings.SplitSeq(filepath.ToSlash(clean), "/") {
		if part == ".." {
			return fmt.Errorf("%s %q must not traverse parent directories: %w", label, dir, errs.ErrValidation)
		}
	}

	return nil
}

func writeAssembly(path string, asm *domainrelease.Assembly) error {
	body, err := json.MarshalIndent(asm, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal release assembly: %w", err)
	}

	body = append(body, '\n')

	if path != cliio.StdSentinel {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // metadata dir under workspace.
			return fmt.Errorf("mkdir %q: %w", filepath.Dir(path), err)
		}
	}

	return cliio.WriteFile(path, body, 0o644)
}

func readAssembly(path string) (domainrelease.Assembly, error) {
	if path != cliio.StdSentinel {
		if err := validateReleasePathComponents(path); err != nil {
			return domainrelease.Assembly{}, err
		}
	}

	body, err := cliio.ReadFile(path)
	if err != nil {
		return domainrelease.Assembly{}, fmt.Errorf("read release assembly %s: %w", path, err)
	}

	var asm domainrelease.Assembly
	if err := json.Unmarshal(body, &asm); err != nil {
		return domainrelease.Assembly{}, fmt.Errorf("parse release assembly %s: %w: %w", path, err, errs.ErrInvalidConfig)
	}

	if asm.Version != domainrelease.AssemblyVersion {
		return domainrelease.Assembly{}, fmt.Errorf("release assembly has unsupported version %d: %w", asm.Version, errs.ErrInvalidConfig)
	}

	if err := validateAssemblyPaths(asm); err != nil {
		return domainrelease.Assembly{}, err
	}

	return asm, nil
}

// ValidateAssembly admits an unsigned assembly without loading signing material.
// Assembly paths are not discovery hints: every declared input must exist, and
// checksums must cover exactly the public assets and the generated SBOM ZIP.
func ValidateAssembly(path string) error {
	_, err := validatedSigningAssembly(path, nil)

	return err
}

func validatedSigningAssembly(path string, extensions []string) ([]string, error) {
	asm, err := readAssembly(path)
	if err != nil {
		return nil, err
	}

	subjectPaths, err := assemblyChecksumSubjects(asm)
	if err != nil {
		return nil, err
	}

	selected := make([]string, 0, len(subjectPaths)+1)
	for _, asset := range asm.Assets {
		selected = append(selected, asset.Path)
	}

	if asm.SBOMZipFile != "" && regularFileExists(asm.SBOMZipFile) {
		selected = append(selected, asm.SBOMZipFile)
	}

	if asm.ChecksumFile == "" {
		return nil, fmt.Errorf("assembly checksums file is required: %w", errs.ErrMissingInput)
	}

	if err := checkChecksumOutputInputs(asm.ChecksumFile, append(append([]string{path}, selected...), assemblySBOMPaths(asm)...)); err != nil {
		return nil, err
	}

	selected = append(selected, asm.ChecksumFile)
	if err := validateAssemblySignerOutputs(selected, extensions); err != nil {
		return nil, err
	}

	body, err := readReleaseFile(asm.ChecksumFile)
	if err != nil {
		return nil, fmt.Errorf("assembly checksums file: %w", err)
	}

	// Container-only releases can have no public file subjects. The producer
	// still writes an empty checksums file; there is nothing to sign in it.
	if len(subjectPaths) == 0 && len(bytes.TrimSpace(body)) == 0 {
		return selected[:len(selected)-1], nil
	}

	subjects, err := provenance.ParseChecksums(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	if err := checkChecksumSubjects(subjects, sortedChecksumSubjectNames(subjectPaths)); err != nil {
		return nil, err
	}

	if err := verifyReleaseChecksumDigests(subjects, subjectPaths); err != nil {
		return nil, err
	}

	return selected, nil
}

func assemblySBOMPaths(asm domainrelease.Assembly) []string {
	paths := make([]string, 0, len(asm.SBOMs))
	for _, file := range asm.SBOMs {
		paths = append(paths, file.Path)
	}

	return paths
}

func assemblyChecksumSubjects(asm domainrelease.Assembly) (map[string]string, error) {
	subjects := make(map[string]string, len(asm.Assets)+1)
	paths := map[string]bool{}

	add := func(path, name string) error {
		if name != filepath.Base(path) || subjects[name] != "" || paths[filepath.Clean(path)] {
			return fmt.Errorf("assembly public asset has inconsistent or duplicate name/path %q: %w", path, errs.ErrValidation)
		}

		if !regularFileExists(path) {
			return fmt.Errorf("assembly public asset %q is missing or not regular: %w", path, errs.ErrMissingInput)
		}

		subjects[name] = path
		paths[filepath.Clean(path)] = true

		return nil
	}
	for _, asset := range asm.Assets {
		if err := add(asset.Path, asset.Name); err != nil {
			return nil, err
		}
	}

	if len(asm.SBOMs) > 0 && (asm.SBOMZipFile == "" || !regularFileExists(asm.SBOMZipFile)) {
		return nil, fmt.Errorf("assembly SBOM ZIP is missing; run release sbom-zip --assembly first: %w", errs.ErrMissingInput)
	}

	if asm.SBOMZipFile != "" && regularFileExists(asm.SBOMZipFile) {
		if err := add(asm.SBOMZipFile, filepath.Base(asm.SBOMZipFile)); err != nil {
			return nil, err
		}
	}

	return subjects, nil
}

// Unsigned handoffs must not supply signer-owned outputs, either as subjects or
// as pre-existing sidecars. Check both production backends, plus the selected
// signer's extensions for defense in depth before its first invocation.
func validateAssemblySignerOutputs(paths, extensions []string) error {
	extensions = append([]string{".asc", ".bundle"}, extensions...)
	for _, path := range paths {
		for _, ext := range extensions {
			if strings.HasSuffix(path, ext) {
				return fmt.Errorf("assembly contains reserved signer output %q: %w", path, errs.ErrValidation)
			}

			if _, err := os.Lstat(path + ext); !errors.Is(err, fs.ErrNotExist) {
				if err != nil {
					return fmt.Errorf("inspect assembly signer output: %w", err)
				}

				return fmt.Errorf("assembly contains reserved signer output %q: %w", path+ext, errs.ErrValidation)
			}
		}
	}

	return nil
}

func validateAssemblyPaths(asm domainrelease.Assembly) error {
	for _, files := range [][]domainrelease.AssemblyFile{asm.Assets, asm.SBOMs} {
		for _, file := range files {
			if err := validateAssemblyPath(file.Path, true); err != nil {
				return err
			}

			if file.Name != "" && (filepath.Base(file.Name) != file.Name || strings.ContainsAny(file.Name, `/\`)) {
				return fmt.Errorf("assembly entry name must be a basename: %q: %w", file.Name, errs.ErrValidation)
			}
		}
	}

	for _, output := range []string{asm.ChecksumFile, asm.SBOMZipFile} {
		if output == "" {
			continue
		}

		if err := validateAssemblyPath(output, false); err != nil {
			return err
		}
	}

	return nil
}

func validateAssemblyPath(path string, required bool) error {
	if !pathsafe.Relative(path) {
		return fmt.Errorf("assembly path must be workspace-relative: %q: %w", path, errs.ErrValidation)
	}

	err := validateReleasePathComponents(path)
	if !required && errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	return err
}

func sortAssemblyFiles(files []domainrelease.AssemblyFile) {
	slices.SortFunc(files, func(a, b domainrelease.AssemblyFile) int {
		return cmp.Compare(a.Name, b.Name)
	})
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}

	return value
}

func regularFileExists(path string) bool {
	return regularReleaseFile(path)
}

func regularFileNonEmpty(path string) bool {
	file, err := openReleaseFile(path)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()

	return err == nil && info.Size() > 0
}
