// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"context"
	"crypto/md5"  //nolint:gosec // Maven repository checksum interoperability; authenticity is OpenPGP.
	"crypto/sha1" //nolint:gosec // Maven repository checksum interoperability; authenticity is OpenPGP.
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
	"github.com/diggsweden/forge-ci/internal/pgp"
)

// MavenPublicationSigner signs exported bytes without executing project code.
type MavenPublicationSigner interface {
	DetachedSignArmored(ctx context.Context, fingerprint, passphrase, input, output string) error
}

// MavenSignInput names the imported key and separately supplied public trust root.
type MavenSignInput struct{ Directory, Version, Fingerprint, Passphrase, PublicKey string }

// SignMavenPublication signs every payload and writes repository checksums. It
// accepts no Gradle/Maven task and refuses a key outside the supplied trust root.
func SignMavenPublication(ctx context.Context, signer MavenPublicationSigner, out io.Writer, in MavenSignInput) error {
	fingerprints, err := pgp.PrimaryFingerprints([]byte(in.PublicKey))
	if err != nil {
		return err
	}

	if !slices.Contains(fingerprints, strings.ToUpper(in.Fingerprint)) {
		return fmt.Errorf("publication signer does not match the trusted public key: %w", errs.ErrPermissionDenied)
	}

	set, err := InspectMavenPublication(in.Directory, in.Version, true)
	if err != nil {
		return err
	}

	root, err := pathsafe.OpenRoot(in.Directory)
	if err != nil {
		return err
	}

	defer func() { _ = root.Close() }()

	for _, name := range set.Payloads {
		if signErr := signMavenPayload(ctx, signer, root, in, name); signErr != nil {
			return signErr
		}
	}

	if _, verifyErr := VerifyMavenPublication(ctx, in.Directory, in.Version, in.PublicKey); verifyErr != nil {
		return verifyErr
	}

	_, err = fmt.Fprintf(out, "Signed %d publication payload(s) with armored OpenPGP signatures\n", len(set.Payloads))

	return err
}

func signMavenPayload(ctx context.Context, signer MavenPublicationSigner, root *os.Root, in MavenSignInput, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	filename := filepath.Join(in.Directory, filepath.FromSlash(name))

	_, statErr := root.Lstat(name + ".asc")
	if os.IsNotExist(statErr) {
		if signErr := signer.DetachedSignArmored(ctx, in.Fingerprint, in.Passphrase, filename, filename+".asc"); signErr != nil {
			return signErr
		}
	} else if statErr != nil {
		return statErr
	}

	for _, target := range []string{name, name + ".asc"} {
		if err := writeMavenChecksums(root, target); err != nil {
			return err
		}
	}

	return nil
}

func writeMavenChecksums(root *os.Root, name string) error {
	checksums, err := mavenChecksums(root, name)
	if err != nil {
		return err
	}

	for extension, value := range checksums {
		if writeErr := root.WriteFile(name+extension, []byte(hex.EncodeToString(value)), 0o600); writeErr != nil {
			return writeErr
		}
	}

	return nil
}

func mavenChecksums(root *os.Root, name string) (map[string][]byte, error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	md, sha, sha256sum, sha512sum := md5.New(), sha1.New(), sha256.New(), sha512.New() //nolint:gosec // Compatibility checksums; OpenPGP is the authentication mechanism.
	if _, err := io.Copy(io.MultiWriter(md, sha, sha256sum, sha512sum), file); err != nil {
		return nil, err
	}

	return map[string][]byte{checksumMD5Extension: md.Sum(nil), checksumSHA1Extension: sha.Sum(nil), checksumSHA256Extension: sha256sum.Sum(nil), checksumSHA512Extension: sha512sum.Sum(nil)}, nil
}

func verifyMavenChecksums(root *os.Root, name string) error {
	checksums, err := mavenChecksums(root, name)
	if err != nil {
		return err
	}

	for extension, value := range checksums {
		body, readErr := root.ReadFile(name + extension)
		if readErr != nil {
			return fmt.Errorf("read publication checksum %s%s: %w", name, extension, readErr)
		}

		if strings.TrimSpace(string(body)) != hex.EncodeToString(value) {
			return fmt.Errorf("publication checksum disagrees with %s: %w", name, errs.ErrValidation)
		}
	}

	return nil
}
