// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// packageSignature is the suffix of a distro package's OpenPGP sidecar.
const packageSignature = ".sig"

// errPackageUnsigned is the finding of a package a release offers without
// its sidecar. It reads on inside the sentence that names both.
var errPackageUnsigned = errors.New("no published OpenPGP signature")

// DistroPackageFile reports whether a release file is a distro package, by
// the suffix the release signs and publishes packages under.
func DistroPackageFile(name string) bool {
	return slices.Contains([]string{".deb", ".rpm", ".apk"}, filepath.Ext(name))
}

// VerifyPackageSignatures verifies the OpenPGP signature of every distro
// package among names, the files a release offers on either lab forge. A
// package has the sidecar <package>.sig, a detached signature over the
// package's bytes, which gpg verifies with the keys of the GnuPG home alone:
// no key is fetched. So the three formats have one verifier, and none reads
// inside a package. read returns a file's bytes, and environment is gpg's.
//
// It returns the packages whose signature verified, and every finding of the
// others: a package without a sidecar, and one whose sidecar does not verify.
// A release of no package has none to verify.
func VerifyPackageSignatures(ctx context.Context, home string, environment, names []string, read func(name string) ([]byte, error)) ([]string, error) {
	var (
		verified []string
		findings []error
	)

	for _, name := range names {
		if !DistroPackageFile(name) {
			continue
		}

		if !slices.Contains(names, name+packageSignature) {
			findings = append(findings, fmt.Errorf("package %s has %w %s", name, errPackageUnsigned, name+packageSignature))

			continue
		}

		if err := verifyPackageSignature(ctx, home, environment, name, read); err != nil {
			findings = append(findings, fmt.Errorf("package %s: %w", name, err))

			continue
		}

		verified = append(verified, name)
	}

	return verified, errors.Join(findings...)
}

func verifyPackageSignature(ctx context.Context, home string, environment []string, name string, read func(name string) ([]byte, error)) error {
	dir, err := os.MkdirTemp("", "livetest-package-signature-")
	if err != nil {
		return err
	}

	defer func() { _ = os.RemoveAll(dir) }()

	for file, published := range map[string]string{"package": name, "signature": name + packageSignature} {
		body, readErr := read(published)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", published, readErr)
		}

		if err = os.WriteFile(filepath.Join(dir, file), body, 0o600); err != nil {
			return err
		}
	}

	command := exec.CommandContext(ctx, "gpg", "--homedir", home, "--batch", "--no-auto-key-retrieve", //nolint:gosec // G204: kit-authored arguments.
		"--verify", filepath.Join(dir, "signature"), filepath.Join(dir, "package"))
	command.Env = environment

	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("the signature does not verify: %w\n%s", err, strings.TrimSpace(string(output)))
	}

	return nil
}
