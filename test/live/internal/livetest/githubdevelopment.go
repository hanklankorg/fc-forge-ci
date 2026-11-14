// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	githubapi "github.com/google/go-github/v91/github"
)

// DispatchDevelopment sends exactly one dispatch of workflow on main to the
// new disposable consumer, with the workflow's inputs, if any. An uncertain
// response fails the trial; it is never dispatched again.
func (trial *GitHubTrial) DispatchDevelopment(repo, workflow string, inputs map[string]any) int64 {
	trial.tb.Helper()

	details, _, err := trial.api.Actions.CreateWorkflowDispatchEventByFileName(trial.tb.Context(), trial.org, repo, workflow,
		githubapi.CreateWorkflowDispatchEventRequest{Ref: defaultBranch, Inputs: inputs, ReturnRunDetails: new(true)})
	if err != nil {
		trial.tb.Fatalf("dispatch development workflow %s: %v", workflow, err)
	}

	if details != nil && details.GetWorkflowRunID() != 0 {
		trial.tb.Logf("development workflow dispatched as run %d (%s)", details.GetWorkflowRunID(), details.GetHTMLURL())

		return details.GetWorkflowRunID()
	}

	return 0
}

// RepositoryID reads the ID of repo, the binding a container package of the
// repository carries.
func (trial *GitHubTrial) RepositoryID(repo string) int64 {
	trial.tb.Helper()

	repository, _, err := trial.api.Repositories.Get(trial.tb.Context(), trial.org, repo)
	if err != nil || repository.GetID() <= 0 {
		trial.tb.Fatalf("livetest: read %s: %v", repo, err)
	}

	return repository.GetID()
}

// RemoveSnapshotPackageAfter removes, when the test ends, the snapshot image a
// development run publishes to repo's container package, the snapshot
// orchestrator's default image repository, whether or not the run got that
// far: the identity is read then, a package never published is nothing to
// remove, and an identity that cannot be read is not deleted. Deleting the
// repository does not delete its packages, so this is registered before the
// dispatch.
func (trial *GitHubTrial) RemoveSnapshotPackageAfter(repo string, repositoryID int64) {
	trial.tb.Helper()
	trial.removeContainerPackageAfter("snapshot", strings.ToLower(repo), repo, repositoryID)
}

// removeContainerPackageAfter removes, when the test ends, the organization's
// container package of that name, and only as a package of repo: the identity
// is read then, a package never published is nothing to remove, and an
// identity that cannot be read is not deleted.
func (trial *GitHubTrial) removeContainerPackageAfter(kind, name, repo string, repositoryID int64) {
	trial.tb.Helper()

	trial.tb.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(trial.tb.Context()), 2*time.Minute)
		defer cancel()

		identity, err := trial.ReadContainerPackageIdentity(ctx, name, repositoryID)
		if errors.Is(err, os.ErrNotExist) {
			return
		}

		if err != nil {
			trial.tb.Errorf("livetest: %s package %s of %s: identity unavailable, refusing deletion: %v", kind, name, repo, err)

			return
		}

		if err := trial.DeleteContainerPackage(ctx, identity); err != nil {
			trial.tb.Errorf("livetest: delete the %s package %s (id %d): %v", kind, identity.Name, identity.ID, err)

			return
		}

		trial.tb.Logf("%s package deletion confirmed: package=%s id=%d acknowledged-repository=%d", kind, identity.Name, identity.ID, repositoryID)
	})
}

// GitHubSnapshotPlanArtifact and GitHubSnapshotArtifact are the artifacts
// the snapshot-images workflow uploads under its default artifact name: the
// plan job's inputs, holding the frozen plan, and the collect job's receipts.
const (
	GitHubSnapshotPlanArtifact = "snapshot-images-inputs"
	GitHubSnapshotArtifact     = "snapshot-images"
)

// SnapshotRun reads what a development run of repo left for the snapshot
// oracle (RequireSnapshotImages): the declaration it planned, the one the
// trial staged at the selected head, the frozen plan and the receipts.
func (trial *GitHubTrial) SnapshotRun(repo string, tree GitHubConsumerTree, runID int64, branch, head string) SnapshotRun {
	trial.tb.Helper()

	return SnapshotRun{
		Declaration: trial.git(tree.Dir, "show", head+":"+ServiceSnapshotDeclarationFile),
		Branch:      branch, Head: head,
		Plan:     trial.runArtifactFile(repo, runID, GitHubSnapshotPlanArtifact, "snapshot-plan.json"),
		Receipts: trial.runArtifactFile(repo, runID, GitHubSnapshotArtifact, "container-images.json"),
	}
}

