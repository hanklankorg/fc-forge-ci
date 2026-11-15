// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
)

// GitLabReleaseBundleEvidence is the verified checksum pair used by the live
// tamper tests; Bundle is empty when the checksums carry an OpenPGP signature
// instead. Successful pipeline status alone is not release acceptance.
type GitLabReleaseBundleEvidence struct {
	ChecksumName string
	Checksum     []byte
	Bundle       []byte
	Verified     int
}

var errReleaseEvidence = errors.New("release evidence contract violated")

const evidenceDistFlag = "--dist-dir"
const evidenceManifestFlag = "--manifest"
const evidenceDist = "dist"
const evidenceLedger = "dist/release-images.json"
const evidenceUnsignedFlag = "--unsigned"
const evidenceChecksumsCommand = "validate-checksums"
const evidenceExpectedManifest = "acceptance-expected.json"
const evidenceValidateCommand = "validate"

type releaseEvidenceCommand func(string, ...string) ([]byte, error)

// VerifyGitLabReleaseEvidence uses the native file-contract CLI in private
// workspaces, never changing the suite's working directory or reimplementing
// manifest/checksum policy. The caller binds all archives to the same native
// child pipeline and uses its authenticated release-asset reader for download.
// A nil publisher archive denotes a file-only release, selected by caller policy.
// verify checks a Cosign bundle over its subject, verifyOpenPGP an OpenPGP
// .asc.
func VerifyGitLabReleaseEvidence(ctx context.Context, binary string, producerZIP, publisherZIP, signerZIP []byte, names []string,
	download func(string) ([]byte, error), verify, verifyOpenPGP func([]byte, []byte) error,
) (GitLabReleaseBundleEvidence, error) {
	var result GitLabReleaseBundleEvidence

	root, err := os.MkdirTemp("", "gitlab-release-evidence-")
	if err != nil {
		return result, err
	}

	defer func() { _ = os.RemoveAll(root) }()

	producer, signer, published := filepath.Join(root, "producer"), filepath.Join(root, "signer"), filepath.Join(root, "published")
	if err := stageReleaseArchive(producerZIP, "_release-build/", producer); err != nil {
		return result, err
	}

	if err := stageReleaseArchive(signerZIP, "_release-sign/", signer); err != nil {
		return result, err
	}

	publisher, err := stageReleasePublisher(root, publisherZIP)
	if err != nil {
		return result, err
	}

	if err := verifyReleaseHandoffDigests(producer, publisher, signer); err != nil {
		return result, err
	}

	run := func(dir string, args ...string) ([]byte, error) {
		// The caller supplies the engine executable; arguments are constructed here.
		cmd := exec.CommandContext(ctx, binary, append([]string{"release", "files"}, args...)...) //nolint:gosec // G204: explicit engine under test, never an archive entry.
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "TMPDIR=" + root, "NO_COLOR=1"}

		var stderr bytes.Buffer

		cmd.Stderr = &stderr

		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("native release files %v: %w: %s", args, err, stderr.String())
		}

		return out, nil
	}

	flags := []string{evidenceDistFlag, evidenceDist, evidenceManifestFlag, "dist/release-files.json"}

	finalized, err := verifyRetainedRelease(producer, publisher, signer, flags, run)
	if err != nil {
		return result, err
	}

	checksumPath, err := run(signer, append([]string{"checksum-file"}, flags...)...)
	if err != nil {
		return result, err
	}

	checksum := strings.TrimSpace(string(checksumPath))

	result.ChecksumName = filepath.Base(checksum)
	if err := stagePublishedEvidence(signer, published, result.ChecksumName, names, finalized, download); err != nil {
		return result, err
	}

	retained, err := readEvidenceFile(signer, checksum)
	if err != nil {
		return result, err
	}

	result.Checksum, err = readEvidenceFile(published, checksum)
	if err != nil {
		return result, err
	}

	if !bytes.Equal(retained, result.Checksum) {
		return result, fmt.Errorf("published checksum bytes differ from retained signer subject: %w", errReleaseEvidence)
	}
	// Exact coverage and hashes use the native owner, including its exclusions
	// for checksum-self, evidence, provenance and post-signing sidecars.
	if _, err := run(published, append([]string{evidenceChecksumsCommand}, flags...)...); err != nil {
		return result, err
	}

	return verifyPublishedBundles(signer, published, checksum, finalized, verify, verifyOpenPGP, result)
}

