// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// AllowedSignersFormat checks principal/key structure and option names.
// OpenSSH remains responsible for option values, matching and authorization.
// Empty files are syntactically valid; they authorize nobody.
func AllowedSignersFormat(path string) error {
	data, err := os.ReadFile(path) //nolint:gosec // G304: explicitly selected local allowlist; read-only validation.
	if err != nil {
		return fmt.Errorf("read allowed signers: %w", err)
	}

	var problems []error

	for index, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if err := checkAllowedSignerEntry(line); err != nil {
			problems = append(problems, fmt.Errorf("%s:%d: %w", path, index+1, err))
		}
	}

	return errors.Join(problems...)
}

func checkAllowedSignerEntry(line string) error {
	separator := strings.IndexAny(line, " \t")
	if separator < 0 {
		return fmt.Errorf("expected principal [options] key-type base64-key: %w", errs.ErrMalformedInput)
	}

	if slices.Contains(strings.Split(line[:separator], ","), "") {
		return fmt.Errorf("empty principal in pattern list: %w", errs.ErrMalformedInput)
	}

	// The key/options suffix shares authorized_keys syntax. The library checks
	// the encoded key against its declared algorithm and handles quoted options.
	_, _, options, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(line[separator:]))) //nolint:dogsled // Only options and parse errors are needed; the parser checks the key type.
	if err != nil {
		return fmt.Errorf("expected principal [options] key-type base64-key: %w: %w", err, errs.ErrMalformedInput)
	}

	for _, option := range options {
		name, _, hasValue := strings.Cut(option, "=")
		switch strings.ToLower(name) {
		case "cert-authority":
			if !hasValue {
				continue
			}
		case "namespaces", "valid-after", "valid-before":
			if hasValue {
				continue
			}
		}

		return fmt.Errorf("unsupported or malformed allowed-signers option %q: %w", name, errs.ErrMalformedInput)
	}

	return nil
}
