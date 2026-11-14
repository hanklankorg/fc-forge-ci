// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package git

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/sshkey"
)

const testHostKeyType = "ed25519"

// startSSHServer serves SSH handshakes on a loopback port with a fresh
// ed25519 host key, refusing every login, and returns the port and the key.
func startSSHServer(t *testing.T) (int, ssh.PublicKey) {
	t.Helper()

	_, signer := sshkey.New(t)

	config := &ssh.ServerConfig{
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
			return nil, ssh.ErrNoAuth
		},
	}
	config.AddHostKey(signer)

	listener := listenLoopback(t)

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			go func() {
				defer func() { _ = conn.Close() }()

				_, _, _, _ = ssh.NewServerConn(conn, config)
			}()
		}
	}()

	return listenerPort(t, listener), signer.PublicKey()
}

func listenLoopback(t *testing.T) net.Listener {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = listener.Close() })

	return listener
}

func listenerPort(t *testing.T, listener net.Listener) int {
	t.Helper()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address %v is not TCP", listener.Addr())
	}

	return addr.Port
}

func pinnedSSHInput(t *testing.T, port int, hostKey ssh.PublicKey) PinnedSSHInput {
	t.Helper()

	return PinnedSSHInput{
		PrivateKey:         sshkey.PrivateKey(t),
		Host:               "127.0.0.1",
		Port:               port,
		HostKeyType:        testHostKeyType,
		HostKeyFingerprint: ssh.FingerprintSHA256(hostKey),
	}
}

