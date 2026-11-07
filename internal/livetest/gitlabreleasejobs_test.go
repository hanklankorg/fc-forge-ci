// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"encoding/json"
	"testing"

	gitlabapi "gitlab.com/gitlab-org/api/client-go"
)

func releaseEvidenceJobsFixture(t *testing.T) []*gitlabapi.Job {
	t.Helper()

	var jobs []*gitlabapi.Job
	if err := json.Unmarshal([]byte(`[
		{"id":1,"name":"Build: release files","status":"success","pipeline":{"id":913},"commit":{"id":"request"},"artifacts_file":{"size":1}},
		{"id":2,"name":"Images: publish","status":"success","pipeline":{"id":913},"commit":{"id":"request"},"artifacts_file":{"size":1}},
		{"id":3,"name":"Sign: files and images","status":"success","pipeline":{"id":913},"commit":{"id":"request"},"artifacts_file":{"size":1}}
	]`), &jobs); err != nil {
		t.Fatal(err)
	}

	return jobs
}

func TestSelectGitLabReleaseEvidenceJobs(t *testing.T) {
	jobs := releaseEvidenceJobsFixture(t)

	got, err := SelectGitLabReleaseEvidenceJobs(jobs, 913, "request", true)
	if err != nil || got != (GitLabReleaseEvidenceJobs{Build: 1, Publisher: 2, Signer: 3}) {
		t.Fatalf("image chain: %+v, %v", got, err)
	}

	got, err = SelectGitLabReleaseEvidenceJobs([]*gitlabapi.Job{jobs[0], jobs[2]}, 913, "request", false)
	if err != nil || got != (GitLabReleaseEvidenceJobs{Build: 1, Signer: 3}) {
		t.Fatalf("file-only chain: %+v, %v", got, err)
	}
}

func TestSelectGitLabReleaseEvidenceJobsRejectsUnboundChains(t *testing.T) {
	for _, name := range []string{"missing build", "missing publisher", "missing signer", "unexpected publisher", "duplicate build", "duplicate publisher", "duplicate signer", "failed publisher", "empty publisher", "wrong child", "wrong commit", "missing commit", "nil job", "invalid id"} {
		t.Run(name, func(t *testing.T) {
			jobs := releaseEvidenceJobsFixture(t)
			images := true

			switch name {
			case "missing build":
				jobs = jobs[1:]
			case "missing publisher":
				jobs = []*gitlabapi.Job{jobs[0], jobs[2]}
			case "missing signer":
				jobs = jobs[:2]
			case "unexpected publisher":
				images = false
			case "duplicate build":
				jobs = append(jobs, jobs[0])
			case "duplicate publisher":
				jobs = append(jobs, jobs[1])
			case "duplicate signer":
				jobs = append(jobs, jobs[2])
			case "failed publisher":
				jobs[1].Status = "failed"
			case "empty publisher":
				jobs[1].ArtifactsFile.Size = 0
			case "wrong child":
				jobs[1].Pipeline.ID++
			case "wrong commit":
				jobs[1].Commit.ID = "other"
			case "missing commit":
				jobs[1].Commit = nil
			case "nil job":
				jobs[1] = nil
			case "invalid id":
				jobs[1].ID = 0
			}

			if _, err := SelectGitLabReleaseEvidenceJobs(jobs, 913, "request", images); err == nil {
				t.Fatal("accepted an unbound or incomplete artifact chain")
			}
		})
	}
}
