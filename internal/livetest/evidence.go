// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	gitlabapi "gitlab.com/gitlab-org/api/client-go"

	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

func snapshotEvidenceRoot() string {
	if root := os.Getenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR"); root != "" {
		return root
	}
	return os.Getenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE")
}

// Publish one plain evidence file atomically through a confined descriptor.
// Replacing an existing link never opens its target (including hard links).
func writeSnapshotEvidence(root, name string, body []byte) error {
	if !filepath.IsAbs(root) || filepath.Base(name) != name || name == "." || name == ".." || strings.ContainsAny(name, "\\\x00") {
		return errors.New("evidence requires an absolute root and plain filename")
	}
	dir, err := pathsafe.MkdirRoot(root, 0700)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	tmp := ".evidence-" + rand.Text()
	file, err := dir.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Remove(tmp) }()
	_, writeErr := file.Write(body)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return dir.Rename(tmp, name)
}

// RetainJobLog reads at most 8 MiB and saves the full redacted log before the
// caller tears down its repository. It returns the same redacted console tail
// as JobLogTail. Pass all scenario-owned secret sets, in addition to the target
// credential. Retention is opt-in through the existing snapshot evidence envs;
// failures are reported without replacing the workflow's verdict.
func RetainJobLog(tb TB, target Target, repo string, job RunJob, secrets ...map[string]string) string {
	tb.Helper()
	log, err := jobLog(tb, target, repo, job)
	if err != nil {
		tb.Logf("livetest: job %d log unavailable; no full evidence retained", job.ID)
		return ""
	}
	return retainJobLogText(tb, target, repo, job, log, secrets...)
}

func retainJobLogText(tb TB, target Target, repo string, job RunJob, log string, secrets ...map[string]string) string {
	tb.Helper()
	log = redactJobLog(target, log, secrets...)
	if root := snapshotEvidenceRoot(); root != "" {
		location, saveErr := retainJobLog(root, repo, job, log)
		if saveErr != nil {
			// Do not echo paths or errors that could contain scenario secrets.
			tb.Errorf("livetest: could not retain full log for job %d in external evidence directory", job.ID)
		} else {
			tb.Logf("livetest: full job log retained at %s", redactJobLog(target, location, secrets...))
		}
	} else {
		tb.Logf("livetest: full job log not retained; configure FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR")
	}
	if len(log) > jobLogTail {
		log = log[len(log)-jobLogTail:]
	}
	return log
}

// RetainGitLabJobLogsAfter captures every executed job in an owned scratch
// project, including child pipelines and retries, before earlier-registered
// repository cleanup. Register after creating the repository and credentials.
func RetainGitLabJobLogsAfter(tb TB, target Target, repo string, secrets ...map[string]string) {
	tb.Helper()
	requireAccepted(tb, target)
	tb.Cleanup(func() {
		// The scenario's context may already be canceled on failure or return.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		client, err := gitlabapi.NewClient(target.Token, gitlabapi.WithBaseURL(target.BaseURL()+"/api/v4/"),
			gitlabapi.WithHTTPClient(HTTPClient(tb, target, time.Minute)))
		if err != nil {
			tb.Errorf("livetest: could not initialize GitLab log retention")
			return
		}
		for page := int64(1); ; {
			jobs, response, err := client.Jobs.ListProjectJobs(RepoSlug(target, repo), &gitlabapi.ListJobsOptions{
				PerPage: 100, Page: page,
			}, gitlabapi.WithContext(ctx))
			if err != nil {
				tb.Errorf("livetest: could not list GitLab jobs for evidence")
				return
			}
			for _, job := range jobs {
				if job.Status == "skipped" || job.Status == "manual" || job.Status == "created" {
					continue
				}
				trace, _, err := client.Jobs.GetTraceFile(RepoSlug(target, repo), job.ID, gitlabapi.WithContext(ctx))
				if err != nil {
					tb.Logf("livetest: job %d log unavailable; no full evidence retained", job.ID)
					continue
				}
				body, err := io.ReadAll(io.LimitReader(trace, maxJobLog+1))
				if err != nil || len(body) > maxJobLog {
					tb.Errorf("livetest: job %d log unreadable or exceeds evidence limit", job.ID)
					continue
				}
				id := RunJob{ID: job.ID, RunID: job.Pipeline.ID, Attempt: 1}
				tail := retainJobLogText(tb, target, repo, id, string(body), secrets...)
				if job.Status != "success" && tail != "" {
					tb.Logf("job %d log (tail):\n%s", job.ID, tail)
				}
			}
			if response.NextPage == 0 {
				return
			}
			page = response.NextPage
		}
	})
}

func redactJobLog(target Target, log string, secrets ...map[string]string) string {
	// Redact before truncation so a credential straddling the tail boundary
	// cannot survive as a partial value.
	forms := targetSecretForms(target)
	for _, set := range secrets {
		for _, secret := range set {
			forms = append(forms, SecretForms(target.CredentialUsername, secret)...)
			// Runner timestamps and masking can split an armored private key
			// across lines, preventing a whole-value match. Its payload lines
			// remain secret even when the public armor headers do not.
			for line := range strings.SplitSeq(strings.TrimSpace(secret), "\n") {
				line = strings.TrimSpace(line)
				if len(line) >= 16 && !strings.HasPrefix(line, "-----") {
					forms = append(forms, SecretForms(target.CredentialUsername, line)...)
				}
			}
		}
	}
	slices.SortFunc(forms, func(a, b string) int {
		return cmp.Or(cmp.Compare(len(b), len(a)), strings.Compare(a, b))
	})
	replacements := make([]string, 0, len(forms)*2)
	for _, form := range slices.Compact(forms) {
		replacements = append(replacements, form, redactedTargetSecret)
	}
	return strings.NewReplacer(replacements...).Replace(log)
}

func retainJobLog(root, repo string, job RunJob, log string) (string, error) {
	if len(log) > maxJobLog {
		return "", errors.New("job log exceeds evidence limit")
	}
	if err := jobLogEvidenceDestination(root); err != nil {
		return "", err
	}
	// The repository digest avoids name collisions across owners and
	// never places forge-controlled names in a local path.
	identity := sha256.Sum256([]byte(repo))
	name := fmt.Sprintf("job-%x-run-%d-job-%d-attempt-%d.log", identity[:8], job.RunID, job.ID, job.Attempt)
	if err := writeSnapshotEvidence(root, name, []byte(log)); err != nil {
		return "", err
	}
	return filepath.Join(root, name), nil
}

func jobLogEvidenceDestination(root string) error {
	if !filepath.IsAbs(root) {
		return errors.New("job evidence requires an absolute external directory")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	// Find the enclosing checkout even when conformance runs in a subpackage.
	checkout := cwd
	for directory := cwd; ; directory = filepath.Dir(directory) {
		if _, err := os.Stat(filepath.Join(directory, ".git")); err == nil {
			checkout = directory
			break
		}
		if filepath.Dir(directory) == directory {
			break
		}
	}
	for _, repository := range []string{checkout, os.Getenv("FORGE_CI_LIVE_CONSUMER")} {
		if repository == "" {
			continue
		}
		resolved, err := filepath.EvalSymlinks(repository)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(resolved, filepath.Clean(root))
		if err != nil {
			return err
		}
		if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("job evidence must be outside engine and consumer checkouts")
		}
	}
	// The writer rejects symlinks in every destination component, including
	// ancestors of not-yet-created directories.
	return nil
}
