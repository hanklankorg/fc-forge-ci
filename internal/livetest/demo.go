// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// Demo mode runs a consumer trial to be shown rather than judged: its
// repositories are kept, under their real names, in the lab account demoAccount
// and nowhere else. A trial is the opposite in every respect that matters for
// safety, so the two never share an account:
//
//   - a trial resets and deletes its fixed fci- repositories; demo mode never
//     deletes anything and refuses a repository that already exists;
//   - a trial runs under labAccount; demo mode only under demoAccount, and a
//     trial refuses a demoAccount contract, so neither reaches the other's work;
//   - a trial pushes one parentless commit; demo mode pushes the consumer's
//     history (Consumer.WithHistory), so its release notes have something to say;
//   - when the run ends, demo mode deletes the repository secrets it set. The
//     account's token is retired by revoking the demo contract
//     (`just targets-revoke` in Forge Lab).
//
// The mode belongs to the whole run, so it is the environment's, set by
// `just demo <forge> <name> <checkout>`, not a per-scenario setting.
const (
	demoEnv     = "FORGE_CI_LIVE_DEMO"
	demoAccount = "demo"

	// demoRepoPrefix starts every demo repository's name on every forge:
	// demo/demo-<name> on the lab, <session prefix>demo-<name> on GitHub. It
	// marks a demo where the account cannot (GitHub), and it is the namespace
	// the lab client's deletion is bound to.
	demoRepoPrefix = "demo-"

	// demoLayerSuffix names the repository that serves a demo's layer, so
	// each demo pins a layer nothing else moves; demoHelperInfix starts the
	// names of a GitLab release's helper projects.
	demoLayerSuffix = "-forge-ci"
	demoHelperInfix = "-release-"

	// demoBaseMessage is the commit demo mode adds on top of the consumer's
	// history: the rendered pins, as a person would commit them.
	demoBaseMessage = "ci: use the lab's Forge CI layer\n\n" + consumerSignOff
)

