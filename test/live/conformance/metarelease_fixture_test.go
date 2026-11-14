// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package conformance_test

import (
	_ "embed"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

// The meta case's declaration, shared with the live owner.
//
//go:embed testdata/meta-artifacts.yml
var layerMetaDeclaration string

const (
	// releaseCommitJob is the job that verifies the planned bump and signs
	// the release commit; the version jobs before it are named by family.
	releaseCommitJob     = "Prepare: release commit and tag"
	releaseVersionJobs   = "Prepare: version "
	releaseVersionPlan   = releaseVersionJobs + "plan"
	releaseVersionNoBump = releaseVersionJobs + "none"

	// metaBumpLine is what the engine's bump prints for a meta project: the
	// version is recorded for the changelog, and no file is its to change.
	metaBumpLine = "Meta project type - no version file to update"
)

// metaReleaseFindings holds a release to its meta declaration. The
// declaration names meta projects and nothing else, so the engine has no
// build, bump or image of its own to add; the run then has no version job of
// a toolchain family; the release commit changes the changelog and no other
// file; and the release commit job's log shows the bump read the meta project
// from the declaration, which a release without one, or one the engine
// ignored, does not print.
func metaReleaseFindings(declaration string, jobs, changed []string, commitLog string) []string {
	var findings []string

	parsed, err := config.Parse([]byte(declaration))

	switch {
	case err != nil:
		return []string{"meta declaration: " + err.Error()}
	case len(parsed.Artifacts) == 0 || len(parsed.Containers) != 0:
		findings = append(findings, fmt.Sprintf("the declaration has %d artifacts and %d containers, want meta projects alone", len(parsed.Artifacts), len(parsed.Containers)))
	}

	for _, artifact := range parsed.Artifacts {
		if artifact.ProjectType != projecttype.Meta {
			findings = append(findings, fmt.Sprintf("artifact %s is a %s project, not meta", artifact.Name, artifact.ProjectType))
		}
	}

	for _, job := range jobs {
		if strings.HasPrefix(job, releaseVersionJobs) && job != releaseVersionPlan && job != releaseVersionNoBump && !strings.HasPrefix(job, releaseVersionNoBump+" (") {
			findings = append(findings, fmt.Sprintf("job %q bumps a version, and a meta release has none to bump", job))
		}
	}

	if !slices.Equal(changed, []string{releaseChangelog}) {
		findings = append(findings, fmt.Sprintf("the release commit changes %v, and a meta release changes %s alone", changed, releaseChangelog))
	}

	if !strings.Contains(commitLog, metaBumpLine) {
		findings = append(findings, fmt.Sprintf("the log of %q lacks %q: the release was not planned from the meta declaration", releaseCommitJob, metaBumpLine))
	}

	return findings
}

func TestMetaReleaseFindings(t *testing.T) {
	t.Parallel()

	jobs := []string{"Prepare: changelog", releaseVersionPlan, releaseVersionNoBump + " ()", releaseCommitJob, "Publish: release"}
	log := "Bumping version to 0.0.1 for meta project in .\n" + metaBumpLine + "\n"

	changelog := []string{releaseChangelog}

	for name, test := range map[string]struct {
		declaration   string
		jobs, changed []string
		log           string
		want          string
	}{
		"meta":                {declaration: layerMetaDeclaration, jobs: jobs, changed: changelog, log: log},
		"a buildable project": {declaration: "artifacts:\n  - name: app\n    project-type: go\n", jobs: jobs, changed: changelog, log: log, want: "is a go project, not meta"},
		"no project":          {declaration: "artifacts: []\n", jobs: jobs, changed: changelog, log: log, want: "want meta projects alone"},
		"an image": {
			declaration: layerMetaDeclaration + "containers:\n  - name: app\n    container-file: Containerfile\n", jobs: jobs, changed: changelog, log: log,
			want: "want meta projects alone",
		},
		"not a declaration": {declaration: "artifacts: {", jobs: jobs, changed: changelog, log: log, want: "meta declaration:"},
		"a version job":     {declaration: layerMetaDeclaration, jobs: append([]string{"Prepare: version rust"}, jobs...), changed: changelog, log: log, want: "bumps a version"},
		"a bumped manifest": {
			declaration: layerMetaDeclaration, jobs: jobs, changed: []string{releaseChangelog, "package.json"}, log: log,
			want: "changes CHANGELOG.md alone",
		},
		"no release commit": {declaration: layerMetaDeclaration, jobs: jobs, log: log, want: "changes CHANGELOG.md alone"},
		"declaration unread": {
			declaration: layerMetaDeclaration, jobs: jobs, changed: changelog, log: "Go project type - no version file to update\n",
			want: "was not planned from the meta declaration",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			findings := metaReleaseFindings(test.declaration, test.jobs, test.changed, test.log)
			if test.want == "" && len(findings) != 0 || test.want != "" && (len(findings) != 1 || !strings.Contains(findings[0], test.want)) {
				t.Fatalf("findings %q, want one holding %q", findings, test.want)
			}
		})
	}
}
