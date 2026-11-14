// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package githubconsumer_test

// The SARIF fixture and its oracles carry no build tag, so an ordinary test
// run judges them; the hosted scenario that uses them is in sarif_api_test.go.

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	sdk "github.com/google/go-github/v91/github"
)

// The one finding the fixture report carries, and where Code Scanning must
// file it.
const (
	sarifFixtureTool     = "forge-ci-live-sarif-fixture"
	sarifFixtureRule     = "FCI-LIVE-0001"
	sarifFixtureFile     = "sarif-fixture.txt"
	sarifAcceptedSubject = "forge-ci-live-accepted"
	sarifDeniedSubject   = "forge-ci-live-denied"
)

const sarifFixtureReport = `{
  "$schema": "https://json.schemastore.org/sarif-2.1.0.json",
  "version": "2.1.0",
  "runs": [
    {
      "tool": {
        "driver": {
          "name": "` + sarifFixtureTool + `",
          "rules": [
            {"id": "` + sarifFixtureRule + `", "shortDescription": {"text": "Live SARIF fixture finding"}}
          ]
        }
      },
      "results": [
        {
          "ruleId": "` + sarifFixtureRule + `",
          "level": "warning",
          "message": {"text": "Live SARIF fixture finding"},
          "locations": [
            {"physicalLocation": {"artifactLocation": {"uri": "` + sarifFixtureFile + `"}, "region": {"startLine": 1}}}
          ]
        }
      ]
    }
  ]
}
`

// sarifWant is the analysis Code Scanning must hold after the accepted upload.
type sarifWant struct {
	Commit, Ref, Category, Tool string
	Results                     int
}

// sarifAnalysisGap judges the analyses Code Scanning lists for the ref: it
// must hold exactly the accepted upload, processed without error, for the
// exact commit, ref, category and tool, and nothing else. An analysis in any
// other category, the denied upload's included, fails.
func sarifAnalysisGap(analyses []*sdk.ScanningAnalysis, want sarifWant) error {
	if len(analyses) != 1 {
		categories := make([]string, 0, len(analyses))
		for _, analysis := range analyses {
			categories = append(categories, analysis.GetCategory())
		}

		return fmt.Errorf("code scanning lists %d analyses %v, want exactly the %s upload", len(analyses), categories, want.Category)
	}

	got := analyses[0]

	switch {
	case got.GetError() != "":
		return fmt.Errorf("analysis %d was processed with an error: %s", got.GetID(), got.GetError())
	case got.GetCommitSHA() != want.Commit:
		return fmt.Errorf("analysis %d is for commit %s, want %s", got.GetID(), got.GetCommitSHA(), want.Commit)
	case got.GetRef() != want.Ref:
		return fmt.Errorf("analysis %d is for ref %s, want %s", got.GetID(), got.GetRef(), want.Ref)
	case got.GetCategory() != want.Category:
		return fmt.Errorf("analysis %d has category %q, want %q", got.GetID(), got.GetCategory(), want.Category)
	case got.GetTool().GetName() != want.Tool:
		return fmt.Errorf("analysis %d is by tool %q, want %q", got.GetID(), got.GetTool().GetName(), want.Tool)
	case got.GetResultsCount() != want.Results:
		return fmt.Errorf("analysis %d holds %d results, want %d", got.GetID(), got.GetResultsCount(), want.Results)
	}

	return nil
}

// sarifAlertGap judges the open alerts of the fixture tool on the ref: exactly
// the fixture's one finding, by its rule, in its file, at the commit.
func sarifAlertGap(alerts []*sdk.Alert, commit string) error {
	if len(alerts) != 1 {
		return fmt.Errorf("code scanning lists %d open alerts of %s, want 1", len(alerts), sarifFixtureTool)
	}

	alert, instance := alerts[0], alerts[0].GetMostRecentInstance()

	switch {
	case alert.GetRule().GetID() != sarifFixtureRule:
		return fmt.Errorf("alert %d is for rule %q, want %q", alert.GetNumber(), alert.GetRule().GetID(), sarifFixtureRule)
	case instance.GetLocation().GetPath() != sarifFixtureFile:
		return fmt.Errorf("alert %d is in %q, want %q", alert.GetNumber(), instance.GetLocation().GetPath(), sarifFixtureFile)
	case instance.GetCommitSHA() != commit:
		return fmt.Errorf("alert %d is at commit %s, want %s", alert.GetNumber(), instance.GetCommitSHA(), commit)
	}

	return nil
}

// codeScanningUnavailable names why GitHub refuses Code Scanning to this
// repository or token as such: the plan or the repository's settings do not
// offer it, or the session credential lacks the permission. Any other
// refusal, a rejected credential included, is not one of these and fails the
// scenario. The phrases are GitHub's own, read from the product's error.
func codeScanningUnavailable(err error) (string, bool) {
	if !errors.Is(err, errs.ErrPermissionDenied) {
		return "", false
	}

	text := strings.ToLower(err.Error())

	for _, refusal := range [][2]string{
		{"advanced security must be enabled", "the organization's plan offers no Code Scanning for this repository (GitHub Advanced Security is not enabled)"},
		{"code security must be enabled", "the organization's plan offers no Code Scanning for this repository (GitHub Code Security is not enabled)"},
		{"code scanning is not enabled", "Code Scanning is not enabled for this repository"},
		{"resource not accessible by", "the session credential lacks the Code scanning alerts permission"},
	} {
		if strings.Contains(text, refusal[0]) {
			return refusal[1], true
		}
	}

	return "", false
}

