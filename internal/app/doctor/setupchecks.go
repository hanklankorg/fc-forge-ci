// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package doctor

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/consumerinit"
	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
)

// The checks in this file answer "is this repository finished?" rather than
// "is what is here valid?". A file that parses can still be the example it
// was copied from, and a repository with a valid artifacts.yml and no
// workflow runs nothing. Before these checks doctor passed both, and said
// to open a pull request.

// Names of the completeness checks, exported for `forge-ci setup`, which
// reads its checklist state from the same checks.
const (
	CheckCIWorkflows   = "CI workflows present"
	CheckPlaceholders  = "example values filled in"
	CheckArtifactPaths = "artifact paths exist"
	CheckSSHAllowlist  = "ssh git-signing allowlist"
	CheckAdminPolicy   = "administrator policy resolves"
)

// maxListed caps how many file:line entries one check prints; the count is
// always complete.
const maxListed = 8

// ciFiles lists the repository's CI entry points relative to root: the
// Actions workflows of GitHub and Forgejo and the GitLab pipeline.
func ciFiles(root string) []string {
	var files []string

	for _, rel := range actionsWorkflowDirRels() {
		entries, err := os.ReadDir(filepath.Join(root, rel))
		if err != nil {
			continue
		}

		for _, e := range entries {
			ext := filepath.Ext(e.Name())
			if !e.IsDir() && (ext == ".yml" || ext == ".yaml") {
				files = append(files, filepath.ToSlash(filepath.Join(rel, e.Name())))
			}
		}
	}

	if regularFileExists(filepath.Join(root, ".gitlab-ci.yml")) {
		files = append(files, ".gitlab-ci.yml")
	}

	return files
}

// checkCIWorkflows fails a repository with no CI entry point at all: its
// configuration may be perfect, and nothing will ever run it.
func checkCIWorkflowsPresent(root string) Check {
	files := ciFiles(root)
	if len(files) == 0 {
		return Check{
			Name:     CheckCIWorkflows,
			Severity: SeverityFail,
			Message:  "no .github/workflows/*.yml, .forgejo/workflows/*.yml or .gitlab-ci.yml; nothing calls the reusable Forge CI workflows",
			Remediation: "preview the workflows for this repository with `forge-ci init <forge-ci URL> --dry-run` " +
				"(for example https://github.com/diggsweden/forge-ci), then run it without --dry-run",
		}
	}

	return Check{
		Name:     CheckCIWorkflows,
		Severity: SeverityOK,
		Message:  fmt.Sprintf("%d CI file(s): %s", len(files), listed(files)),
	}
}

// checkPlaceholdersFilled fails while an example value is still in the
// repository's Forge CI files: a FIXME bot identity, a PIN_ digest, a my-cli
// name. These parse and validate, and fail only when the workflow runs. With
// no Forge CI file at all there is nothing to judge, and ok is false: the
// CI-workflow check already fails that repository.
func checkPlaceholdersFilled(root, artifactsPath string) (Check, bool) {
	files := ciFiles(root)

	var artifactsRel string

	if regularFileExists(artifactsPath) {
		rel, err := filepath.Rel(root, artifactsPath)
		if err != nil || !filepath.IsLocal(rel) {
			rel = artifactsPath
		}

		artifactsRel = filepath.ToSlash(rel)
		files = append([]string{artifactsRel}, files...)
	}

	if len(files) == 0 {
		return Check{}, false
	}

	var (
		found []string
		hints []string
		// blocking is set once a value is in a file a pull request runs; while
		// every value is in a release-only file the check only warns.
		blocking bool
	)

	for _, rel := range files {
		path := rel
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, filepath.FromSlash(rel))
		}

		body, err := os.ReadFile(path) //nolint:gosec // repository CI files under the inspected root.
		if err != nil {
			continue
		}

		// Every workflow init writes calls forge-ci. Another workflow's
		// FIXME or PIN_NODE_VERSION is the repository's own business.
		if rel != artifactsRel && !strings.Contains(string(body), "forge-ci") {
			continue
		}

		for _, p := range consumerinit.Remaining(string(body)) {
			// An example host (registry.example.com) is also how workflow
			// prose gives an example ("e.g. harbor.example.com"); in a
			// repository's own files it is not a reliable sign of an unfilled
			// value. init, which scans only the examples it copied, reports it.
			if strings.Contains(p.Token, ".example.") {
				continue
			}

			found = append(found, fmt.Sprintf("%s:%d %s", rel, p.Line, p.Token))
			blocking = blocking || !releaseOnly(rel)

			if hint := p.Token + ": " + consumerinit.Hint(p.Token); !slices.Contains(hints, hint) {
				hints = append(hints, hint)
			}
		}
	}

	if len(found) == 0 {
		return Check{
			Name:     CheckPlaceholders,
			Severity: SeverityOK,
			Message:  fmt.Sprintf("no example placeholders left in %d file(s)", len(files)),
		}, true
	}

	check := Check{
		Name:        CheckPlaceholders,
		Severity:    SeverityFail,
		Message:     fmt.Sprintf("%d example value(s) still to fill in: %s", len(found), listed(found)),
		Remediation: strings.Join(hints, "\n            "),
	}

	if !blocking {
		check.Severity = SeverityWarn
		check.Message += " (release workflow only: the pull request runs without them)"
	}

	return check, true
}

