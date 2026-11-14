// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package git

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"mvdan.cc/sh/v3/syntax"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domaingit "github.com/diggsweden/forge-ci/internal/domain/git"
	"github.com/diggsweden/forge-ci/internal/runcontext"
)

// PinnedSSHInput is what SetupPinnedSSH needs to build an SSH transport that
// trusts exactly one host key.
type PinnedSSHInput struct {
	// PrivateKey is the OpenSSH private key text used to authenticate and sign.
	PrivateKey string
	Host       string
	Port       int
	// HostKeyType is the host key type to pin: ed25519, ecdsa or rsa.
	HostKeyType string
	// HostKeyFingerprint is the trust anchor: the host key the server
	// presents must carry exactly this SHA256 fingerprint.
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
	pin, err := domaingit.ParseHostKeyPin(in.HostKeyFingerprint)
	if err != nil {
		return PinnedSSH{}, func() {}, fmt.Errorf("commit-changelog: %w", err)
	}

	key, err := domaingit.ParseSSHPrivateKey(in.PrivateKey)
	if err != nil {
		return PinnedSSH{}, func() {}, fmt.Errorf("commit-changelog: %w", err)
	}

	base := runcontext.TempRoot(os.Getenv)

	dir, err := os.MkdirTemp(base, "forge-ci-ssh.")
	if err != nil {
		return PinnedSSH{}, func() {}, fmt.Errorf("commit-changelog: create ssh temp dir: %w", err)
	}

	cleanup := func() { _ = os.RemoveAll(dir) }

	keyPath := filepath.Join(dir, "signing_key")
	if writeErr := os.WriteFile(keyPath, key.OpenSSH, 0o600); writeErr != nil {
		cleanup()

		return PinnedSSH{}, func() {}, fmt.Errorf("commit-changelog: write SSH signing key: %w", writeErr)
	}

	if writeErr := os.WriteFile(filepath.Join(dir, "signing_key.pub"), key.AuthorizedKey, 0o644); writeErr != nil { //nolint:gosec // 0o644 is deliberate for the non-secret public key; the path lives under the os.MkdirTemp dir above.
		cleanup()

		return PinnedSSH{}, func() {}, fmt.Errorf("commit-changelog: write SSH public key: %w", writeErr)
	}

	knownHostsPath, err := pinScannedHostKey(ctx, dir, in.Host, in.Port, in.HostKeyType, pin, scanHostKey)
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

	config := fmt.Sprintf("Host *\n  HostName %s\n  Port %d\n  User %s\n  IdentityFile \"%s\"\n  IdentitiesOnly yes\n  UserKnownHostsFile \"%s\"\n  StrictHostKeyChecking yes\n", in.Host, in.Port, sshUser, keyPath, knownHostsPath)
	if writeErr := os.WriteFile(configPath, []byte(config), 0o600); writeErr != nil {
		cleanup()

		return PinnedSSH{}, func() {}, fmt.Errorf("commit-changelog: write SSH config: %w", writeErr)
	}

	return PinnedSSH{Command: "ssh -F " + quotedConfigPath, SigningKeyPath: keyPath}, cleanup, nil
}

// sshUser is the account every forge serves Git over SSH as.
const sshUser = "git"

// hostKeyAlgorithms returns the SSH algorithms offered for a host key type,
// so the server presents the key of the pinned type.
func hostKeyAlgorithms(keyType string) ([]string, bool) {
	switch keyType {
	case "ed25519":
		return []string{ssh.KeyAlgoED25519}, true
	case "ecdsa":
		return []string{ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521}, true
	case "rsa":
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}, true
	}

	return nil, false
}

// errHostKeyCaptured ends the handshake once the host key is in hand; no
// authentication is attempted.
var errHostKeyCaptured = errors.New("host key captured")

// scanHostKey returns the host key the server presents for keyType, read
// from the SSH handshake itself.
func scanHostKey(ctx context.Context, host string, port int, keyType string) (ssh.PublicKey, error) {
	algorithms, ok := hostKeyAlgorithms(keyType)
	if !ok {
		return nil, fmt.Errorf("host key type %q is not ed25519, ecdsa or rsa: %w", keyType, errs.ErrValidation)
	}

	address := net.JoinHostPort(host, strconv.Itoa(port))

	var dialer net.Dialer

	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", address, err)
	}
	defer func() { _ = conn.Close() }()

	// The handshake has no context of its own; closing the connection ends it.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	var presented ssh.PublicKey

	client, _, _, err := ssh.NewClientConn(conn, address, &ssh.ClientConfig{
		User:              sshUser,
		HostKeyAlgorithms: algorithms,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			presented = key

			return errHostKeyCaptured
		},
	})
	if client != nil {
		_ = client.Close()
	}

	if presented == nil {
		return nil, fmt.Errorf("SSH handshake with %s: %w", address, err)
	}

	return presented, nil
}

// pinScannedHostKey reads the SSH host key, verifies it against the pin and
// writes the pinned known_hosts file inside dir, returning its path. scan is
// the network seam.
func pinScannedHostKey(ctx context.Context, dir, host string, port int, keyType string, pin domaingit.HostKeyPin, scan func(context.Context, string, int, string) (ssh.PublicKey, error)) (string, error) {
	key, err := scan(ctx, host, port, keyType)
	if err != nil {
		return "", fmt.Errorf("commit-changelog: scan SSH host key: %w", err)
	}

	// Both fingerprints are public and together tell a rotated host key from
	// an interception, which is the operator's next decision.
	if !pin.Matches(key) {
		return "", fmt.Errorf("SSH host key fingerprint mismatch: scanned %s, pinned %s: %w", ssh.FingerprintSHA256(key), pin, errs.ErrValidation)
	}

	knownHostsPath := filepath.Join(dir, "known_hosts")

	line := knownhosts.Line([]string{knownhosts.Normalize(net.JoinHostPort(host, strconv.Itoa(port)))}, key) + "\n"
	if writeErr := os.WriteFile(knownHostsPath, []byte(line), 0o644); writeErr != nil { //nolint:gosec // 0o644 is deliberate for the non-secret known_hosts; the path lives under the caller's os.MkdirTemp dir.
		return "", fmt.Errorf("commit-changelog: write known_hosts: %w", writeErr)
	}

	return knownHostsPath, nil
}
