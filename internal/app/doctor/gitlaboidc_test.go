// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package doctor_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/app/doctor"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

func TestRun_GitLabOIDC(t *testing.T) {
	t.Parallel()

	const declaration = "sign:\n  script: forge-ci release sign\n  id_tokens:\n    SIGSTORE_ID_TOKEN:\n      aud: sigstore\n"

	const header = "spec:\n  inputs:\n    greeting:\n      default: hello\n---\n"

	for _, tc := range []struct {
		name, pipeline string
		severity       doctor.Severity
	}{
		{"job_token", declaration, doctor.SeverityOK},
		{"input_header_with_job_token", header + declaration, doctor.SeverityOK},
		{"input_header_without_token", header + "sign:\n  script: echo hello\n", doctor.SeverityFail},
		{"input_audience_expression", header + strings.ReplaceAll(declaration, "aud: sigstore", "aud: $[[ inputs.greeting ]]"), doctor.SeverityWarn},
		{"input_job_name_expression", header + strings.ReplaceAll(declaration, "sign:", "$[[ inputs.greeting ]]:"), doctor.SeverityWarn},
		{"input_expression_in_comment_only", header + "# $[[ inputs.greeting ]]\n" + declaration, doctor.SeverityOK},
		{"header_only", "spec:\n  inputs: {}\n", doctor.SeverityFail},
		{"header_without_separator", "spec:\n  inputs: {}\n" + declaration, doctor.SeverityFail},
		{"header_with_malformed_pipeline", header + "PRIVATE-CANARY: [", doctor.SeverityFail},
		{"two_pipelines", declaration + "---\n" + declaration, doctor.SeverityFail},
		{"extra_document", header + declaration + "---\nPRIVATE-CANARY: ignored\n", doctor.SeverityFail},
		{"malformed_trailing_document", header + declaration + "---\nPRIVATE-CANARY: [", doctor.SeverityFail},
		{"non_mapping_pipeline", header + "- PRIVATE-CANARY\n", doctor.SeverityFail},
		{"audience_list", strings.ReplaceAll(declaration, "aud: sigstore", "aud: [sigstore, other]"), doctor.SeverityOK},
		{"no_token", "sign:\n  script: forge-ci release sign\n", doctor.SeverityFail},
		{"comment_only", "# id_tokens: {SIGSTORE_ID_TOKEN: {aud: sigstore}}\nsign:\n  script: echo hello\n", doctor.SeverityFail},
		{"variable_not_declaration", "variables:\n  SIGSTORE_ID_TOKEN: PRIVATE-CANARY\nsign:\n  script: echo hello\n", doctor.SeverityFail},
		{"hidden_template_only", "." + declaration, doctor.SeverityFail},
		{"null_audience", strings.ReplaceAll(declaration, "aud: sigstore", "aud: null"), doctor.SeverityFail},
		{"empty_audience", strings.ReplaceAll(declaration, "aud: sigstore", "aud: ''"), doctor.SeverityFail},
		{"empty_audience_list", strings.ReplaceAll(declaration, "aud: sigstore", "aud: []"), doctor.SeverityFail},
		{"non_string_audience", strings.ReplaceAll(declaration, "aud: sigstore", "aud: true"), doctor.SeverityFail},
		{"remote_include", "include: https://example.invalid/ci.yml\n", doctor.SeverityWarn},
		{"local_include", "include: local.yml\n", doctor.SeverityWarn},
		{"include_with_local_token", "include: remote.yml\n" + declaration, doctor.SeverityWarn},
		{"inheritance", "." + declaration + "release:\n  extends: .sign\n", doctor.SeverityWarn},
		{"defaults", "default:\n  id_tokens:\n    SIGSTORE_ID_TOKEN:\n      aud: sigstore\nsign:\n  script: echo hello\n", doctor.SeverityWarn},
		{"custom_token_variable", strings.ReplaceAll(declaration, "SIGSTORE_ID_TOKEN", "OTHER_TOKEN"), doctor.SeverityWarn},
		{"child_pipeline", "release:\n  trigger: child/project\n", doctor.SeverityWarn},
		{"yaml_alias", "template: &job\n  script: echo hello\nsign: *job\n", doctor.SeverityWarn},
		{"malformed_yaml", "PRIVATE-CANARY: [", doctor.SeverityFail},
		{"not_a_mapping", "- PRIVATE-CANARY\n", doctor.SeverityFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := writeRepo(t, "artifacts:\n  - name: app\n    project-type: meta\nsign:\n  method: sigstore\n", map[string]string{".gitlab-ci.yml": tc.pipeline})

			checks, err := doctor.Run(doctor.Input{Root: root, ForgeAPI: provider.ForgeGitLab})
			if err != nil {
				t.Fatal(err)
			}

			check := findCheck(checks, "workflow id-token permission")
			if check == nil || check.Severity != tc.severity {
				t.Fatalf("OIDC check = %+v, want %s", check, tc.severity)
			}

			if (doctor.ExitCode(checks) != 0) != (tc.severity == doctor.SeverityFail) {
				t.Fatalf("unexpected exit status: %d", doctor.ExitCode(checks))
			}

			var text, encoded bytes.Buffer

			doctor.FormatText(&text, checks)

			if err := doctor.FormatJSON(&encoded, doctor.Report{Checks: checks, Failures: doctor.CountFailures(checks)}); err != nil {
				t.Fatal(err)
			}

			for _, report := range []string{text.String(), encoded.String()} {
				if strings.Contains(report, "PRIVATE-CANARY") || strings.Contains(report, "permissions: id-token: write") || strings.Contains(report, "enable-openid-connect") {
					t.Fatalf("report leaked fixture data or recommended another forge's syntax: %s", report)
				}
			}
		})
	}
}

