// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage

import (
	"fmt"

	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

// The toolchain families a snapshot's artifact producers build in, one image
// each: the artifact-job-image-<family> inputs of the Forgejo workflow and the
// GitLab component.
const (
	FamilyGo   = "go"
	FamilyJava = "java"
	FamilyNode = "node"
	FamilyRust = "rust"
)

// ToolchainFamily is the family whose image builds an artifact of a project
// type: go, java (Maven and Gradle), node (npm) or rust (Cargo). An artifact
// of another type has no snapshot producer.
func ToolchainFamily(projectType projecttype.Type) (string, error) {
	switch projectType {
	case projecttype.Go:
		return FamilyGo, nil
	case projecttype.Maven, projecttype.Gradle:
		return FamilyJava, nil
	case projecttype.NPM:
		return FamilyNode, nil
	case projecttype.Cargo:
		return FamilyRust, nil
	default:
		return "", fmt.Errorf("%w %s", errUnsupportedProducerType, projectType)
	}
}
