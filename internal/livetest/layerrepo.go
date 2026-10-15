// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
)

// A scenario about the layer builds a small consumer repository around it: a
// few source files, a branch, a pull request, a tag. And much of what the
// layer promises is about what a run did not do, so the same repository is
// read back afterwards: its branches, tags, releases, keys and secrets, and
// the packages of its owner. All of it is Forgejo's API, spoken directly.

// listLimit is one page of a listing here. A scratch repository holds a
// handful of anything, so a full page means the listing was cut short and
// the helpers refuse it instead of comparing against part of the truth.
const listLimit = 50

// fieldMessage is the commit or tag message member of Forgejo's write requests.
const fieldMessage = "message"

// FileCommit is one commit of whole files to a scratch repository.
type FileCommit struct {
	// Branch is the branch the commit builds on.
	Branch string
	// NewBranch, when set, is created at the commit and Branch stays put.
	NewBranch string
	Message   string
	// Files maps a repository path to the file's content. A file the branch
	// already holds is replaced.
	Files map[string]string
}

// existingBlob is the id of the blob a branch holds at path, which replacing
// the file needs, or empty when it holds none. An empty repository answers
// any path with an empty list, not with 404.
func existingBlob(ctx context.Context, tb TB, target Target, repo, branch, path string) string {
	tb.Helper()

	var (
		answer   json.RawMessage
		existing struct {
			SHA string `json:"sha"`
		}
	)

	lookup := actionsURL(target, repo, "/contents/"+path) + "?ref=" + url.QueryEscape(branch)

	_, err := decode(ctx, target, http.MethodGet, lookup, nil, &answer, http.StatusOK, http.StatusNotFound)
	if err == nil && len(answer) > 0 && string(answer) != "[]" {
		err = json.Unmarshal(answer, &existing)
	}

	if err != nil {
		tb.Fatalf("livetest: look up %s on %s: %v", path, branch, err)
	}

	return existing.SHA
}

// CommitFiles writes files as ONE commit and returns its id.
//
// One commit and not one per file, because a commit is an event: a workflow
// that starts on a push would otherwise start once per file, and a pull
// request would show a history nobody wrote.
func CommitFiles(tb TB, target Target, repo string, commit FileCommit) string {
	tb.Helper()
	requireForgejoActions(tb, target)

	ctx, cancel := context.WithTimeout(Context(tb), timeoutScratch)
	defer cancel()

	type operation struct {
		Operation string `json:"operation"`
		Path      string `json:"path"`
		Content   string `json:"content"`
		SHA       string `json:"sha,omitempty"`
	}

	paths := make([]string, 0, len(commit.Files))
	for path := range commit.Files {
		paths = append(paths, path)
	}

	slices.Sort(paths)

	operations := make([]operation, 0, len(paths))

	for _, path := range paths {
		change := operation{Operation: "create", Path: path, Content: base64.StdEncoding.EncodeToString([]byte(commit.Files[path]))}
		if blob := existingBlob(ctx, tb, target, repo, commit.Branch, path); blob != "" {
			change.Operation, change.SHA = "update", blob
		}

		operations = append(operations, change)
	}

	body := map[string]any{"branch": commit.Branch, fieldMessage: commit.Message, "files": operations}
	if commit.NewBranch != "" {
		body["new_branch"] = commit.NewBranch
	}

	var created struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}

	if _, err := decode(ctx, target, http.MethodPost, actionsURL(target, repo, "/contents"), body, &created, http.StatusCreated); err != nil {
		tb.Fatalf("livetest: commit %d files to %s/%s: %v", len(paths), target.Owner, repo, err)
	}

	if !fixtureObjectID(created.Commit.SHA) {
		tb.Fatalf("livetest: %s answered the commit to %s/%s without a commit id", target.Forge, target.Owner, repo)
	}

	return created.Commit.SHA
}

// AnnotateTag creates an annotated tag at the head of a branch.
//
// PrepareTag makes a lightweight tag, which is all a release from a tag
// needs. A release REQUEST is a statement by a person, so the layer admits
// only a tag object, with a tagger and a message, and refuses a bare ref.
func AnnotateTag(tb TB, target Target, repo, tag, branch, message string) {
	tb.Helper()
	requireForgejoActions(tb, target)

	ctx, cancel := context.WithTimeout(Context(tb), timeoutAPI)
	defer cancel()

	body := map[string]any{"tag_name": tag, "target": branch, fieldMessage: message}
	if err := discard(ctx, target, http.MethodPost, actionsURL(target, repo, "/tags"), body, http.StatusCreated); err != nil {
		tb.Fatalf("livetest: tag %s/%s at %s as %s: %v", target.Owner, repo, branch, tag, err)
	}
}

