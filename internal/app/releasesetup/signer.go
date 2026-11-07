// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package releasesetup is the `forge-ci setup` family: the steps between a
// repository's first Forge CI files and its first release that a person does
// by hand. It lists them with their state, writes the allowed-signers entry a
// release is verified against, says which secrets to create and how, and
// checks a release request before it is pushed.
package releasesetup

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/crypto/ssh"

	appvalidate "github.com/diggsweden/forge-ci/internal/app/validate"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// Signer files by forge layout. The GitHub release and git-signing checks
// read .forge-ci/allowed_signers; the Forgejo examples point
// release-request-allowed-signers at .forgejo/release-allowed-signers. GitLab
// takes the text as the request-allowed-signers input, so there is no file.
const (
	SignersFile        = ".forge-ci/allowed_signers"
	ForgejoSignersFile = ".forgejo/release-allowed-signers"
)

const signersHeader = "# Who may sign Forge CI release requests and release commits.\n" +
	"# One line per key: principal key-type base64-key [comment]. See forge-ci docs verification.\n"

// DefaultSignersFile is the allowed-signers file the repository's release
// reads: Forgejo's when the repository has only Forgejo workflows, else the
// shared .forge-ci one.
func DefaultSignersFile(root string) string {
	forgejo := dirExists(filepath.Join(root, ".forgejo", "workflows"))
	github := dirExists(filepath.Join(root, ".github", "workflows"))

	if forgejo && !github {
		return ForgejoSignersFile
	}

	return SignersFile
}

// SignerInput adds one key to an allowed-signers file.
type SignerInput struct {
	Root          string
	File          string // relative to Root; empty takes DefaultSignersFile
	Principal     string // the signer's email, as git's user.email or the workflow's bot-email
	PublicKeyPath string
	DryRun        bool // build the line without writing it
}

// SignerResult is the line, and whether the file already had it.
type SignerResult struct {
	File    string
	Line    string
	Present bool
	Created bool
}

// AddSigner appends principal and the public key to the allowed-signers
// file, creating it with a header when it does not exist. It refuses a
// private key, and leaves the file alone when the line is already there.
func AddSigner(in SignerInput) (SignerResult, error) {
	if err := checkPrincipal(in.Principal); err != nil {
		return SignerResult{}, err
	}

	key, err := readPublicKey(in.PublicKeyPath)
	if err != nil {
		return SignerResult{}, err
	}

	file := in.File
	if file == "" {
		file = DefaultSignersFile(in.Root)
	}

	result := SignerResult{File: file, Line: in.Principal + " " + key}
	path := filepath.Join(in.Root, filepath.FromSlash(file))

	existing, err := os.ReadFile(path) //nolint:gosec // the repository's own allowlist, chosen by the operator.
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return SignerResult{}, err
	}

	if hasEntry(existing, in.Principal, key) {
		result.Present = true

		return result, nil
	}

	if in.DryRun {
		return result, nil
	}

	// The new line is valid by construction; refuse to append it to a file
	// that is already malformed rather than leave the person a changed,
	// still-broken allowlist.
	if existing != nil {
		if err := appvalidate.AllowedSignersFormat(path); err != nil {
			return SignerResult{}, fmt.Errorf("%s is malformed; fix it first: %w", file, err)
		}
	}

	result.Created = existing == nil

	body := existing
	if result.Created {
		body = []byte(signersHeader)
	} else if len(body) > 0 && !bytes.HasSuffix(body, []byte("\n")) {
		body = append(body, '\n')
	}

	body = append(body, []byte(result.Line+"\n")...)

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return SignerResult{}, err
	}

	if err := os.WriteFile(path, body, 0o644); err != nil { //nolint:gosec // a committed public allowlist, world-readable by design.
		return SignerResult{}, err
	}

	return result, nil
}

// principalName is one literal signer identity. allowed_signers principals
// are pattern lists, so a "*" or "?" would trust the key for every signer,
// and a leading "#" would turn the line into a comment.
var principalName = regexp.MustCompile(`^[A-Za-z0-9._%+-]+(@[A-Za-z0-9.-]+)?$`)

func checkPrincipal(principal string) error {
	if !principalName.MatchString(principal) {
		return fmt.Errorf("--principal %q must be one email address or name, without patterns, spaces or commas: %w", principal, errs.ErrUsage)
	}

	return nil
}

// readPublicKey returns "key-type base64" of an OpenSSH public key file.
func readPublicKey(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-named public key file; read-only.
	if err != nil {
		return "", fmt.Errorf("read public key: %w: %w", err, errs.ErrMissingInput)
	}

	return ParsePublicKey(data, path)
}

// ParsePublicKey reads an authorized_keys-format public key and returns its
// "key-type base64" form. A private key is refused by name, so it is never
// copied into a committed file.
func ParsePublicKey(data []byte, source string) (string, error) {
	pub, _, _, _, err := ssh.ParseAuthorizedKey(data)
	if err != nil {
		if bytes.Contains(data, []byte("PRIVATE KEY")) {
			return "", fmt.Errorf("%s is a private key; pass its .pub file: %w", source, errs.ErrUsage)
		}

		return "", fmt.Errorf("%s is not an OpenSSH public key (ssh-ed25519 AAAA…): %w", source, errs.ErrMalformedInput)
	}

	return marshalKey(pub), nil
}

func marshalKey(pub ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
}

// hasEntry reports whether body has a line for key ("key-type base64"),
// listing principal among its principals when principal is not empty. Each
// line is parsed as principals [options] key, the format git and the release
// read, so a key is matched exactly, never as part of another line.
func hasEntry(body []byte, principal, key string) bool {
	for line := range strings.SplitSeq(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		separator := strings.IndexAny(line, " \t")
		if separator < 0 {
			continue
		}

		principals := line[:separator]

		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line[separator:])) //nolint:dogsled // only the key is compared.
		if err != nil || marshalKey(pub) != key {
			continue
		}

		if principal == "" {
			return true
		}

		for p := range strings.SplitSeq(strings.Trim(principals, `"`), ",") {
			if p == principal {
				return true
			}
		}
	}

	return false
}

func dirExists(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.IsDir()
}
