// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/provenance"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
)

// VerifyReleaseReadback composes signature authentication and published
// checksum verification with the release evidence owner, and refuses an
// offered file that neither binds (releaseFilesUnbound) and one without a
// verified signature of its own (releaseFilesUnsigned). A .bundle is
// verified with verify, the Cosign one, and an .asc with verifyOpenPGP. Bounded
// provider reads and native signature verification are supplied by the
// caller.
func VerifyReleaseReadback(evidence *ReleaseEvidence, names []string, download func(string) ([]byte, error), verify, verifyOpenPGP func([]byte, []byte) error) (map[string]bool, error) {
	read, err := evidence.CaptureReadback(download)
	if err != nil {
		return nil, err
	}

	authenticatedChecksums := map[string][]byte{}
	pair := func(verify func([]byte, []byte) error) func(subject, sidecar string) error {
		return func(subject, sidecar string) error {
			body, err := read(subject)
			if err != nil {
				return err
			}

			signature, err := read(sidecar)
			if err != nil {
				return err
			}

			if err := verify(body, signature); err != nil {
				return err
			}

			if isReleaseChecksums(subject) {
				authenticatedChecksums[subject] = body
			}

			return nil
		}
	}

	verified, bundleErr := VerifyReleaseBundles(names, pair(verify))
	signed, openPGPErr := verifyReleaseSidecars(names, openPGPSignature, "OpenPGP signature", pair(verifyOpenPGP))
	maps.Copy(verified, signed)

	failures := []error{bundleErr, openPGPErr}

	// These are the very bytes handed to signature verification, not another
	// read of even the same asset name or retained file after authentication.
	for _, body := range authenticatedChecksums {
		failures = append(failures, VerifyReleaseChecksums(body, names, read))
	}

	failures = append(failures, releaseFilesUnbound(names, verified, authenticatedChecksums), releaseFilesUnsigned(names, verified), releaseImageSBOMsUnbundled(names), releaseChecksumsMissing(names))

	return verified, errors.Join(failures...)
}

// releaseImageSBOMsUnbundled refuses an image SBOM offered as a release file
// of its own. On every forge the one SBOM bundle holds them, each named
// <name>-<version>-<sha>-analyzed-container-<platform|index>-sbom.<format>.json:
// an image's index SBOM and its platform SBOMs. The names image SBOMs had
// before (image-sbom-<id>.cyclonedx.json, sbom-<id>-<arch>.json and
// *-analyzed-container-sbom.<format>.json) are refused as well.
func releaseImageSBOMsUnbundled(names []string) error {
	var unbundled []string

	for _, name := range names {
		subject := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, cosignBundle), openPGPSignature), packageSignature)
		if strings.HasPrefix(subject, "image-sbom-") || (strings.HasPrefix(subject, "sbom-") && strings.HasSuffix(subject, ".json")) || strings.Contains(subject, "container-sbom.") ||
			(strings.Contains(subject, "-analyzed-container-") && strings.Contains(subject, "-sbom.")) {
			unbundled = append(unbundled, name)
		}
	}

	if len(unbundled) != 0 {
		slices.Sort(unbundled)

		return fmt.Errorf("image SBOMs %q are release files of their own, outside the SBOM bundle: %w", unbundled, errReleaseEvidence)
	}

	return nil
}

// isReleaseChecksums reports whether a release file is the checksums file:
// checksums.sha256, the one forge-ci writes on every forge.
func isReleaseChecksums(name string) bool {
	return name == domainrelease.ChecksumsFile
}

// releaseChecksumsMissing refuses a release without checksums.sha256 and one
// that offers a checksums file under the name GoReleaser gives it,
// checksums.txt or <project>_<version>_checksums.txt: forge-ci writes the one
// checksums file on every forge, and `release files import-goreleaser`
// refuses GoReleaser's.
func releaseChecksumsMissing(names []string) error {
	var stale []string

	for _, name := range names {
		subject := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, cosignBundle), openPGPSignature), packageSignature)
		if subject == "checksums.txt" || strings.HasSuffix(subject, "_checksums.txt") {
			stale = append(stale, name)
		}
	}

	if len(stale) != 0 {
		slices.Sort(stale)

		return fmt.Errorf("release files %q carry GoReleaser's checksums name; the release's checksums file is %s: %w", stale, domainrelease.ChecksumsFile, errReleaseEvidence)
	}

	if !slices.Contains(names, domainrelease.ChecksumsFile) {
		return fmt.Errorf("the release offers no %s among %q: %w", domainrelease.ChecksumsFile, names, errReleaseEvidence)
	}

	return nil
}

