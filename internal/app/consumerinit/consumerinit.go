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
)

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
	Ref     string // release tag to pin; empty takes the highest stable X.Y.Z or vX.Y.Z
	Dir     string // the consumer's root
	Example string // example to start from, e.g. go-cli
	DryRun  bool   // render and inspect destinations without writing project files
}

// Result says what was pinned, proposed and written, and what still needs review.
type Result struct {
	Directory string
	DryRun    bool
	Forge     provider.ForgeAPI
	Source    domain.Source
	Defaults  bool // the mirror has a defaults file at the pinned tag
	Written   []string
	Proposed  []string
	Kept      []string // existing files init keeps rather than refuses, such as the changelog configuration
	Conflicts []string
	Remaining map[string][]domain.Placeholder
	// Project is what init read from the consumer's build files and wrote in
	// place of the example's project placeholders.
	Project domain.Project
	// Optional names the example's workflows init did not write, such as a
	// snapshot workflow, which a person may copy from the release by hand.
	Optional []string
}

// Run pins the mirror's release, reads its examples and defaults at that
// release, and writes the example's files for the consumer's forge. It
// writes nothing in dry-run mode or when any target already exists. Dry runs
// still acquire the remote templates in temporary storage.
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

	result := Result{DryRun: in.DryRun, Forge: forge, Source: src, Defaults: found, Remaining: map[string][]domain.Placeholder{}}

	return initializeFiles(os.DirFS(snapshot), in, defaults, result)
}

func initializeFiles(release fs.FS, in Input, defaults domain.Defaults, result Result) (Result, error) {
	plan, optional, err := planFiles(release, result.Forge, in.Example)
	if err != nil {
		return Result{}, err
	}

	result.Optional = optional

	directory, err := filepath.Abs(in.Dir)
	if err != nil {
		return Result{}, err
	}

	result.Directory = directory
	result.Project = DetectProject(os.DirFS(directory))

	plan, result.Kept, err = keepExisting(directory, plan)
	if err != nil {
		return Result{}, err
	}

	files, err := render(release, plan, domain.Replacements(result.Forge, in.Example, result.Source, defaults), domain.ProjectReplacements(result.Project), &result)
	if err != nil {
		return Result{}, err
	}

	result.Conflicts, err = existingDestinations(directory, result.Proposed)
	if err != nil {
		return Result{}, err
	}

	if len(result.Conflicts) > 0 {
		return result, fmt.Errorf("existing destinations prevent initialization: %s; init writes only new files: %w", strings.Join(result.Conflicts, ", "), errs.ErrUsage)
	}

	if in.DryRun {
		return result, nil
	}

	err = write(files, directory, &result)

	return result, err
}

// keepExisting takes the copies the consumer may already have, and has,
// out of the plan.
func keepExisting(dir string, plan []domain.FileCopy) ([]domain.FileCopy, []string, error) {
	var kept []string

	remaining := plan[:0:0]
	for _, c := range plan {
		if c.KeepExisting {
			_, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(c.To)))
			if err == nil {
				kept = append(kept, c.To)

				continue
			}

			if !errors.Is(err, fs.ErrNotExist) {
				return nil, nil, err
			}
		}

		remaining = append(remaining, c)
	}

	return remaining, kept, nil
}

func existingDestinations(dir string, proposed []string) ([]string, error) {
	var conflicts []string

	for _, file := range proposed {
		_, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(file)))
		if err == nil {
			conflicts = append(conflicts, file)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}

	return conflicts, nil
}

// parseURL splits https://host/owner/repo into the URL git fetches from and
// the host and repository path the examples are pinned to.
func parseURL(raw string) (string, domain.Source, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", domain.Source{}, fmt.Errorf("the Forge CI URL must be https://<host>/<owner>/<repository>, got %q: %w", raw, errs.ErrUsage)
	}

	repository := strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git")
	if !strings.Contains(repository, "/") {
		return "", domain.Source{}, fmt.Errorf("the Forge CI URL must be https://<host>/<owner>/<repository>, got %q: %w", raw, errs.ErrUsage)
	}

	return strings.TrimSuffix(raw, "/"), domain.Source{Host: parsed.Host, Repository: repository}, nil
}

