// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/diggsweden/forge-ci/internal/cliio"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/security"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
	"github.com/diggsweden/forge-ci/internal/retry"
)

// The scan defaults are those of `security scan container-json`: Trivy's
// vulnerability database download fails transiently, so a failed scan is run
// again, three times in all with a linear backoff.
const (
	defaultImageEvidenceTrivyTimeout    = "30m"
	defaultImageEvidenceTrivyAttempts   = 3
	defaultImageEvidenceTrivyRetryDelay = 20 * time.Second
)

// ImageEvidenceBuildah is the Buildah surface needed for local-image evidence.
type ImageEvidenceBuildah interface {
	ExportLocalImageToLayout(ctx context.Context, imageRef, layoutDir string, out io.Writer) error
	ExportLocalManifestToLayout(ctx context.Context, manifest, layoutDir string, out io.Writer) error
}

// ImageEvidenceCopier copies one platform of an image into a single-platform
// OCI layout, from a local layout or a digest-pinned registry ref.
type ImageEvidenceCopier interface {
	CopyOCILayoutToOCILayout(ctx context.Context, sourceLayout, destLayout, osName, arch string) error
	CopyDockerDigestToOCILayout(ctx context.Context, ref, digest, destLayout, osName, arch string) error
}

// ImageEvidenceTrivy is the Trivy surface needed for local OCI-layout evidence.
type ImageEvidenceTrivy interface {
	RunInherit(ctx context.Context, stdout, stderr io.Writer, args ...string) (int, error)
}

// ImageEvidenceSyft is the Syft surface needed for optional CycloneDX output.
type ImageEvidenceSyft interface {
	Generate(ctx context.Context, target string, outputs map[string]string, errOut io.Writer) error
}

// ImageEvidenceInput drives local image evidence generation.
type ImageEvidenceInput struct {
	OCILayout           string
	LocalImageRef       string
	TrivyOutput         string
	SBOMOutput          string
	ScanLayout          string
	LocalManifest       string
	RegistryDigestRef   string
	RegistryRef         string
	Digest              string
	Platforms           []string
	TrivyOutputTemplate string
	SBOMOutputTemplate  string
	TrivyTimeout        string
	// TrivyAttempts bounds how often one Trivy scan is run before it counts
	// as failed, and TrivyRetryDelay is the base of the linear backoff between
	// attempts. Zero takes the default of each.
	TrivyAttempts   int
	TrivyRetryDelay time.Duration
	TempDir         string
}

// imageEvidenceScan is how one Trivy scan runs: its own timeout and the
// bounded retry around it.
type imageEvidenceScan struct {
	timeout  string
	attempts int
	delay    time.Duration
}

func imageEvidenceScanOf(in ImageEvidenceInput) (imageEvidenceScan, error) {
	if in.TrivyAttempts < 0 || in.TrivyRetryDelay < 0 {
		return imageEvidenceScan{}, fmt.Errorf("trivy attempts and retry delay must not be negative: %w", errs.ErrUsage)
	}

	return imageEvidenceScan{
		timeout:  trivyTimeoutValue(in.TrivyTimeout),
		attempts: retry.Attempts(in.TrivyAttempts, defaultImageEvidenceTrivyAttempts),
		delay:    retry.Delay(in.TrivyRetryDelay, defaultImageEvidenceTrivyRetryDelay),
	}, nil
}

// imageEvidenceDeps is the collaborator set the evidence pipeline shares: the
// two exporters (buildah for local images/manifests, a copier for per-platform
// OCI layouts), the two scanners (trivy, syft), and the two writers it narrates
// on. These six are exactly what ImageEvidence receives, and they thread down
// the scan tree, so passing them positionally gave the leaves six- to
// nine-parameter signatures.
//
// The set narrows with depth: once a layout exists the source exporters fall
// away, so the inner leaves leave buildah/copier unused. Per the layerDeps
// precedent in internal/app/sbom, an unused field is preferred to splitting the
// set into per-depth structs. The one exception is the innermost trivy primitive
// (runImageEvidenceTrivy): it would leave three fields unused, which obscures
// what it touches, so it keeps explicit parameters.
type imageEvidenceDeps struct {
	buildah ImageEvidenceBuildah
	copier  ImageEvidenceCopier
	trivy   ImageEvidenceTrivy
	syft    ImageEvidenceSyft
	out     io.Writer // progress, user-facing
	stderr  io.Writer // underlying tool output
}

