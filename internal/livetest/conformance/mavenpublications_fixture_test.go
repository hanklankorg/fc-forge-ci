// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package conformance_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
	"gopkg.in/yaml.v3"
)

func exportedPublicationProbe(forge provider.ForgeAPI, prelude, version, mode string) string {
	commands := prelude + `
keytool -importcert -noprompt -alias forge-lab -cacerts -storepass changeit -file ` + livetest.LabCAPath + `
version='` + version + `'
for kind in jar aar; do
  artifact="fci-export-$kind"
  destination="publication/com/example/livetest/$artifact/$version"
  mkdir -p "$destination" contents
  printf '%s' "$kind:$version" > contents/payload.txt
  printf '%s' '<manifest package="com.example.fixture"/>' > contents/AndroidManifest.xml
  jar --create --file "$destination/$artifact-$version.$kind" -C contents .
  printf '%s' "<project><modelVersion>4.0.0</modelVersion><groupId>com.example.livetest</groupId><artifactId>$artifact</artifactId><version>$version</version><packaging>$kind</packaging></project>" > "$destination/$artifact-$version.pom"
done
run_product publish forge-packages deploy --project-type gradle --working-dir publication --version "$version" --snapshot=` + map[string]string{"release": "false", "snapshot": "true"}[mode] + `
`
	if mode == "snapshot" {
		commands += `run_product publish forge-packages deploy --project-type gradle --working-dir publication --snapshot
`
	}

	if forge == provider.ForgeGitLab {
		return "publish:\n  image: maven:3.9-eclipse-temurin-21\n  script:\n    - |\n      " + indent(commands, 6) + "\n"
	}

	return `on: [push]
jobs:
  publish:
    runs-on: ubuntu-latest
    container:
      image: maven:3.9-eclipse-temurin-21
    steps:
      - name: publish exported Maven artifacts
        env:
          FORGEJO_TOKEN: ${{ secrets.RC_PACKAGE_TOKEN }}
        run: |
          ` + indent(commands, 10) + "\n"
}

func TestExportedPublicationProbeContainsExecutableScript(t *testing.T) {
	t.Parallel()

	for _, forge := range []provider.ForgeAPI{provider.ForgeGitLab, provider.ForgeForgejo} {
		for _, mode := range []string{"release", "snapshot"} {
			t.Run(string(forge)+"/"+mode, func(t *testing.T) {
				version := "1.2.3"
				if mode == "snapshot" {
					version += "-SNAPSHOT"
				}

				body := exportedPublicationProbe(forge, "set -eu", version, mode)
				script := publicationProbeScript(t, forge, body)

				want := 1
				if mode == "snapshot" {
					want = 2
				}

				if strings.Count(script, "run_product publish forge-packages") != want {
					t.Fatal("publication invocations escaped the YAML script")
				}

				command := exec.CommandContext(t.Context(), "bash", "-n")

				command.Stdin = strings.NewReader(script)
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("shell syntax: %v: %s", err, output)
				}
			})
		}
	}
}

func publicationProbeScript(t *testing.T, forge provider.ForgeAPI, body string) string {
	t.Helper()

	var workflow struct {
		Publish probeJob            `yaml:"publish"`
		Jobs    map[string]probeJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(body), &workflow); err != nil {
		t.Fatal(err)
	}

	if forge == provider.ForgeGitLab {
		if len(workflow.Publish.Script) != 1 {
			t.Fatal("missing GitLab script")
		}

		return workflow.Publish.Script[0]
	}

	if len(workflow.Jobs["publish"].Steps) != 1 {
		t.Fatal("missing Forgejo step")
	}

	return workflow.Jobs["publish"].Steps[0].Run
}
