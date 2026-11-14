// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/config"
)

// ForgejoSignsReleaseFilesWithGPG reports whether the Forgejo release of the
// consumer at root signs its checksums and SBOMs with GPG: the declaration
// its release workflow (a file name under .forgejo/workflows) passes
// prepare-release.yml says so (SignsReleaseFilesWithGPG).
func ForgejoSignsReleaseFilesWithGPG(root, workflow string) (bool, error) {
	artifactsConfig, err := forgejoCalledArtifactsConfig(filepath.Join(root, ".forgejo", "workflows", workflow), "prepare-release.yml")
	if err != nil {
		return false, err
	}

	return SignsReleaseFilesWithGPG(root, artifactsConfig)
}

// SignsReleaseFilesWithGPG reports whether the Forgejo or GitLab release of
// the consumer at root, reading the declaration artifactsConfig (empty, the
// default one), signs its checksums and SBOMs with GPG: the declaration
// says sign.method gpg or sign.files-method gpg. A consumer without the
// declaration, or one that names neither, signs them with its Cosign key.
func SignsReleaseFilesWithGPG(root, artifactsConfig string) (bool, error) {
	declaration := filepath.Join(root, cmp.Or(artifactsConfig, DefaultArtifactsConfig))

	body, err := os.ReadFile(declaration) //nolint:gosec // G304: the declaration of the consumer checkout the caller names
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	cfg, err := config.Parse(body)
	if err != nil {
		return false, fmt.Errorf("%s: %w", declaration, err)
	}

	return cfg.Sign.DeclaresGPG(), nil
}

// GPGReleaseFileFindings are the findings of the files a release offers
// whose declaration signs its files with GPG: the checksums carry an
// OpenPGP signature, and no file with one also carries a Cosign bundle.
// Whether each signature verifies is VerifyReleaseReadback's.
func GPGReleaseFileFindings(names []string) []string {
	var findings []string

	checksums := false

	for _, name := range names {
		subject, signature := strings.CutSuffix(name, openPGPSignature)
		if !signature {
			continue
		}

		if subject == "checksums.txt" || strings.HasSuffix(subject, "_checksums.txt") {
			checksums = true
		}

		if slices.Contains(names, subject+cosignBundle) {
			findings = append(findings, subject+" carries both an OpenPGP signature and a Cosign bundle; the declaration signs it with GPG alone")
		}
	}

	if !checksums {
		findings = append(findings, fmt.Sprintf("the declaration signs the release files with GPG, and the release offers no OpenPGP signature of its checksums among %q", names))
	}

	return findings
}

// VerifyOpenPGPSignature verifies signature, an armored detached OpenPGP
// signature over body, with the keys of the GnuPG home alone, no key
// fetched, and requires it to be the key with fingerprint's own: gpg
// --verify alone accepts any key of the home. environment is gpg's.
func VerifyOpenPGPSignature(ctx context.Context, home string, environment []string, fingerprint string, body, signature []byte) error {
	dir, err := os.MkdirTemp("", "livetest-openpgp-signature-")
	if err != nil {
		return err
	}

	defer func() { _ = os.RemoveAll(dir) }()

	subject, sidecar := filepath.Join(dir, "subject"), filepath.Join(dir, "subject"+openPGPSignature)
	for path, content := range map[string][]byte{subject: body, sidecar: signature} {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return err
		}
	}

	command := exec.CommandContext(ctx, "gpg", "--homedir", home, "--batch", "--no-auto-key-retrieve", "--status-fd", "1", //nolint:gosec // G204: kit-authored arguments.
		"--verify", sidecar, subject)
	command.Env = environment

	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("the OpenPGP signature does not verify: %w", err)
	}

	if !signedBy(string(output), fingerprint) {
		return fmt.Errorf("the OpenPGP signature is not the key %s's: %w", fingerprint, errReleaseEvidence)
	}

	return nil
}