// ImageEvidence scans an OCI layout with Trivy and optionally writes a CycloneDX SBOM with Syft.
func ImageEvidence(ctx context.Context, buildah ImageEvidenceBuildah, copier ImageEvidenceCopier, trivy ImageEvidenceTrivy, syft ImageEvidenceSyft, out, stderr io.Writer, in ImageEvidenceInput) error {
	if err := normalizeImageEvidenceRegistryDigestRef(&in); err != nil {
		return err
	}

	if err := validateImageEvidenceOutputPaths([]string{in.TrivyOutput, in.SBOMOutput}); err != nil {
		return err
	}

	scan, err := imageEvidenceScanOf(in)
	if err != nil {
		return err
	}

	deps := imageEvidenceDeps{buildah: buildah, copier: copier, trivy: trivy, syft: syft, out: out, stderr: stderr}

	if imageEvidenceSingleRegistryDigestRequested(in) {
		return imageEvidenceSingleRegistryDigest(ctx, deps, in, scan)
	}

	if imageEvidenceMultiArchRequested(in) {
		return imageEvidenceMultiArch(ctx, deps, in, scan)
	}

	if err := validateSingleImageEvidenceInput(in); err != nil {
		return err
	}

	layout := in.OCILayout

	if in.LocalImageRef != "" {
		exported, cleanup, err := exportImageEvidenceLayout(ctx, deps.buildah, deps.out, in)
		if err != nil {
			return err
		}

		defer cleanup()

		layout = exported
	}

	return scanImageEvidenceLayout(ctx, deps, layout, in.TrivyOutput, in.SBOMOutput, scan)
}

func imageEvidenceSingleRegistryDigestRequested(in ImageEvidenceInput) bool {
	return in.RegistryDigestRef != "" && in.TrivyOutput != "" && in.TrivyOutputTemplate == "" && in.SBOMOutputTemplate == "" && in.OCILayout == "" && in.LocalImageRef == "" && in.ScanLayout == "" && in.LocalManifest == ""
}

func imageEvidenceSingleRegistryDigest(ctx context.Context, deps imageEvidenceDeps, in ImageEvidenceInput, scan imageEvidenceScan) error {
	platforms, err := parseImageEvidencePlatforms(in.Platforms)
	if err != nil {
		return err
	}

	if len(platforms) != 1 {
		return fmt.Errorf("exactly one --platform is required with --registry-digest-ref and --trivy-output: %w", errs.ErrUsage)
	}

	layout, err := makeImageEvidenceTempDir(in, "image-evidence-registry.*")
	if err != nil {
		return err
	}

	defer func() { _ = os.RemoveAll(layout) }()

	platform := platforms[0]
	if err := deps.copier.CopyDockerDigestToOCILayout(ctx, in.RegistryRef, in.Digest, layout, platform.OS, platform.Arch); err != nil {
		return err
	}

	return scanImageEvidenceLayout(ctx, deps, layout, in.TrivyOutput, in.SBOMOutput, scan)
}

func validateSingleImageEvidenceInput(in ImageEvidenceInput) error {
	if (in.OCILayout == "") == (in.LocalImageRef == "") {
		return fmt.Errorf("set exactly one of --oci-layout or --local-image-ref: %w", errs.ErrUsage)
	}

	if in.TrivyOutput == "" {
		return fmt.Errorf("trivy output is required: %w", errs.ErrUsage)
	}

	if in.OCILayout != "" {
		if err := requireImageEvidenceDir(in.OCILayout, "oci layout"); err != nil {
			return err
		}
	}

	return nil
}

func imageEvidenceMultiArchRequested(in ImageEvidenceInput) bool {
	return in.ScanLayout != "" || in.LocalManifest != "" || in.RegistryDigestRef != "" || in.RegistryRef != "" || in.Digest != "" || len(in.Platforms) > 0 || in.TrivyOutputTemplate != "" || in.SBOMOutputTemplate != ""
}