func stageReleasePublisher(root string, body []byte) (string, error) {
	if body == nil {
		return "", nil
	}

	publisher := filepath.Join(root, "publisher")
	if err := stageReleaseArchive(body, "_release-images/", publisher); err != nil {
		return "", err
	}

	return publisher, nil
}

func verifyReleaseHandoffDigests(dirs ...string) error {
	for _, dir := range dirs {
		if dir == "" {
			continue
		}

		digest, err := readEvidenceFile(dir, "digest")
		if err != nil {
			return err
		}

		if err := apprelease.VerifyDistWithManifestRoot(filepath.Join(dir, "dist"), strings.TrimSpace(string(digest)), "."); err != nil {
			return err
		}
	}

	return nil
}

func verifyRetainedRelease(producer, publisher, signer string, flags []string, run releaseEvidenceCommand) (apprelease.FileManifest, error) {
	var empty apprelease.FileManifest

	if _, err := run(producer, append([]string{evidenceValidateCommand, evidenceUnsignedFlag}, flags...)...); err != nil {
		return empty, err
	}

	if _, err := run(producer, append([]string{evidenceChecksumsCommand}, flags...)...); err != nil {
		return empty, err
	}

	original, err := readReleaseEvidenceManifest(producer, "dist/release-files.json")
	if err != nil {
		return empty, err
	}

	if err := verifyProducerDeclaration(producer, original, run); err != nil {
		return empty, err
	}

	if publisher != "" {
		var imageSBOMs []string

		original, imageSBOMs, err = verifyPublisherDeclaration(producer, publisher, original, flags, run)
		if err != nil {
			return empty, err
		}

		producer = publisher

		// The signer attests the image SBOMs from its own copy; they are no
		// release files, so the ledger and each of them is held here.
		for _, name := range append([]string{evidenceLedger}, imageSBOMs...) {
			if err := compareEvidenceSubject(publisher, signer, name); err != nil {
				return empty, err
			}
		}
	}

	if err := compareProducerSubjects(producer, signer, original); err != nil {
		return empty, err
	}

	if _, err := run(signer, append([]string{evidenceValidateCommand}, flags...)...); err != nil {
		return empty, err
	}

	finalized, err := readReleaseEvidenceManifest(signer, "dist/release-files.json")
	if err != nil {
		return empty, err
	}

	if err := verifySignerDeclaration(signer, original, finalized, run); err != nil {
		return empty, err
	}

	return finalized, nil
}