// PullRequest is a pull request as the forge reports it.
type PullRequest struct {
	Number int64  `json:"number"`
	State  string `json:"state"`
	Merged bool   `json:"merged"`
	Head   GitRef `json:"head"`
	Base   GitRef `json:"base"`
}

// GitRef is one side of a pull request.
type GitRef struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

// OpenPullRequest opens a pull request from head into base, both branches of
// the scratch repository.
func OpenPullRequest(tb TB, target Target, repo, head, base, title string) PullRequest {
	tb.Helper()
	requireForgejoActions(tb, target)

	ctx, cancel := context.WithTimeout(Context(tb), timeoutAPI)
	defer cancel()

	var opened PullRequest

	body := map[string]any{"head": head, "base": base, "title": title}
	if _, err := decode(ctx, target, http.MethodPost, actionsURL(target, repo, "/pulls"), body, &opened, http.StatusCreated); err != nil {
		tb.Fatalf("livetest: open a pull request from %s into %s: %v", head, base, err)
	}

	return opened
}

// ReadPullRequest reads a pull request back.
func ReadPullRequest(tb TB, target Target, repo string, number int64) PullRequest {
	tb.Helper()

	var found PullRequest

	readActions(tb, target, repo, "/pulls/"+strconv.FormatInt(number, 10), &found)

	return found
}

// Branches maps each branch of the scratch repository to the commit it is at.
func Branches(tb TB, target Target, repo string) map[string]string {
	tb.Helper()

	var listed []struct {
		Name   string `json:"name"`
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}

	readList(tb, target, repo, "/branches", &listed, func() int { return len(listed) })

	branches := make(map[string]string, len(listed))
	for _, branch := range listed {
		branches[branch.Name] = branch.Commit.ID
	}

	return branches
}

// RepoCount counts what the scratch repository holds of one kind, named by
// its API route: "/tags", "/releases", "/keys", "/actions/secrets".
//
// A count and not a listing, because what scenarios ask is whether a run left
// something behind, and the answer they want is none.
func RepoCount(tb TB, target Target, repo, route string) int {
	tb.Helper()

	var listed []map[string]any

	readList(tb, target, repo, route, &listed, func() int { return len(listed) })

	return len(listed)
}

// readList reads one page of a repository listing and refuses a full one.
func readList(tb TB, target Target, repo, route string, into any, count func() int) {
	tb.Helper()

	readActions(tb, target, repo, route+"?limit="+strconv.Itoa(listLimit), into)

	if count() >= listLimit {
		tb.Fatalf("livetest: %s of %s/%s fills a page of %d, so the listing may be cut short", route, target.Owner, repo, listLimit)
	}
}

// OwnerPackage is one package version of the account this suite acts as.
type OwnerPackage struct {
	ID         int64
	Type       string
	Name       string
	Version    string
	Repository string // the repository it is linked to, or ""
}

// OwnerPackages lists every package version of the owner, ordered by id.
//
// Forgejo scopes packages to the owner and not to a repository, so "this run
// published nothing" can only be asked of the whole account: a package under
// a name nobody expected is exactly the one a narrower question would miss.
// Two listings compare with slices.Equal. That is sound because scenarios run
// one after another and one run holds the account at a time.
func OwnerPackages(tb TB, target Target) []OwnerPackage {
	tb.Helper()
	requireForgejoActions(tb, target)

	ctx, cancel := context.WithTimeout(Context(tb), timeoutAPI)
	defer cancel()

	var packages []OwnerPackage

	for page := 1; ; page++ {
		var rows []struct {
			ID         int64  `json:"id"`
			Type       string `json:"type"`
			Name       string `json:"name"`
			Version    string `json:"version"`
			Repository *struct {
				Name string `json:"name"`
			} `json:"repository"`
		}

		endpoint := target.BaseURL() + "/api/v1/packages/" + url.PathEscape(target.Owner) +
			"?limit=" + strconv.Itoa(listLimit) + "&page=" + strconv.Itoa(page)
		if _, err := decode(ctx, target, http.MethodGet, endpoint, nil, &rows, http.StatusOK); err != nil {
			tb.Fatalf("livetest: list the packages of %s: %v", target.Owner, err)
		}

		for _, row := range rows {
			pkg := OwnerPackage{ID: row.ID, Type: row.Type, Name: row.Name, Version: row.Version}
			if row.Repository != nil {
				pkg.Repository = row.Repository.Name
			}

			packages = append(packages, pkg)
		}

		if len(rows) < listLimit {
			break
		}
	}

	slices.SortFunc(packages, func(a, b OwnerPackage) int { return cmp.Compare(a.ID, b.ID) })

	return packages
}
