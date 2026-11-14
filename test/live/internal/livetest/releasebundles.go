// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"fmt"
	"strings"
)

// VerifyReleaseBundles pairs offered .bundle assets with their offered subjects.
// The callback owns bounded reads, retention and native trust verification, and
// must fail if either read or verification fails. Only successful subjects enter
// the result; failures are joined so other pairs can still be read and verified.
// Inventory admission and minimum/all-subject signature coverage stay caller-owned.
func VerifyReleaseBundles(names []string, verify func(subject, bundle string) error) (map[string]bool, error) {
	return verifyReleaseSidecars(names, cosignBundle, "bundle", verify)
}

// The signature files a release offers beside the file each signs: a Cosign
// bundle, and the armored OpenPGP signature of a release whose declaration
// signs its files with GPG.
const (
	cosignBundle     = ".bundle"
	openPGPSignature = ".asc"
)

// verifyReleaseSidecars is VerifyReleaseBundles for the signature files
// named by suffix, which its findings call kind.
func verifyReleaseSidecars(names []string, suffix, kind string, verify func(subject, sidecar string) error) (map[string]bool, error) {
	offered := make(map[string]bool, len(names))
	for _, name := range names {
		offered[name] = true
	}

	verified := map[string]bool{}

	var failures []error

	for _, name := range names {
		subject, isSidecar := strings.CutSuffix(name, suffix)
		if !isSidecar {
			continue
		}

		if !offered[subject] {
			failures = append(failures, fmt.Errorf("release %s %q has no offered subject %q: %w", kind, name, subject, errReleaseEvidence))

			continue
		}

		if err := verify(subject, name); err != nil {
			failures = append(failures, fmt.Errorf("verify release %s %q for %q: %w", kind, name, subject, err))

			continue
		}

		verified[subject] = true
	}

	return verified, errors.Join(failures...)
}
