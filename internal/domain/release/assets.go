// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

// SignatureSidecars returns every possible signature sidecar path
// next to a release asset, across all supported signing methods.
// The release-create flow scans the result and attaches whichever
// files exist on disk — the producer chose one method per repo
// (`sign.method` in artifacts.yml), so in practice exactly one of
// the returned paths will exist.
//
// Order is stable; callers should not assume a specific layout.
func SignatureSidecars(path string) []string {
	return []string{
		path + ".asc",    // gpg detached signature
		path + ".bundle", // cosign v3 bundle (sigstore + kms)
	}
}
