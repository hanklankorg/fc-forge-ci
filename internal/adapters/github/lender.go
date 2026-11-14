// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	gogithub "github.com/google/go-github/v76/github"

	"github.com/diggsweden/forge-ci/internal/adapters/httpretry"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/lender"
	"github.com/diggsweden/forge-ci/internal/runcontext"
)

// lenderWorkflowFile is the workflow a lender repository holds, by
// convention: examples/runner-lender/github-actions/lend-runner.yml copied with the
// installation's literals.
const lenderWorkflowFile = "lend-runner.yml"

// Lender starts an ephemeral runner by dispatching the lender repository's
// workflow on GitHub. The job that dispatches it runs on another forge, so
// nothing here reads the runner's environment: the API root is GitHub's
// unless the operator names an enterprise server, and the token is the one
// the operator supplied for exactly this purpose. $GITHUB_API_URL on a
// Forgejo runner is Forgejo's own API and must not be consulted.
type Lender struct {
	// Owner and Repository name the lender repository.
	Owner      string
	Repository string
	// Ref is the branch the workflow is dispatched on.
	Ref string
	// APIBase overrides https://api.github.com for an enterprise server.
	APIBase string
	// Token may start workflows in the lender repository and nothing else.
	Token runcontext.Credential
	// HTTPClient overrides the retry-wrapped default; tests inject one.
	HTTPClient *http.Client
}

// Name identifies the lender in summaries and errors.
func (l *Lender) Name() string {
	return "github:" + l.Owner + "/" + l.Repository
}

// Request dispatches lend-runner.yml with the architecture and the wait as
// its inputs. It returns once GitHub has accepted the dispatch; the runner
// the workflow starts registers with the orchestrating forge on its own.
func (l *Lender) Request(ctx context.Context, req lender.Request) error {
	base := l.apiBase()

	token := l.Token.For(base)
	if token == "" {
		return fmt.Errorf("no token may be sent to %s: pass the lender's token with --token-file, a runner-issued token stays with its own forge: %w", base, errs.ErrUsage)
	}

	client, err := l.client(base, token)
	if err != nil {
		return err
	}

	event := gogithub.CreateWorkflowDispatchEventRequest{
		Ref: l.Ref,
		Inputs: map[string]any{
			"arch":        string(req.Arch),
			"ttl-minutes": strconv.Itoa(int(req.TTL / time.Minute)),
		},
	}

	_, err = client.Actions.CreateWorkflowDispatchEventByFileName(ctx, l.Owner, l.Repository, lenderWorkflowFile, event)

	return classifyDispatchError(err, l.Owner+"/"+l.Repository, l.Ref)
}

func (l *Lender) apiBase() string {
	if base := strings.TrimRight(strings.TrimSpace(l.APIBase), "/"); base != "" {
		return base
	}

	return defaultAPIBase
}

func (l *Lender) client(base, token string) (*gogithub.Client, error) {
	httpClient := l.HTTPClient
	if httpClient == nil {
		httpClient = defaultHTTPClient()
	}

	client := gogithub.NewClient(httpretry.SameAuthority(httpClient, "GitHub")).WithAuthToken(token)

	if base != defaultAPIBase {
		root, err := url.Parse(base + "/")
		if err != nil {
			return nil, fmt.Errorf("configure GitHub API base %q: %w", base, err)
		}

		client.BaseURL = root
		client.UploadURL = githubUploadURL(root)
	}

	return client, nil
}

// classifyDispatchError names the one answer the generic classifier reads
// wrong for a dispatch: GitHub answers 404 both for a repository the token
// cannot see and for a workflow file or branch that does not exist.
func classifyDispatchError(err error, repository, ref string) error {
	if err == nil {
		return nil
	}

	var resp *gogithub.ErrorResponse
	if errors.As(err, &resp) && resp.Response != nil && resp.Response.StatusCode == http.StatusNotFound {
		return fmt.Errorf("GitHub finds no %s on branch %s of %s, or the token cannot see that repository: %w", lenderWorkflowFile, ref, repository, errs.ErrMissingInput)
	}

	return classifyGitHubError(err)
}