// verifyPublisherDeclaration reconstructs what the trusted image publisher
// did to the build's hand-off: it wrote the image SBOMs the ledger pins into
// the one SBOM bundle and rewrote the bundle's checksums line. The native
// command does the same in the owned producer copy, with the publisher's
// ledger and image SBOM files; its admission owns the exact ledger-derived
// set and every content and pin check. The publisher's manifest, bundle,
// checksums file and every other release file must then be the bytes this
// produced, so nothing but the admitted image SBOMs was added to the bundle
// and no image SBOM became a release file of its own. It returns the
// publisher's manifest and the image SBOM files it admitted.
func verifyPublisherDeclaration(producer, publisher string, original apprelease.FileManifest, flags []string, run releaseEvidenceCommand) (apprelease.FileManifest, []string, error) {
	var empty apprelease.FileManifest

	if _, err := run(publisher, append([]string{evidenceValidateCommand, evidenceUnsignedFlag}, flags...)...); err != nil {
		return empty, nil, err
	}

	if _, err := run(publisher, append([]string{evidenceChecksumsCommand}, flags...)...); err != nil {
		return empty, nil, err
	}

	declared, err := readReleaseEvidenceManifest(publisher, "dist/release-files.json")
	if err != nil {
		return empty, nil, err
	}

	imageSBOMs, err := stagePublisherImageSBOMs(producer, publisher)
	if err != nil {
		return empty, nil, err
	}

	args := []string{"manifest", evidenceUnsignedFlag, evidenceDistFlag, evidenceDist, evidenceManifestFlag, "dist/release-files.json",
		"--output", evidenceExpectedManifest, "--release-images-ledger", evidenceLedger}

	// The publisher named its one SBOM bundle, and the published file set
	// holds that name. A hand-off without an SBOM has no bundle yet and takes
	// the project and version from it; the image SBOMs carry the version.
	if len(declared.SBOMs) != 1 {
		return empty, nil, fmt.Errorf("publisher manifest declares %d SBOM bundles, want one: %w", len(declared.SBOMs), errReleaseEvidence)
	}

	naming, ok := sbomBundleNaming(declared.SBOMs[0].Name)
	if !ok {
		return empty, nil, fmt.Errorf("publisher manifest's SBOM %s is not <project>-<version>%s: %w", declared.SBOMs[0].Name, sbomBundleSuffix, errReleaseEvidence)
	}

	if len(original.SBOMs) == 0 {
		args = append(args, naming...)
	} else {
		args = append(args, naming[2:]...)
	}

	for _, name := range imageSBOMs {
		args = append(args, "--image-sbom", name)
	}

	if _, err := run(producer, args...); err != nil {
		return empty, nil, err
	}

	expected, err := readReleaseEvidenceManifest(producer, evidenceExpectedManifest)
	if err != nil {
		return empty, nil, err
	}

	if !sameReleaseManifest(declared, expected) {
		return empty, nil, fmt.Errorf("publisher manifest disagrees with native extension of producer declaration: %w", errReleaseEvidence)
	}

	return declared, imageSBOMs, compareProducerSubjects(producer, publisher, declared)
}

// stagePublisherImageSBOMs copies the publisher's ledger and the image SBOM
// files it wrote into the owned producer copy, and returns those files. They
// are the publisher's two output families, directly in dist, that the build
// did not hand over; which of them the ledger pins is the native admission's
// decision, and it refuses any other.
func stagePublisherImageSBOMs(producer, publisher string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(publisher, evidenceDist))
	if err != nil {
		return nil, err
	}

	names := []string{evidenceLedger}

	for _, entry := range entries {
		name := entry.Name()
		// <name>-<version>-<sha>-analyzed-container-<platform|index>-sbom.cyclonedx.json,
		// and the names image SBOMs had before, which admission refuses.
		imageSBOM := strings.Contains(name, "-analyzed-container-") && strings.HasSuffix(name, "-sbom.cyclonedx.json")
		former := (strings.HasPrefix(name, "image-sbom-") && strings.HasSuffix(name, ".cyclonedx.json")) ||
			(strings.HasPrefix(name, "sbom-") && strings.HasSuffix(name, ".json"))

		if _, err := os.Lstat(filepath.Join(producer, evidenceDist, name)); err == nil || (!imageSBOM && !former) {
			continue
		}

		names = append(names, filepath.Join(evidenceDist, name))
	}

	for _, name := range names {
		body, err := readEvidenceFile(publisher, name)
		if err != nil {
			return nil, err
		}

		if err := os.WriteFile(filepath.Join(producer, name), body, 0o600); err != nil {
			return nil, err
		}
	}

	return names[1:], nil
}

// sbomBundleNaming returns the arguments that name the SBOM bundle called
// name. The constructor drops one v from the version it is given.
func sbomBundleNaming(name string) ([]string, bool) {
	stem, isBundle := strings.CutSuffix(name, sbomBundleSuffix)
	dash := strings.LastIndex(stem, "-")

	if !isBundle || dash < 1 {
		return nil, false
	}

	return []string{"--project-name", stem[:dash], "--version", "v" + stem[dash+1:]}, true
}

