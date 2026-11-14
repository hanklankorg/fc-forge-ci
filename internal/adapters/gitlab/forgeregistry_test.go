// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlab_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/gitlab"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

func TestResolveForgeMavenRegistry_GitLab(t *testing.T) {
	t.Parallel()

	p := &gitlab.Provider{Env: func(k string) string {
		return map[string]string{"CI_API_V4_URL": "https://gl.example.com/api/v4/", "CI_PROJECT_ID": "42", "CI_JOB_TOKEN": "jt"}[k]
	}}

	reg, err := p.ResolveForgeMavenRegistry()
	if err != nil {
		t.Fatal(err)
	}

	if reg.URL != "https://gl.example.com/api/v4/projects/42/packages/maven" || reg.AuthScheme != provider.MavenAuthJobTokenHeader || reg.Token != "jt" {
		t.Errorf("registry = %+v", reg)
	}
}

func TestResolveForgeMavenRegistry_GitLab_MissingProjectID(t *testing.T) {
	t.Parallel()

	p := &gitlab.Provider{Env: func(k string) string { return map[string]string{"CI_API_V4_URL": "https://gl/api/v4"}[k] }}
	if _, err := p.ResolveForgeMavenRegistry(); !errors.Is(err, errs.ErrUsage) {
		t.Errorf("missing CI_PROJECT_ID should be ErrUsage, got %v", err)
	}
}

func TestResolveForgeNPMRegistry_GitLab(t *testing.T) {
	t.Parallel()

	p := &gitlab.Provider{Env: func(k string) string {
		return map[string]string{"CI_API_V4_URL": "https://gl/api/v4", "CI_PROJECT_ID": "7", "CI_JOB_TOKEN": "jt"}[k]
	}}

	reg, err := p.ResolveForgeNPMRegistry()
	if err != nil {
		t.Fatal(err)
	}

	if reg.Registry != "https://gl/api/v4/projects/7/packages/npm/" || reg.Token != "jt" || reg.Scope != "" {
		t.Errorf("npm registry = %+v", reg)
	}
}

// TestResolveForgeRegistries_GitLab_MissingContext runs both resolvers through
// the same missing-input matrix. Only a missing project ID was tested, and only
// for Maven, so a resolver that checked just one of its two inputs -- or npm
// checking neither -- passed. Whitespace counts as missing: a runner variable
// set to spaces produces a URL like " /projects/42" that fails far from here.
//
// A refusal returns the zero registry, never a half-built URL a caller could
// still try to deploy to.
func TestResolveForgeRegistries_GitLab_MissingContext(t *testing.T) {
	t.Parallel()

	for name, env := range map[string]map[string]string{
		"nothing set":           {},
		"no API URL":            {"CI_PROJECT_ID": "42", "CI_JOB_TOKEN": "jt"},
		"no project ID":         {"CI_API_V4_URL": "https://gl.example.com/api/v4", "CI_JOB_TOKEN": "jt"},
		"a blank API URL":       {"CI_API_V4_URL": "  ", "CI_PROJECT_ID": "42"},
		"a blank project ID":    {"CI_API_V4_URL": "https://gl.example.com/api/v4", "CI_PROJECT_ID": " \t"},
		"an API URL of slashes": {"CI_API_V4_URL": "///", "CI_PROJECT_ID": "42"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gitlabProvider := &gitlab.Provider{Env: func(k string) string { return env[k] }}

			maven, err := gitlabProvider.ResolveForgeMavenRegistry()
			if !errors.Is(err, errs.ErrUsage) || maven != (provider.ForgeMavenRegistry{}) {
				t.Errorf("maven = (%+v, %v), want the zero registry and ErrUsage", maven, err)
			}

			npm, err := gitlabProvider.ResolveForgeNPMRegistry()
			if !errors.Is(err, errs.ErrUsage) || npm != (provider.ForgeNPMRegistry{}) {
				t.Errorf("npm = (%+v, %v), want the zero registry and ErrUsage", npm, err)
			}
		})
	}
}

