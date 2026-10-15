// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appversion "github.com/diggsweden/forge-ci/internal/app/version"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/runcontext"
)

type fakeChangelogRenderGit struct {
	status string

	checkedOut  []string
	credentials []runcontext.Credential
}

func (f *fakeChangelogRenderGit) FetchBranch(_ context.Context, _, _ string, cred runcontext.Credential) error {
	f.credentials = append(f.credentials, cred)

	return nil
}

func (f *fakeChangelogRenderGit) Checkout(_ context.Context, ref string) error {
	f.checkedOut = append(f.checkedOut, ref)

	return nil
}

func (f *fakeChangelogRenderGit) StatusPorcelain(_ context.Context, _ string) (string, error) {
	return f.status, nil
}

type fakeChangelogRenderer struct {
	backend           string
	fullTag           string
	bodyTag           string
	fullRepositoryURL string
	bodyRepositoryURL string
}

func (f *fakeChangelogRenderer) RenderFull(_ context.Context, backend, _, tag, outputPath, repositoryURL string) error {
	f.backend = backend
	f.fullTag = tag
	f.fullRepositoryURL = repositoryURL

	return os.WriteFile(outputPath, []byte("# Release\n\nChanges\n"), 0o600)
}

func (f *fakeChangelogRenderer) RenderBody(_ context.Context, _, _, tag, repositoryURL string) (string, error) {
	f.bodyTag = tag
	f.bodyRepositoryURL = repositoryURL

	return "body line\n", nil
}

func TestChangelogRender_GitChglogRendersCommitMessage(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	gitr := &fakeChangelogRenderGit{status: " M CHANGELOG.md"}
	renderer := &fakeChangelogRenderer{}

	var out bytes.Buffer

	cred := runcontext.OperatorCredential("read-token")

	err := appversion.ChangelogRender(context.Background(), gitr, renderer, &out, appversion.ChangelogRenderInput{
		Backend:          "git-chglog",
		Tag:              "v1.2.3",
		RepositoryURL:    "https://forgejo.example/forge/itiquette/consumer",
		ChangelogConfig:  ".chglog/full.yml",
		CommitBodyConfig: ".chglog/body.yml",
		CommitTrailers:   "Release-Request: release-request/v1.2.3",
		Token:            cred,
	})
	if err != nil {
		t.Fatalf("ChangelogRender: %v", err)
	}

	// One condition per claim: a combined guard reported "unexpected render
	// result" for any of four different regressions.
	if renderer.backend != "git-chglog" {
		t.Errorf("backend = %q, want git-chglog", renderer.backend)
	}

	if renderer.fullTag != "v1.2.3" || renderer.bodyTag != "v1.2.3" {
		t.Errorf("rendered tags = (%q, %q), want both v1.2.3", renderer.fullTag, renderer.bodyTag)
	}

	if renderer.fullRepositoryURL != "https://forgejo.example/forge/itiquette/consumer" ||
		renderer.bodyRepositoryURL != "https://forgejo.example/forge/itiquette/consumer" {
		t.Errorf("rendered repository URLs = (%q, %q), want the trusted repository URL", renderer.fullRepositoryURL, renderer.bodyRepositoryURL)
	}

	msg := readTestFile(t, "commit-msg.txt")
	for _, want := range []string{"chore(release): bump to v1.2.3", "body line", "[skip ci]", "Release-Request: release-request/v1.2.3"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("commit-msg.txt missing %q:\n%s", want, msg)
		}
	}

	if !strings.Contains(out.String(), "Generated CHANGELOG.md (3 lines)") {
		t.Fatalf("stdout = %q", out.String())
	}

	if len(gitr.credentials) != 1 {
		t.Fatalf("forwarded credentials = %d, want 1", len(gitr.credentials))
	}

	for i, got := range gitr.credentials {
		if got.For("https://forge.example") != "read-token" {
			t.Fatalf("credential %d was not forwarded", i)
		}
	}
}

func TestChangelogRender_RejectsInvalidBackend(t *testing.T) {
	t.Parallel()

	err := appversion.ChangelogRender(context.Background(), &fakeChangelogRenderGit{}, &fakeChangelogRenderer{}, &bytes.Buffer{}, appversion.ChangelogRenderInput{
		Backend:          "bad",
		Tag:              "v1.2.3",
		RepositoryURL:    "https://forgejo.example/itiquette/consumer",
		ChangelogConfig:  "full",
		CommitBodyConfig: "body",
	})
	if !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
}

func TestChangelogRender_RejectsInvalidRepositoryURL(t *testing.T) {
	t.Parallel()

	for _, repositoryURL := range []string{
		"",
		"http://forgejo.example/itiquette/consumer",
		"https://user@forgejo.example/itiquette/consumer",
		"https://forgejo.example/itiquette/consumer/",
		"https://forgejo.example/itiquette/consumer?view=release",
		"https://forgejo.example/itiquette/%63onsumer",
	} {
		err := appversion.ChangelogRender(context.Background(), &fakeChangelogRenderGit{}, &fakeChangelogRenderer{}, &bytes.Buffer{}, appversion.ChangelogRenderInput{
			Tag:              "v1.2.3",
			RepositoryURL:    repositoryURL,
			ChangelogConfig:  "full",
			CommitBodyConfig: "body",
		})
		if err == nil {
			t.Errorf("repository URL %q was accepted", repositoryURL)
		}
	}
}

func readTestFile(t *testing.T, rel string) string {
	t.Helper()

	body, err := os.ReadFile(filepath.Clean(rel))
	if err != nil {
		t.Fatal(err)
	}

	return string(body)
}