// runArtifactFile downloads the run's one artifact called artifactName and
// returns its member file.
func (trial *GitHubTrial) runArtifactFile(repo string, runID int64, artifactName, file string) []byte {
	trial.tb.Helper()

	options := &githubapi.ListOptions{PerPage: 100}

	var selected *githubapi.Artifact

	for {
		artifacts, response, err := trial.api.Actions.ListWorkflowRunArtifacts(trial.tb.Context(), trial.org, repo, runID, options)
		if err != nil {
			trial.tb.Fatalf("list the artifacts of run %d: %v", runID, err)
		}

		for _, artifact := range artifacts.Artifacts {
			if artifact.GetName() == artifactName {
				if selected != nil || artifact.GetExpired() {
					trial.tb.Fatalf("run %d has duplicate or expired artifact %s", runID, artifactName)
				}

				selected = artifact
			}
		}

		if response.NextPage == 0 {
			break
		}

		options.Page = response.NextPage
	}

	if selected == nil {
		trial.tb.Fatalf("run %d left no artifact %s", runID, artifactName)
	}

	archive, _ := trial.downloadArtifact(repo, selected)

	return trial.archiveMember(archive, selected.GetID(), file)
}

// archiveMember reads one member of a downloaded artifact archive, within
// the artifact budget.
func (trial *GitHubTrial) archiveMember(archiveFile string, artifactID int64, member string) []byte {
	trial.tb.Helper()

	archive, err := zip.OpenReader(archiveFile)
	if err != nil {
		trial.tb.Fatalf("artifact %d is not a valid ZIP: %v", artifactID, err)
	}

	defer func() { _ = archive.Close() }()

	matches := 0

	for _, file := range archive.File {
		if file.Name == member {
			matches++
		}
	}

	if matches != 1 {
		trial.tb.Fatalf("artifact %d must contain exactly one %s, found %d", artifactID, member, matches)
	}

	reader, err := archive.Open(member)
	if err != nil {
		trial.tb.Fatalf("artifact %d has no %s: %v", artifactID, member, err)
	}

	defer func() { _ = reader.Close() }()

	body, err := io.ReadAll(io.LimitReader(reader, maxDevelopmentArtifactBytes+1))
	if err != nil || len(body) > maxDevelopmentArtifactBytes {
		trial.tb.Fatalf("artifact %d member %s is damaged or over budget", artifactID, member)
	}

	return body
}

// CheckDevelopmentRelease proves artifact transport and that a branch build
// did not turn into a stable release. Package publication, artifact-specific
// versions and containers require their own consumer-specific verification.
func (trial *GitHubTrial) CheckDevelopmentRelease(repo, source string, runID int64) {
	trial.tb.Helper()

	trial.checkDevelopmentStableState(repo, source)
	verified := trial.checkDevelopmentArtifacts(repo, runID)
	trial.tb.Logf("development run %d: %d artifact archives downloaded and verified; main unchanged, no stable tags or releases", runID, verified)
}

func (trial *GitHubTrial) checkDevelopmentStableState(repo, source string) {
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
}

func (trial *GitHubTrial) checkDevelopmentArtifacts(repo string, runID int64) int {
	ctx := trial.tb.Context()
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

	return verified
}

// maxDevelopmentArtifactBytes bounds one artifact's download and total expanded
// contents. Archives are checked in place, never extracted or executed.
const maxDevelopmentArtifactBytes = 1 << 30

func (trial *GitHubTrial) checkDevelopmentArtifact(repo string, artifact *githubapi.Artifact) {
	file, downloaded := trial.downloadArtifact(repo, artifact)
	files := trial.checkDevelopmentArchive(file, artifact.GetID())
	trial.tb.Logf("development artifact %d %q: %d bytes downloaded, %d files verified", artifact.GetID(), artifact.GetName(), downloaded, files)
}

// downloadArtifact downloads an artifact archive within the 1 GiB budget,
// checks it against GitHub's digest and returns its file and size.
func (trial *GitHubTrial) downloadArtifact(repo string, artifact *githubapi.Artifact) (string, int64) {
	location, located, err := trial.api.Actions.DownloadArtifact(trial.tb.Context(), trial.org, repo, artifact.GetID(), 3)
	if err != nil || location == nil {
		// The status says why; the error may quote a signed location.
		status := 0
		if located != nil {
			status = located.StatusCode
		}

		trial.tb.Fatalf("could not obtain download location for development artifact %d %q: HTTP %d", artifact.GetID(), artifact.GetName(), status)
	}

	request, err := http.NewRequestWithContext(trial.tb.Context(), http.MethodGet, location.String(), nil)
	if err != nil {
		trial.tb.Fatalf("could not prepare development artifact download")
	}
	// Do not forward the API credential or print transport errors containing
	// this pre-signed URL's private query.
	response, err := hostedReads.Do(request)
	if err != nil {
		trial.tb.Fatalf("development artifact %d download failed", artifact.GetID())
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		trial.tb.Fatalf("development artifact %d download returned HTTP %d", artifact.GetID(), response.StatusCode)
	}

	file := filepath.Join(trial.tempDir("development-artifact"), "artifact.zip")

	destination, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // G304: fixed basename inside the trial-owned temporary directory.
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

	return file, downloaded
}

func (trial *GitHubTrial) checkDevelopmentArchive(file string, artifactID int64) int {
	archive, err := zip.OpenReader(file)
	if err != nil {
		trial.tb.Fatalf("development artifact %d is not a valid ZIP: %v", artifactID, err)
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
			trial.tb.Fatalf("development artifact %d has damaged or over-budget contents", artifactID)
		}

		files++
	}

	if files == 0 {
		trial.tb.Fatalf("development artifact ZIP contains no files")
	}

	return files
}
