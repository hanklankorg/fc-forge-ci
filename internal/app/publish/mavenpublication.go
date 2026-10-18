// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domainpublish "github.com/diggsweden/forge-ci/internal/domain/publish"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
	"github.com/diggsweden/forge-ci/internal/pgp"
)

// MavenPublicationSet is an inspected local Maven repository. Payloads excludes
// generated checksums/signatures; Files is the exact transferable file set.
type MavenPublicationSet struct {
	Publications []domainpublish.MavenPublication
	Payloads     []string
	Files        []string
}

const (
	checksumMD5Extension    = ".md5"
	checksumSHA1Extension   = ".sha1"
	checksumSHA256Extension = ".sha256"
	checksumSHA512Extension = ".sha512"
)

// InspectMavenPublication validates every publication before any signing/upload.
// Gradle's repository-wide maven-metadata is deliberately not a release payload.
func InspectMavenPublication(directory, expectedVersion string, central bool) (MavenPublicationSet, error) {
	return inspectMavenPublication(directory, expectedVersion, central, false)
}

// InspectMavenSnapshot validates every exported snapshot before any upload.
func InspectMavenSnapshot(directory, expectedVersion string) (MavenPublicationSet, error) {
	return inspectMavenPublication(directory, expectedVersion, false, true)
}

func inspectMavenPublication(directory, expectedVersion string, central, snapshot bool) (MavenPublicationSet, error) {
	var result MavenPublicationSet

	root, err := pathsafe.OpenRoot(directory)
	if err != nil {
		return result, err
	}

	defer func() { _ = root.Close() }()

	result.Files, err = scanMavenPublication(root)
	if err != nil {
		return result, err
	}

	files := make(map[string]bool, len(result.Files))
	for _, name := range result.Files {
		files[name] = true
		if strings.HasSuffix(name, ".pom") {
			publication, readErr := readPublicationPOM(root, name, expectedVersion, central, snapshot)
			if readErr != nil {
				return result, readErr
			}

			result.Publications = append(result.Publications, publication)
		}
	}

	if artifactsErr := requirePublicationArtifacts(result.Publications, files, central); artifactsErr != nil {
		return result, artifactsErr
	}

	result.Payloads, err = publicationPayloads(result, files)

	return result, err
}

func scanMavenPublication(root *os.Root) ([]string, error) {
	var (
		files []string
		total int64
		count int
	)

	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			return nil
		}

		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}

		if fileErr := validatePublicationFile(info); fileErr != nil {
			return fileErr
		}

		total += info.Size()

		count++
		if total > 2<<30 || count > 10000 {
			return fmt.Errorf("publication exceeds 2 GiB or 10000 files: %w", errs.ErrValidation)
		}

		base := stripPublicationSidecar(path.Base(name))
		if base != "maven-metadata.xml" && base != "maven-metadata-local.xml" {
			files = append(files, name)
		}

		return nil
	})

	return files, err
}

func validatePublicationFile(info fs.FileInfo) error {
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > publicationFileLimit(info.Name()) {
		return fmt.Errorf("publication files must be nonempty bounded regular files: %w", errs.ErrValidation)
	}

	return nil
}

func publicationFileLimit(name string) int64 {
	switch path.Ext(name) {
	case ".pom", ".asc":
		return 1 << 20
	case checksumMD5Extension, checksumSHA1Extension, checksumSHA256Extension, checksumSHA512Extension:
		return 4096
	default:
		return 512 << 20
	}
}

func readPublicationPOM(root *os.Root, name, version string, central, snapshot bool) (domainpublish.MavenPublication, error) {
	body, err := root.ReadFile(name)
	if err != nil {
		return domainpublish.MavenPublication{}, err
	}

	var publication domainpublish.MavenPublication
	if snapshot {
		publication, err = domainpublish.ParseMavenSnapshot(body, version, central)
	} else {
		publication, err = domainpublish.ParseMavenPublication(body, version, central)
	}

	if err != nil {
		return publication, fmt.Errorf("%s: %w", name, err)
	}

	if name != path.Join(publication.Directory(), publication.Stem()+".pom") {
		return publication, fmt.Errorf("POM coordinates disagree with repository path %s: %w", name, errs.ErrValidation)
	}

	return publication, nil
}