func verifyProducerDeclaration(producer string, original apprelease.FileManifest, run releaseEvidenceCommand) error {
	// Recreate producer classification through the owning constructors. A
	// non-empty but fabricated Source/section is not an accepted declaration.
	args := []string{"import-goreleaser", evidenceDistFlag, evidenceDist, evidenceManifestFlag, evidenceExpectedManifest}

	_, err := os.Stat(filepath.Join(producer, "dist/artifacts.json"))
	declared := os.IsNotExist(err)

	if err != nil && !declared {
		return err
	}

	if declared {
		args = []string{"declare", evidenceDistFlag, evidenceDist, evidenceManifestFlag, evidenceExpectedManifest, "--checksums-file", original.Checksums[0].Path}
		for _, a := range original.Assets {
			if slices.ContainsFunc(original.Checksums, func(e apprelease.FileEntry) bool { return e.Path == a.Path }) ||
				slices.ContainsFunc(original.Evidence, func(e apprelease.FileEntry) bool { return e.Path == a.Path }) ||
				slices.ContainsFunc(original.SBOMs, func(e apprelease.FileEntry) bool { return e.Path == a.Path }) {
				continue
			}

			args = append(args, "--asset", a.Path)
		}
	}

	// Both constructors write checksums.sha256. Only the validated checksum
	// file in this owned copy is replaced; the retained signer/published bytes
	// remain untouched.
	if err := os.Remove(filepath.Join(producer, original.Checksums[0].Path)); err != nil {
		return err
	}

	bundleArgs, err := restageProducerSBOMBundle(producer, original, declared)
	if err != nil {
		return err
	}

	args = append(args, bundleArgs...)

	for _, a := range original.Evidence {
		args = append(args, "--evidence-file", a.Path)
	}

	if _, err := run(producer, args...); err != nil {
		return err
	}

	expectedProducer, err := readReleaseEvidenceManifest(producer, evidenceExpectedManifest)
	if err != nil {
		return err
	}

	if !sameReleaseManifest(original, expectedProducer) {
		return fmt.Errorf("producer manifest disagrees with native producer classification: %w", errReleaseEvidence)
	}

	return nil
}

// restageProducerSBOMBundle puts the owned producer copy back to the state
// before its constructor bundled the SBOMs, and returns the arguments that
// make the constructor write the same bundle again: its name's project and
// version, and for `declare` the SBOM files the hand-off still holds, found by
// the names of the bundle's entries. The bundle is reproducible, so the
// rewritten bundle and checksums must equal the signer's retained bytes, which
// compareProducerSubjects holds them to. A hand-off has one bundle or no SBOM.
func restageProducerSBOMBundle(producer string, original apprelease.FileManifest, declared bool) ([]string, error) {
	if len(original.SBOMs) == 0 {
		return nil, nil
	}

	bundle := original.SBOMs[0]

	args, isBundle := sbomBundleNaming(bundle.Name)
	if len(original.SBOMs) != 1 || !isBundle {
		return nil, fmt.Errorf("producer manifest's SBOMs %v are not the one <project>-<version>%s: %w", original.SBOMs, sbomBundleSuffix, errReleaseEvidence)
	}

	body, err := readEvidenceFile(producer, bundle.Path)
	if err != nil {
		return nil, err
	}

	entries, err := tarGzipNames(body)
	if err != nil {
		return nil, fmt.Errorf("producer SBOM bundle %s: %w", bundle.Name, err)
	}

	if err := os.Remove(filepath.Join(producer, bundle.Path)); err != nil {
		return nil, err
	}

	if !declared {
		// The import bundles the SBOMs artifacts.json lists again.
		return args, nil
	}

	for _, entry := range entries {
		matches, err := evidenceFilesNamed(filepath.Join(producer, evidenceDist), entry)
		if err != nil {
			return nil, err
		}

		if len(matches) != 1 {
			return nil, fmt.Errorf("producer hand-off holds %d files named %s, the SBOM bundle's entry, want one: %w", len(matches), entry, errReleaseEvidence)
		}

		args = append(args, "--sbom", filepath.Join(evidenceDist, matches[0]))
	}

	return args, nil
}

// tarGzipNames lists the entries of a tar.gz in order.
func tarGzipNames(body []byte) ([]string, error) {
	unzipped, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	var names []string

	for reader := tar.NewReader(unzipped); ; {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return names, nil
		}

		if err != nil {
			return nil, err
		}

		names = append(names, header.Name)
	}
}

// evidenceFilesNamed lists the regular files called name under root, relative
// to it.
func evidenceFilesNamed(root, name string) ([]string, error) {
	var matches []string

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.Type().IsRegular() && entry.Name() == name {
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}

			matches = append(matches, relative)
		}

		return nil
	})

	return matches, err
}

