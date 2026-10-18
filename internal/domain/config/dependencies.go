// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package config

import (
	"fmt"
	"github.com/diggsweden/forge-ci/internal/domain/build"
)

func validatePrivateDependencies(artifact Artifact) []string {
	if artifact.NPM != nil && artifact.NPM.PrivateDependencies {
		if _, err := build.NPMDependencyAuthConfig(artifact.NPM.DependencyRegistry); err != nil {
			return []string{fmt.Sprintf("artifact %q: %v", artifact.Name, err)}
		}
	}

	if artifact.Maven != nil && artifact.Maven.PrivateDependencies && artifact.Maven.SettingsPath == "" {
		return []string{fmt.Sprintf("artifact %q: private Maven dependencies require config.settings-path with consumer-owned authentication settings", artifact.Name)}
	}

	return nil
}
