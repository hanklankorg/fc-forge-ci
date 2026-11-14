// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package doctor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/app/doctor"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestRun_GitLabComponentJobNames(t *testing.T) {
	t.Parallel()

	const (
		check  = "  - component: gitlab.example.com/team/ci/forge-ci/check@1.0.0\n"
		goJob  = "  - component: gitlab.example.com/team/ci/forge-ci/build-go@1.0.0\n"
		inputs = "    inputs:\n      job-name: "
	)

	for _, tc := range []struct {
		name, pipeline string
		severity       doctor.Severity
		mentions       string
	}{
		{"distinct defaults", "include:\n" + check + goJob, doctor.SeverityOK, ""},
		{"one component twice", "include:\n" + check + check, doctor.SeverityFail, `"check" from check and check`},
		{"distinct job names", "include:\n" + check + check + inputs + "check-docs\n", doctor.SeverityOK, ""},
		{"a job name equal to another default", "include:\n" + check + goJob + inputs + "check\n", doctor.SeverityFail, `"check" from check and build-go`},
		{"a single include", "include:\n  component: gitlab.example.com/team/ci/forge-ci/check@1.0.0\n", doctor.SeverityOK, ""},
		{"an evaluated job name", "include:\n" + check + check + inputs + "$[[ inputs.name ]]\n", doctor.SeverityOK, ""},
		{"another project's component", "include:\n" + check + "  - component: gitlab.example.com/team/other/check@1.0.0\n", doctor.SeverityOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := writeRepo(t, "artifacts:\n  - name: app\n    project-type: meta\n", map[string]string{".gitlab-ci.yml": tc.pipeline})

			checks, err := doctor.Run(doctor.Input{Root: root, RepoSlug: "diggsweden/forge-ci"})
			if err != nil {
				t.Fatal(err)
			}

			got := findCheck(checks, "gitlab component job names")
			if got == nil || got.Severity != tc.severity || !strings.Contains(got.Message, tc.mentions) {
				t.Fatalf("job-name check = %+v, want %s mentioning %q", got, tc.severity, tc.mentions)
			}
		})
	}

	checks, err := doctor.Run(doctor.Input{Root: writeRepo(t, "artifacts:\n  - name: app\n    project-type: meta\n", nil), RepoSlug: "diggsweden/forge-ci"})
	if err != nil {
		t.Fatal(err)
	}

	if got := findCheck(checks, "gitlab component job names"); got != nil {
		t.Fatalf("a repository without .gitlab-ci.yml got %+v", got)
	}
}

// TestGitLabComponents_JobNameDefaultsToTheComponentName pins what the doctor
// check assumes: a component with a job-name input defaults it to its own name.
func TestGitLabComponents_JobNameDefaultsToTheComponentName(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(reporoot.Path(t), "templates")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range entries {
		name, isTemplate := strings.CutSuffix(entry.Name(), ".yml")
		if entry.IsDir() || !isTemplate {
			continue
		}

		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}

		var header struct {
			Spec struct {
				Inputs map[string]struct {
					Default any `yaml:"default"`
				} `yaml:"inputs"`
			} `yaml:"spec"`
		}
		if err := yaml.NewDecoder(strings.NewReader(string(body))).Decode(&header); err != nil {
			t.Fatalf("%s: %v", entry.Name(), err)
		}

		if input, ok := header.Spec.Inputs["job-name"]; ok && input.Default != name {
			t.Errorf("%s: job-name defaults to %v, want %q", entry.Name(), input.Default, name)
		}
	}
}
