// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package config

import (
	"fmt"
	"slices"

	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/domain/publish"
)

func validateNPMPublish(artifact Artifact) []string {
	if artifact.ProjectType != projecttype.NPM || !slices.Contains(artifact.PublishTo, PublishForgePackages) {
		return nil
	}

	tag := ""
	if artifact.NPM != nil {
		tag = artifact.NPM.Tag
	}

	if err := publish.ValidateNPMTag(tag); err != nil {
		return []string{fmt.Sprintf("artifact %q: %v", artifact.Name, err)}
	}

	return nil
}