func imageEvidenceMultiArch(ctx context.Context, deps imageEvidenceDeps, in ImageEvidenceInput, scan imageEvidenceScan) error {
	platforms, err := validateMultiArchImageEvidenceInput(in)
	if err != nil {
		return err
	}

	fullLayout, cleanup, err := imageEvidenceFullLayout(ctx, deps.buildah, deps.out, in)
	if err != nil {
		return err
	}
	defer cleanup()

	var firstScanErr error

	for _, platform := range platforms {
		scanErr, fatalErr := imageEvidenceScanOnePlatform(ctx, deps, in, scan, fullLayout, platform)
		if fatalErr != nil {
			return fatalErr
		}

		if scanErr != nil && firstScanErr == nil {
			firstScanErr = scanErr
		}
	}

	return firstScanErr
}

// imageEvidenceScanOnePlatform extracts one platform into a temp OCI layout
// and scans it. The first return value is a non-fatal scan/cleanup error (the
// caller keeps going); the second aborts the whole run.
func imageEvidenceScanOnePlatform(ctx context.Context, deps imageEvidenceDeps, in ImageEvidenceInput, scan imageEvidenceScan, fullLayout string, platform imageEvidencePlatform) (error, error) { //nolint:revive // (scanErr, fatalErr): non-fatal scan error vs run-aborting error.
	layout, err := makeImageEvidenceTempDir(in, "image-evidence-"+platform.Arch+".*")
	if err != nil {
		return nil, err
	}

	if fullLayout != "" {
		err = deps.copier.CopyOCILayoutToOCILayout(ctx, fullLayout, layout, platform.OS, platform.Arch)
	} else {
		err = deps.copier.CopyDockerDigestToOCILayout(ctx, in.RegistryRef, in.Digest, layout, platform.OS, platform.Arch)
	}

	if err != nil {
		_ = os.RemoveAll(layout)

		return nil, err
	}

	trivyOutput := renderImageEvidenceTemplate(in.TrivyOutputTemplate, platform)
	sbomOutput := renderImageEvidenceTemplate(in.SBOMOutputTemplate, platform)

	var scanErr error
	if err := scanImageEvidenceLayout(ctx, deps, layout, trivyOutput, sbomOutput, scan); err != nil {
		scanErr = err
	}

	if err := os.RemoveAll(layout); err != nil && scanErr == nil {
		scanErr = fmt.Errorf("remove image evidence temp layout: %w", err)
	}

	return scanErr, nil
}

func validateMultiArchImageEvidenceInput(in ImageEvidenceInput) ([]imageEvidencePlatform, error) {
	if in.OCILayout != "" || in.LocalImageRef != "" || in.TrivyOutput != "" || in.SBOMOutput != "" {
		return nil, fmt.Errorf("multi-arch image evidence uses --scan-layout/--local-manifest/--registry-ref with --trivy-output-template, not single-image source/output flags: %w", errs.ErrUsage)
	}

	platforms, err := parseImageEvidencePlatforms(in.Platforms)
	if err != nil {
		return nil, err
	}

	if err := validateMultiArchImageEvidenceTemplates(in, platforms); err != nil {
		return nil, err
	}

	if err := validateMultiArchImageEvidenceSource(in); err != nil {
		return nil, err
	}

	return platforms, nil
}

// validateMultiArchImageEvidenceTemplates checks the per-arch output templates.
func validateMultiArchImageEvidenceTemplates(in ImageEvidenceInput, platforms []imageEvidencePlatform) error {
	if in.TrivyOutputTemplate == "" {
		return fmt.Errorf("trivy output template is required: %w", errs.ErrUsage)
	}

	if err := validateImageEvidenceOutputTemplate(in.TrivyOutputTemplate, "trivy output template", platforms); err != nil {
		return err
	}

	if in.SBOMOutputTemplate != "" {
		if err := validateImageEvidenceOutputTemplate(in.SBOMOutputTemplate, "sbom output template", platforms); err != nil {
			return err
		}
	}

	paths := make([]string, 0, 2*len(platforms))
	for _, platform := range platforms {
		paths = append(paths, renderImageEvidenceTemplate(in.TrivyOutputTemplate, platform), renderImageEvidenceTemplate(in.SBOMOutputTemplate, platform))
	}

	return validateImageEvidenceOutputPaths(paths)
}

