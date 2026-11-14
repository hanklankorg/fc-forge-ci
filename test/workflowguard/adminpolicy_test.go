// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"gopkg.in/yaml.v3"
)

// The engine scans every exported layout. Its provider must install the scanner
// whenever it exports one, offer no consumer scan toggle, and preserve reports
// after a blocking scan rather than silently losing the evidence.
func TestPublishedLayoutScanCannotLosePrerequisiteOrReports(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile(filepath.Join(reporoot.Path(t), ".github/workflows/publish-container.yml"))
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(body), "install-trivy.sh") || strings.Contains(string(body), "install_trivy") {
		t.Fatal("the host build legs install a scanner; the scan runs in the full runtime image")
	}

	var workflow struct {
		Jobs map[string]struct {
			If        string   `yaml:"if"`
			Needs     []string `yaml:"needs"`
			Container struct {
				Image string `yaml:"image"`
			} `yaml:"container"`
			Steps []struct {
				Name string            `yaml:"name"`
				If   string            `yaml:"if"`
				Env  map[string]string `yaml:"env"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		t.Fatal(err)
	}

	const exported = "${{ inputs['push-image'] || needs.prep.outputs.builder == 'kaniko' }}"

	build, scan, publish := workflow.Jobs["build-arch"], workflow.Jobs["scan-layouts"], workflow.Jobs["publish-images"]
	if scan.Container.Image != "${{ inputs['runtime-image-full'] }}" || scan.If != exported {
		t.Fatalf("the layout scan must run in the full image for every exported layout, with no consumer toggle: image=%q if=%q", scan.Container.Image, scan.If)
	}

	if !slices.Contains(publish.Needs, "scan-layouts") {
		t.Fatalf("publication does not wait for the layout scan: %v", publish.Needs)
	}

	planned, uploaded := false, false

	for _, step := range build.Steps {
		switch step.Name {
		case "Write build plan":
			if _, ok := step.Env["ENABLE_SCAN"]; ok {
				t.Fatalf("the build plan carries a scan toggle the engine does not read: %v", step.Env)
			}

			if step.Env["BUILD_DEFER_SCAN"] != "true" || !strings.Contains(step.Run, `--set "defer-scan=${BUILD_DEFER_SCAN}"`) {
				t.Fatalf("the build must leave its exported layout to the scan job: env=%v run=%s", step.Env, step.Run)
			}

			planned = true
		case "Upload staged OCI layout":
			if step.If != exported {
				t.Fatalf("a layout the scan job needs is not uploaded for every exported build: %s", step.If)
			}

			uploaded = true
		}
	}

	if !planned || !uploaded {
		t.Fatal("no exported-layout build was checked")
	}

	scanned, reports := false, 0

	for _, step := range scan.Steps {
		switch step.Name {
		case "Scan the staged layout before publication":
			if !strings.Contains(step.Run, "security scan container") || !strings.Contains(step.Run, "--enforce-vulnerabilities") || step.Env["CONTAINER_OCI_LAYOUT"] == "" || step.If != "" {
				t.Fatalf("the layout scan is not the gated engine scan of the downloaded layout: if=%q env=%v run=%s", step.If, step.Env, step.Run)
			}

			scanned = true
		case "Save container scan SARIF artifact", "Save GitLab container-scanning report", "Upload container scan results to Code Scanning":
			if !strings.Contains(step.If, "always()") || strings.Contains(step.If, "enable-scan") {
				t.Fatalf("blocking findings lose reports at %s: %s", step.Name, step.If)
			}

			reports++
		}
	}

	if !scanned || reports != 3 {
		t.Fatalf("the layout scan job was not checked: scanned=%v reports=%d", scanned, reports)
	}
}

// Snapshot images publish too: every published image is scanned, whatever
// the plan's legacy scan field says. On GitHub the publish job runs in the
// full runtime image, where Trivy is baked in, with no install step a
// consumer could condition; the Forgejo and GitLab providers install the
// scanner for every image.
func TestSnapshotPublicationAlwaysInstallsTheScanner(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)

	github, err := os.ReadFile(filepath.Join(root, ".github/workflows/snapshot-images.yml"))
	if err != nil {
		t.Fatal(err)
	}

	var workflow struct {
		Jobs map[string]struct {
			Container struct {
				Image string `yaml:"image"`
			} `yaml:"container"`
			Steps []struct {
				Name string            `yaml:"name"`
				If   string            `yaml:"if"`
				Env  map[string]string `yaml:"env"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(github, &workflow); err != nil {
		t.Fatal(err)
	}

	publish := workflow.Jobs["publish"]
	if publish.Container.Image != "${{ inputs['runtime-image-full'] }}" {
		t.Fatalf("the GitHub snapshot publisher does not run where the scanner is baked in: %q", publish.Container.Image)
	}

	checked := false

	for _, step := range publish.Steps {
		if strings.Contains(step.Run, "install_trivy") || strings.Contains(step.Run, "install_syft") || step.Name == "Install configured evidence tools" {
			t.Fatalf("the GitHub snapshot publisher installs evidence tools: %s", step.Name)
		}

		if !strings.Contains(step.Run, "snapshot-images publish") {
			continue
		}

		checked = true

		if step.If != "" || step.Env["SCAN"] != "" || strings.Contains(step.Run, "SCAN") {
			t.Fatalf("the snapshot publication is conditional on a scan toggle: if=%q env=%v run=%s", step.If, step.Env, step.Run)
		}
	}

	if !checked {
		t.Fatal("no GitHub snapshot publication step was checked")
	}

	forgejo, err := os.ReadFile(filepath.Join(root, ".forgejo/bin/snapshot-images.sh"))
	if err != nil {
		t.Fatal(err)
	}

	_, tools, found := strings.Cut(string(forgejo), "snapshot_evidence_tools() {")
	tools, _, _ = strings.Cut(tools, "\n}\n")

	if !found || strings.Contains(tools, "--field scan") || !strings.Contains(tools, "\n  install_trivy\n") {
		t.Fatalf("the Forgejo/GitLab snapshot scanner install is conditional:\n%s", tools)
	}
}

func TestRuntimePublicationInstallsScannerBeforeItsScan(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile(filepath.Join(reporoot.Path(t), ".github/workflows/self-runtime-container.yml"))
	if err != nil {
		t.Fatal(err)
	}

	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Run  string            `yaml:"run"`
				Uses string            `yaml:"uses"`
				If   string            `yaml:"if"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		t.Fatal(err)
	}

	checked := false

	for _, job := range workflow.Jobs {
		install, build := -1, -1

		for index, step := range job.Steps {
			if strings.Contains(step.Run, "source scripts/bootstrap/install-trivy.sh") && strings.Contains(step.Run, "install_trivy") {
				install = index
			}

			if step.Env["BUILD_MODE"] == "push-by-digest" && strings.Contains(step.Run, "forge-ci container build") {
				build = index

				if !strings.Contains(step.Run, `--enforce-vulnerabilities="${RUNTIME_ADMIN_VULNERABILITIES:?}"`) ||
					step.Env["RUNTIME_ADMIN_VULNERABILITIES"] != "${{ needs.image-coordinates.outputs.enforce-vulnerabilities }}" {
					t.Fatal("runtime publication does not scan under its administrator's setting")
				}
			}

			if strings.HasPrefix(step.Uses, "aquasecurity/trivy-action@") {
				t.Fatal("runtime publication has a second advisory scan outside the engine policy")
			}

			if step.Name == "Save runtime scan reports" || step.Name == "Upload trivy SARIF to Code Scanning" {
				if !strings.Contains(step.If, "always()") {
					t.Fatalf("runtime gate failures lose evidence at %s", step.Name)
				}
			}
		}

		if build >= 0 {
			checked = true

			if install < 0 || install >= build {
				t.Fatal("runtime publication scans before installing Trivy")
			}
		}
	}

	if !checked {
		t.Fatal("no runtime publication build was checked")
	}
}

// The engine's own runtime images report their findings unless the
// repository's administrator opts in: only the variable's value `enforce`
// turns the gate on, and a value that is neither it nor unset refuses the run,
// since whoever set it meant to enforce.
func TestRuntimePublicationEnforcesOnlyOnItsAdministratorsWord(t *testing.T) {
	t.Parallel()

	var workflow struct {
		Jobs map[string]struct {
			Outputs map[string]string `yaml:"outputs"`
			Steps   []struct {
				ID  string            `yaml:"id"`
				Run string            `yaml:"run"`
				Env map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(reporoot.ReadFile(t, ".github/workflows/self-runtime-container.yml"), &workflow); err != nil {
		t.Fatal(err)
	}

	job := workflow.Jobs["image-coordinates"]
	if job.Outputs["enforce-vulnerabilities"] != "${{ steps.vulnerabilities.outputs.enforce-vulnerabilities }}" {
		t.Fatalf("the setting the build reads is not the resolved one: %q", job.Outputs["enforce-vulnerabilities"])
	}

	script := ""

	for _, step := range job.Steps {
		if step.ID == "vulnerabilities" {
			if step.Env["SETTING"] != "${{ vars.FORGE_CI_RUNTIME_VULNERABILITIES }}" {
				t.Fatalf("the setting is read from %q", step.Env["SETTING"])
			}

			script = step.Run
		}
	}

	if script == "" {
		t.Fatal("no step resolves the runtime vulnerability setting")
	}

	for _, tc := range []struct{ setting, want string }{
		{"", "enforce-vulnerabilities=false\n"},
		{"enforce", "enforce-vulnerabilities=true\n"},
		{"report", ""},
		{"true", ""},
		{"Enforce", ""},
		{"enforce ", ""},
	} {
		output := filepath.Join(t.TempDir(), "output")
		cmd := exec.CommandContext(t.Context(), "bash", "-c", script)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "SETTING=" + tc.setting, "GITHUB_OUTPUT=" + output}
		out, err := cmd.CombinedOutput()

		if tc.want == "" {
			if err == nil {
				t.Errorf("FORGE_CI_RUNTIME_VULNERABILITIES=%q is accepted: %s", tc.setting, out)
			}

			if _, statErr := os.Stat(output); statErr == nil {
				t.Errorf("FORGE_CI_RUNTIME_VULNERABILITIES=%q is refused but resolved", tc.setting)
			}

			continue
		}

		if err != nil {
			t.Fatalf("FORGE_CI_RUNTIME_VULNERABILITIES=%q: %v: %s", tc.setting, err, out)
		}

		if got, _ := os.ReadFile(output); string(got) != tc.want {
			t.Errorf("FORGE_CI_RUNTIME_VULNERABILITIES=%q resolves %q, want %q", tc.setting, got, tc.want)
		}
	}
}