// releaseOnly reports whether rel is a workflow only a release runs: the
// examples name those release*.yml (release-workflow.yml, release.yml). Any
// other file, artifacts.yml and GitLab's single .gitlab-ci.yml included, can
// stop a pull request, so its values fail.
func releaseOnly(rel string) bool {
	return strings.HasPrefix(path.Base(rel), "release")
}

// checkAdminPolicyResolves fails where every gate would refuse to run: an
// environment variable named like an administrator setting. The gates read
// the settings only from the command line, and stop rather than guess.
func checkAdminPolicyResolves(environ []string) Check {
	if err := enforcement.RefuseEnvironment(environ); err != nil {
		return Check{
			Name:        CheckAdminPolicy,
			Severity:    SeverityFail,
			Message:     err.Error(),
			Remediation: "unset the variable; an administrator passes the setting as a --enforce-* flag (forge-ci docs admin-policy)",
		}
	}

	return Check{Name: CheckAdminPolicy, Severity: SeverityOK, Message: "no administrator setting in the environment"}
}

// checkArtifactPathsExist fails an artifact whose working directory, or Go
// main package, is not in the repository: the example's ./cmd/my-cli parses
// and validates, and the build fails in CI with "directory not found".
func checkArtifactPathsExist(root string, artifacts []config.Artifact) Check {
	var missing []string

	for _, a := range artifacts {
		dir := a.WorkingDirectory
		if dir == "" {
			dir = "."
		}

		if !dirExists(filepath.Join(root, filepath.FromSlash(dir))) {
			missing = append(missing, fmt.Sprintf("%s: working-directory %s", a.Name, dir))

			continue
		}

		if a.Go != nil && a.Go.MainPackage != "" && !strings.Contains(a.Go.MainPackage, "...") {
			pkg := filepath.Join(root, filepath.FromSlash(dir), filepath.FromSlash(a.Go.MainPackage))
			if !dirHasGoFiles(pkg) {
				missing = append(missing, fmt.Sprintf("%s: main-package %s (no .go files in %s)", a.Name, a.Go.MainPackage, relTo(root, pkg)))
			}
		}
	}

	if len(missing) > 0 {
		return Check{
			Name:     CheckArtifactPaths,
			Severity: SeverityFail,
			Message:  strings.Join(missing, "; "),
			Remediation: "point working-directory at the directory holding the artifact's build file, and main-package at the " +
				"Go package with func main (\".\" at the module root, ./cmd/<name> below it)",
		}
	}

	return Check{
		Name:     CheckArtifactPaths,
		Severity: SeverityOK,
		Message:  fmt.Sprintf("%d artifact(s); every working directory and Go main package exists", len(artifacts)),
	}
}

func dirExists(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.IsDir()
}

func dirHasGoFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}

	return slices.ContainsFunc(entries, func(e os.DirEntry) bool {
		return !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go")
	})
}

func relTo(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}

	return filepath.ToSlash(rel)
}

// listed joins items, keeping the first maxListed and counting the rest.
func listed(items []string) string {
	if len(items) <= maxListed {
		return strings.Join(items, ", ")
	}

	return strings.Join(items[:maxListed], ", ") + fmt.Sprintf(", and %d more", len(items)-maxListed)
}

// Find returns the first check named name, and whether there is one.
func Find(checks []Check, name string) (Check, bool) {
	for _, c := range checks {
		if c.Name == name {
			return c, true
		}
	}

	return Check{}, false
}