func validateImageEvidenceOutputPaths(paths []string) error {
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}

		absolute, err := filepath.Abs(path)
		if err != nil {
			return err
		}

		if seen[absolute] {
			return fmt.Errorf("image evidence output paths collide: %w", errs.ErrUsage)
		}

		seen[absolute] = true
	}

	return nil
}

// validateMultiArchImageEvidenceSource checks that exactly one usable image
// source was supplied.
func validateMultiArchImageEvidenceSource(in ImageEvidenceInput) error {
	if in.ScanLayout != "" {
		return requireImageEvidenceDir(in.ScanLayout, "scan layout")
	}

	if in.LocalManifest != "" {
		return nil
	}

	if in.RegistryRef == "" || in.Digest == "" {
		return fmt.Errorf("set --scan-layout, --local-manifest, --registry-digest-ref, or both --registry-ref and --digest for multi-arch image evidence: %w", errs.ErrUsage)
	}

	return nil
}

func normalizeImageEvidenceRegistryDigestRef(in *ImageEvidenceInput) error {
	if in.RegistryDigestRef == "" {
		if in.RegistryRef != "" || in.Digest != "" {
			if _, _, err := splitImageEvidenceRegistryDigestRef(in.RegistryRef + "@" + in.Digest); err != nil {
				return err
			}
		}

		return nil
	}

	ref, digest, err := splitImageEvidenceRegistryDigestRef(in.RegistryDigestRef)
	if err != nil {
		return err
	}

	if in.RegistryRef != "" && in.RegistryRef != ref {
		return fmt.Errorf("--registry-digest-ref conflicts with --registry-ref: %w", errs.ErrUsage)
	}

	if in.Digest != "" && in.Digest != digest {
		return fmt.Errorf("--registry-digest-ref conflicts with --digest: %w", errs.ErrUsage)
	}

	in.RegistryRef = ref
	in.Digest = digest

	return nil
}

func splitImageEvidenceRegistryDigestRef(value string) (string, string, error) {
	ref := strings.TrimSpace(value)
	if ref == "" || strings.ContainsAny(ref, " \t\n\r") {
		return "", "", fmt.Errorf("registry digest ref is empty or unsafe: %w", errs.ErrUsage)
	}

	imageRef, digest, ok := strings.Cut(ref, "@")
	if !ok || imageRef == "" || digest == "" || strings.Contains(digest, "@") {
		return "", "", fmt.Errorf("registry digest ref must be image@sha256:<digest>: %w", errs.ErrUsage)
	}

	if !domaincontainer.ValidDigest(digest) {
		return "", "", fmt.Errorf("registry digest ref has invalid digest %q: %w", digest, errs.ErrUsage)
	}

	if !domaincontainer.ValidRepositoryName(imageRef) && !domaincontainer.ValidTaggedRef(imageRef) {
		return "", "", fmt.Errorf("registry ref must be a canonical OCI repository or tag: %w", errs.ErrUsage)
	}

	return imageRef, digest, nil
}

func imageEvidenceFullLayout(ctx context.Context, buildah ImageEvidenceBuildah, out io.Writer, in ImageEvidenceInput) (string, func(), error) {
	if in.ScanLayout != "" {
		return in.ScanLayout, func() {}, nil
	}

	if in.LocalManifest == "" {
		return "", func() {}, nil
	}

	layout, err := makeImageEvidenceTempDir(in, "image-evidence-full.*")
	if err != nil {
		return "", nil, err
	}

	cleanup := func() { _ = os.RemoveAll(layout) }
	if err := buildah.ExportLocalManifestToLayout(ctx, in.LocalManifest, layout, out); err != nil {
		cleanup()

		return "", nil, err
	}

	return layout, cleanup, nil
}

type imageEvidencePlatform struct {
	Raw  string
	OS   string
	Arch string
}

