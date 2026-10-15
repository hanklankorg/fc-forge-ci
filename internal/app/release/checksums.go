// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/diggsweden/forge-ci/internal/clicolor"
	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// ChecksumsInput drives `forge-ci release checksums`.
type ChecksumsInput struct {
	OutputFile          string // default: domainrelease.ChecksumsFile
	ReleaseArtifactsDir string // default: ./release-artifacts
	AttachArtifacts     string // comma-separated glob list (verbatim from artifacts.yml)
	SBOMDir             string // default: ./sbom-artifacts
	WorkingDir          string // glob root for the *-sbom.{spdx,cyclonedx}.json patterns; default: cwd
	AssemblyFile        string // when set, checksum exactly the staged release assembly
}

// withChecksumDiscoveryDefaults fills the directories and output path the
// discovery mode scans when the caller named none.
func withChecksumDiscoveryDefaults(in ChecksumsInput) ChecksumsInput {
	if in.OutputFile == "" {
		in.OutputFile = domainrelease.ChecksumsFile
	}

	if in.ReleaseArtifactsDir == "" {
		in.ReleaseArtifactsDir = domainrelease.DefaultReleaseArtifactsDir
	}

	if in.SBOMDir == "" {
		in.SBOMDir = domainrelease.DefaultSBOMArtifactsDir
	}

	return in
}

// checksumManifest is the sha256sum-format file both discovery modes write.
//
// The two modes differ in how they find files, not in how the manifest is
// produced, and they had grown two copies of the producing half: only the
// assembly path created the output directory, so --output some/dir/file worked
// under --assembly and failed without it. Owning creation, the line format,
// the count and the summary in one place is what keeps those in step.
//
//nolint:cyclop // emits checksum file + uploads + summary entry per artifact.
type checksumManifest struct {
	file  io.WriteCloser
	path  string
	count int
}

// newChecksumManifest creates the manifest file, and any directory it needs.
func newChecksumManifest(outputFile string) (*checksumManifest, error) {
	if dir := filepath.Dir(outputFile); outputFile != cliio.StdSentinel && dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec,mnd // public release staging dir.
			return nil, fmt.Errorf("create checksum dir for %q: %w", outputFile, err)
		}
	}

	file, err := cliio.CreateWriter(outputFile, 0o644)
	if err != nil {
		return nil, fmt.Errorf("create %q: %w", outputFile, err)
	}

	return &checksumManifest{file: file, path: outputFile}, nil
}

// add hashes path and records it under label, the name a consumer running
// sha256sum --check resolves.
func (m *checksumManifest) add(path, label string) error {
	if strings.ContainsAny(label, "\r\n") {
		return fmt.Errorf("checksum filename contains a line break: %q: %w", label, errs.ErrValidation)
	}

	hash, err := sha256File(path)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(m.file, "%s  %s\n", hash, label); err != nil {
		return err
	}

	m.count++

	return nil
}

func (m *checksumManifest) close() { _ = m.file.Close() }

// finish closes the manifest before reporting it. A failed close can leave the
// manifest incomplete on disk, so it fails the command instead of being
// reported as generated.
func (m *checksumManifest) finish(out io.Writer) (int, error) {
	if err := m.file.Close(); err != nil {
		return m.count, fmt.Errorf("close %q: %w", m.path, err)
	}

	m.report(out)

	return m.count, nil
}

func (m *checksumManifest) report(out io.Writer) {
	_, _ = fmt.Fprintf(out, "%s Generated %d checksums in %s\n", clicolor.Check(out), m.count, m.path)
}