// releaseFilesUnsigned refuses an offered file that has no verified signature
// of its own: a Forgejo or GitLab release signs the checksums, every file they
// list, the provenance statement and the evidence, as a GitHub release signs
// its files (releaseSignatureGaps). A signature file is no subject, and a
// distro package that offers its detached .sig is held to that signature by
// the package checks (VerifyPackageSignatures), not to a second one here.
func releaseFilesUnsigned(names []string, verified map[string]bool) error {
	subjects := slices.DeleteFunc(slices.Clone(names), func(name string) bool {
		return slices.Contains(names, name+packageSignature)
	})

	if unsigned := unsignedSubjects(slices.Values(subjects), verified, nil); len(unsigned) != 0 {
		return fmt.Errorf("release files %q have no verified signature of their own: %w", unsigned, errReleaseEvidence)
	}

	return nil
}

// releaseFilesUnbound refuses an offered file that nothing verified binds:
// neither the subject of a verified bundle nor a subject of an authenticated
// checksums document. The signature file of another offered file, its
// .bundle, .asc or .sig, is not a payload. The product makes the same rule when it
// signs: the checksums name every published file but the checksums, the
// provenance statement and the evidence, each of which has a bundle
// (manifestChecksumSubjectPaths, requireFinalizedManifest in
// internal/app/release). A package signed with a .sig alone is bound by the
// checksums only, so this stays the rule that covers it.
func releaseFilesUnbound(names []string, verified map[string]bool, checksums map[string][]byte) error {
	offered, bound := map[string]bool{}, map[string]bool{}
	for _, name := range names {
		offered[name] = true
	}

	for _, document := range checksums {
		// A document that does not parse is VerifyReleaseChecksums' failure.
		subjects, _ := provenance.ParseChecksums(bytes.NewReader(document))
		for _, subject := range subjects {
			bound[subject.Name] = true
		}
	}

	var unbound []string

	for _, name := range names {
		signature := false
		subject := name

		for _, suffix := range []string{cosignBundle, openPGPSignature, packageSignature} {
			if cut, found := strings.CutSuffix(name, suffix); found {
				subject, signature = cut, true

				break
			}
		}

		if verified[name] || bound[name] || (signature && offered[subject]) {
			continue
		}

		unbound = append(unbound, name)
	}

	if len(unbound) != 0 {
		slices.Sort(unbound)

		return fmt.Errorf("release files %q are neither signed nor listed in the signed checksums: %w", unbound, errReleaseEvidence)
	}

	return nil
}

// VerifyReleaseChecksums binds a verified checksum document to the actual
// published payloads. Signature verification is caller-owned; this must only be
// called with an authenticated document. It does not assert that the consumer
// built its complete intended inventory or verify embedded package signatures.
// The read callback owns bounded provider reads and evidence retention.
func VerifyReleaseChecksums(document []byte, names []string, read func(string) ([]byte, error)) error {
	subjects, err := provenance.ParseChecksums(bytes.NewReader(document))
	if err != nil {
		return fmt.Errorf("release checksums: %w", err)
	}

	offered := make(map[string]bool, len(names))
	for _, name := range names {
		if offered[name] {
			return fmt.Errorf("duplicate release asset %q: %w", name, errReleaseEvidence)
		}

		offered[name] = true
	}

	seen := map[string]bool{}

	for _, subject := range subjects {
		name := subject.Name
		if name == "." || path.Base(name) != name || !offered[name] || seen[name] {
			return fmt.Errorf("checksum subject %q is not one unique offered asset: %w", name, errReleaseEvidence)
		}

		seen[name] = true

		body, err := read(name)
		if err != nil {
			return fmt.Errorf("read checksum subject %q: %w", name, err)
		}

		if fmt.Sprintf("%x", sha256.Sum256(body)) != subject.SHA256 {
			return fmt.Errorf("published checksum subject %q differs from signed digest: %w", name, errReleaseEvidence)
		}
	}

	return nil
}