func parseImageEvidencePlatforms(values []string) ([]imageEvidencePlatform, error) {
	raws := splitImageEvidenceList(values)
	if len(raws) == 0 {
		return nil, fmt.Errorf("at least one --platform is required for multi-arch image evidence: %w", errs.ErrUsage)
	}

	seen := make(map[string]bool, len(raws))

	platforms := make([]imageEvidencePlatform, 0, len(raws))
	for _, raw := range raws {
		// Evidence scans take exactly os/arch: the OCI variant component is
		// layered out here (the per-arch copy selects os/arch only),
		// on top of the domain parser that owns the OCI format rule.
		parsed, err := domaincontainer.ParsePlatform(raw)
		if err != nil || parsed.Variant != "" {
			return nil, fmt.Errorf("platform %q must be os/arch, for example linux/amd64: %w", raw, errs.ErrUsage)
		}

		osName, arch := parsed.OS, parsed.Arch

		if seen[raw] {
			return nil, fmt.Errorf("duplicate platform %q: %w", raw, errs.ErrUsage)
		}

		seen[raw] = true
		platforms = append(platforms, imageEvidencePlatform{Raw: raw, OS: osName, Arch: arch})
	}

	return platforms, nil
}

func splitImageEvidenceList(values []string) []string {
	var out []string

	for _, value := range values {
		for _, field := range strings.FieldsFunc(value, func(r rune) bool {
			return r == ',' || r == '\n' || r == '\t' || r == ' '
		}) {
			if field != "" {
				out = append(out, field)
			}
		}
	}

	return out
}

func validateImageEvidenceOutputTemplate(tpl, label string, platforms []imageEvidencePlatform) error {
	if !strings.Contains(tpl, "{arch}") && !strings.Contains(tpl, "{platform}") {
		return fmt.Errorf("%s must contain {arch} or {platform}: %w", label, errs.ErrUsage)
	}

	seen := make(map[string]bool, len(platforms))
	for _, platform := range platforms {
		path := renderImageEvidenceTemplate(tpl, platform)
		if path == "" {
			return fmt.Errorf("%s rendered an empty path for %s: %w", label, platform.Raw, errs.ErrUsage)
		}

		if seen[path] {
			return fmt.Errorf("%s renders duplicate path %q: %w", label, path, errs.ErrUsage)
		}

		seen[path] = true
	}

	return nil
}

func renderImageEvidenceTemplate(tpl string, platform imageEvidencePlatform) string {
	path := strings.ReplaceAll(tpl, "{platform}", strings.ReplaceAll(platform.Raw, "/", "-"))
	path = strings.ReplaceAll(path, "{os}", platform.OS)
	path = strings.ReplaceAll(path, "{arch}", platform.Arch)

	return path
}

func exportImageEvidenceLayout(ctx context.Context, buildah ImageEvidenceBuildah, out io.Writer, in ImageEvidenceInput) (string, func(), error) {
	layout, err := makeImageEvidenceTempDir(in, "image-evidence.*")
	if err != nil {
		return "", nil, fmt.Errorf("create image evidence temp layout: %w", err)
	}

	cleanup := func() { _ = os.RemoveAll(layout) }
	if err := buildah.ExportLocalImageToLayout(ctx, in.LocalImageRef, layout, out); err != nil {
		cleanup()

		return "", nil, err
	}

	return layout, cleanup, nil
}

// makeImageEvidenceTempDir creates a scratch layout under the run context's
// temp dir, or under the OS default when TempDir is empty (os.MkdirTemp
// treats "" as os.TempDir()).
//
// TempDir arrives from --temp-dir ($CI_TEMP_DIR, $RUNNER_TEMP); the app does
// not read the environment for it.
func makeImageEvidenceTempDir(in ImageEvidenceInput, pattern string) (string, error) {
	layout, err := os.MkdirTemp(in.TempDir, pattern)
	if err != nil {
		return "", fmt.Errorf("create image evidence temp layout: %w", err)
	}

	return layout, nil
}