// pin returns the release tag, the given one or the highest stable version, and
// the commit it names on the mirror.
func pin(ctx context.Context, remote Remote, repoURL, ref string) (string, string, error) {
	tag := ref
	if tag == "" {
		tags, err := remote.VersionTags(ctx, repoURL)
		if err != nil {
			return "", "", unreachable(repoURL, err)
		}

		if tag = domain.LatestReleaseTag(tags); tag == "" {
			return "", "", fmt.Errorf("%s has no stable X.Y.Z or vX.Y.Z release tag; pass --ref: %w", repoURL, errs.ErrUsage)
		}
	}

	// Not unreachable: a missing --ref tag fails here too, with the same
	// error class, and the mirror hint would mislead it.
	commit, err := remote.TagCommit(ctx, repoURL, tag)

	return tag, commit, err
}

// unreachable says what the URL has to be when git cannot read it: off
// github.com, a Forge CI mirror an operator keeps on the organization's forge.
func unreachable(repoURL string, err error) error {
	return fmt.Errorf("cannot read the release tags of %s; on a forge other than github.com it is your "+
		"organization's Forge CI mirror: ask its operator for the URL, or see forge-ci docs forgejo#quick-start: %w", repoURL, err)
}

// readRelease exports the release's examples, changelog configurations and
// defaults file into snapshot and parses the defaults, when there are any.
func readRelease(ctx context.Context, remote Remote, repoURL, tag, snapshot string) (domain.Defaults, bool, error) {
	missing, err := remote.Export(ctx, repoURL, tag, snapshot, []string{domain.ExamplesDir, domain.ChangelogDir, domain.DefaultsFile})
	if err != nil {
		return domain.Defaults{}, false, err
	}

	if slices.Contains(missing, domain.ExamplesDir) {
		return domain.Defaults{}, false, fmt.Errorf("%s at %s has no %s/: it is not a Forge CI repository: %w", repoURL, tag, domain.ExamplesDir, errs.ErrUsage)
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

// planFiles lists the available initialization files of the selected example.
func planFiles(release fs.FS, forge provider.ForgeAPI, example string) ([]domain.FileCopy, []string, error) {
	entries, err := fs.ReadDir(release, path.Join(domain.ExamplesDir, example, domain.ForgeDir(forge)))
	if err != nil {
		return nil, nil, fmt.Errorf("this release has no %s files for example %s; choose another with --example: %w", forge, example, errs.ErrUsage)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}

	// An older release may lack files newer examples have;
	// init writes what the pinned release holds.
	var plan []domain.FileCopy

	for _, c := range domain.Plan(forge, example, names) {
		if _, statErr := fs.Stat(release, c.From); statErr == nil {
			plan = append(plan, c)
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			return nil, nil, statErr
		}
	}

	if !slices.ContainsFunc(plan, func(c domain.FileCopy) bool { return strings.HasPrefix(c.From, domain.ExamplesDir+"/") }) {
		return nil, nil, fmt.Errorf("this release has no initialization files for %s example %s: %w", forge, example, errs.ErrUsage)
	}

	return plan, domain.OptionalWorkflows(forge, names), nil
}

type renderedFile struct {
	path    string
	content []byte
}

// render is shared by previews and writes, including placeholder reporting.
func render(release fs.FS, plan []domain.FileCopy, replacements, project []domain.Replacement, result *Result) ([]renderedFile, error) {
	files := make([]renderedFile, 0, len(plan))
	for _, file := range plan {
		body, err := fs.ReadFile(release, file.From)
		if err != nil {
			return nil, err
		}

		content := domain.ApplyProject(domain.Apply(string(body), replacements), project)
		files = append(files, renderedFile{path: file.To, content: []byte(content)})
		result.Proposed = append(result.Proposed, file.To)

		if left := domain.Remaining(content); len(left) > 0 {
			result.Remaining[file.To] = left
		}
	}

	return files, nil
}

func write(files []renderedFile, dir string, result *Result) error {
	for _, file := range files {
		target := filepath.Join(dir, filepath.FromSlash(file.path))
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return err
		}

		if err := os.WriteFile(target, file.content, 0o644); err != nil { //nolint:gosec // repository files, world-readable by design.
			return err
		}

		result.Written = append(result.Written, file.path)
	}

	return nil
}