// Checksums computes SHA256 over a fileset and writes lines of the form
//
//	<hex-hash>  <filename>
//
// to OutputFile (sha256sum-compatible). The fileset is composed from:
//
//   - release-artifacts/*       → basename in the manifest
//   - $ATTACH_ARTIFACTS globs   → original path in the manifest
//   - sbom-artifacts/*-analyzed-container-sbom.*.json → basename
//   - cwd *-sbom.{spdx,cyclonedx}.json → basename
//
// Returns the line count and any I/O error.
func Checksums(out io.Writer, in ChecksumsInput) (int, error) {
	if in.AssemblyFile != "" {
		return checksumsFromAssembly(out, in)
	}

	in = withChecksumDiscoveryDefaults(in)

	manifest, err := newChecksumManifest(in.OutputFile)
	if err != nil {
		return 0, err
	}

	defer manifest.close()

	write := manifest.add

	// Compute the output-file absolute path once so the per-walk
	// helpers can skip it. Without this guard, pointing
	// --release-artifacts-dir at the directory that holds the
	// checksums file itself would hash the freshly-created empty
	// file (sha256 e3b0c44…) and the resulting manifest would fail
	// `sha256sum --check` against itself.
	outputAbs := absOrSelf(in.OutputFile)

	skip := func(path string) bool {
		return outputAbs != "" && absOrSelf(path) == outputAbs
	}

	// Release artifacts: basename labels.
	if err := checksumReleaseArtifacts(in.ReleaseArtifactsDir, out, write, skip); err != nil {
		return manifest.count, err
	}

	// Attach-artifacts globs: keep original path in manifest.
	if err := checksumAttachArtifacts(in.AttachArtifacts, out, write, skip); err != nil {
		return manifest.count, err
	}

	// Container SBOMs: basename labels.
	if err := checksumContainerSBOMs(in.SBOMDir, out, write, skip); err != nil {
		return manifest.count, err
	}

	// Working-dir SBOMs: basename labels.
	if err := checksumWorkdirSBOMs(in.WorkingDir, out, write, skip); err != nil {
		return manifest.count, err
	}

	return manifest.finish(out)
}

//nolint:cyclop // sequential: read assembly → resolve output path → ensure dir → hash each entry → write manifest. Phases, not nested logic.
func checksumsFromAssembly(out io.Writer, in ChecksumsInput) (int, error) {
	asm, err := readAssembly(in.AssemblyFile)
	if err != nil {
		return 0, err
	}

	outputFile := in.OutputFile
	if outputFile == "" {
		outputFile = asm.ChecksumFile
	}

	if outputFile == "" {
		outputFile = domainrelease.ChecksumsFile
	}

	if outputErr := validateAssemblyChecksumOutput(outputFile, in.AssemblyFile, asm); outputErr != nil {
		return 0, outputErr
	}

	manifest, err := newChecksumManifest(outputFile)
	if err != nil {
		return 0, err
	}

	defer manifest.close()

	// The assembly names its inputs, so a missing one is an error rather than
	// something to walk past — the discovery modes only ever see files that
	// exist. That is the whole difference between the two here.
	write := func(path, label string) error {
		if !regularFileExists(path) {
			return fmt.Errorf("assembly checksum input %q is missing or not a regular file: %w", path, errs.ErrMissingInput)
		}

		return manifest.add(path, label)
	}

	for _, asset := range asm.Assets {
		if err := write(asset.Path, asset.Name); err != nil {
			return manifest.count, err
		}
	}

	if asm.SBOMZipFile != "" {
		if regularFileExists(asm.SBOMZipFile) {
			if err := write(asm.SBOMZipFile, filepath.Base(asm.SBOMZipFile)); err != nil {
				return manifest.count, err
			}
		} else if len(asm.SBOMs) > 0 {
			return manifest.count, fmt.Errorf("assembly SBOM ZIP %q is missing; run release sbom-zip --assembly first: %w", asm.SBOMZipFile, errs.ErrMissingInput)
		}
	}

	return manifest.finish(out)
}

// validateAssemblyChecksumOutput prevents the truncating writer from destroying
// the assembly or a declared file. The checksum file itself may be regenerated.
func validateAssemblyChecksumOutput(outputFile, assemblyFile string, asm domainrelease.Assembly) error {
	if outputFile == cliio.StdSentinel {
		return nil
	}

	inputs := make([]string, 0, len(asm.Assets)+len(asm.SBOMs)+2)
	inputs = append(inputs, asm.SBOMZipFile)

	if assemblyFile != cliio.StdSentinel {
		inputs = append(inputs, assemblyFile)
	}

	for _, files := range [][]domainrelease.AssemblyFile{asm.Assets, asm.SBOMs} {
		for _, file := range files {
			inputs = append(inputs, file.Path)
		}
	}

	return checkChecksumOutputInputs(outputFile, inputs)
}

// Compare both path spellings and existing file identities: a different name
// for a hard-linked input would otherwise be truncated just as the input would.
func checkChecksumOutputInputs(outputFile string, inputs []string) error {
	outputInfo, err := os.Stat(outputFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect checksum output %q: %w", outputFile, err)
	}

	outputAbs := absOrSelf(outputFile)

	for _, input := range inputs {
		if input == "" {
			continue
		}

		inputInfo, err := os.Stat(input)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("inspect assembly input %q: %w", input, err)
		}

		if outputAbs == absOrSelf(input) || os.SameFile(outputInfo, inputInfo) {
			return fmt.Errorf("checksum output %q overlaps assembly input %q: %w", outputFile, input, errs.ErrValidation)
		}
	}

	return nil
}

