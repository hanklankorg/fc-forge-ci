// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"gopkg.in/yaml.v3"
)

func TestGitHubUnsignedAssemblyAdmissionBeforeSigningMaterial(t *testing.T) {
	var workflow struct {
		Env  map[string]string `yaml:"env"`
		Jobs map[string]struct {
			Env   map[string]string   `yaml:"env"`
			Steps []githubReleaseStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(reporoot.ReadFile(t, ".github/workflows/release-create-github.yml"), &workflow); err != nil {
		t.Fatal(err)
	}

	// Two signers: the GPG one on the host, with the canonical CLI consumed
	// first, and the cosign-backed one in the admitted image, whose PR-context
	// refusal is its first CLI use. Each admits the unsigned assembly before
	// any signing material.
	var admission githubReleaseStep

	for job, bootstrap := range map[string]string{
		"sign-release-gpg": "Consume canonical CLI from the independently pinned engine checkout",
		"sign-release":     "Refuse PR-context trigger (signing material in scope)",
	} {
		steps := workflow.Jobs[job]
		positions := map[string]int{}

		for i, step := range steps.Steps {
			positions[step.Name] = i
			if step.Name == "Validate unsigned release assembly" {
				admission = step
			}
		}

		if admission.Run != "forge-ci release validate-assembly" || admission.If != "" || admission.ContinueOnError != nil && admission.ContinueOnError != false {
			t.Fatalf("%s: missing or bypassable admission: %+v", job, admission)
		}

		pairs := [][2]string{
			{bootstrap, "Download assembled release files"},
			{"Download assembled release files", admission.Name},
			{admission.Name, "Sign artifacts"},
		}
		if job == "sign-release-gpg" {
			pairs = append(pairs, [2]string{admission.Name, "Import GPG key"})
		} else {
			pairs = append(pairs, [2]string{admission.Name, "Bind Cosign signing key to trusted public key"})
		}

		for _, pair := range pairs {
			before, okBefore := positions[pair[0]]

			after, okAfter := positions[pair[1]]
			if !okBefore || !okAfter || before >= after {
				t.Fatalf("%s: admission ordering violated: %v", job, pair)
			}
		}

		checkSecretFree := func(env map[string]string) {
			t.Helper()

			for _, value := range env {
				if strings.Contains(value, "secrets.") {
					t.Fatalf("%s: signing secrets available before assembly admission", job)
				}
			}
		}
		checkSecretFree(workflow.Env)
		checkSecretFree(steps.Env)

		for _, step := range steps.Steps[:positions[admission.Name]+1] {
			checkSecretFree(step.Env)
		}
	}

	engine := forgeCIEngine(t)
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprintf("corrupt=%t", corrupt), func(t *testing.T) {
			sandbox := newSandbox(t, "assembly-admission")
			sandbox.write("bin/forge-ci", "#!/bin/sh\nexec \"${FORGE_CI_ENGINE:?}\" \"$@\"\n", 0o700)
			sandbox.write(".forge-ci/release-assembly.json", `{"version":1,"assets":[{"path":"release-files/assets/app.tgz","name":"app.tgz","required":true}],"checksum_file":"release-files/checksums.sha256"}`, 0o600)
			sandbox.write("release-files/checksums.sha256", fmt.Sprintf("%x  app.tgz\n", sha256.Sum256([]byte("archive"))), 0o600)

			body := "archive"
			if corrupt {
				body = "corrupted"
			}

			sandbox.write("release-files/assets/app.tgz", body, 0o600)

			result := sandbox.run(admission.Run,
				"PATH="+filepath.Join(sandbox.root, "bin")+":"+os.Getenv("PATH"),
				"FORGE_CI_ENGINE="+engine,
				"RELEASE_ASSEMBLY="+admission.Env["RELEASE_ASSEMBLY"])
			if !corrupt && result.status != 0 || corrupt && (result.status == 0 || !strings.Contains(result.stderr, "digest mismatch")) {
				t.Fatalf("admission status=%d stderr=%s", result.status, result.stderr)
			}
		})
	}
}