func TestRun_GitLabOIDCRouting(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		forge    provider.ForgeAPI
		files    map[string]string
		severity doctor.Severity
	}{
		{"explicit_missing_file", provider.ForgeGitLab, nil, doctor.SeverityWarn},
		{"local_gitlab", provider.ForgeLocal, map[string]string{".gitlab-ci.yml": "sign:\n  script: echo hello\n  id_tokens: {SIGSTORE_ID_TOKEN: {aud: sigstore}}\n"}, doctor.SeverityOK},
		{"explicit_gitlab_ignores_github_grant", provider.ForgeGitLab, map[string]string{".gitlab-ci.yml": "sign:\n  script: echo hello\n", ".github/workflows/release.yml": "permissions: {id-token: write}\n"}, doctor.SeverityFail},
		{"explicit_github_keeps_actions_check", provider.ForgeGitHub, map[string]string{".gitlab-ci.yml": "sign:\n  script: echo hello\n", ".github/workflows/release.yml": "permissions: {id-token: write}\n"}, doctor.SeverityOK},
		{"mixed_local_keeps_actions_check", provider.ForgeLocal, map[string]string{".gitlab-ci.yml": "sign:\n  script: echo hello\n", ".github/workflows/release.yml": "permissions: {id-token: write}\n"}, doctor.SeverityOK},
		{"explicit_forgejo_keeps_actions_check", provider.ForgeForgejo, map[string]string{".gitlab-ci.yml": "sign:\n  script: echo hello\n", ".forgejo/workflows/release.yml": "jobs:\n  sign:\n    enable-openid-connect: true\n"}, doctor.SeverityOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			checks, err := doctor.Run(doctor.Input{Root: writeRepo(t, "artifacts:\n  - name: app\n    project-type: meta\nsign:\n  method: sigstore\n", tc.files), ForgeAPI: tc.forge})
			if err != nil {
				t.Fatal(err)
			}

			if check := findCheck(checks, "workflow id-token permission"); check == nil || check.Severity != tc.severity {
				t.Fatalf("OIDC routing result = %+v, want %s", check, tc.severity)
			}
		})
	}
}