func requirePublicationArtifacts(publications []domainpublish.MavenPublication, files map[string]bool, central bool) error {
	if len(publications) == 0 {
		return fmt.Errorf("no Maven publications were exported; apply maven-publish and declare a publication: %w", errs.ErrMissingInput)
	}

	for _, publication := range publications {
		if publication.Packaging == "pom" {
			continue
		}

		required := []string{"." + publication.Packaging}
		if central {
			required = append(required, "-sources.jar", "-javadoc.jar")
		}

		for _, suffix := range required {
			if !files[path.Join(publication.Directory(), publication.Stem()+suffix)] {
				return fmt.Errorf("publication %s lacks %s: %w", publication.ArtifactID, suffix, errs.ErrValidation)
			}
		}
	}

	return nil
}

func publicationPayloads(set MavenPublicationSet, files map[string]bool) ([]string, error) {
	var payloads []string

	for _, name := range set.Files {
		base := stripPublicationSidecar(name)
		if !files[base] {
			return nil, fmt.Errorf("orphan publication sidecar %s: %w", name, errs.ErrValidation)
		}

		if !ownedPublicationPayload(set.Publications, base) {
			return nil, fmt.Errorf("unexpected file in Maven publication: %s: %w", name, errs.ErrValidation)
		}

		if base == name {
			payloads = append(payloads, name)
		}
	}

	return payloads, nil
}

func ownedPublicationPayload(publications []domainpublish.MavenPublication, name string) bool {
	extension := path.Ext(name)
	switch extension {
	case ".pom", ".jar", ".aar", ".module":
	default:
		return false
	}

	for _, publication := range publications {
		if path.Dir(name) != publication.Directory() {
			continue
		}

		stem := path.Join(publication.Directory(), publication.Stem())
		if name == stem+extension {
			return true
		}

		classified, ok := strings.CutPrefix(name, stem+"-")
		if ok && strings.TrimSuffix(classified, extension) != "" {
			return true
		}
	}

	return false
}

func stripPublicationSidecar(name string) string {
	for _, suffix := range []string{checksumMD5Extension, checksumSHA1Extension, checksumSHA256Extension, checksumSHA512Extension} {
		if strings.HasSuffix(name, suffix) {
			return strings.TrimSuffix(strings.TrimSuffix(name, suffix), ".asc")
		}
	}

	return strings.TrimSuffix(name, ".asc")
}

// VerifyMavenPublication proves every payload's armored signature before upload.
func VerifyMavenPublication(ctx context.Context, directory, version, publicKey string) (MavenPublicationSet, error) {
	set, err := InspectMavenPublication(directory, version, true)
	if err != nil {
		return set, err
	}

	root, err := pathsafe.OpenRoot(directory)
	if err != nil {
		return set, err
	}

	defer func() { _ = root.Close() }()

	for _, name := range set.Payloads {
		if err := ctx.Err(); err != nil {
			return set, err
		}

		body, err := root.Open(name)
		if err != nil {
			return set, err
		}

		signature, err := root.Open(name + ".asc")
		if err != nil {
			_ = body.Close()

			return set, fmt.Errorf("missing signature for %s: %w", name, errs.ErrMissingInput)
		}

		err = pgp.VerifyDetachedArmored(body, io.LimitReader(signature, 1<<20), []byte(publicKey))
		_ = body.Close()
		_ = signature.Close()

		if err != nil {
			return set, fmt.Errorf("signature for %s: %w", name, err)
		}

		for _, target := range []string{name, name + ".asc"} {
			if checksumErr := verifyMavenChecksums(root, target); checksumErr != nil {
				return set, checksumErr
			}
		}
	}

	return set, nil
}
