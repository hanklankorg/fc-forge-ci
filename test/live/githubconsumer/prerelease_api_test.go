// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package githubconsumer_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"codefloe.com/itiquette/forge-lab-contract/githubclient"
	"codefloe.com/itiquette/forge-lab-contract/githubcontext"
	githubadapter "github.com/diggsweden/forge-ci/internal/adapters/github"
	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
	sdk "github.com/google/go-github/v91/github"
)

// No runner, runtime image, engine push or product checkout is needed: exercise
// the policy and immutable publication boundary with tiny disposable assets.
func TestGitHubRelease_PrereleaseAPI(t *testing.T) {
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

	runPrereleaseAPICases(t, func(t *testing.T, visibility livetest.ConsumerVisibility) {
		t.Helper()

		private := visibility.Private

		ctx, cancel := context.WithTimeout(livetest.Context(t), 3*time.Minute)
		defer cancel()

		name := document.Prefix + "prerelease-api-" + visibility.Name + "-" + document.Session[:8]

		repo, err := client.CreateRepository(ctx, name, private)
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

		checkID := func() {
			t.Helper()

			current, _, err := api.Repositories.GetByID(ctx, repo.GetID())
			if err != nil {
				t.Fatalf("read created repository identity: %v", err)
			}

			if current.GetID() != repo.GetID() || current.GetName() != name || current.GetOwner().GetID() != id || !strings.EqualFold(current.GetOwner().GetLogin(), owner) || current.Private == nil || current.GetPrivate() != private {
				t.Fatal("scratch repository identity changed")
			}
		}
		checkID()

		seed, _, err := api.Repositories.CreateFile(ctx, owner, name, "prerelease-fixture.txt", &sdk.RepositoryContentFileOptions{Message: new("test: seed disposable prerelease fixture"), Content: []byte("fixture\n")})
		if err != nil {
			t.Fatal(err)
		}

		const tag = "v0.0.1-beta.2"

		checkID()

		if _, _, err := api.Git.CreateRef(ctx, owner, name, sdk.CreateRef{Ref: "refs/tags/" + tag, SHA: seed.GetSHA()}); err != nil {
			t.Fatal(err)
		}

		t.Chdir(t.TempDir())

		if err := os.WriteFile("asset.txt", []byte("original\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile("notes.md", []byte("prerelease API fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		backend := &githubadapter.Provider{Env: func(key string) string {
			if key == "GH_TOKEN" {
				return document.Token
			}

			return ""
		}}

		input := apprelease.PublishReleaseInput{Tag: tag, Repository: owner + "/" + name, ReleaseName: "Prerelease API fixture", ReleaseNotesFile: "notes.md", Assets: []string{"asset.txt"}}
		if err := apprelease.PublishRelease(ctx, backend, io.Discard, input); !errors.Is(err, errs.ErrValidation) {
			t.Fatalf("default policy accepted beta: %v", err)
		}

		if _, response, err := api.Repositories.GetReleaseByTag(ctx, owner, name, tag); err == nil || response == nil || response.StatusCode != http.StatusNotFound {
			t.Fatal("policy refusal left a release")
		}

		input.ReleasePolicy = "stable-prerelease"

		for range 2 {
			checkID()

			if err := apprelease.PublishRelease(ctx, backend, io.Discard, input); err != nil {
				t.Fatal(err)
			}
		}

		release, _, err := api.Repositories.GetReleaseByTag(ctx, owner, name, tag)
		if err != nil || !release.GetPrerelease() || release.GetDraft() || len(release.Assets) != 1 {
			t.Fatal("published prerelease metadata is incorrect")
		}

		if err := os.WriteFile("asset.txt", []byte("changed\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		checkID()

		if err := apprelease.PublishRelease(ctx, backend, io.Discard, input); !errors.Is(err, errs.ErrValidation) {
			t.Fatalf("published beta accepted changed bytes: %v", err)
		}

		body, _, err := api.Repositories.DownloadReleaseAsset(ctx, owner, name, release.Assets[0].GetID(), &http.Client{Timeout: time.Minute})
		if err != nil || body == nil {
			t.Fatal("published asset download failed")
		}
		defer func() { _ = body.Close() }()

		bytes, err := io.ReadAll(io.LimitReader(body, 1024))
		if err != nil || string(bytes) != "original\n" {
			t.Fatal("refused retry changed published bytes")
		}
	})
}

func runPrereleaseAPICases(t *testing.T, run func(*testing.T, livetest.ConsumerVisibility)) {
	t.Helper()

	for _, visibility := range livetest.ConsumerVisibilities(t) {
		t.Run(visibility.Name, func(t *testing.T) {
			run(t, visibility)
		})
	}
}

func TestPrereleaseAPIVisibilitySelection(t *testing.T) {
	t.Setenv("FORGE_CI_LIVE_DEMO", "")

	public := livetest.ConsumerVisibility{Name: "public"}
	private := livetest.ConsumerVisibility{Name: "private", Private: true}

	for _, test := range []struct {
		name     string
		selector string
		want     []livetest.ConsumerVisibility
	}{
		{"default", "", []livetest.ConsumerVisibility{public, private}},
		{"both", "both", []livetest.ConsumerVisibility{public, private}},
		{"public", "public", []livetest.ConsumerVisibility{public}},
		{"private", "private", []livetest.ConsumerVisibility{private}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("FORGE_CI_LIVE_CONSUMER_VISIBILITY", test.selector)

			var dispatched []livetest.ConsumerVisibility

			runPrereleaseAPICases(t, func(t *testing.T, visibility livetest.ConsumerVisibility) {
				t.Helper()

				if !strings.HasSuffix(t.Name(), "/"+visibility.Name) {
					t.Errorf("case name does not match visibility: %s", t.Name())
				}

				dispatched = append(dispatched, visibility)
			})

			if !reflect.DeepEqual(dispatched, test.want) {
				t.Fatalf("dispatched cases = %v, want %v", dispatched, test.want)
			}
		})
	}
}