// TestSetupPinnedSSH_PinsTheHostKey covers the MITM defence on the
// release-tag push: the host key presented in the handshake must carry the
// fingerprint the caller pinned, or the setup aborts.
//
// A mismatch that was accepted would let a man-in-the-middle take the push,
// and the private signing key is already on disk by the time the scan runs,
// so the cleanup on that path matters as much as the refusal.
func TestSetupPinnedSSH_PinsTheHostKey(t *testing.T) {
	// A pin copied with its host and key type compared unequal to the very
	// fingerprint it names, and the mismatch read like an interception.
	t.Run("a pin that is not the fingerprint alone is refused by name", func(t *testing.T) {
		port, hostKey := startSSHServer(t)
		t.Setenv("RUNNER_TEMP", t.TempDir())

		in := pinnedSSHInput(t, port, hostKey)
		in.HostKeyFingerprint = "codefloe.com ED25519 " + in.HostKeyFingerprint

		_, cleanup, err := SetupPinnedSSH(t.Context(), in)
		t.Cleanup(cleanup)

		if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "SHA256:<base64> value alone") {
			t.Fatalf("err = %v, want the pin format refusal", err)
		}

		assertNoLeftoverSSHDir(t, os.Getenv("RUNNER_TEMP"))
	})

	t.Run("a padded pin matches the presented key", func(t *testing.T) {
		port, hostKey := startSSHServer(t)
		t.Setenv("RUNNER_TEMP", t.TempDir())

		in := pinnedSSHInput(t, port, hostKey)
		in.HostKeyFingerprint += "=\n"

		_, cleanup, err := SetupPinnedSSH(t.Context(), in)
		t.Cleanup(cleanup)

		if err != nil {
			t.Fatalf("SetupPinnedSSH: %v", err)
		}
	})

	t.Run("fingerprint mismatch is refused and cleans up", func(t *testing.T) {
		port, hostKey := startSSHServer(t)
		_, otherKey := startSSHServer(t)
		t.Setenv("RUNNER_TEMP", t.TempDir())

		_, cleanup, err := SetupPinnedSSH(t.Context(), pinnedSSHInput(t, port, otherKey))
		t.Cleanup(cleanup)

		if !errors.Is(err, errs.ErrValidation) {
			t.Fatalf("err = %v, want ErrValidation", err)
		}

		// The message names both fingerprints so an operator can tell a
		// rotation from an attack.
		if !strings.Contains(err.Error(), "scanned "+ssh.FingerprintSHA256(hostKey)+", pinned "+ssh.FingerprintSHA256(otherKey)) {
			t.Errorf("error should name the scanned and the pinned fingerprint: %v", err)
		}

		assertNoLeftoverSSHDir(t, os.Getenv("RUNNER_TEMP"))
	})

	t.Run("a host key of another type is not accepted in its place", func(t *testing.T) {
		port, hostKey := startSSHServer(t)
		t.Setenv("RUNNER_TEMP", t.TempDir())

		in := pinnedSSHInput(t, port, hostKey)
		in.HostKeyType = "rsa"

		_, cleanup, err := SetupPinnedSSH(t.Context(), in)
		t.Cleanup(cleanup)

		if err == nil || !strings.Contains(err.Error(), "scan SSH host key") {
			t.Fatalf("err = %v, want the scan to fail without an rsa host key", err)
		}

		assertNoLeftoverSSHDir(t, os.Getenv("RUNNER_TEMP"))
	})

	t.Run("an unreadable signing key is refused before anything is written", func(t *testing.T) {
		port, hostKey := startSSHServer(t)
		t.Setenv("RUNNER_TEMP", t.TempDir())

		in := pinnedSSHInput(t, port, hostKey)
		body := strings.Split(in.PrivateKey, "\n")[1]
		in.PrivateKey = strings.ReplaceAll(in.PrivateKey, "\n", "")

		_, cleanup, err := SetupPinnedSSH(t.Context(), in)
		t.Cleanup(cleanup)

		if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "SSH private key is unreadable") {
			t.Fatalf("err = %v, want the key refusal", err)
		}

		if strings.Contains(err.Error(), body) {
			t.Errorf("key material reached the error: %v", err)
		}

		assertNoLeftoverSSHDir(t, os.Getenv("RUNNER_TEMP"))
	})

	t.Run("matching fingerprint yields a pinned, locked-down config", func(t *testing.T) {
		port, hostKey := startSSHServer(t)
		t.Setenv("RUNNER_TEMP", t.TempDir())

		setup, cleanup, err := SetupPinnedSSH(t.Context(), pinnedSSHInput(t, port, hostKey))
		if err != nil {
			t.Fatalf("setup: %v", err)
		}

		t.Cleanup(cleanup)

		keyPath := setup.SigningKeyPath
		dir := filepath.Dir(keyPath)

		// The private key is owner-only.
		info, statErr := os.Stat(keyPath)
		if statErr != nil {
			t.Fatal(statErr)
		}

		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("signing key mode = %v, want 0600", perm)
		}

		config, readErr := os.ReadFile(filepath.Join(dir, "config"))
		if readErr != nil {
			t.Fatal(readErr)
		}

		// The generated config is the point: without these lines ssh would
		// accept an unknown host key, fall back to an agent key, read the
		// user's own ssh config, or connect to another port than the scan.
		want := "Host *\n  HostName 127.0.0.1\n  Port " + strconv.Itoa(port) + "\n  User git\n  IdentityFile \"" + keyPath +
			"\"\n  IdentitiesOnly yes\n  UserKnownHostsFile \"" + filepath.Join(dir, "known_hosts") + "\"\n  StrictHostKeyChecking yes\n"
		if string(config) != want {
			t.Errorf("ssh config:\n%s\nwant:\n%s", config, want)
		}

		// ssh looks a non-default port up as [host]:port, so the pinned
		// entry must be written in that form to be found at all.
		knownHosts, readErr := os.ReadFile(filepath.Join(dir, "known_hosts"))
		if readErr != nil {
			t.Fatal(readErr)
		}

		wantLine := "[127.0.0.1]:" + strconv.Itoa(port) + " " + string(ssh.MarshalAuthorizedKey(hostKey))
		if string(knownHosts) != wantLine {
			t.Errorf("known_hosts = %q, want %q", knownHosts, wantLine)
		}

		// GIT_SSH_COMMAND must point at the generated config, or none of
		// the above applies to the push.
		if !strings.Contains(setup.Command, "-F") || !strings.Contains(setup.Command, dir) {
			t.Errorf("GIT_SSH_COMMAND does not use the generated config: %q", setup.Command)
		}

		// And cleanup really removes the key.
		cleanup()
		assertNoLeftoverSSHDir(t, os.Getenv("RUNNER_TEMP"))
	})
}

// A cancelled context ends a handshake that would otherwise wait on a
// silent server.
func TestScanHostKey_StopsWhenTheContextEnds(t *testing.T) {
	t.Parallel()

	port := listenerPort(t, listenLoopback(t))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := scanHostKey(ctx, "127.0.0.1", port, testHostKeyType); err == nil {
		t.Fatal("scan of a silent server succeeded after cancellation")
	}
}

func assertNoLeftoverSSHDir(t *testing.T, base string) {
	t.Helper()

	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "forge-ci-ssh.") {
			t.Errorf("ssh temp dir %q was left behind", e.Name())
		}
	}
}
