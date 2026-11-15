// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"fmt"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/archive"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domainversion "github.com/diggsweden/forge-ci/internal/domain/version"
)

// buildSBOMSuffix ends the name of every build-layer SBOM file.
const buildSBOMSuffix = "-build-sbom.cyclonedx.json"

// requireContainerFirstBuildSBOMs holds the SBOM bundle to the build SBOM
// of each named container-first artifact, as `sbom assemble
// --artifacts-config` names it: <artifact>-<version>[-<sha>]-build-sbom.
// cyclonedx.json. A file is credited to the longest name it starts with,
// among these and the built artifacts' names, so the build SBOM of a built
// api-gateway is never a container-first api's.
func requireContainerFirstBuildSBOMs(ctx releaseFilesCtx, manifest *FileManifest, names, built []string) error {
	if len(names) == 0 {
		return nil
	}

	var members []archive.TarFile

	if len(manifest.SBOMs) != 0 {
		var err error
		if _, members, err = handoffSBOMBundle(ctx, manifest, SBOMBundleNaming{}); err != nil {
			return err
		}
	}

	all := append(slices.Clone(names), built...)
	covered := map[string]bool{}

	for _, member := range members {
		covered[buildSBOMSubject(member.Name, all)] = true
	}

	var missing []string

	for _, name := range names {
		if !covered[name] {
			missing = append(missing, name)
		}
	}

	if len(missing) == 0 {
		return nil
	}

	return fmt.Errorf("the SBOM bundle holds no build SBOM of the container-first %s: write it with `forge-ci sbom assemble --artifacts-config` into dist/sboms/ (the sbom-go and sbom-cargo actions on Forgejo, the SBOM jobs on GitLab), and declare it with `release files declare --sbom 'dist/sboms/*'`: %w",
		strings.Join(missing, ", "), errs.ErrValidation)
}

// buildSBOMSubject is the longest of names whose build SBOM is the file
// member, empty when it is none of theirs.
func buildSBOMSubject(member string, names []string) string {
	rest, ok := strings.CutSuffix(member, buildSBOMSuffix)
	if !ok {
		return ""
	}

	subject := ""

	for _, name := range names {
		token := domainversion.SanitizePathToken(name)
		if strings.HasPrefix(rest, token+"-") && len(token) > len(domainversion.SanitizePathToken(subject)) {
			subject = name
		}
	}

	return subject
}
