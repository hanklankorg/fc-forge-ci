// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package doctor

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/release"
	"gopkg.in/yaml.v3"
)

// RequirementsReport covers known release entry points, not arbitrary user jobs.
// Complete is about static coverage, never remote readiness.
type RequirementsReport struct {
	Scope    string      `json:"scope"`
	Complete bool        `json:"complete"`
	Calls    []SetupCall `json:"calls"`
	Unknowns []string    `json:"unknowns"`
	Notes    []string    `json:"notes"`
}

// SetupCall binds advice to a caller location and an engine revision.
type SetupCall struct {
	Location     string         `json:"location"`
	Forge        string         `json:"forge"`
	Workflow     string         `json:"workflow"`
	Revision     string         `json:"revision"`
	Contract     string         `json:"contract"`
	Requirements []SetupItem    `json:"requirements"`
	Settings     []SetupSetting `json:"settings"`
}

// SetupItem adds a value-free source description to a requirement.
type SetupItem struct {
	release.SetupRequirement

	Source string `json:"source"`
}

// SetupSetting describes a public input's source without exposing its value.
type SetupSetting struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	Purpose string `json:"purpose"`
}

var setupReference = regexp.MustCompile(`^\$\{\{\s*(secrets|vars)\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}$`)
var setupRevision = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)

const (
	setupGitHub          = "github"
	setupGitLab          = "gitlab"
	setupForgejo         = "forgejo"
	setupPrepareWorkflow = "prepare-release.yml"
)

// Requirements reads local YAML only. Recognized entry points receive reference
// guidance; only an exact known build revision has revision-matched coverage.
func Requirements(root, repo, commit string) RequirementsReport {
	report := RequirementsReport{
		Scope:    "Known release entry points only; custom jobs, remote secrets, runners and permissions are not checked",
		Complete: true, Calls: []SetupCall{}, Unknowns: []string{}, Notes: []string{},
	}

	if root == "" {
		root = "."
	}

	if repo == "" {
		repo = deriveSelfRepoSlug()
	}

	report.scanActions(root, repo, commit)

	if _, err := os.Lstat(filepath.Join(root, ".gitlab-ci.yml")); !os.IsNotExist(err) {
		doc := report.read(root, ".gitlab-ci.yml")
		for _, include := range gitLabIncludes(yamlChild(doc, "include")) {
			component := yamlChild(include, "component")
			if component == nil {
				report.unknown(".gitlab-ci.yml: only direct component includes are inspected; local/project/remote includes are not expanded")

				continue
			}

			report.inspect(root, repo, commit, setupGitLab, ".gitlab-ci.yml", component, yamlChild(include, "inputs"), nil, nil)
		}
	}

	if len(report.Calls) == 0 {
		report.unknown("No recognized release entry points found; custom workflows and indirect includes need manual review")
	}

	return report
}

func (report *RequirementsReport) unknown(message string) {
	report.Complete = false
	report.Unknowns = append(report.Unknowns, message)
}

func (report *RequirementsReport) read(root, rel string) *yaml.Node {
	path := filepath.Join(root, filepath.FromSlash(rel))

	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 || !setupPathWithin(root, path) {
		report.unknown(fmt.Sprintf("%q: expected a readable regular YAML file (limit 1 MiB)", rel))

		return nil
	}

	boundary, err := os.OpenRoot(root)
	if err != nil {
		report.unknown(fmt.Sprintf("%q: cannot open repository root", rel))

		return nil
	}

	defer func() { _ = boundary.Close() }()

	f, err := boundary.Open(filepath.FromSlash(rel))
	if err != nil {
		report.unknown(fmt.Sprintf("%q: cannot read YAML", rel))

		return nil
	}
	defer func() { _ = f.Close() }()

	body, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))

	var doc yaml.Node

	decoder := yaml.NewDecoder(bytes.NewReader(body))

	var extra yaml.Node
	if err != nil || len(body) > 1<<20 || decoder.Decode(&doc) != nil || !errors.Is(decoder.Decode(&extra), io.EOF) || !simpleSetupYAML(&doc, 0) || len(doc.Content) != 1 {
		// YAML decoder errors can quote a line containing an inline credential.
		report.unknown(fmt.Sprintf("%q: malformed YAML or unsupported aliases, merges, duplicate keys or nesting", rel))

		return nil
	}

	return doc.Content[0]
}

func setupPathWithin(root, path string) bool {
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}

	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}

	rel, err := filepath.Rel(base, target)

	return err == nil && filepath.IsLocal(rel)
}

func simpleSetupYAML(node *yaml.Node, depth int) bool {
	if depth > 40 || node.Kind == yaml.AliasNode || node.Anchor != "" {
		return false
	}

	if node.Kind == yaml.MappingNode {
		seen := map[string]bool{}

		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Value == "<<" || seen[key.Value] {
				return false
			}

			seen[key.Value] = true
		}
	}

	for _, child := range node.Content {
		if !simpleSetupYAML(child, depth+1) {
			return false
		}
	}

	return true
}

func setupScalar(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}

	return n.Value
}

func setupContract(raw, forge, repo string) (string, bool) {
	if repo == "" {
		return "", false
	}

	location, ref, ok := strings.Cut(raw, "@")
	if !ok || !safeSetupRef(ref) {
		return "", false
	}

	location = setupLocation(location, forge)
	if forge == setupGitLab {
		return "release", location == repo+"/release"
	}

	prefix := repo + "/." + forge + "/workflows/"
	if !strings.HasPrefix(location, prefix) {
		return "", false
	}

	name := strings.TrimPrefix(location, prefix)

	return name, (forge == setupGitHub && name == "release-orchestrator.yml") ||
		(forge == setupForgejo && (name == setupPrepareWorkflow || name == "sign-and-publish-release.yml"))
}

