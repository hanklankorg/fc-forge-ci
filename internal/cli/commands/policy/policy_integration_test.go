// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build integration

package policy_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/isolatedenv"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// Build overlays exercise real compiled administrator distributions without
// modifying the checkout's policy, injecting a runtime override, or contacting
// a provider. Child exit statuses and reports travel through the real CLI.
func TestCompiledAdminPolicyCannotBeOverriddenByConsumers(t *testing.T) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	envCommand := exec.CommandContext(t.Context(), goTool, "env", "GOCACHE", "GOMODCACHE")
	caches, err := envCommand.Output()
	if err != nil {
		t.Fatal(err)
	}
	cachePaths := strings.Fields(string(caches))
	if len(cachePaths) != 2 {
		t.Fatalf("unexpected Go cache coordinates: %s", caches)
	}
	isolatedenv.Isolate(t)
	t.Setenv("GOCACHE", cachePaths[0])
	t.Setenv("GOMODCACHE", cachePaths[1])
	root := reporoot.Path(t)
	consumer := t.TempDir()
	write := func(path, body string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(consumer, "policy.json"), `{"enforce-lint":false,"enforce-vulnerabilities":false,"enforce-application-tests":false}`, 0o600)
	write(filepath.Join(consumer, "go.mod"), "module example.invalid/admin-policy-fixture\n\ngo 1.27.3\n", 0o600)
	write(filepath.Join(consumer, "main.go"), "package main\nfunc main() {}\n", 0o600)
	write(filepath.Join(consumer, "failure_test.go"), "package main\nimport \"testing\"\nfunc TestAssertion(t *testing.T) { t.Error(\"real native assertion failure\") }\n", 0o600)
	t.Setenv("FORGE_CI_POLICY", filepath.Join(consumer, "policy.json"))
	t.Setenv("ENFORCE_LINT", "false")
	t.Setenv("ENFORCE_VULNERABILITIES", "false")
	t.Setenv("ENFORCE_APPLICATION_TESTS", "false")
	for _, variant := range []struct {
		name                         string
		lint, vulnerabilities, tests bool
	}{
		{"enforcing", true, true, true}, {"mixed", false, true, false}, {"report-only", false, false, false},
	} {
		t.Run(variant.name, func(t *testing.T) {
			dir := t.TempDir()
			policyFile := filepath.Join(dir, "admin-policy.json")
			policy, _ := json.Marshal(map[string]bool{"enforce-lint": variant.lint, "enforce-vulnerabilities": variant.vulnerabilities, "enforce-application-tests": variant.tests})
			write(policyFile, string(policy), 0o600)
			overlay, _ := json.Marshal(map[string]any{"Replace": map[string]string{filepath.Join(root, "internal/domain/enforcement/policy.json"): policyFile}})
			overlayFile := filepath.Join(dir, "overlay.json")
			write(overlayFile, string(overlay), 0o600)
			binary := filepath.Join(dir, "forge-ci")
			build := exec.CommandContext(t.Context(), goTool, "build", "-overlay", overlayFile, "-o", binary, "./cmd/forge-ci")
			build.Dir = root
			if output, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build policy variant: %v\n%s", err, output)
			}
			run := func(wantSuccess bool, args ...string) string {
				t.Helper()
				command := exec.CommandContext(t.Context(), binary, args...)
				command.Dir = consumer
				body, err := command.CombinedOutput()
				if (err == nil) != wantSuccess {
					t.Fatalf("%v: err=%v\n%s", args, err, body)
				}
				return string(body)
			}
			shown := run(true, "policy", "show")
			var actual struct {
				Settings map[string]bool `json:"settings"`
				SHA256   string          `json:"sha256"`
			}
			if err := json.Unmarshal([]byte(shown), &actual); err != nil || len(actual.SHA256) != 64 || actual.Settings["enforce-lint"] != variant.lint || actual.Settings["enforce-vulnerabilities"] != variant.vulnerabilities || actual.Settings["enforce-application-tests"] != variant.tests {
				t.Fatalf("consumer replaced compiled policy: %s %v", shown, err)
			}
			for _, check := range []struct {
				id, class string
				enforce   bool
			}{{"format", "formatter", variant.lint}, {"osv", "security", variant.vulnerabilities}} {
				script := filepath.Join(dir, check.id+".sh")
				sarif := `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"nanolinter"}},"results":[{"ruleId":"finding","properties":{"source":"curated","check_id":"` + check.id + `","class":"` + check.class + `","status":"fail"}}]}]}`
				write(script, "#!/bin/sh\nprintf '%s\\n' '"+sarif+"'\nexit \"$1\"\n", 0o700)
				body := run(!check.enforce, "policy", "nanolinter", "--", "/bin/sh", script, "1")
				if !strings.Contains(body, "result=findings") {
					t.Fatal("findings were hidden")
				}
				for _, status := range []int{70, 125, 137} {
					run(false, "policy", "nanolinter", "--", "/bin/sh", script, strconv.Itoa(status))
				}
			}
			assertions := filepath.Join(dir, "tests.sh")
			runtimeLint := filepath.Join(dir, "runtime-lint.sh")
			write(runtimeLint, "#!/bin/sh\nprintf '%s\\n' '{\"version\":\"2.1.0\",\"runs\":[{\"tool\":{\"driver\":{\"name\":\"nanolinter\"}},\"results\":[{\"ruleId\":\"sast\",\"properties\":{\"source\":\"curated\",\"check_id\":\"sast\",\"class\":\"security\",\"status\":\"fail\",\"details\":\"tool failed (exit 2)\",\"exit_code\":2}}]}]}'\nexit 1\n", 0o700)
			run(false, "policy", "nanolinter", "--", "/bin/sh", runtimeLint)
			write(assertions, "#!/bin/sh\nprintf '%s\\n' 'Tests run: 4, Failures: 1, Errors: 0, Skipped: 0' '[ERROR] Failed to execute goal org.apache.maven.plugins:maven-surefire-plugin:3.5.4:test on project app: There are test failures.'\nexit 1\n", 0o700)
			run(!variant.tests, "policy", "tests", "--protocol", "maven", "--", "/bin/sh", assertions)
			write(assertions, "#!/bin/sh\nprintf '%s\\n' 'Tests run: 4, Failures: 0, Errors: 1, Skipped: 0'\nexit 1\n", 0o700)
			run(false, "policy", "tests", "--protocol", "maven", "--", "/bin/sh", assertions)
			write(assertions, "#!/bin/sh\nprintf '%s\\n' '> Task :test FAILED' '4 tests completed, 2 failed' 'FAILURE: Build failed with an exception.' '* What went wrong:' \"Execution failed for task ':test'.\" '> There were failing tests. See the report at: file:///fixture/report.html' '* Try:' '> Run with --stacktrace' 'BUILD FAILED in 2s'\nexit 1\n", 0o700)
			run(!variant.tests, "policy", "tests", "--protocol", "gradle", "--", "/bin/sh", assertions)
			write(assertions, "#!/bin/sh\nprintf '%s\\n' '> Task :test FAILED' '4 tests completed, 2 failed' 'FAILURE: Build failed with an exception.' '* What went wrong:' 'Gradle build daemon disappeared unexpectedly (it may have been killed or may have crashed)' '* Try:' '> Run with --stacktrace'\nexit 1\n", 0o700)
			run(false, "policy", "tests", "--protocol", "gradle", "--", "/bin/sh", assertions)
			// Exercise the engine-owned Go test path, not just the generic
			// workflow wrapper. The non-enforcing engine must actually run it.
			goResult := run(!variant.tests, "build", "go", "run", "--working-dir", consumer, "--build-sbom=false", "--platforms", "linux/amd64", "--version", "1.2.3")
			if !strings.Contains(goResult, "real native assertion failure") {
				t.Fatal("native application tests did not execute")
			}
			previousPath := os.Getenv("PATH")
			t.Setenv("PATH", dir+string(os.PathListSeparator)+previousPath)
			trivy := filepath.Join(dir, "trivy")
			write(trivy, "#!/bin/sh\nwhile [ \"$#\" -gt 0 ]; do\nif [ \"$1\" = --output ]; then output=$2; shift 2; else shift; fi\ndone\nprintf '%s\\n' '{\"ArtifactName\":\"fixture\",\"Results\":[{\"Target\":\"fixture\",\"Vulnerabilities\":[{\"VulnerabilityID\":\"CVE-FIXTURE\",\"PkgName\":\"fixture\",\"InstalledVersion\":\"1\",\"FixedVersion\":\"2\",\"Severity\":\"HIGH\"}]}]}' > \"$output\"\n", 0o700)
			scan := run(!variant.vulnerabilities, "security", "scan", "container", "--image-ref", "registry.example/app:fixture")
			if !strings.Contains(scan, "CVE-FIXTURE") {
				t.Fatal("scan findings disappeared")
			}
			for _, report := range []string{"trivy-results.json", "trivy-results.sarif", "gl-container-scanning-report.json"} {
				body, err := os.ReadFile(filepath.Join(consumer, report))
				if err != nil || !json.Valid(body) {
					t.Fatalf("fresh %s missing/invalid: %v", report, err)
				}
			}
			write(trivy, "#!/bin/sh\nexit 125\n", 0o700)
			run(false, "security", "scan", "container", "--image-ref", "registry.example/app:fixture")
			run(false, "policy", "show", "--enforce-lint=false")
			run(false, "policy", "show", "--policy-file", filepath.Join(consumer, "policy.json"))
			run(false, "policy", "set", "enforce-lint=false")
		})
	}
}
