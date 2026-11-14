// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package githubconsumer_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"codefloe.com/itiquette/forge-lab-contract/githubclient"
	"codefloe.com/itiquette/forge-lab-contract/githubcontext"
	githubadapter "github.com/diggsweden/forge-ci/internal/adapters/github"
	appsecurity "github.com/diggsweden/forge-ci/internal/app/security"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/retry"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
	sdk "github.com/google/go-github/v91/github"
)

// Positive SARIF ingestion on hosted GitHub, which no lab forge offers: the
// product's own upload (`security report upload-sarif`'s use case over the
// GitHub adapter, not an upload action) submits one tiny report for an exact
// repository, commit, ref and category; Code Scanning must then list exactly
// that analysis and its one alert. A second upload with a credential GitHub
// rejects must be denied, leak nothing and leave no analysis. No runner,
// engine push or scanner is involved.
//
// Code Scanning is free on a public repository and needs GitHub Advanced
// Security on a private one. Where the plan, the repository or the session
// credential's permissions prohibit it, the case is reported not covered with
// that reason, never passed.
func TestGitHubSecurity_SARIFUploadAPI(t *testing.T) {
	livetest.RegisterLiveScenario(t)

	path := os.Getenv(livetest.GitHubContextEnv)
	if path == "" {
		t.Skip("run inside Forge Lab github run --fixtures none")
	}

	id, err := strconv.ParseInt(os.Getenv(organizationEnv), 10, 64)
	if err != nil || id <= 0 {
		t.Fatal("explicit GitHub organization ID is required")
	}

	document, err := githubcontext.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	client, err := githubclient.New(livetest.Context(t), document, id)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(client.Close)
	api, owner := client.API(), document.Organization.Login

	for _, visibility := range livetest.ConsumerVisibilities(t) {
		t.Run(visibility.Name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(livetest.Context(t), 5*time.Minute)
			defer cancel()

			name := document.Prefix + "sarif-api-" + visibility.Name + "-" + document.Session[:8]

			repo, err := client.CreateRepository(ctx, name, visibility.Private)
			if repo != nil && repo.GetID() > 0 {
				repoID := repo.GetID()
				t.Logf("disposable repository: %s/%s id=%d", owner, name, repoID)
				t.Cleanup(func() {
					cleanup, stop := context.WithTimeout(context.WithoutCancel(t.Context()), time.Minute)
					defer stop()

					if err := client.DeleteRepository(cleanup, name, repoID); err != nil {
						t.Errorf("repository cleanup: %v", err)

						return
					}

					if _, response, err := api.Repositories.GetByID(cleanup, repoID); err == nil || response == nil || response.StatusCode != http.StatusNotFound {
						t.Error("repository deletion not confirmed")
					}
				})
			}

			if err != nil {
				t.Fatal(err)
			}

			seed, _, err := api.Repositories.CreateFile(ctx, owner, name, sarifFixtureFile, &sdk.RepositoryContentFileOptions{Message: new("test: seed disposable SARIF fixture"), Content: []byte("fixture\n")})
			if err != nil {
				t.Fatal(err)
			}

			current, _, err := api.Repositories.GetByID(ctx, repo.GetID())
			if err != nil {
				t.Fatalf("read created repository identity: %v", err)
			}

			if current.GetName() != name || current.GetOwner().GetID() != id || current.Private == nil || current.GetPrivate() != visibility.Private || current.GetDefaultBranch() == "" {
				t.Fatal("scratch repository identity changed")
			}

			report := filepath.Join(t.TempDir(), "fixture.sarif")
			if err := os.WriteFile(report, []byte(sarifFixtureReport), 0o600); err != nil {
				t.Fatal(err)
			}

			want := sarifWant{Commit: seed.GetSHA(), Ref: "refs/heads/" + current.GetDefaultBranch(), Category: sarifAcceptedSubject, Tool: sarifFixtureTool, Results: 1}

			// The product's upload: the use case and adapter the CLI command runs.
			upload := func(token, category string) (string, error) {
				var notes bytes.Buffer

				err := appsecurity.UploadSARIF(ctx, &githubadapter.Provider{Env: func(string) string { return "" }}, io.Discard,
					output.NewAnnotator(&notes, output.FormatText), appsecurity.UploadSARIFInput{
						SARIFFile: report, Token: token, Repository: owner + "/" + name, SHA: want.Commit, Ref: want.Ref, Category: category,
					})

				text := notes.String()
				if err != nil {
					text += err.Error()
				}

				return text, err
			}

			if said, err := upload(document.Token, want.Category); err != nil {
				if strings.Contains(said, document.Token) {
					t.Fatal("the refused upload's diagnostics carry the session credential")
				}

				if reason, unavailable := codeScanningUnavailable(err); unavailable {
					livetest.NotCoveredf(t, "SARIF ingestion on a %s repository of %s: %s", visibility.Name, owner, reason)
				}

				t.Fatalf("the product's SARIF upload was refused: %v", err)
			}

			// The controlled denial: a credential GitHub does not know. It is
			// random, so nothing it could leak is a secret.
			random := make([]byte, 20)
			if _, err := rand.Read(random); err != nil {
				t.Fatal(err)
			}

			rejected := "forge-ci-live-rejected-" + hex.EncodeToString(random)

			said, err := upload(rejected, sarifDeniedSubject)
			if !errors.Is(err, errs.ErrPermissionDenied) {
				t.Fatalf("an upload with a rejected credential was not denied: %v", err)
			}

			if strings.Contains(said, rejected) {
				t.Fatal("the denied upload's diagnostics carry its credential")
			}

			// Processing is asynchronous: GitHub answers the upload before the
			// analysis is listed, and lists none (404) until it is.
			if err := retry.Run(ctx, nil, 36, 5*time.Second, func() error {
				analyses, response, err := api.CodeScanning.ListAnalysesForRepo(ctx, owner, name, &sdk.AnalysesListOptions{Ref: &want.Ref})
				if err != nil && (response == nil || response.StatusCode != http.StatusNotFound) {
					return retry.Permanent(fmt.Errorf("read the analyses back: %w", err))
				}

				if gap := sarifAnalysisGap(analyses, want); gap != nil {
					return gap
				}

				alerts, _, err := api.CodeScanning.ListAlertsForRepo(ctx, owner, name, &sdk.AlertListOptions{State: "open", Ref: want.Ref, ToolName: sarifFixtureTool})
				if err != nil {
					return retry.Permanent(fmt.Errorf("read the alerts back: %w", err))
				}

				return sarifAlertGap(alerts, want.Commit)
			}, retry.WithBackoff(retry.Constant)); err != nil {
				t.Fatalf("Code Scanning readback: %v", err)
			}

			t.Logf("Code Scanning holds the %s analysis of %s@%s on %s with its alert; the rejected upload left none", want.Category, name, want.Commit, want.Ref)
		})
	}
}
