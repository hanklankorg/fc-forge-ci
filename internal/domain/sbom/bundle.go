// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package sbom holds pure SBOM-related domain helpers: canonical
// filenames, format decisions over a SBOMPolicy, and the like. SBOM
// generation itself shells out to syft via adapter/syft.
package sbom

import (
	"fmt"

	domainversion "github.com/diggsweden/forge-ci/internal/domain/version"
)

// BundleName returns the canonical SBOM bundle filename, e.g.
//
//	BundleName("my-app", "1.2.3") = "my-app-1.2.3-sboms.tar.gz"
func BundleName(projectName, version string) string {
	return fmt.Sprintf("%s-%s-sboms.tar.gz", domainversion.SanitizePathToken(projectName), domainversion.SanitizePathToken(version))
}