// TestResolveForgeRegistries_GitLab_EmptyTokenPassesThrough pins the token
// policy rather than leaving it implicit. Resolution does not refuse an empty
// CI_JOB_TOKEN; the registry rejects the deploy instead. That matches the
// GitHub and Forgejo resolvers, which also pass their token through as found,
// and inside a GitLab job the runner always sets the variable -- an empty one
// means the command ran outside a job, where the missing URL and project ID
// normally refuse first. Changing this belongs to all three adapters at once.
func TestResolveForgeRegistries_GitLab_EmptyTokenPassesThrough(t *testing.T) {
	t.Parallel()

	gitlabProvider := &gitlab.Provider{Env: func(k string) string {
		return map[string]string{"CI_API_V4_URL": "https://gl.example.com/api/v4", "CI_PROJECT_ID": "42"}[k]
	}}

	maven, err := gitlabProvider.ResolveForgeMavenRegistry()
	if err != nil {
		t.Fatal(err)
	}

	wantMaven := provider.ForgeMavenRegistry{
		ServerID: "gitlab-maven", URL: "https://gl.example.com/api/v4/projects/42/packages/maven",
		AuthScheme: provider.MavenAuthJobTokenHeader,
	}
	if maven != wantMaven {
		t.Errorf("maven = %+v, want %+v", maven, wantMaven)
	}

	npm, err := gitlabProvider.ResolveForgeNPMRegistry()
	if err != nil {
		t.Fatal(err)
	}

	if want := (provider.ForgeNPMRegistry{Registry: "https://gl.example.com/api/v4/projects/42/packages/npm/"}); npm != want {
		t.Errorf("npm = %+v, want %+v", npm, want)
	}
}

// The release's package credential outranks the job token, which a hardened
// project gives no package write; the project ID is a number and nothing a
// variable could turn into another path of the API.
func TestResolveForgeNPMRegistry_GitLab_ReleaseCredentialAndProjectID(t *testing.T) {
	t.Parallel()

	env := map[string]string{
		"CI_API_V4_URL": "https://gl.example.com/api/v4", "CI_PROJECT_ID": "42",
		"CI_JOB_TOKEN": "job", "RELEASE_PACKAGE_TOKEN": "release",
	}
	gitlabProvider := &gitlab.Provider{Env: func(k string) string { return env[k] }}

	npm, err := gitlabProvider.ResolveForgeNPMRegistry()
	if err != nil || npm.Token != "release" {
		t.Fatalf("npm = (%+v, %v), want the release package token", npm, err)
	}

	for _, id := range []string{"42/../7", "7?x", "group%2Fproject", "-1", "042", "4 2"} {
		env := map[string]string{"CI_API_V4_URL": "https://gl.example.com/api/v4", "CI_PROJECT_ID": id, "CI_JOB_TOKEN": "job"}
		gitlabProvider := &gitlab.Provider{Env: func(k string) string { return env[k] }}

		if npm, err := gitlabProvider.ResolveForgeNPMRegistry(); !errors.Is(err, errs.ErrUsage) || npm != (provider.ForgeNPMRegistry{}) {
			t.Errorf("project ID %q: npm = (%+v, %v), want the zero registry and ErrUsage", id, npm, err)
		}

		if _, err := gitlabProvider.ResolveForgeMavenRegistry(); !errors.Is(err, errs.ErrUsage) {
			t.Errorf("project ID %q: maven error = %v, want ErrUsage", id, err)
		}
	}
}

// CI_PROJECT_ID is the registry a package goes to, and a project variable
// outranks it: the job's own token says which project the job runs in.
func TestVerifyForgeProject_GitLab(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, projectID, token, body string
		status                       int
		want                         error
	}{
		{"the job's project", "42", "job", `{"pipeline":{"project_id":42}}`, http.StatusOK, nil},
		{"another project", "7", "job", `{"pipeline":{"project_id":42}}`, http.StatusOK, errs.ErrValidation},
		{"no project in the job", "42", "job", `{"pipeline":{}}`, http.StatusOK, errs.ErrMalformedInput},
		{"a refused token", "42", "job", `{}`, http.StatusUnauthorized, errs.ErrPermissionDenied},
		{"no token", "42", "", `{"pipeline":{"project_id":42}}`, http.StatusOK, errs.ErrUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v4/job" || r.Header.Get("JOB-TOKEN") != "job" {
					t.Errorf("request %s with JOB-TOKEN %q", r.URL.Path, r.Header.Get("JOB-TOKEN"))
				}

				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			env := map[string]string{"CI_API_V4_URL": server.URL + "/api/v4", "CI_PROJECT_ID": tc.projectID, "CI_JOB_TOKEN": tc.token}
			gitlabProvider := &gitlab.Provider{Env: func(k string) string { return env[k] }, HTTPClient: server.Client()}

			if err := gitlabProvider.VerifyForgeProject(t.Context()); !errors.Is(err, tc.want) {
				t.Errorf("error = %v, want %v", err, tc.want)
			}
		})
	}
}
