// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/diggsweden/forge-ci/internal/pathsafe"
	"github.com/google/go-github/v91/github"
	"gopkg.in/yaml.v3"
)

const migrationReportArtifact = "migration-java-surefire"
const migrationZIPLimit = 16 << 20

func migrationEvidenceDestination(root string, repositories ...string) error {
	if !filepath.IsAbs(root) {
		return errors.New("Java evidence requires an absolute external directory")
	}
	dir, err := pathsafe.OpenRoot(root)
	if err != nil {
		return err
	}
	if err := dir.Close(); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	for _, repository := range repositories {
		repo, err := filepath.EvalSymlinks(repository)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(repo, resolved)
		if err != nil {
			return err
		}
		if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("Java evidence directory must be outside source and scratch repositories")
		}
	}
	return nil
}

func migrationJavaJob(original string) (string, error) {
	var workflow struct {
		Jobs map[string]struct {
			Name string `yaml:"name"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(original), &workflow); err != nil {
		return "", err
	}
	if len(workflow.Jobs) != 1 {
		return "", errors.New("Java baseline expects exactly one original test job")
	}
	for id, job := range workflow.Jobs {
		name := job.Name
		if name == "" {
			name = id
		}
		if strings.Contains(name, "${{") {
			return "", errors.New("dynamic Java job names require an explicit oracle")
		}
		return "original-java21-unit-check / " + name, nil
	}
	return "", errors.New("missing Java test job")
}

type migrationJUnitSummary struct{ Reports, Tests, Skipped, Failures, Errors int }

type migrationSuite struct {
	XMLName  xml.Name
	Tests    string `xml:"tests,attr"`
	Skipped  string `xml:"skipped,attr"`
	Failures string `xml:"failures,attr"`
	Errors   string `xml:"errors,attr"`
	Cases    []struct {
		Skipped  []struct{} `xml:"skipped"`
		Failures []struct{} `xml:"failure"`
		Errors   []struct{} `xml:"error"`
	} `xml:"testcase"`
	Suites []migrationSuite `xml:"testsuite"`
}

func migrationJUnitReports(body []byte) (migrationJUnitSummary, error) {
	var result migrationJUnitSummary
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil || len(body) > migrationZIPLimit {
		return result, errors.New("invalid or oversized report ZIP")
	}
	if len(archive.File) > 2048 {
		return result, errors.New("too many report ZIP entries")
	}
	tree, err := snapshotArtifactZIPTree(body, 32<<20)
	if err != nil {
		return result, err
	}
	for name, content := range tree {
		if !strings.HasPrefix(path.Base(name), "TEST-") || !strings.HasSuffix(name, ".xml") {
			continue
		}
		if len(content) > 2<<20 {
			return result, errors.New("JUnit report exceeds file quota")
		}
		decoder := xml.NewDecoder(bytes.NewReader(content))
		depth, tokens := 0, 0
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return result, errors.New("malformed JUnit XML")
			}
			tokens++
			switch token.(type) {
			case xml.Directive:
				return result, errors.New("JUnit directives are forbidden")
			case xml.StartElement:
				depth++
			case xml.EndElement:
				depth--
			}
			if depth > 64 || tokens > 200000 {
				return result, errors.New("JUnit XML exceeds structural quota")
			}
		}
		var suite migrationSuite
		decoder = xml.NewDecoder(bytes.NewReader(content))
		if err := decoder.Decode(&suite); err != nil {
			return result, errors.New("malformed JUnit suite")
		}
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return result, errors.New("trailing JUnit XML")
			}
			if text, ok := token.(xml.CharData); !ok || strings.TrimSpace(string(text)) != "" {
				return result, errors.New("trailing JUnit content")
			}
		}
		if suite.XMLName.Local != "testsuite" && suite.XMLName.Local != "testsuites" {
			return result, errors.New("missing JUnit suite root")
		}
		counts, err := migrationSuiteCounts(suite)
		if err != nil {
			return result, err
		}
		result.Reports++
		result.Tests += counts.Tests
		result.Skipped += counts.Skipped
		result.Failures += counts.Failures
		result.Errors += counts.Errors
	}
	if result.Reports == 0 || result.Tests-result.Skipped <= 0 {
		return result, errors.New("Surefire evidence has no executed tests")
	}
	if result.Failures != 0 || result.Errors != 0 {
		return result, errors.New("Surefire reports contain failed or errored tests")
	}
	return result, nil
}

func migrationSuiteCounts(suite migrationSuite) (migrationJUnitSummary, error) {
	var result migrationJUnitSummary
	if len(suite.Suites) != 0 {
		if len(suite.Cases) != 0 {
			return result, errors.New("mixed nested suites and test cases")
		}
		for _, child := range suite.Suites {
			counts, err := migrationSuiteCounts(child)
			if err != nil {
				return result, err
			}
			result.Tests += counts.Tests
			result.Skipped += counts.Skipped
			result.Failures += counts.Failures
			result.Errors += counts.Errors
		}
	} else {
		result.Tests = len(suite.Cases)
		for _, test := range suite.Cases {
			if len(test.Skipped)+len(test.Failures)+len(test.Errors) > 1 {
				return result, errors.New("conflicting JUnit test outcomes")
			}
			result.Skipped += len(test.Skipped)
			result.Failures += len(test.Failures)
			result.Errors += len(test.Errors)
		}
	}
	for _, field := range []struct {
		text     string
		actual   int
		required bool
	}{{suite.Tests, result.Tests, suite.XMLName.Local == "testsuite"}, {suite.Skipped, result.Skipped, false}, {suite.Failures, result.Failures, false}, {suite.Errors, result.Errors, false}} {
		if field.text == "" && !field.required {
			if field.actual != 0 && suite.XMLName.Local == "testsuite" {
				return result, errors.New("missing JUnit outcome count")
			}
			continue
		}
		n, err := strconv.Atoi(field.text)
		if err != nil || n < 0 || n != field.actual {
			return result, errors.New("JUnit counts disagree with test cases")
		}
	}
	return result, nil
}

// The API locates the artifact for this run. Its signed download uses a fresh,
// unauthenticated HTTP client, never the controller's GitHub credential client.
func (trial *GitHubTrial) migrationJavaEvidence(ctx context.Context, repo string, run *github.WorkflowRun, expectedJob, evidenceRoot string) (migrationJUnitSummary, error) {
	var empty migrationJUnitSummary
	if evidenceRoot == "" {
		return empty, errors.New("Java baseline requires a configured evidence directory")
	}
	jobs, _, err := trial.api.Actions.ListWorkflowJobs(ctx, trial.org, repo, run.GetID(), &github.ListWorkflowJobsOptions{Filter: "latest", PerPage: 100})
	if err != nil || jobs.GetTotalCount() != len(jobs.Jobs) {
		return empty, errors.New("cannot completely list Java jobs")
	}
	var matched *github.WorkflowJob
	for _, job := range jobs.Jobs {
		if job.GetName() == expectedJob {
			if matched != nil {
				return empty, errors.New("duplicate expected Java job")
			}
			matched = job
		}
	}
	if matched == nil || matched.GetStatus() != "completed" || matched.GetRunnerID() <= 0 || matched.GetStartedAt().IsZero() || matched.GetCompletedAt().IsZero() || !matched.GetCompletedAt().After(matched.GetStartedAt().Time) {
		return empty, errors.New("expected Java test job did not execute successfully")
	}
	var jobErr error
	if matched.GetConclusion() != "success" || run.GetStatus() != "completed" || run.GetConclusion() != "success" {
		// Still retain reports from an executed failing job before cleanup.
		jobErr = errors.New("Java test job or enclosing run did not succeed")
	}
	list, _, err := trial.api.Actions.ListWorkflowRunArtifacts(ctx, trial.org, repo, run.GetID(), &github.ListOptions{PerPage: 100})
	if err != nil || list.GetTotalCount() != int64(len(list.Artifacts)) {
		return empty, errors.New("cannot completely list Java report artifacts")
	}
	var selected *github.Artifact
	for _, artifact := range list.Artifacts {
		if artifact.GetName() == migrationReportArtifact {
			if selected != nil {
				return empty, errors.New("duplicate Surefire artifact")
			}
			selected = artifact
		}
	}
	if selected == nil || selected.GetExpired() || selected.GetSizeInBytes() <= 0 || selected.GetSizeInBytes() > migrationZIPLimit {
		return empty, errors.New("missing, expired or oversized Surefire artifact")
	}
	location, _, err := trial.api.Actions.DownloadArtifact(ctx, trial.org, repo, selected.GetID(), 0)
	if err != nil || location == nil {
		return empty, errors.New("cannot locate Surefire artifact")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
	if err != nil {
		return empty, errors.New("invalid artifact location")
	}
	response, err := (&http.Client{Timeout: 2 * time.Minute}).Do(request)
	if err != nil {
		return empty, errors.New("cannot download Surefire artifact")
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, migrationZIPLimit+1))
	if err != nil || response.StatusCode != http.StatusOK || len(body) > migrationZIPLimit {
		return empty, errors.New("incomplete or oversized Surefire download")
	}
	filename := fmt.Sprintf("github-java-run-%d-surefire.zip", run.GetID())
	if err := writeSnapshotEvidence(evidenceRoot, filename, body); err != nil {
		return empty, fmt.Errorf("retain Surefire evidence: %w", err)
	}
	summary, err := migrationJUnitReports(body)
	receipt, marshalErr := json.MarshalIndent(struct {
		RunID         int64                 `json:"run_id"`
		HeadSHA       string                `json:"head_sha"`
		RunConclusion string                `json:"run_conclusion"`
		Job           *github.WorkflowJob   `json:"job"`
		ArtifactID    int64                 `json:"artifact_id"`
		ZIPHash       string                `json:"zip_sha256"`
		JUnit         migrationJUnitSummary `json:"junit"`
		Accepted      bool                  `json:"accepted"`
		Coverage      string                `json:"coverage"`
	}{run.GetID(), run.GetHeadSHA(), run.GetConclusion(), matched, selected.GetID(), fmt.Sprintf("%x", sha256.Sum256(body)), summary, jobErr == nil && err == nil, "original Java21 baseline; not full-engine coverage"}, "", "  ")
	if marshalErr != nil {
		return summary, marshalErr
	}
	if saveErr := writeSnapshotEvidence(evidenceRoot, strings.TrimSuffix(filename, ".zip")+".json", receipt); saveErr != nil {
		return summary, fmt.Errorf("retain Java execution receipt: %w", saveErr)
	}
	trial.tb.Logf("Java baseline evidence: job=%s job-id=%d runner=%d status=%s conclusion=%s reports=%d tests=%d skipped=%d failures=%d errors=%d evidence=%s; baseline only, not full-engine coverage", matched.GetName(), matched.GetID(), matched.GetRunnerID(), matched.GetStatus(), matched.GetConclusion(), summary.Reports, summary.Tests, summary.Skipped, summary.Failures, summary.Errors, filepath.Join(evidenceRoot, filename))
	return summary, errors.Join(jobErr, err)
}