// demoRepoName is a demo's repository name: short, lowercase, and leaving room
// for demoLayerSuffix within the forges' path limits.
var demoRepoName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,38}[a-z0-9]$`)

// DemoName returns the demo's repository name, or "" outside demo mode.
func DemoName() string { return os.Getenv(demoEnv) }

// DemoRepo names a demo's repository: demo-<name>.
func DemoRepo(name string) string { return demoRepoPrefix + name }

// DemoLayerRepo names the repository that serves a demo's layer.
func DemoLayerRepo(name string) string { return DemoRepo(name) + demoLayerSuffix }

// DemoHelperRepo names one of a GitLab demo's release helper projects.
func DemoHelperRepo(name, role string) string { return DemoRepo(name) + demoHelperInfix + role }

// validDemoName reports whether name can name a demo: a repository name that
// leaves room for the helpers' suffixes and cannot be mistaken for one.
func validDemoName(name string) bool {
	return demoRepoName.MatchString(name) && !strings.HasSuffix(name, demoLayerSuffix) && !strings.Contains(name, demoHelperInfix)
}

// liveAccount is the lab account whose contract this run reads.
func liveAccount() (string, error) {
	name := DemoName()
	if name == "" {
		return labAccount, nil
	}

	if !validDemoName(name) {
		return "", fmt.Errorf("%s=%q is not a demo name (lowercase letters, digits and inner hyphens, 2 to 40 characters, not ending %s or holding %s): %w",
			demoEnv, name, demoLayerSuffix, demoHelperInfix, errs.ErrValidation)
	}

	return demoAccount, nil
}

// requireAccountMode keeps each account's work out of the other's reach: demo
// mode acts only as demoAccount, and every other run never does.
func requireAccountMode(owner, demo string) error {
	switch {
	case demo != "" && owner != demoAccount:
		return fmt.Errorf("demo mode acts only as lab account %q, and this contract is %q's: %w", demoAccount, owner, errs.ErrValidation)
	case demo == "" && owner == demoAccount:
		return fmt.Errorf("lab account %q holds kept demos, which a trial would reset; use the %q contract: %w",
			demoAccount, labAccount, errs.ErrValidation)
	}

	return nil
}

// runNamespace is the name prefix a run may write under: the trials' prefix,
// or demo- in the demo account. Every helper that pushes to, protects or
// configures a repository holds the repository to it.
func runNamespace(target Target) string {
	if target.Owner == demoAccount {
		return demoRepoPrefix
	}

	return ResourcePrefix
}

// inRunNamespace reports whether repo is a repository of the run's namespace.
func inRunNamespace(target Target, repo string) bool {
	namespace := runNamespace(target)

	return strings.HasPrefix(repo, namespace) && repo != namespace
}

// refuseDemoDeletion holds the rule that nothing deletes or resets in the demo
// account, whichever path a scenario takes to a deletion.
func refuseDemoDeletion(target Target) error {
	if target.Owner == demoAccount {
		return fmt.Errorf("lab account %q holds kept demos, and nothing in this tier deletes or resets there: %w", demoAccount, errs.ErrValidation)
	}

	return nil
}

// NewDemoRepo creates a public repository for a demo and registers nothing to
// delete it. A repository of that name that already exists is refused, not
// reset: it is an earlier demo. An empty repository receives its history by a
// push, in objectFormat; an initialised one starts with a first commit on
// main, as a pushed layer and the helper projects of a GitLab release expect.
func NewDemoRepo(tb TB, target Target, repo, objectFormat string, initialised bool) string {
	tb.Helper()
	requireAccepted(tb, target)

	if target.Owner != demoAccount {
		tb.Fatalf("livetest: demo repositories live under lab account %q only", demoAccount)
	}

	if target.Forge != provider.ForgeForgejo && target.Forge != provider.ForgeGitLab {
		tb.Fatalf("livetest: demo repositories are unsupported on %s", target.Forge)
	}

	ctx, cancel := context.WithTimeout(Context(tb), timeoutScratch)
	defer cancel()

	exists, err := repoExists(ctx, target, repo)
	if err != nil {
		tb.Fatalf("livetest: look up %s/%s: %v", target.Owner, repo, err)
	}

	if exists {
		tb.Fatalf("livetest: %s/%s already exists, and demo mode never replaces a demo; delete it by hand or choose another name",
			target.Owner, repo)
	}

	if initialised {
		createInitialisedRepo(ctx, tb, target, repo)
	} else {
		createBareRepo(ctx, tb, target, repo, objectFormat, false)
	}

	return repo
}

// createInitialisedRepo creates a public repository with a first commit on
// main.
func createInitialisedRepo(ctx context.Context, tb TB, target Target, repo string) {
	tb.Helper()

	endpoint, body := target.BaseURL()+"/api/v1/user/repos", map[string]any{
		fieldName: repo, fieldDefaultBranch: defaultBranch, fieldPrivate: false, "auto_init": true,
	}
	if target.Forge == provider.ForgeGitLab {
		endpoint, body = target.BaseURL()+"/api/v4/projects", map[string]any{
			fieldName: repo, fieldPath: repo, "visibility": visibilityPublic, fieldDefaultBranch: defaultBranch,
			"initialize_with_readme": true, "auto_devops_enabled": false,
		}
	}

	if err := discard(ctx, target, http.MethodPost, endpoint, body, http.StatusCreated); err != nil {
		tb.Fatalf("livetest: create %s/%s: %v", target.Owner, repo, err)
	}
}

// repoExists asks the forge whether the owner has a repository of that name.
func repoExists(ctx context.Context, target Target, repo string) (bool, error) {
	endpoint := actionsURL(target, repo, "")
	if target.Forge == provider.ForgeGitLab {
		endpoint = target.BaseURL() + "/api/v4/projects/" + url.PathEscape(RepoSlug(target, repo))
	}

	status, err := decode(ctx, target, http.MethodGet, endpoint, nil, nil, http.StatusOK, http.StatusNotFound)
	if err != nil {
		return false, err
	}

	return status == http.StatusOK, nil
}

// DeleteSecretsAfter removes, whatever the outcome, the repository secrets
// (GitLab: project variables) named, so a repository that is kept holds no
// credential once the run is over.
func DeleteSecretsAfter(tb TB, target Target, repo string, names []string) {
	tb.Helper()

	tb.Cleanup(func() {
		ctx, cancel := cleanupContext(timeoutScratch)
		defer cancel()

		for _, name := range names {
			endpoint := actionsURL(target, repo, "/actions/secrets/"+url.PathEscape(name))
			if target.Forge == provider.ForgeGitLab {
				endpoint = target.BaseURL() + "/api/v4/projects/" + url.PathEscape(RepoSlug(target, repo)) + "/variables/" + url.PathEscape(name)
			}

			if _, err := decode(ctx, target, http.MethodDelete, endpoint, nil, nil, http.StatusNoContent, http.StatusNotFound); err != nil {
				tb.Errorf("livetest: delete secret %s of %s/%s: %v", name, target.Owner, repo, err)
			}
		}
	})
}