func compareProducerSubjects(producer, signer string, original apprelease.FileManifest) error {
	for _, a := range original.Assets {
		if err := compareEvidenceSubject(producer, signer, a.Path); err != nil {
			return err
		}
	}

	return nil
}

func compareEvidenceSubject(beforeRoot, afterRoot, name string) error {
	before, err := readEvidenceFile(beforeRoot, name)
	if err != nil {
		return err
	}

	after, err := readEvidenceFile(afterRoot, name)
	if err != nil {
		return err
	}

	if !bytes.Equal(before, after) {
		return fmt.Errorf("handoff changed producer subject %s: %w", name, errReleaseEvidence)
	}

	return nil
}

func verifySignerDeclaration(signer string, original, finalized apprelease.FileManifest, run releaseEvidenceCommand) error {
	// Finalize the actual producer declaration against retained signer files;
	// do not trust roles asserted only by the finalized manifest.
	body, err := json.Marshal(original)
	if err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(signer, "producer-manifest.json"), body, 0o600); err != nil {
		return err
	}

	if _, err := run(signer, "manifest", evidenceDistFlag, evidenceDist, evidenceManifestFlag, "producer-manifest.json", "--output", evidenceExpectedManifest); err != nil {
		return err
	}

	expectedSigner, err := readReleaseEvidenceManifest(signer, evidenceExpectedManifest)
	if err != nil {
		return err
	}

	if !sameReleaseManifest(finalized, expectedSigner) {
		return fmt.Errorf("signer manifest disagrees with native finalization of producer declaration: %w", errReleaseEvidence)
	}

	return nil
}

func stagePublishedEvidence(signer, published, checksumName string, names []string, finalized apprelease.FileManifest, download func(string) ([]byte, error)) error {
	wanted := make([]string, 0, len(finalized.Assets))
	for _, a := range finalized.Assets {
		wanted = append(wanted, a.Name)
	}

	actual := slices.Clone(names)

	slices.Sort(wanted)
	slices.Sort(actual)

	if !slices.Equal(wanted, actual) {
		return fmt.Errorf("published release asset set differs from finalized signer declaration: %w", errReleaseEvidence)
	}

	// The checksums and the provenance statement carry a Cosign bundle or,
	// for a declaration that signs its files with GPG, an OpenPGP .asc.
	for required, signatures := range map[string][]string{
		checksumName:                       {cosignBundle, openPGPSignature},
		apprelease.ProvenanceStatementName: {cosignBundle, openPGPSignature},
	} {
		signed := slices.ContainsFunc(signatures, func(suffix string) bool { return slices.Contains(wanted, required+suffix) })
		if !slices.Contains(wanted, required) || !signed {
			return fmt.Errorf("release lacks mandatory signed subject %s: %w", required, errReleaseEvidence)
		}
	}

	if err := os.MkdirAll(filepath.Join(published, "dist"), 0o700); err != nil {
		return err
	}

	manifestBody, err := readEvidenceFile(signer, "dist/release-files.json")
	if err != nil {
		return err
	}

	root, err := os.OpenRoot(published)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	if err := root.WriteFile("dist/release-files.json", manifestBody, 0o600); err != nil {
		return err
	}

	var remaining int64 = 1 << 30

	for _, a := range finalized.Assets {
		payload, err := download(a.Name)
		if err != nil {
			return fmt.Errorf("published asset %s: %w", a.Name, err)
		}

		remaining -= int64(len(payload))
		if remaining < 0 {
			return fmt.Errorf("published release exceeds evidence byte budget: %w", errReleaseEvidence)
		}

		if err := root.MkdirAll(filepath.Dir(a.Path), 0o700); err != nil {
			return err
		}

		if err := root.WriteFile(a.Path, payload, 0o600); err != nil {
			return err
		}
	}

	return nil
}

