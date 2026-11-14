// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var errSigningInputs = errors.New("reused signing inputs refused")

// CheckReusedSigningInputs checks metadata only, never prints or reads private
// contents, and never generates replacement keys. The signer checks key validity.
// Inputs are looked up through an os.Root, so a link cannot leave the directory.
func CheckReusedSigningInputs(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("FORGE_CI_LIVE_SIGNING_KEY_DIR must name an absolute protected directory: %w", errSigningInputs)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("reused signing directory must exist and be accessible only to its owner: %w", errSigningInputs)
	}
	defer func() { _ = root.Close() }()

	info, err := root.Stat(".")
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("reused signing directory must exist and be accessible only to its owner: %w", errSigningInputs)
	}

	for _, name := range []string{"lab-cosign.key", "lab-cosign.pub", "lab-cosign.passphrase"} {
		info, err := root.Stat(name)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 1<<20 {
			return fmt.Errorf("reused signing input %s must be an existing nonempty regular file (at most 1 MiB); restore the original input, do not silently rekey: %w", name, errSigningInputs)
		}

		if name != "lab-cosign.pub" && info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("reused signing input %s must be accessible only to its owner: %w", name, errSigningInputs)
		}
	}

	return nil
}