// checksumReleaseArtifacts walks dir (one level) and invokes write for
// every regular file, labelling the manifest entry with basename. A
// missing dir is silently treated as empty. skip lets the caller
// exclude the output manifest itself.
func checksumReleaseArtifacts(dir string, out io.Writer, write func(absPath, label string) error, skip func(string) bool) error {
	root, err := pathsafe.OpenRoot(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	defer func() { _ = root.Close() }()

	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out, "→ Checksumming release artifacts from %s\n", dir)

	for _, e := range entries { //nolint:varnamelen // idiomatic loop var for os.DirEntry.
		if e.IsDir() {
			continue
		}

		path := filepath.Join(dir, e.Name())
		if skip != nil && skip(path) {
			continue
		}

		if err := write(path, e.Name()); err != nil {
			return err
		}
	}

	return nil
}

// checksumContainerSBOMs walks dir for files matching the
// analyzed-container SBOM pattern. Missing dir → no-op. skip excludes
// the output manifest when it lives inside the walked directory.
func checksumContainerSBOMs(dir string, out io.Writer, write func(absPath, label string) error, skip func(string) bool) error {
	matches, err := globAll(dir, []string{domainrelease.AnalyzedContainerSBOMPattern})
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out, "→ Checksumming container SBOMs from %s\n", dir)

	for _, path := range matches {
		if skip != nil && skip(path) {
			continue
		}

		if err := write(path, filepath.Base(path)); err != nil {
			return err
		}
	}

	return nil
}

// checksumWorkdirSBOMs globs the SBOM-layer patterns rooted at workdir
// (defaulting to ".") and invokes write for each regular-file match.
// skip excludes the output manifest when it sits in workdir.
func checksumWorkdirSBOMs(workdir string, out io.Writer, write func(absPath, label string) error, skip func(string) bool) error {
	root := workdir
	if root == "" {
		root = "."
	}

	_, _ = fmt.Fprintln(out, "→ Checksumming all SBOM layers")

	matches, err := globAll(root, domainrelease.SBOMFilePatterns)
	if err != nil {
		return err
	}

	for _, m := range matches {
		if skip != nil && skip(m) {
			continue
		}

		if err := write(m, filepath.Base(m)); err != nil {
			return err
		}
	}

	return nil
}

func sha256File(path string) (string, error) {
	file, err := openReleaseFile(path)
	if err != nil {
		return "", fmt.Errorf("open %q: %w", path, err)
	}

	defer func() { _ = file.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", fmt.Errorf("hash %q: %w", path, err)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// checksumAttachArtifacts iterates each glob in patterns (comma-separated)
// and invokes write(absPath, manifestLabel) for every match that exists
// and is a regular file. No-op when patterns is empty. skip excludes
// the output manifest when a glob accidentally matches it.
//
//nolint:cyclop // CSV → globs → stat → filter → write — one branch per phase.
func checksumAttachArtifacts(patterns string, out io.Writer, write func(absPath, label string) error, skip func(string) bool) error {
	if patterns == "" {
		return nil
	}

	_, _ = fmt.Fprintf(out, "→ Checksumming attached artifacts matching: %s\n", patterns)

	for raw := range strings.SplitSeq(patterns, ",") {
		pattern := strings.TrimSpace(raw)
		if pattern == "" {
			continue
		}

		matches, err := filepath.Glob(pattern)
		if err != nil {
			return fmt.Errorf("glob %q: %w", pattern, err)
		}

		for _, m := range matches { //nolint:varnamelen // idiomatic short name (testing/http/io conventions).
			info, statErr := os.Lstat(m)
			if statErr != nil || info.IsDir() {
				continue
			}

			if skip != nil && skip(m) {
				continue
			}

			if writeErr := write(m, m); writeErr != nil {
				return writeErr
			}
		}
	}

	return nil
}

// absOrSelf returns filepath.Abs(path) when possible, otherwise the
// input unchanged. Used to compare an arbitrary input path against the
// manifest output path without erroring out when either is already
// absolute or filesystem-resolution fails.
func absOrSelf(path string) string {
	if path == "" {
		return ""
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}

	return abs
}