func TestSARIFAnalysisGap(t *testing.T) {
	want := sarifWant{Commit: "c0ffee", Ref: "refs/heads/main", Category: sarifAcceptedSubject, Tool: sarifFixtureTool, Results: 1}
	accepted := func() *sdk.ScanningAnalysis {
		return &sdk.ScanningAnalysis{
			ID: new(int64(7)), CommitSHA: new("c0ffee"), Ref: new("refs/heads/main"), Category: new(sarifAcceptedSubject),
			Tool: &sdk.Tool{Name: new(sarifFixtureTool)}, ResultsCount: new(1), Error: new(""),
		}
	}
	altered := func(alter func(*sdk.ScanningAnalysis)) *sdk.ScanningAnalysis {
		analysis := accepted()
		alter(analysis)

		return analysis
	}

	if err := sarifAnalysisGap([]*sdk.ScanningAnalysis{accepted()}, want); err != nil {
		t.Fatalf("the accepted analysis is refused: %v", err)
	}

	for name, analyses := range map[string][]*sdk.ScanningAnalysis{
		"none listed":      nil,
		"denied also held": {accepted(), altered(func(a *sdk.ScanningAnalysis) { a.Category = new(sarifDeniedSubject) })},
		"other commit":     {altered(func(a *sdk.ScanningAnalysis) { a.CommitSHA = new("decaf") })},
		"other ref":        {altered(func(a *sdk.ScanningAnalysis) { a.Ref = new("refs/heads/other") })},
		"other category":   {altered(func(a *sdk.ScanningAnalysis) { a.Category = new(sarifDeniedSubject) })},
		"other tool":       {altered(func(a *sdk.ScanningAnalysis) { a.Tool = &sdk.Tool{Name: new("other")} })},
		"no results":       {altered(func(a *sdk.ScanningAnalysis) { a.ResultsCount = new(0) })},
		"processing error": {altered(func(a *sdk.ScanningAnalysis) { a.Error = new("invalid SARIF") })},
	} {
		t.Run(name, func(t *testing.T) {
			if err := sarifAnalysisGap(analyses, want); err == nil {
				t.Fatal("a wrong readback is accepted")
			}
		})
	}
}

func TestSARIFAlertGap(t *testing.T) {
	found := func() *sdk.Alert {
		return &sdk.Alert{Number: new(1), Rule: &sdk.Rule{ID: new(sarifFixtureRule)}, MostRecentInstance: &sdk.MostRecentInstance{
			CommitSHA: new("c0ffee"), Location: &sdk.Location{Path: new(sarifFixtureFile)},
		}}
	}
	altered := func(alter func(*sdk.Alert)) *sdk.Alert {
		alert := found()
		alter(alert)

		return alert
	}

	if err := sarifAlertGap([]*sdk.Alert{found()}, "c0ffee"); err != nil {
		t.Fatalf("the fixture's alert is refused: %v", err)
	}

	for name, alerts := range map[string][]*sdk.Alert{
		"none listed":  nil,
		"two listed":   {found(), found()},
		"other rule":   {altered(func(a *sdk.Alert) { a.Rule = &sdk.Rule{ID: new("OTHER")} })},
		"other file":   {altered(func(a *sdk.Alert) { a.MostRecentInstance.Location.Path = new("other.txt") })},
		"other commit": {altered(func(a *sdk.Alert) { a.MostRecentInstance.CommitSHA = new("decaf") })},
	} {
		t.Run(name, func(t *testing.T) {
			if err := sarifAlertGap(alerts, "c0ffee"); err == nil {
				t.Fatal("a wrong alert readback is accepted")
			}
		})
	}
}

// The fixture report is what the product uploads, so it must be the SARIF the
// oracle's constants describe.
func TestSARIFFixtureReportNamesItsOracle(t *testing.T) {
	for _, needle := range []string{`"version": "2.1.0"`, `"name": "` + sarifFixtureTool + `"`, `"ruleId": "` + sarifFixtureRule + `"`, `"uri": "` + sarifFixtureFile + `"`} {
		if !strings.Contains(sarifFixtureReport, needle) {
			t.Errorf("the fixture report lacks %s", needle)
		}
	}
}

func TestCodeScanningUnavailableIsNarrow(t *testing.T) {
	denied := func(body string) error {
		return fmt.Errorf("upload sarif: HTTP 403: %s: %w", body, errs.ErrPermissionDenied)
	}

	for name, err := range map[string]error{
		"advanced security": denied(`{"message":"Advanced Security must be enabled for this repository to use code scanning."}`),
		"code security":     denied(`{"message":"Code Security must be enabled for this repository to use code scanning."}`),
		"not enabled":       denied(`{"message":"Code scanning is not enabled for this repository."}`),
		"token permission":  denied(`{"message":"Resource not accessible by personal access token"}`),
	} {
		t.Run(name, func(t *testing.T) {
			if reason, unavailable := codeScanningUnavailable(err); !unavailable || reason == "" {
				t.Fatal("a plan or permission refusal is not named as unavailable")
			}
		})
	}

	for name, err := range map[string]error{
		"accepted":            nil,
		"rejected credential": fmt.Errorf("upload sarif: HTTP 401: %s: %w", `{"message":"Bad credentials"}`, errs.ErrPermissionDenied),
		"other forbidden":     denied(`{"message":"Forbidden"}`),
		"invalid request":     fmt.Errorf("upload sarif: HTTP 422: Advanced Security must be enabled: %w", errs.ErrValidation),
		"service unavailable": fmt.Errorf("upload sarif: HTTP 502: %w", errs.ErrDependencyUnavailable),
	} {
		t.Run(name, func(t *testing.T) {
			if reason, unavailable := codeScanningUnavailable(err); unavailable {
				t.Fatalf("a product or request failure would be reported not covered: %s", reason)
			}
		})
	}
}
