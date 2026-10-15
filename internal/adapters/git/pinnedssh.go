// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/runcontext"
	"github.com/diggsweden/forge-ci/internal/safeexec"
)

// PinnedSSHInput is what SetupPinnedSSH needs to build an SSH transport that
// trusts exactly one host key.
type PinnedSSHInput struct {
	// PrivateKey is the OpenSSH private key text used to authenticate and sign.
	PrivateKey string
	Host       string
	Port       int
	// HostKeyType is the key type asked of ssh-keyscan (for example "ed25519").
	HostKeyType string
	// HostKeyFingerprint is the trust anchor: every scanned host key must
	// carry exactly this fingerprint.
	HostKeyFingerprint string
}

// PinnedSSH is the prepared transport.
type PinnedSSH struct {
	// Command is the GIT_SSH_COMMAND value that makes git use the pinned setup.
	Command string
	// SigningKeyPath is the private key file on disk, for git's SSH signing.
	SigningKeyPath string
}

// SetupPinnedSSH prepares an isolated SSH setup (signing key, pinned
// known_hosts, ssh config) in a private temp dir and returns the
// GIT_SSH_COMMAND value, the signing-key path and a cleanup func.
//
// The error text carries the "commit-changelog:" prefix of the one command
// that drives this, version commit-changelog-release.
func SetupPinnedSSH(ctx context.Context, in PinnedSSHInput) (PinnedSSH, func(), error) {
	base := runcontext.TempRoot(os.Getenv)

	dir, err := os.MkdirTemp(base, "forge-ci-ssh.")
	if err != nil {
		return PinnedSSH{}, func() {}, fmt.Errorf("commit-changelog: create ssh temp dir: %w", err)
	}

	cleanup := func() { _ = os.RemoveAll(dir) } //nolint:gosec // G703 false positive: dir comes from os.MkdirTemp above.

	keyPath := filepath.Join(dir, "signing_key")
	if writeErr := os.WriteFile(keyPath, []byte(ensureTrailingNewline(in.PrivateKey)), 0o600); writeErr != nil { //nolint:gosec // G703 false positive: keyPath lives under the os.MkdirTemp dir above.
		cleanup()

		return PinnedSSH{}, func() {}, fmt.Errorf("commit-changelog: write SSH signing key: %w", writeErr)
	}

	pub, err := runTool(ctx, "ssh-keygen", "-y", "-f", keyPath)
	if err != nil {
		cleanup()

		return PinnedSSH{}, func() {}, fmt.Errorf("commit-changelog: validate SSH signing key: %w", err)
	}

	if writeErr := os.WriteFile(filepath.Join(dir, "signing_key.pub"), []byte(ensureTrailingNewline(pub)), 0o644); writeErr != nil { //nolint:gosec // 0o644 is deliberate for the non-secret public key; the path lives under the os.MkdirTemp dir above.
		cleanup()

		return PinnedSSH{}, func() {}, fmt.Errorf("commit-changelog: write SSH public key: %w", writeErr)
	}

	knownHostsPath, err := pinScannedHostKey(ctx, dir, in.Host, in.Port, in.HostKeyType, in.HostKeyFingerprint, runTool)
	if err != nil {
		cleanup()

		return PinnedSSH{}, func() {}, err
	}

	configPath := filepath.Join(dir, "config")

	// ssh_config double quotes allow spaces but cannot escape a quote or a
	// line break, and GIT_SSH_COMMAND goes through a shell.
	if strings.ContainsAny(dir, "\"\n") {
		cleanup()

		return PinnedSSH{}, func() {}, fmt.Errorf("commit-changelog: SSH temp dir %q cannot be quoted: %w", dir, errs.ErrUsage)
	}

	quotedConfigPath, err := syntax.Quote(configPath, syntax.LangPOSIX)
	if err != nil {
		cleanup()

		return PinnedSSH{}, func() {}, fmt.Errorf("commit-changelog: quote SSH config path: %w", err)
	}

	config := fmt.Sprintf("Host *\n  HostName %s\n  Port %d\n  User git\n  IdentityFile \"%s\"\n  IdentitiesOnly yes\n  UserKnownHostsFile \"%s\"\n  StrictHostKeyChecking yes\n", in.Host, in.Port, keyPath, knownHostsPath)
	if writeErr := os.WriteFile(configPath, []byte(config), 0o600); writeErr != nil { //nolint:gosec // G703 false positive: configPath lives under the os.MkdirTemp dir above.
		cleanup()

		return PinnedSSH{}, func() {}, fmt.Errorf("commit-changelog: write SSH config: %w", writeErr)
	}

	return PinnedSSH{Command: "ssh -F " + quotedConfigPath, SigningKeyPath: keyPath}, cleanup, nil
}