// Each signer writes the SLSA provenance statement itself: from the assembly
// it has just admitted, before any signing material is in scope, and before
// the step that signs it with the release files.
func TestGitHubSignerWritesTheProvenanceStatementItSigns(t *testing.T) {
	var workflow struct {
		Jobs map[string]struct {
			Steps []githubReleaseStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(reporoot.ReadFile(t, ".github/workflows/release-create-github.yml"), &workflow); err != nil {
		t.Fatal(err)
	}

	const name = "Write the SLSA provenance statement"

	var statement githubReleaseStep

	for job, key := range map[string]string{
		"sign-release-gpg": "Import GPG key",
		"sign-release":     "Bind Cosign signing key to trusted public key",
	} {
		steps := workflow.Jobs[job].Steps
		positions := map[string]int{}

		for i, step := range steps {
			positions[step.Name] = i
		}

		at, ok := positions[name]
		if !ok {
			t.Fatalf("%s: no step %q", job, name)
		}

		statement = steps[at]
		if statement.Run != `forge-ci release provenance --ref-name "$TAG_NAME" --source-sha "$RELEASE_SHA"` ||
			statement.If != "${{ inputs['sign-artifacts'] }}" || statement.ContinueOnError != nil {
			t.Fatalf("%s: statement step = %+v", job, statement)
		}

		if positions["Validate unsigned release assembly"] != at-1 {
			t.Fatalf("%s: the statement is written from the assembly admitted in the step before it", job)
		}

		for _, later := range []string{key, "Sign artifacts"} {
			if after, ok := positions[later]; !ok || after <= at {
				t.Fatalf("%s: the statement must be written before %q", job, later)
			}
		}

		for key, value := range statement.Env {
			if strings.Contains(value, "secrets.") || strings.Contains(value, "github.token") {
				t.Fatalf("%s: the statement step holds a credential in %s", job, key)
			}
		}
	}

	engine := forgeCIEngine(t)
	run := func(t *testing.T, manifest, checksums string) (*sandbox, result) {
		t.Helper()

		sandbox := newSandbox(t, "provenance-statement")
		sandbox.write("bin/forge-ci", "#!/bin/sh\nexec \"${FORGE_CI_ENGINE:?}\" \"$@\"\n", 0o700)
		sandbox.write(".forge-ci/release-assembly.json", manifest, 0o600)
		sandbox.write("release-files/checksums.sha256", checksums, 0o600)
		sandbox.write("release-files/assets/app.tgz", "archive", 0o600)

		return sandbox, sandbox.run(statement.Run,
			"PATH="+filepath.Join(sandbox.root, "bin")+":"+os.Getenv("PATH"),
			"FORGE_CI_ENGINE="+engine,
			"GITHUB_ACTIONS=true",
			"GITHUB_SERVER_URL=https://github.com",
			"GITHUB_REPOSITORY=example/app",
			"GITHUB_RUN_ID=42",
			"GITHUB_WORKFLOW_REF=example/app/.github/workflows/release.yml@refs/tags/v1.2.3",
			"SOURCE_DATE_EPOCH=1700000000",
			"RELEASE_ASSEMBLY="+statement.Env["RELEASE_ASSEMBLY"],
			"TAG_NAME=v1.2.3",
			"RELEASE_SHA="+strings.Repeat("a", 40))
	}

	const path = "release-files/slsa-provenance.intoto.json"

	t.Run("a release with files", func(t *testing.T) {
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte("archive")))
		sandbox, result := run(t,
			`{"version":1,"assets":[{"path":"release-files/assets/app.tgz","name":"app.tgz","required":true}],"checksum_file":"release-files/checksums.sha256","provenance_file":"`+path+`"}`,
			digest+"  app.tgz\n")

		if result.status != 0 {
			t.Fatalf("status=%d stderr=%s", result.status, result.stderr)
		}

		body, err := os.ReadFile(filepath.Join(sandbox.root, path))
		if err != nil {
			t.Fatal(err)
		}

		var got struct {
			Subject []struct {
				Name   string            `json:"name"`
				Digest map[string]string `json:"digest"`
			} `json:"subject"`
			PredicateType string `json:"predicateType"`
			Predicate     struct {
				BuildDefinition struct {
					ExternalParameters   map[string]string `json:"externalParameters"`
					ResolvedDependencies []struct {
						Digest map[string]string `json:"digest"`
					} `json:"resolvedDependencies"`
				} `json:"buildDefinition"`
				RunDetails struct {
					Builder  map[string]string `json:"builder"`
					Metadata map[string]string `json:"metadata"`
				} `json:"runDetails"`
			} `json:"predicate"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}

		definition, details := got.Predicate.BuildDefinition, got.Predicate.RunDetails
		if len(got.Subject) != 1 || got.Subject[0].Name != "app.tgz" || got.Subject[0].Digest["sha256"] != digest ||
			got.PredicateType != "https://slsa.dev/provenance/v1" ||
			definition.ExternalParameters["source"] != "git+https://github.com/example/app" ||
			definition.ExternalParameters["ref"] != "v1.2.3" ||
			len(definition.ResolvedDependencies) != 1 ||
			definition.ResolvedDependencies[0].Digest["gitCommit"] != strings.Repeat("a", 40) ||
			details.Builder["id"] != "https://github.com/example/app/.github/workflows/release.yml@refs/tags/v1.2.3" ||
			details.Metadata["invocationId"] != "https://github.com/example/app/actions/runs/42" ||
			details.Metadata["startedOn"] != "2023-11-14T22:13:20Z" {
			t.Fatalf("statement = %s", body)
		}
	})

	t.Run("a release without files", func(t *testing.T) {
		sandbox, result := run(t, `{"version":1,"checksum_file":"release-files/checksums.sha256","provenance_file":"`+path+`"}`, "")

		if result.status != 0 || !strings.Contains(result.stderr, "no provenance statement") {
			t.Fatalf("status=%d stderr=%s", result.status, result.stderr)
		}

		if _, err := os.Stat(filepath.Join(sandbox.root, path)); !os.IsNotExist(err) {
			t.Fatalf("a release without files got a statement: %v", err)
		}
	})
}