// verifyPublishedBundles verifies every published signature, and requires
// every published file to have one of its own (releaseFilesUnsigned).
func verifyPublishedBundles(signer, published, checksum string, finalized apprelease.FileManifest, verify, verifyOpenPGP func([]byte, []byte) error, result GitLabReleaseBundleEvidence) (GitLabReleaseBundleEvidence, error) {
	names, verified := make([]string, 0, len(finalized.Assets)), map[string]bool{}

	for _, asset := range finalized.Assets {
		names = append(names, asset.Name)

		check := verify

		plain, bundled := strings.CutSuffix(asset.Path, cosignBundle)
		if !bundled {
			var armored bool
			if plain, armored = strings.CutSuffix(asset.Path, openPGPSignature); !armored {
				continue
			}

			check = verifyOpenPGP
		}

		blob, err := readEvidenceFile(published, plain)
		if err != nil {
			return result, err
		}

		retainedSubject, err := readEvidenceFile(signer, plain)
		if err != nil {
			return result, err
		}

		if !bytes.Equal(blob, retainedSubject) {
			return result, fmt.Errorf("published signed subject %s differs from retained signer bytes: %w", plain, errReleaseEvidence)
		}

		bundle, err := readEvidenceFile(published, asset.Path)
		if err != nil {
			return result, err
		}

		if err := check(blob, bundle); err != nil {
			return result, fmt.Errorf("published signature %s: %w", asset.Name, err)
		}

		result.Verified++
		verified[filepath.Base(plain)] = true

		if bundled && plain == checksum {
			result.Bundle = bundle
		}
	}

	return result, releaseFilesUnsigned(names, verified)
}

func readReleaseEvidenceManifest(root, name string) (apprelease.FileManifest, error) {
	var m apprelease.FileManifest

	b, err := readEvidenceFile(root, name)
	if err != nil {
		return m, err
	}

	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&m)

	return m, err
}

func readEvidenceFile(directory, name string) ([]byte, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()

	return root.ReadFile(name)
}

func sameReleaseManifest(a, b apprelease.FileManifest) bool {
	// Sections are sets, but every native validator has already rejected
	// duplicate/conflicting entries. Preserve all path/name/source fields.
	for _, m := range []*apprelease.FileManifest{&a, &b} {
		for _, section := range []*[]apprelease.FileEntry{&m.Assets, &m.Checksums, &m.SBOMs, &m.Evidence, &m.Provenance} {
			*section = slices.Clone(*section)
			slices.SortFunc(*section, func(x, y apprelease.FileEntry) int { return strings.Compare(x.Path, y.Path) })

			if len(*section) == 0 {
				*section = nil
			}
		}
	}

	return reflect.DeepEqual(a, b)
}

func stageReleaseArchive(body []byte, prefix, root string) error {
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return err
	}

	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}

	directory, err := os.OpenRoot(root)
	if err != nil {
		return err
	}

	defer func() { _ = directory.Close() }()

	var remaining int64 = 1 << 30

	for _, entry := range archive.File {
		if entry.FileInfo().IsDir() {
			if !cleanZIPEntryName(strings.TrimSuffix(entry.Name, "/")) {
				return fmt.Errorf("unsafe archive directory %q: %w", entry.Name, errReleaseEvidence)
			}

			continue
		}

		if !cleanZIPEntryName(entry.Name) || !entry.Mode().IsRegular() {
			return fmt.Errorf("unsafe archive entry %q: %w", entry.Name, errReleaseEvidence)
		}

		name, found := strings.CutPrefix(entry.Name, prefix)
		if !found || (name != "digest" && !strings.HasPrefix(name, "dist/")) {
			continue
		}

		body, err := readZIPEntry(entry, remaining)
		if err != nil {
			return err
		}

		remaining -= int64(len(body))

		if err := createEvidenceFile(directory, name, body); err != nil {
			return err
		}
	}

	return nil
}

func createEvidenceFile(directory *os.Root, name string, body []byte) error {
	if err := directory.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}

	file, err := directory.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}

	_, writeErr := file.Write(body)

	return errors.Join(writeErr, file.Close())
}

// GitLabReleaseArchive retains the public handoff files, not signer credentials,
// through the existing confined evidence writer before provider cleanup.
func GitLabReleaseArchive(tb TB, target Target, address, evidenceName string) []byte {
	tb.Helper()

	body := Download(tb, target, address, 1<<30)
	if root := snapshotEvidenceRoot(); root != "" {
		if err := writeSnapshotEvidence(root, evidenceName, body); err != nil {
			tb.Fatalf("retain native release evidence: %v", err)
		}
	}

	return body
}
