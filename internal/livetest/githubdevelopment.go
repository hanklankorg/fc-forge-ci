// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	githubapi "github.com/google/go-github/v91/github"
)

// DispatchDevelopment sends exactly one dispatch to the new disposable
// consumer. An uncertain response fails the trial; it is never dispatched again.
func (trial *GitHubTrial) DispatchDevelopment(repo, workflow string) int64 {
	trial.tb.Helper()
	details, _, err := trial.api.Actions.CreateWorkflowDispatchEventByFileName(trial.tb.Context(), trial.org, repo, workflow,
		githubapi.CreateWorkflowDispatchEventRequest{Ref: "main", ReturnRunDetails: githubapi.Ptr(true)})
	if err != nil {
		trial.tb.Fatalf("dispatch development workflow %s: %v", workflow, err)
	}
	if details != nil && details.GetWorkflowRunID() != 0 {
		trial.tb.Logf("development workflow dispatched as run %d (%s)", details.GetWorkflowRunID(), details.GetHTMLURL())
		return details.GetWorkflowRunID()
	}
	return 0
}

// CheckDevelopmentRelease proves artifact transport and that a branch build
// did not turn into a stable release. Package publication, artifact-specific
// versions and containers require their own consumer-specific verification.
func (trial *GitHubTrial) CheckDevelopmentRelease(repo, source string, runID int64) {
	trial.tb.Helper()
	ctx := trial.tb.Context()
	main, _, err := trial.api.Git.GetRef(ctx, trial.org, repo, "heads/main")
	if err != nil || main.GetObject().GetSHA() != source {
		trial.tb.Fatalf("development build changed main or main could not be read: %v", err)
	}
	tags, _, err := trial.api.Repositories.ListTags(ctx, trial.org, repo, &githubapi.ListOptions{PerPage: 1})
	if err != nil || len(tags) != 0 {
		trial.tb.Fatalf("development build created a tag or tags could not be read: %v", err)
	}
	releases, _, err := trial.api.Repositories.ListReleases(ctx, trial.org, repo, &githubapi.ListOptions{PerPage: 1})
	if err != nil || len(releases) != 0 {
		trial.tb.Fatalf("development build created a release or releases could not be read: %v", err)
	}
	options := &githubapi.ListOptions{PerPage: 100}
	verified := 0
	for {
		artifacts, response, err := trial.api.Actions.ListWorkflowRunArtifacts(ctx, trial.org, repo, runID, options)
		if err != nil {
			trial.tb.Fatalf("list development artifacts: %v", err)
		}
		for _, artifact := range artifacts.Artifacts {
			if artifact.GetExpired() {
				trial.tb.Fatalf("development artifact %d expired before verification", artifact.GetID())
			}
			trial.checkDevelopmentArtifact(repo, artifact)
			verified++
		}
		if response.NextPage == 0 {
			break
		}
		options.Page = response.NextPage
	}
	if verified == 0 {
		trial.tb.Fatalf("development workflow produced no downloadable artifacts")
	}
	trial.tb.Logf("development run %d: %d artifact archives downloaded and verified; main unchanged, no stable tags or releases", runID, verified)
}

// maxDevelopmentArtifactBytes bounds one artifact's download and total expanded
// contents. Archives are checked in place, never extracted or executed.
const maxDevelopmentArtifactBytes = 1 << 30

func (trial *GitHubTrial) checkDevelopmentArtifact(repo string, artifact *githubapi.Artifact) {
	location, _, err := trial.api.Actions.DownloadArtifact(trial.tb.Context(), trial.org, repo, artifact.GetID(), 3)
	if err != nil || location == nil {
		trial.tb.Fatalf("could not obtain download location for development artifact %d", artifact.GetID())
	}
	request, err := http.NewRequestWithContext(trial.tb.Context(), http.MethodGet, location.String(), nil)
	if err != nil {
		trial.tb.Fatalf("could not prepare development artifact download")
	}
	// Do not forward the API credential or print transport errors containing
	// this pre-signed URL's private query.
	response, err := (&http.Client{Timeout: 5 * time.Minute}).Do(request)
	if err != nil {
		trial.tb.Fatalf("development artifact %d download failed", artifact.GetID())
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		trial.tb.Fatalf("development artifact %d download returned HTTP %d", artifact.GetID(), response.StatusCode)
	}
	file := filepath.Join(trial.tempDir("development-artifact"), "artifact.zip")
	destination, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		trial.tb.Fatalf("create development artifact file: %v", err)
	}
	hash := sha256.New()
	downloaded, copyErr := io.Copy(io.MultiWriter(destination, hash), io.LimitReader(response.Body, maxDevelopmentArtifactBytes+1))
	closeErr := destination.Close()
	if copyErr != nil || closeErr != nil || downloaded > maxDevelopmentArtifactBytes {
		trial.tb.Fatalf("development artifact %d could not be downloaded completely within the 1 GiB budget", artifact.GetID())
	}
	if digest := artifact.GetDigest(); digest != "" && digest != "sha256:"+hex.EncodeToString(hash.Sum(nil)) {
		trial.tb.Fatalf("development artifact %d digest differs from GitHub metadata", artifact.GetID())
	}
	archive, err := zip.OpenReader(file)
	if err != nil {
		trial.tb.Fatalf("development artifact %d is not a valid ZIP: %v", artifact.GetID(), err)
	}
	defer func() { _ = archive.Close() }()
	var expanded int64
	files := 0
	for _, entry := range archive.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		reader, err := entry.Open()
		if err != nil {
			trial.tb.Fatalf("open development artifact member: %v", err)
		}
		size, readErr := io.Copy(io.Discard, io.LimitReader(reader, maxDevelopmentArtifactBytes-expanded+1))
		closeErr := reader.Close()
		expanded += size
		if readErr != nil || closeErr != nil || expanded > maxDevelopmentArtifactBytes {
			trial.tb.Fatalf("development artifact %d has damaged or over-budget contents", artifact.GetID())
		}
		files++
	}
	if files == 0 {
		trial.tb.Fatalf("development artifact ZIP contains no files")
	}
	trial.tb.Logf("development artifact %d %q: %d bytes downloaded, %d files verified", artifact.GetID(), artifact.GetName(), downloaded, files)
}
