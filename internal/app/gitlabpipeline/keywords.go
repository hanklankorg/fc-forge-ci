// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabpipeline

// GitLab CI keywords and the stage names the rendered pipelines use.
const (
	keyArtifacts = "artifacts"
	keyExtends   = "extends"
	keyJob       = "job"
	keyNeeds     = "needs"
	keyScript    = "script"
	keyStage     = "stage"

	stageArtifacts = "artifacts"
	stageBuild     = "build"
	stagePublish   = "publish"
)
