// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package consumerinit writes a consumer's first Forge CI files: the
// examples of the release it pins, read from the Forge CI mirror at that
// release, and filled with the values the mirror's operator records there.
package consumerinit

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	domain "github.com/diggsweden/forge-ci/internal/domain/consumerinit"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/domain/version"
)

// examplesDir is where a Forge CI release keeps its consumer examples.
const examplesDir = "examples"

// Remote reads a Forge CI mirror without credentials.
type Remote interface {
	VersionTags(ctx context.Context, repoURL string) ([]string, error)
	TagCommit(ctx context.Context, repoURL, tag string) (string, error)
	// Export writes paths of the tag's tree into dest and returns those the
	// tree does not have.
	Export(ctx context.Context, repoURL, tag, dest string, paths []string) (missing []string, err error)
}

// Input is one `forge-ci init` run.
type Input struct {
	Remote  Remote
	URL     string // the Forge CI mirror, https://host/owner/repo
	Ref     string // release tag to pin; empty takes the highest vX.Y.Z
	Dir     string // the consumer's root
	Example string // example to start from, e.g. go-cli
}

// Result says what was pinned and written, and what a person still fills in.
type Result struct {
	Forge     provider.ForgeAPI
	Source    domain.Source
	Defaults  bool // the mirror has a defaults file at the pinned tag
	Written   []string
	Remaining map[string][]domain.Placeholder
}

// Run pins the mirror's release, reads its examples and defaults at that
// release, and writes the example's files for the consumer's forge. It
// writes nothing when any target already exists.
func Run(ctx context.Context, in Input) (Result, error) {
	repoURL, src, err := parseURL(in.URL)
	if err != nil {
		return Result{}, err
	}

	if src.Tag, src.Commit, err = pin(ctx, in.Remote, repoURL, in.Ref); err != nil {
		return Result{}, err
	}

	snapshot, err := os.MkdirTemp("", "forge-ci-init-")
	if err != nil {
		return Result{}, err
	}

	defer func() { _ = os.RemoveAll(snapshot) }()

	defaults, found, err := readRelease(ctx, in.Remote, repoURL, src.Tag, snapshot)
	if err != nil {
		return Result{}, err
	}

	forge, err := domain.ResolveForge(src.Host, defaults)
	if err != nil {
		return Result{}, err
	}

	examples := os.DirFS(filepath.Join(snapshot, examplesDir))

	plan, err := planFiles(examples, forge, in.Example, in.Dir)
	if err != nil {
		return Result{}, err
	}

	result := Result{Forge: forge, Source: src, Defaults: found, Remaining: map[string][]domain.Placeholder{}}
	err = write(examples, plan, domain.Replacements(forge, in.Example, src, defaults), in.Dir, &result)

	return result, err
}

// parseURL splits https://host/owner/repo into the URL git fetches from and
// the host and repository path the examples are pinned to.
func parseURL(raw string) (string, domain.Source, error) {
	parsed, err := url.Parse(raw)
	repository := strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git")

	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || !strings.Contains(repository, "/") {
		return "", domain.Source{}, fmt.Errorf("the Forge CI URL must be https://<host>/<owner>/<repository>, got %q: %w", raw, errs.ErrUsage)
	}

	return strings.TrimSuffix(raw, "/"), domain.Source{Host: parsed.Host, Repository: repository}, nil
}

// pin returns the release tag, the given one or the highest vX.Y.Z, and
// the commit it names on the mirror.
func pin(ctx context.Context, remote Remote, repoURL, ref string) (string, string, error) {
	tag := ref
	if tag == "" {
		tags, err := remote.VersionTags(ctx, repoURL)
		if err != nil {
			return "", "", err
		}

		if tag = version.LatestSemverTag(tags); tag == "" {
			return "", "", fmt.Errorf("%s has no vX.Y.Z release tag; pass --ref: %w", repoURL, errs.ErrUsage)
		}
	}

	commit, err := remote.TagCommit(ctx, repoURL, tag)

	return tag, commit, err
}

// readRelease exports the release's examples and defaults file into
// snapshot and parses the defaults, when there are any.
func readRelease(ctx context.Context, remote Remote, repoURL, tag, snapshot string) (domain.Defaults, bool, error) {
	missing, err := remote.Export(ctx, repoURL, tag, snapshot, []string{examplesDir, domain.DefaultsFile})
	if err != nil {
		return domain.Defaults{}, false, err
	}

	if slices.Contains(missing, examplesDir) {
		return domain.Defaults{}, false, fmt.Errorf("%s at %s has no %s/: it is not a Forge CI repository: %w", repoURL, tag, examplesDir, errs.ErrUsage)
	}

	if slices.Contains(missing, domain.DefaultsFile) {
		return domain.Defaults{}, false, nil
	}

	data, err := fs.ReadFile(os.DirFS(snapshot), domain.DefaultsFile)
	if err != nil {
		return domain.Defaults{}, false, err
	}

	defaults, err := domain.ParseDefaults(data)

	return defaults, err == nil, err
}

// planFiles lists the files to write and refuses when any already exists.
func planFiles(examples fs.FS, forge provider.ForgeAPI, example, dir string) ([]domain.FileCopy, error) {
	entries, err := fs.ReadDir(examples, path.Join(example, domain.ForgeDir(forge)))
	if err != nil {
		return nil, fmt.Errorf("this release has no %s files for example %s; choose another with --example: %w", forge, example, errs.ErrUsage)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}

	// An older release lacks files newer examples have, such as
	// nanolinter.toml; init writes what the pinned release holds.
	var plan []domain.FileCopy

	for _, c := range domain.Plan(forge, example, names) {
		if _, statErr := fs.Stat(examples, c.From); statErr == nil {
			plan = append(plan, c)
		}
	}

	for _, c := range plan {
		_, statErr := os.Lstat(filepath.Join(dir, filepath.FromSlash(c.To)))
		if statErr == nil {
			return nil, fmt.Errorf("%s already exists; init writes only new files: %w", c.To, errs.ErrUsage)
		}

		if !errors.Is(statErr, fs.ErrNotExist) {
			return nil, statErr
		}
	}

	return plan, nil
}

// write substitutes and writes each planned file, and records what it
// still holds for a person to fill in.
func write(examples fs.FS, plan []domain.FileCopy, replacements []domain.Replacement, dir string, result *Result) error {
	for _, file := range plan {
		body, err := fs.ReadFile(examples, file.From)
		if err != nil {
			return err
		}

		content := domain.Apply(string(body), replacements)
		target := filepath.Join(dir, filepath.FromSlash(file.To))

		if err = os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return err
		}

		if err = os.WriteFile(target, []byte(content), 0o644); err != nil { //nolint:gosec // repository files, world-readable by design.
			return err
		}

		result.Written = append(result.Written, file.To)
		if left := domain.Remaining(content); len(left) > 0 {
			result.Remaining[file.To] = left
		}
	}

	return nil
}
