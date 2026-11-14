// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The verifier holds each package of a release to a detached signature of
// the release's own key over the package's bytes, whatever its format: a
// package of each format verifies, and a changed package, one without a
// sidecar and one signed with another key are each a finding that names it,
// while the others still count.
func TestVerifyPackageSignatures(t *testing.T) {
	t.Parallel()

	const passphrase = "package-signature-test"

	runtime := runtimeEnvironment(t)
	environment := make([]string, 0, len(runtime))

	for key, value := range runtime {
		environment = append(environment, key+"="+value)
	}

	// Two throwaway keys, each in a GnuPG home of its own: the release's,
	// and one the verifier's home does not hold.
	release, foreign := t.TempDir(), t.TempDir()
	GPGKey(t, release, passphrase)
	GPGKey(t, foreign, passphrase)

	sign := func(keys string, body string) string {
		file := filepath.Join(t.TempDir(), "package")
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}

		command := exec.CommandContext(t.Context(), "gpg", "--homedir", filepath.Join(keys, "gnupg"), "--batch",
			"--pinentry-mode", "loopback", "--passphrase-fd", "0", "--detach-sign", "--output", file+".sig", file)
		command.Stdin = strings.NewReader(passphrase + "\n")
		command.Env = environment

		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("gpg --detach-sign: %v\n%s", err, output)
		}

		signature, err := os.ReadFile(file + ".sig")
		if err != nil {
			t.Fatal(err)
		}

		return string(signature)
	}

	files := map[string]string{
		"tool_1.0.0_linux_amd64.deb":     "debian package",
		"tool_1.0.0_linux_amd64.deb.sig": sign(release, "debian package"),
		"tool_1.0.0_linux_amd64.rpm":     "rpm package",
		"tool_1.0.0_linux_amd64.rpm.sig": sign(release, "rpm package"),
		"tool_1.0.0_linux_amd64.apk":     "alpine package",
		"tool_1.0.0_linux_amd64.apk.sig": sign(release, "alpine package"),
		// Not a package: no signature is asked of it.
		"checksums.txt": "checksums",
	}
	signed := []string{"tool_1.0.0_linux_amd64.apk", "tool_1.0.0_linux_amd64.deb", "tool_1.0.0_linux_amd64.rpm"}

	verify := func(files map[string]string) ([]string, error) {
		names := make([]string, 0, len(files))
		for name := range files {
			names = append(names, name)
		}

		slices.Sort(names)

		return VerifyPackageSignatures(t.Context(), filepath.Join(release, "gnupg"), environment, names, func(name string) ([]byte, error) {
			return []byte(files[name]), nil
		})
	}

	if verified, err := verify(files); err != nil || !slices.Equal(verified, signed) {
		t.Fatalf("verified %v, %v; want %v", verified, err, signed)
	}

	for name, test := range map[string]struct {
		file, signature, finding string
	}{
		"changed after signing": {"changed.deb", sign(release, "another package"), "package changed.deb: the signature does not verify"},
		"no sidecar":            {"unsigned.rpm", "", "package unsigned.rpm has no published OpenPGP signature unsigned.rpm.sig"},
		"another key":           {"foreign.apk", sign(foreign, "a package"), "package foreign.apk: the signature does not verify"},
	} {
		released := map[string]string{test.file: "a package"}
		if test.signature != "" {
			released[test.file+".sig"] = test.signature
		}

		maps.Copy(released, files)

		verified, err := verify(released)
		if err == nil || !strings.Contains(err.Error(), test.finding) || !slices.Equal(verified, signed) {
			t.Errorf("%s: verified %v, %v; want %v and a finding %q", name, verified, err, signed, test.finding)
		}
	}

	// A file that cannot be read is a finding too, not a pass.
	unread := errors.New("not served")

	if _, err := VerifyPackageSignatures(t.Context(), filepath.Join(release, "gnupg"), environment, []string{"tool.deb", "tool.deb.sig"}, func(string) ([]byte, error) {
		return nil, unread
	}); !errors.Is(err, unread) {
		t.Errorf("an unread package: %v, want %v", err, unread)
	}
}