func scanImageEvidenceLayout(ctx context.Context, deps imageEvidenceDeps, layout, trivyOutput, sbomOutput string, scan imageEvidenceScan) error { //nolint:cyclop // staging, scanner status, JSON validation and publication have independent failure boundaries.
	trivyStage, err := pathsafe.NewArtifactStaging(filepath.Dir(trivyOutput))
	if err != nil {
		return err
	}
	defer func() { _ = trivyStage.Close() }()

	trivyPath := filepath.Join(trivyStage.Root().Name(), filepath.Base(trivyOutput))

	var sbomStage *pathsafe.ArtifactStaging

	if sbomOutput != "" {
		if deps.syft == nil {
			return fmt.Errorf("SBOM output requires Syft: %w", errs.ErrUsage)
		}

		sbomStage, err = pathsafe.NewArtifactStaging(filepath.Dir(sbomOutput))
		if err != nil {
			return err
		}

		defer func() { _ = sbomStage.Close() }()
	}

	if err := retryImageEvidenceTrivy(ctx, deps, layout, trivyPath, scan); err != nil {
		return err
	}

	if err := validateImageEvidenceTrivyOutput(trivyPath); err != nil {
		return err
	}

	if sbomOutput != "" {
		sbomPath := filepath.Join(sbomStage.Root().Name(), filepath.Base(sbomOutput))
		if err := deps.syft.Generate(ctx, "oci-dir:"+layout, map[string]string{"cyclonedx-json": sbomPath}, deps.stderr); err != nil {
			return fmt.Errorf("syft oci-dir:%s: %w", layout, err)
		}

		body, err := readImageEvidenceReport(sbomPath)
		if err != nil {
			return err
		}

		var bom struct {
			Format      string `json:"bomFormat"`
			SpecVersion string `json:"specVersion"`
			Version     int    `json:"version"`
		}
		if json.Unmarshal(body, &bom) != nil || bom.Format != "CycloneDX" || bom.SpecVersion == "" || bom.Version < 1 {
			return fmt.Errorf("SBOM output must be a CycloneDX document with specVersion and positive version: %w", errs.ErrMalformedInput)
		}
	}

	if err := trivyStage.Install(); err != nil {
		return err
	}

	if sbomStage != nil {
		return sbomStage.Install()
	}

	return nil
}

// retryImageEvidenceTrivy runs the scan until it exits zero or the attempts
// are used up. A scanner that cannot be started is not run again, and each
// attempt starts without the report a failed one may have left.
func retryImageEvidenceTrivy(ctx context.Context, deps imageEvidenceDeps, layout, trivyOutput string, scan imageEvidenceScan) error {
	return retry.Run(ctx, nil, scan.attempts, scan.delay, func() error {
		if err := os.Remove(trivyOutput); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return retry.Permanent(fmt.Errorf("remove previous trivy output %s: %w", trivyOutput, err))
		}

		return runImageEvidenceTrivy(ctx, deps.trivy, deps.out, deps.stderr, layout, trivyOutput, scan.timeout)
	}, retry.OnRetry(func(attempt, total int, wait time.Duration, _ error) {
		_, _ = fmt.Fprintf(deps.out, "Trivy image scan failed (attempt %d/%d) for %s, retrying in %s...\n", attempt, total, layout, wait)
	}))
}

func runImageEvidenceTrivy(ctx context.Context, trivy ImageEvidenceTrivy, out, stderr io.Writer, layout, trivyOutput, timeout string) error {
	code, err := trivy.RunInherit(ctx, out, stderr,
		"image",
		"--input", layout,
		"--scanners", "vuln",
		"--skip-version-check",
		"--timeout", timeout,
		"--format", "json",
		"--output", trivyOutput,
	)
	if err != nil {
		return retry.Permanent(fmt.Errorf("trivy image --input %s: %w", layout, err))
	}

	if code != 0 {
		return fmt.Errorf("trivy image --input %s exited with status %d: %w", layout, code, errs.ErrDependencyUnavailable)
	}

	return nil
}

func validateImageEvidenceTrivyOutput(path string) error {
	body, err := readImageEvidenceReport(path)
	if err != nil {
		return fmt.Errorf("read trivy output %s: %w", path, err)
	}

	if _, err := security.ParseTrivyReport(body); err != nil {
		return fmt.Errorf("parse trivy output %s: %w", path, err)
	}

	return nil
}

func readImageEvidenceReport(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("scanner did not create a regular report: %w", errs.ErrMalformedInput)
	}

	return cliio.ReadFile(path)
}

func requireImageEvidenceDir(path, label string) error {
	info, err := os.Stat(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("%s not found: %s: %w", label, path, errs.ErrMissingInput)
	}

	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory: %s: %w", label, path, errs.ErrUsage)
	}

	return nil
}

func trivyTimeoutValue(value string) string {
	if value != "" {
		return value
	}

	return defaultImageEvidenceTrivyTimeout
}
