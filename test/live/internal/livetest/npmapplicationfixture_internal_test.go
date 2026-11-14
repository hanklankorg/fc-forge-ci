// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domainpublish "github.com/diggsweden/forge-ci/internal/domain/publish"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestNPMApplicationFixtureSourceBoundary(t *testing.T) {
	for _, variant := range []string{
		"sha1/application", "sha256/application",
		"sha1/library", "sha256/library",
		"sha1/library-main", "sha256/library-main",
		"sha1/private-library", "sha256/private-library",
	} {
		t.Run(variant, func(t *testing.T) {
			format, kind, _ := strings.Cut(variant, "/")
			root, git := stageTestRepo(t, format)

			pkg := `{"name":"@example/application","version":"2.1.0","bin":{"app":"dist/cli.js"}}`
			if kind != "application" {
				pkg = `{"name":"@example/library","version":"2.1.0","exports":"./src/cli.js","private":` + strconv.FormatBool(kind == "private-library") + `}`
			}

			if kind == "library-main" {
				pkg = `{"name":"@example/library","version":"2.1.0","exports":null,"main":"src/cli.js"}`
			}

			stageTestWrite(t, root, "package.json", pkg, 0644)
			stageTestWrite(t, root, "Containerfile", "FROM scratch\n", 0644)
			stageTestWrite(t, root, "src/cli.js", "#!/usr/bin/env node\n", 0755)
			stageTestCommit(t, git, root)

			source, err := openConsumer(t.Context(), git, root, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}

			recipe := filepath.Join(reporoot.Path(t), "test/live/fixtures/consumers/hello-npm-service/consumer")

			oracle := filepath.Join(t.TempDir(), "oracle.sh")
			if err := os.WriteFile(oracle, []byte("exit 0\n"), 0600); err != nil {
				t.Fatal(err)
			}

			config := NPMApplicationFixtureConfig{RecipeDirectory: recipe, NodeImage: "docker.io/library/node:24.17.0-trixie@sha256:" + strings.Repeat("a", 64), Oracle: oracle, Branch: "development"}

			config.Library = kind != "application"
			for _, development := range []bool{false, true} {
				fixture, err := npmApplicationFixture(t.Context(), source, config, development)
				if kind == "private-library" || (config.Library && development) {
					if err == nil {
						t.Fatal("accepted private publication or unsupported library image-development mode")
					}

					continue
				}

				if err != nil {
					t.Fatal(err)
				}

				for _, path := range []string{"package.json", "Containerfile", "src/cli.js"} {
					before, _ := git.run(t.Context(), source.stage, "ls-tree", source.Commit, "--", path)

					after, err := git.run(t.Context(), fixture.stage, "ls-tree", fixture.Commit, "--", path)
					if err != nil || before != after {
						t.Fatalf("source identity/mode changed: %s", path)
					}
				}

				name := ".forgejo/workflows/release.yml"
				if development {
					name = ".forgejo/workflows/development.yml"
				}

				workflow, err := githubWorkflow(t.Context(), fixture, name)
				if err != nil || !strings.Contains(workflow, "forge-ci build npm run") || strings.Contains(workflow, "tar -x") {
					t.Fatalf("invalid package workflow: %v", err)
				}

				if !development && (strings.Contains(workflow, "needs.release-images") || strings.Contains(workflow, "  release-images:")) {
					t.Fatal("dangling image dependency")
				}

				if !development {
					var policy struct {
						Jobs map[string]struct {
							Needs any              `yaml:"needs"`
							Steps []map[string]any `yaml:"steps"`
						} `yaml:"jobs"`
					}
					if err := yaml.Unmarshal([]byte(workflow), &policy); err != nil {
						t.Fatal(err)
					}

					publisher, ok := policy.Jobs["publish-package"]
					if !ok || len(publisher.Steps) != 4 {
						t.Fatal("missing separate publisher")
					}

					publisherText := strings.SplitN(workflow, "  publish-package:", 2)[1]
					for _, required := range []string{"sign-and-publish", "sha256sum -c SHA256SUMS", "publish npm validate-tarball", "publish npm write-npmrc", "publish npm validate-version", "publish npm deploy", "release.{version}", `--npm-tag "$NPM_TAG"`, "unset FORGEJO_TOKEN GITHUB_TOKEN GH_TOKEN RELEASE_TOKEN"} {
						if !strings.Contains(publisherText, required) {
							t.Fatalf("missing publisher boundary %s", required)
						}
					}

					for _, forbidden := range []string{"platform checkout", "checkout-consumer", "npm pack", "npm ci", "npm run", "publish forge-packages", "tar -x", "secrets.RELEASE_SSH", "secrets.RELEASE_COSIGN"} {
						if strings.Contains(publisherText, forbidden) {
							t.Fatalf("publisher has source/lifecycle/signing authority: %s", forbidden)
						}
					}
				}

				if development && !strings.Contains(workflow, "needs: snapshot") {
					t.Fatal("original source image must run first")
				}
			}

			if err := git.requireClean(t.Context(), root, "."); err != nil {
				t.Fatal(err)
			}
			// A symlink is refused even when its target is a valid package object.
			stageTestWrite(t, root, "identity.json", pkg, 0644)

			if err := os.Remove(filepath.Join(root, "package.json")); err != nil {
				t.Fatal(err)
			}

			if err := os.Symlink("identity.json", filepath.Join(root, "package.json")); err != nil {
				t.Fatal(err)
			}

			stageTestCommit(t, git, root)

			linked, err := openConsumer(t.Context(), git, root, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}

			if _, err := npmApplicationFixture(t.Context(), linked, config, false); err == nil {
				t.Fatal("followed package metadata symlink")
			}
		})
	}
}

