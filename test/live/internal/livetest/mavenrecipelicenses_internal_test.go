// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/pelletier/go-toml/v2"
)

func TestMavenFixtureREUSE(t *testing.T) {
	reuse, err := exec.LookPath("reuse")
	if err != nil {
		t.Skip("REUSE CLI is not installed")
	}

	recipe := filepath.Join(reporoot.Path(t), "test/live/fixtures/consumers/hello-maven/consumer")

	root, git := stageTestRepo(t, "sha1")
	stageTestWrite(t, root, "pom.xml", "<!--\n"+mavenRecipeLicenseMetadata+"-->\n<project><artifactId>sample</artifactId></project>", 0644)
	stageTestCommit(t, git, root)

	source, err := openConsumer(t.Context(), git, root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	image := "registry.invalid/maven:25@sha256:" + strings.Repeat("c", 64)
	source = NewMavenApplicationConsumerFixture(t, source, recipe, image)
	// Materialize only the private index in a fresh directory for the
	// scanner; neither checkout-index nor REUSE touches the source tree.
	checkout := t.TempDir()
	if _, err := git.run(t.Context(), source.stage, "--work-tree="+checkout, "checkout-index", "--all", "--prefix="+checkout+string(filepath.Separator)); err != nil {
		t.Fatal(err)
	}

	output, err := exec.CommandContext(t.Context(), reuse, "--root", checkout, "lint").CombinedOutput()
	if err != nil {
		t.Fatalf("fixture REUSE lint: %v\n%s", err, output)
	}
}

func TestConsumerPRWorkflowREUSE(t *testing.T) {
	reuse, err := exec.LookPath("reuse")
	if err != nil {
		t.Skip("REUSE CLI is not installed")
	}

	workflow, err := consumerPRWorkflow(publicShellPRFixture("runner", "https://forge.invalid/engine@commit", "digest"))
	if err != nil {
		t.Fatal(err)
	}

	license, err := os.ReadFile(filepath.Join(reporoot.Path(t), "LICENSES/CC0-1.0.txt"))
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	stageTestWrite(t, root, "LICENSES/CC0-1.0.txt", string(license), 0600)
	stageTestWrite(t, root, ".forgejo/workflows/pullrequest.yml", workflow, 0600)

	output, err := exec.CommandContext(t.Context(), reuse, "--root", root, "lint").CombinedOutput()
	if err != nil {
		t.Fatalf("generated PR workflow must not add a consumer license finding: %v\n%s", err, output)
	}
}

func TestMavenRecipeProvenanceMatchesSourceAnnotation(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(reporoot.Path(t), "REUSE.toml"))
	if err != nil {
		t.Fatal(err)
	}

	var policy struct {
		Annotations []struct {
			Path       any
			Precedence string
			Copyright  string `toml:"SPDX-FileCopyrightText"`
			License    string `toml:"SPDX-License-Identifier"`
		}
	}
	if err := toml.Unmarshal(body, &policy); err != nil {
		t.Fatal(err)
	}

	for _, file := range mavenConsumerRecipeFiles {
		var metadata string

		for _, annotation := range policy.Annotations {
			var paths []string

			switch path := annotation.Path.(type) {
			case string:
				paths = []string{path}
			case []any:
				paths = make([]string, 0, len(path))
				for _, item := range path {
					paths = append(paths, item.(string))
				}
			}

			for _, path := range paths {
				match, err := doublestar.Match(path, "test/live/fixtures/consumers/hello-maven/consumer/"+file)
				if err != nil {
					t.Fatal(err)
				}

				if match && annotation.Precedence == "override" {
					metadata = "SPDX-FileCopyrightText" + ": " + annotation.Copyright + "\nSPDX-License-Identifier" + ": " + annotation.License + "\n"
				}
			}
		}

		if metadata != mavenRecipeLicenseMetadata {
			t.Fatalf("%s: overlay metadata differs from upstream REUSE override: %q", file, metadata)
		}
	}
}

func TestMavenFixtureLicensesStayWithinOwnedOverlay(t *testing.T) {
	recipe := filepath.Join(reporoot.Path(t), "test/live/fixtures/consumers/hello-maven/consumer")

	upstreamLicense, err := os.ReadFile(filepath.Join(reporoot.Path(t), "LICENSES/CC0-1.0.txt"))
	if err != nil {
		t.Fatal(err)
	}

	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			root, git := stageTestRepo(t, format)
			originals := map[string]string{
				"pom.xml":                      "<project><artifactId>sample</artifactId></project>",
				"LICENSE":                      "consumer's original license notice\n",
				"REUSE.toml":                   "version = 1\n\n[[annotations]]\npath = [\"consumer.txt\"]\nSPDX-FileCopyrightText = \"Consumer Authors\"\nSPDX-License-Identifier = \"MIT\"\n",
				"consumer.txt":                 "consumer-owned data\n",
				".chglog/consumer.tpl":         "unrelated template without metadata\n",
				".chglog/consumer.tpl.license": "SPDX-FileCopyrightText" + ": Consumer Authors\nSPDX-License-Identifier" + ": MIT\n",
				"LICENSES/MIT.txt":             "consumer-owned license text\n",
			}
			// Exercise both supplying missing CC0 and preserving an existing text.
			if format == "sha256" {
				originals["LICENSES/CC0-1.0.txt"] = string(upstreamLicense)
			}

			for path, text := range originals {
				stageTestWrite(t, root, path, text, 0644)
			}

			stageTestCommit(t, git, root)

			source, err := openConsumer(t.Context(), git, root, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}

			image := "registry.invalid/maven:25@sha256:" + strings.Repeat("c", 64)

			fixture := NewMavenApplicationConsumerFixture(t, source, recipe, image)

			read := func(path string) string {
				t.Helper()

				body, err := githubWorkflow(t.Context(), fixture, path)
				if err != nil {
					t.Fatal(err)
				}

				return body
			}
			for path, text := range originals {
				if read(path) != text {
					t.Errorf("consumer file changed: %s", path)
				}
			}

			allowed := make([]string, 0, 1+2*len(mavenConsumerRecipeFiles))
			allowed = append(allowed, "LICENSES/CC0-1.0.txt")

			for _, file := range mavenConsumerRecipeFiles {
				allowed = append(allowed, file, file+".license")
				if read(file+".license") != mavenRecipeLicenseMetadata {
					t.Errorf("missing source provenance for %s", file)
				}

				if strings.HasSuffix(file, ".tpl") {
					body, err := os.ReadFile(filepath.Join(recipe, file))
					if err != nil || read(file) != string(body) {
						t.Errorf("template modified: %s, %v", file, err)
					}
				}
			}

			if read("LICENSES/CC0-1.0.txt") != string(upstreamLicense) {
				t.Fatal("CC0 text differs from upstream")
			}

			changed, err := git.run(t.Context(), fixture.stage, "diff-tree", "--no-commit-id", "--name-only", "-r", source.Commit, fixture.Commit)
			if err != nil {
				t.Fatal(err)
			}

			for path := range strings.FieldsSeq(changed) {
				if !slices.Contains(allowed, path) {
					t.Errorf("overlay changed unowned path: %s", path)
				}
			}

			if err := git.requireClean(t.Context(), root, "."); err != nil {
				t.Fatal("source checkout modified", err)
			}
		})
	}
}
