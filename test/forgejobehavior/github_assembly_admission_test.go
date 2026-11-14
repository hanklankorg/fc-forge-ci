// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"crypto/sha256"
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
