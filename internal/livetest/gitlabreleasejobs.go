// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"fmt"

	gitlabapi "gitlab.com/gitlab-org/api/client-go"
)

// GitLabReleaseEvidenceJobs identifies the policy-selected native handoffs.
type GitLabReleaseEvidenceJobs struct {
	Build, Publisher, Signer int64
}

// SelectGitLabReleaseEvidenceJobs requires the unique policy-selected artifact
// chain; failed, missing, duplicate and foreign handoffs cannot be substituted.
// The caller supplies a complete job listing and its expected image contract.
func SelectGitLabReleaseEvidenceJobs(jobs []*gitlabapi.Job, child int64, requestSHA string, images bool) (GitLabReleaseEvidenceJobs, error) {
	var selected GitLabReleaseEvidenceJobs

	for _, job := range jobs {
		if !releaseJobBoundToChild(job, child, requestSHA) {
			return selected, fmt.Errorf("release artifact job is not bound to the expected native child pipeline/commit: %w", errReleaseEvidence)
		}

		var id *int64

		switch job.Name {
		case "Build: release files":
			id = &selected.Build
		case "Images: publish":
			if !images {
				return selected, fmt.Errorf("unexpected release image publisher: %w", errReleaseEvidence)
			}

			id = &selected.Publisher
		case "Sign: files and images":
			id = &selected.Signer
		default:
			continue
		}

		if *id != 0 || !releaseJobHasArtifact(job) {
			return selected, fmt.Errorf("release needs a unique successful %s artifact: %w", job.Name, errReleaseEvidence)
		}

		*id = job.ID
	}

	if selected.Build == 0 || selected.Signer == 0 || (images && selected.Publisher == 0) {
		return selected, fmt.Errorf("release lacks its required build/publisher/signer artifact chain: %w", errReleaseEvidence)
	}

	return selected, nil
}

func releaseJobBoundToChild(job *gitlabapi.Job, child int64, requestSHA string) bool {
	return job != nil && child > 0 && job.Pipeline.ID == child && job.Commit != nil && requestSHA != "" && job.Commit.ID == requestSHA
}

func releaseJobHasArtifact(job *gitlabapi.Job) bool {
	return job.ID > 0 && job.Status == "success" && job.ArtifactsFile.Size > 0
}