// Classification only, not contract recognition: unknown release calls must
// remain visible, while unrelated reusable build/check jobs are outside scope.
func setupReleaseCandidate(raw, forge string) bool {
	if raw == "" || strings.Contains(raw, "${") || strings.Contains(raw, "$[[") {
		return true
	}

	location, _, _ := strings.Cut(raw, "@")

	name := location[strings.LastIndex(location, "/")+1:]
	if forge != setupGitLab {
		name = strings.TrimSuffix(strings.TrimSuffix(name, ".yml"), ".yaml")
	}

	return strings.Contains(strings.ToLower(name), "release")
}

// A ref is public metadata, but an unrecognized scalar must not be echoed as
// metadata: malformed uses values can accidentally contain a credential.
func safeSetupRef(ref string) bool {
	return setupRevision.MatchString(ref)
}

func setupChoice(inputs *yaml.Node, name string, defaultValue bool) release.SetupState {
	n := yamlChild(inputs, name)
	if n == nil {
		if defaultValue {
			return release.SetupRequired
		}

		return release.SetupUnused
	}

	switch setupScalar(n) {
	case "true":
		return release.SetupRequired
	case "false":
		return release.SetupUnused
	default:
		return release.SetupConditional
	}
}

func setupSource(n *yaml.Node, namespace string) (string, bool) {
	if n == nil {
		return "not mapped in caller", false
	}

	if match := setupReference.FindStringSubmatch(setupScalar(n)); len(match) == 3 && match[1] == namespace {
		return namespace + "." + match[2], true
	}

	if namespace == "vars" && n.Kind == yaml.ScalarNode && n.Value != "" && !strings.Contains(n.Value, "$") {
		return "literal in caller (value omitted)", true
	}

	return "cannot resolve mapping (value omitted)", false
}

// FormatRequirements prints an actionable report without echoing YAML values.
func FormatRequirements(out io.Writer, report RequirementsReport) {
	_, _ = fmt.Fprintln(out, "\nRelease setup requirements (not remote readiness)")
	for _, call := range report.Calls {
		_, _ = fmt.Fprintf(out, "\n%s: %s / %s @ %s\nContract: %s\n", call.Location, call.Forge, call.Workflow, call.Revision, call.Contract)
		for _, item := range call.Requirements {
			if item.State == release.SetupUnused {
				continue
			}

			_, _ = fmt.Fprintf(out, "  %-15s %s ← %s\n", item.State, item.Name, item.Source)

			// What the credential is for and what it must be allowed to do
			// are what a person creating it needs; they were JSON-only.
			if item.Purpose != "" {
				_, _ = fmt.Fprintf(out, "  %-15s   for: %s\n", "", item.Purpose)
			}

			if item.Authority != "" {
				_, _ = fmt.Fprintf(out, "  %-15s   needs: %s\n", "", item.Authority)
			}
		}

		for _, item := range call.Settings {
			_, _ = fmt.Fprintf(out, "  setting         %s ← %s\n", item.Name, item.Source)
		}
	}

	for _, message := range report.Unknowns {
		_, _ = fmt.Fprintln(out, "  COVERAGE:", message)
	}

	for _, message := range report.Notes {
		_, _ = fmt.Fprintln(out, "  NOTE:", message)
	}

	_, _ = fmt.Fprintln(out, "Scope:", report.Scope)
	_, _ = fmt.Fprintln(out, "Details (job scope and unused settings): forge-ci doctor --requirements --json")
	_, _ = fmt.Fprintln(out, "Next: create the credentials listed above; forge-ci setup secrets prints the command that stores each one. "+
		"Reference guidance is not proof of the pinned workflow's requirements or remote readiness; see forge-ci docs setup-requirements.")
}

func (report *RequirementsReport) scanActions(root, repo, commit string) {
	for _, forge := range []string{setupGitHub, setupForgejo} {
		dir := filepath.Join(root, "."+forge, "workflows")

		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}

		if err != nil {
			report.unknown("Cannot read " + forge + " workflows")

			continue
		}

		for _, entry := range entries {
			if entry.IsDir() || (filepath.Ext(entry.Name()) != ".yml" && filepath.Ext(entry.Name()) != ".yaml") {
				continue
			}

			rel := filepath.ToSlash(filepath.Join("."+forge, "workflows", entry.Name()))
			doc := report.read(root, rel)

			jobs := yamlChild(doc, "jobs")
			if jobs == nil || jobs.Kind != yaml.MappingNode {
				continue
			}

			for i := 1; i < len(jobs.Content); i += 2 {
				job := jobs.Content[i]

				uses := yamlChild(job, "uses")
				if uses == nil {
					continue
				}

				report.inspect(root, repo, commit, forge, rel, uses, yamlChild(job, "with"), yamlChild(job, "secrets"), jobs)
			}
		}
	}
}

func setupLocation(location, forge string) string {
	if strings.HasPrefix(location, "https://") {
		u, err := url.Parse(location)
		if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return ""
		}

		location = strings.TrimPrefix(u.Path, "/")
	} else if forge == setupGitLab {
		_, remainder, ok := strings.Cut(location, "/") // component host/project/name
		location = remainder

		if !ok {
			return ""
		}
	}

	return location
}
