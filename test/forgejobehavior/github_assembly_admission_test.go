// SPDX-FileCopyrightText: The Forge CI Authors
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
	job := workflow.Jobs["sign-release"]
	positions := map[string]int{}
	var admission githubReleaseStep
	for i, step := range job.Steps {
		positions[step.Name] = i
		if step.Name == "Validate unsigned release assembly" {
			admission = step
		}
	}
	if admission.Run != "forge-ci release validate-assembly" || admission.If != "" || admission.ContinueOnError != nil && admission.ContinueOnError != false {
		t.Fatalf("missing or bypassable admission: %+v", admission)
	}
	for _, pair := range [][2]string{
		{"Install forge-ci binary", "Download assembled release files"},
		{"Download assembled release files", admission.Name},
		{admission.Name, "Import GPG key"},
		{admission.Name, "Bind Cosign signing key to trusted public key"},
		{admission.Name, "Sign artifacts"},
	} {
		before, okBefore := positions[pair[0]]
		after, okAfter := positions[pair[1]]
		if !okBefore || !okAfter || before >= after {
			t.Fatalf("admission ordering violated: %v", pair)
		}
	}
	checkSecretFree := func(env map[string]string) {
		t.Helper()
		for _, value := range env {
			if strings.Contains(value, "secrets.") {
				t.Fatal("signing secrets available before assembly admission")
			}
		}
	}
	checkSecretFree(workflow.Env)
	checkSecretFree(job.Env)
	for _, step := range job.Steps[:positions[admission.Name]+1] {
		checkSecretFree(step.Env)
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