func TestNPMApplicationLibraryMetadataRefusesBeforeWorkflowPreparation(t *testing.T) {
	for _, metadata := range []string{
		`"main":"src/index.js","exports":{ ".":"./missing.js"}`,
		`"main":"src/index.js","exports":["./src/index.js"]`,
		`"main":"src/index.js","exports":42`,
		`"exports":"src/index.js"`,
		`"exports":"./src/./index.js"`,
		`"exports":"./src/index.js "`,
		`"exports":"./src/index.js\u0001"`,
		`"main":"../outside.js"`,
	} {
		t.Run(metadata, func(t *testing.T) {
			root, git := stageTestRepo(t, "sha1")
			stageTestWrite(t, root, "package.json", `{"name":"@example/library","version":"2.1.0",`+metadata+`}`, 0o644)
			stageTestCommit(t, git, root)

			source, err := openConsumer(t.Context(), git, root, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			// Neither policy path exists. Metadata refusal must precede reading
			// these files or preparing a workflow/signing cycle.
			config := NPMApplicationFixtureConfig{
				Library: true, RecipeDirectory: filepath.Join(t.TempDir(), "absent-recipe"),
				Oracle:    filepath.Join(t.TempDir(), "absent-oracle"),
				NodeImage: "docker.io/library/node:24.17.0-trixie@sha256:" + strings.Repeat("a", 64),
			}

			result, err := npmApplicationFixture(t.Context(), source, config, false)
			if !errors.Is(err, errs.ErrValidation) || !strings.Contains(fmt.Sprint(err), "library fixture packed metadata") {
				t.Fatalf("metadata was not refused before policy preparation: %v", err)
			}

			if result.Commit != source.Commit {
				t.Fatal("refusal changed source commit")
			}

			if err := git.requireClean(t.Context(), root, "."); err != nil {
				t.Fatal(err)
			}

			staged, err := git.run(t.Context(), source.stage, "diff", "--cached", "--name-only", source.Commit)
			if err != nil || staged != "" {
				t.Fatalf("refusal staged workflow changes: %s, %v", staged, err)
			}
		})
	}
}

func TestNPMApplicationForgejoTagCompatibility(t *testing.T) {
	var policy struct {
		Jobs map[string]struct {
			Steps []struct {
				Env map[string]string `yaml:"env"`
				Run string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}

	text := strings.NewReplacer("@IMAGE@", "registry.invalid/node@sha256:"+strings.Repeat("a", 64), "@ACTIONS@", "https://forge.invalid/owner/engine/.forgejo/actions/", "@PIN@", strings.Repeat("b", 40)).Replace(npmApplicationStableConsumers)
	if err := yaml.Unmarshal([]byte("jobs:\n"+text), &policy); err != nil {
		t.Fatal(err)
	}

	steps := policy.Jobs["publish-package"].Steps
	if len(steps) == 0 {
		t.Fatal("missing publisher steps")
	}

	publication := steps[len(steps)-1]

	setting := publication.Env["NPM_TAG"]
	if setting != domainpublish.NPMDottedVersionTagTemplate {
		t.Fatalf("Forgejo fixture selected incompatible tag policy %q", setting)
	}

	if !strings.Contains(publication.Run, `--npm-tag "$NPM_TAG"`) {
		t.Fatal("deploy must consume the same tag setting as preflight")
	}
	// Forgejo v16.0.6 setPackageTag rejects any version.NewVersion success.
	// Its go.mod REPLACES hashicorp/go-version with 6543/go-version v1.3.1.
	// That parser starts with this optional-prefix/numeric-core grammar. For
	// these valid canonical versions, it distinguishes the rejected hyphenated
	// aliases from dotted aliases before the rest of the version grammar.
	// https://github.com/6543/go-version/blob/v1.3.1/version.go
	versionStart := regexp.MustCompile(`^(?:\w+\-)*[vV]?[0-9]`)

	for _, version := range []string{"2.1.1", "2.1.1-rc.1", "2.1.1+build.7"} {
		bad, err := domainpublish.NPMVersionTag(version, domainpublish.NPMVersionTagTemplate)
		if err != nil || !versionStart.MatchString(bad) {
			t.Fatalf("lost upstream rejection reproducer: %q %v", bad, err)
		}

		good, err := domainpublish.NPMVersionTag(version, setting)
		if err != nil || versionStart.MatchString(good) {
			t.Fatalf("version-like Forgejo tag %q: %v", good, err)
		}

		canonical, err := domainpublish.NPMRegistryVersion(version)
		if err != nil || good != "release."+canonical {
			t.Fatalf("tag lost exact canonical version binding: %q %v", good, err)
		}

		if _, err := domainpublish.NPMVersionTag("3.0.0", good); err == nil {
			t.Fatal("accepted tag bound to another version")
		}
	}

	for _, movable := range []string{"latest", "dev", "next", "snapshot"} {
		if domainpublish.ValidateNPMTag(movable) == nil {
			t.Fatalf("accepted movable tag %q", movable)
		}
	}
}

// Optional read-only real-source rendering; no npm or application process runs.
func TestNPMApplicationActualSourceRenderer(t *testing.T) {
	root := os.Getenv("FORGE_CI_LIVE_CONSUMER")
	if root == "" {
		t.Skip("name source checkout for read-only renderer verification")
	}

	git := githubStageGit()
	git.env = append(git.env, "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")

	source, err := openConsumer(t.Context(), git, root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	recipe := filepath.Join(reporoot.Path(t), "test/live/fixtures/consumers/hello-npm-service/consumer")

	config := NPMApplicationFixtureConfig{RecipeDirectory: recipe, NodeImage: os.Getenv("FORGE_CI_LIVE_NPM_APPLICATION_IMAGE"), Oracle: os.Getenv("FORGE_CI_LIVE_NPM_APPLICATION_ORACLE"), Branch: "development"}
	for _, development := range []bool{false, true} {
		fixture, err := npmApplicationFixture(t.Context(), source, config, development)
		if err != nil {
			t.Fatal(err)
		}

		diff, err := git.run(t.Context(), fixture.stage, "diff-tree", "--no-commit-id", "--name-only", "-r", source.Commit, fixture.Commit)
		if err != nil {
			t.Fatal(err)
		}

		for path := range strings.SplitSeq(strings.TrimSpace(diff), "\n") {
			if !strings.HasPrefix(path, ".forgejo/") && !strings.HasPrefix(path, ".forge-ci/") && !strings.HasPrefix(path, ".chglog/") {
				t.Fatalf("non-CI source mutation: %s", path)
			}
		}

		t.Logf("admitted source %s; development=%t private fixture %s; CI-only diff:\n%s", source.Commit, development, fixture.Commit, diff)
	}
}

// The stable fixture keeps the recipe's npm version bump and runs it in the
// trial's Node image, as it runs the build.
func TestNPMStableReleaseWorkflowBumpsInTheTrialImage(t *testing.T) {
	t.Parallel()

	recipe, err := os.ReadFile(filepath.Join(reporoot.Path(t), "test/live/fixtures/consumers/hello-npm-service/consumer/.forgejo/workflows/release.yml"))
	if err != nil {
		t.Fatal(err)
	}

	pin, err := ReadConsumerPin(map[string]string{trialReleaseWorkflow: string(recipe)})
	if err != nil {
		t.Fatal(err)
	}

	image := "registry.invalid/node@sha256:" + strings.Repeat("a", 64)

	workflow, err := npmStableReleaseWorkflow(string(recipe), pin, image)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(workflow, "      runtime-image-node: "+image+"\n") || strings.Count(workflow, "runtime-image-node:") != 1 || strings.Contains(workflow, "      version-bump:") || strings.Contains(workflow, "      version-bump-image:") {
		t.Fatalf("the stable fixture does not bump package.json in the trial's image:\n%s", workflow)
	}

	// The recipe publishes its own package to the forge's registry; the
	// fixture publishes the application's with its own job alone.
	if strings.Count(workflow, "  publish-package:\n") != 1 || strings.Contains(workflow, "publish forge-packages deploy") {
		t.Fatalf("the stable fixture keeps the recipe's package publication:\n%s", workflow)
	}

	const runtimeInput = "      runtime-image-node: ${{ vars.CI_JOB_IMAGE }}\n"
	for name, replacement := range map[string]string{
		"missing":              "",
		"duplicate":            runtimeInput + runtimeInput,
		"different value":      runtimeInput + "      runtime-image-node: " + image + "\n",
		"different whitespace": runtimeInput + "      runtime-image-node:  ${{ vars.CI_JOB_IMAGE }}\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			modified := strings.Replace(string(recipe), runtimeInput, replacement, 1)
			if _, err := npmStableReleaseWorkflow(modified, pin, image); !errors.Is(err, errNPMRecipeStructure) {
				t.Fatalf("a recipe with %s Node version-runtime binding was accepted: %v", name, err)
			}
		})
	}
}