// HostKeyScanArgs returns the ssh-keyscan argv, program name first, that
// SetupPinnedSSH runs for this host. It is exported so a caller can show the
// operator the exact command that yields the fingerprint to pin.
func HostKeyScanArgs(host string, port int, keyType string) []string {
	args := []string{"ssh-keyscan"}
	if port != 22 {
		args = append(args, "-p", strconv.Itoa(port))
	}

	return append(args, "-t", keyType, host)
}

// pinScannedHostKey scans the SSH host key, verifies its fingerprint against
// the expected value and writes the pinned known_hosts file inside dir,
// returning the final known_hosts path. run is the process seam.
func pinScannedHostKey(ctx context.Context, dir, host string, port int, keyType, expectedFingerprint string, run func(context.Context, string, ...string) (string, error)) (string, error) {
	knownHostsTmp := filepath.Join(dir, "known_hosts.tmp")

	knownHosts, err := run(ctx, "ssh-keyscan", HostKeyScanArgs(host, port, keyType)[1:]...)
	if err != nil {
		return "", fmt.Errorf("commit-changelog: scan SSH host key: %w", err)
	}

	if writeErr := os.WriteFile(knownHostsTmp, []byte(ensureTrailingNewline(knownHosts)), 0o644); writeErr != nil { //nolint:gosec // 0o644 is deliberate for the non-secret known_hosts; the path lives under the caller's os.MkdirTemp dir.
		return "", fmt.Errorf("commit-changelog: write known_hosts: %w", writeErr)
	}

	fingerprintLine, err := run(ctx, "ssh-keygen", "-lf", knownHostsTmp)
	if err != nil {
		return "", fmt.Errorf("commit-changelog: fingerprint SSH host key: %w", err)
	}

	if err := validateHostKeyFingerprints(fingerprintLine, expectedFingerprint); err != nil {
		return "", err
	}

	knownHostsPath := filepath.Join(dir, "known_hosts")
	if renameErr := os.Rename(knownHostsTmp, knownHostsPath); renameErr != nil { //nolint:gosec // G703 false positive: both paths live under the caller's os.MkdirTemp dir.
		return "", fmt.Errorf("commit-changelog: finalize known_hosts: %w", renameErr)
	}

	return knownHostsPath, nil
}

func validateHostKeyFingerprints(output, expected string) error {
	if strings.TrimSpace(output) == "" || expected == "" {
		return fmt.Errorf("SSH host key fingerprints are required: %w", errs.ErrValidation)
	}

	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		// Both fingerprints are public and together tell a rotated host key
		// from an interception, which is the operator's next decision.
		if got := secondField(line); got != expected {
			return fmt.Errorf("SSH host key fingerprint mismatch: scanned %s, pinned %s: %w", got, expected, errs.ErrValidation)
		}
	}

	return nil
}

// runTool runs one ssh tool and returns its trimmed stdout. A start failure
// is classified through safeexec.WrapError like every other adapter process.
func runTool(ctx context.Context, bin string, args ...string) (string, error) {
	cmd := safeexec.Command(ctx, bin, args...)

	// Only stdout is the value: it becomes a key or known_hosts file, so a
	// warning on stderr must not end up inside it.
	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		wrapped := safeexec.WrapError(err, bin, safeexec.FirstArg(args))
		if stderr.Len() == 0 {
			return "", wrapped
		}

		return "", fmt.Errorf("%w\n%s", wrapped, safeexec.RedactKeyMaterial(stderr.Bytes()))
	}

	return strings.TrimRight(string(out), "\n"), nil
}

func secondField(line string) string {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return ""
	}

	return fields[1]
}

func ensureTrailingNewline(value string) string {
	if strings.HasSuffix(value, "\n") {
		return value
	}

	return value + "\n"
}
