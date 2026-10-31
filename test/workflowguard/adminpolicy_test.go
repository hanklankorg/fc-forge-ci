// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"gopkg.in/yaml.v3"
)

// The engine scans every exported layout. Its provider must install the scanner
// even when a legacy consumer passes enable-scan=false, and preserve reports
// after a blocking scan rather than silently losing the evidence.
func TestPublishedLayoutScanCannotLosePrerequisiteOrReports(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile(filepath.Join(reporoot.Path(t), ".github/workflows/publish-container.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
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
			switch step.Name {
			case "Install trivy":
				install = index
				if strings.Contains(step.If, "enable-scan") || !strings.Contains(step.If, "push-image") {
					t.Fatalf("consumer can disable scan prerequisite: %s", step.If)
				}
			case "Write build plan":
				if step.Env["ENABLE_SCAN"] != "${{ inputs['push-image'] }}" {
					t.Fatalf("scan plan is consumer-controlled: %v", step.Env)
				}
			case "Build and scan retained layout":
				build = index
			case "Save container scan SARIF artifact", "Save GitLab container-scanning report", "Upload container scan results to Code Scanning":
				if !strings.Contains(step.If, "always()") || strings.Contains(step.If, "enable-scan") {
					t.Fatalf("blocking findings lose reports at %s: %s", step.Name, step.If)
				}
			}
		}
		if build >= 0 {
			checked = true
			if install < 0 || install >= build {
				t.Fatal("layout scan runs before its scanner prerequisite")
			}
		}
	}
	if !checked {
		t.Fatal("no exported-layout build was checked")
	}
}

func TestRuntimePublicationInstallsScannerBeforeItsMandatoryGate(t *testing.T) {
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
