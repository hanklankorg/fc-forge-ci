// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard_test

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// The trees that ship workflows, actions, templates, release scripts, demos
// and consumer fixtures.
var shippedCITrees = []string{".forgejo", ".github", ".gitlab", "templates", "examples", "test/live/demos", "test/live/consumer-fixtures"}

// The demos and examples, which a consumer copies: what they hard-wire, the
// consumer does too.
var demoTrees = []string{"examples", "test/live/demos"}

// buildahHardWired names Buildah directly: the Forgejo setup-buildah action,
// the engine's setup-buildah verb, or the bootstrap installer.
var buildahHardWired = regexp.MustCompile(`/setup-buildah@|/setup-buildah\b\s*$|container setup-buildah\b|install-buildah\.sh`)

// buildahAllowed is every place that may name Buildah directly, and why: the
// builder selection itself, which prepares Buildah when CI_IMAGE_BUILDER
// selects it, and the paths that are Buildah's by design.
var buildahAllowed = map[string]string{
	".forgejo/actions/setup-buildah/action.yml":                   "the Buildah preparation itself",
	".github/workflows/publish-container.yml":                     "the GitHub builder selection prepares Buildah only for its host branch",
	".github/workflows/snapshot-images.yml":                       "the GitHub builder selection prepares Buildah only for its host branch",
	".github/workflows/self-release.yml":                          "forge-ci's own release on GitHub's runners, no consumer workflow",
	".github/workflows/self-runtime-container.yml":                "forge-ci's own runtime images on GitHub's runners, no consumer workflow",
	"test/live/consumer-fixtures/generic-cli/forgejo-release.yml": "the Go lane's checks are Buildah's own integration tests",
}

// hardCodedArch is a container step's literal platform or architecture: a
// demo follows the release's selected platforms instead.
var hardCodedArch = regexp.MustCompile(`--platform[= ]+["']?linux/|--arch[= ]+["']?(amd64|arm64)\b|release-platforms:\s*["']?linux/`)

// A container build must go through the builder the organization selects.
// A shipped step that names Buildah directly ignores CI_IMAGE_BUILDER (the
// cargo-container demo's export did, and kaniko was then refused there), so
// only the builder selection and the documented Buildah-only paths may.
func TestContainerBuildsUseTheSelectedImageBuilder(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)
	seen := map[string]bool{}

	for _, line := range shippedCILines(t, root, shippedCITrees) {
		if !buildahHardWired.MatchString(line.text) {
			continue
		}

		seen[line.path] = true

		if _, allowed := buildahAllowed[line.path]; !allowed {
			t.Errorf("%s:%d names Buildah directly (%s); prepare the builder with setup-image-builder, in the job image CI_IMAGE_BUILDER_JOB_IMAGE names, so CI_IMAGE_BUILDER has its effect", line.path, line.number, strings.TrimSpace(line.text))
		}
	}

	for path, reason := range buildahAllowed {
		if !seen[path] {
			t.Errorf("%s is allowed to name Buildah (%s) but no longer does; drop it from the allowlist", path, reason)
		}
	}
}

type githubBuilderStep struct {
	ID   string            `yaml:"id"`
	Run  string            `yaml:"run"`
	Uses string            `yaml:"uses"`
	If   string            `yaml:"if"`
	Env  map[string]string `yaml:"env"`
	With map[string]string `yaml:"with"`
}

func TestGitHubImageBuildersKeepHostArtifactsAndIsolateKaniko(t *testing.T) {
	t.Parallel()

	for _, config := range []struct{ file, prepare, build string }{
		{"publish-container.yml", "prep", "build-arch"},
		{"snapshot-images.yml", "plan", "build"},
	} {
		t.Run(config.file, func(t *testing.T) {
			t.Parallel()

			var workflow struct {
				Jobs map[string]struct {
					Container any                 `yaml:"container"`
					Steps     []githubBuilderStep `yaml:"steps"`
				} `yaml:"jobs"`
			}
			require.NoError(t, yaml.Unmarshal(reporoot.ReadFile(t, ".github/workflows/"+config.file), &workflow))
			build := workflow.Jobs[config.build]
			require.Nil(t, build.Container, "host setup and artifact actions must survive Kaniko cleanup")

			prepared, isolated, hostUpload, source := false, false, false, false
			ownershipRestored := false
			sourcePathsValidated := false

			for _, step := range build.Steps {
				if config.file == "publish-container.yml" && strings.Contains(step.Run, "container validate containerfile") {
					require.Empty(t, step.If, "both builders must use the same source boundary")
					require.Contains(t, step.Run, `--context "$BUILD_CONTEXT"`)

					sourcePathsValidated = true
				}

				if config.file == "publish-container.yml" && strings.Contains(step.Run, "artifact download") {
					require.True(t, sourcePathsValidated, "reject control-directory contexts before downloading into them")
				}

				if strings.HasPrefix(step.Uses, "actions/checkout@") && step.With["path"] == "source" {
					source = true
				}

				if buildahHardWired.MatchString(step.Run) || strings.Contains(step.Run, "forge-ci container build --enforce") || strings.Contains(step.Run, "forge-ci container snapshot-images build ") {
					require.Contains(t, step.If, "needs."+config.prepare+".outputs.builder == 'buildah'")
				}

				if strings.Contains(step.Run, "container prepare-kaniko-action") {
					require.Contains(t, step.If, "needs."+config.prepare+".outputs.builder == 'kaniko'")
					require.True(t, source, "source must be separated from the generated action payload")
					require.Contains(t, step.Run, "container image usable-digest")
					require.Contains(t, step.Run, "org.opencontainers.image.source=")
					require.Contains(t, step.Run, "org.opencontainers.image.title=forge-ci kaniko job image")

					prepared = true
				}

				if step.Uses == "./.forge-ci-kaniko-action" {
					if config.file == "publish-container.yml" {
						require.True(t, sourcePathsValidated)
					}

					require.True(t, prepared, "only the trusted CLI may generate the local Docker action")
					require.Contains(t, step.If, "needs."+config.prepare+".outputs.builder == 'kaniko'")

					if config.file == "publish-container.yml" {
						require.Equal(t, "${{ inputs['enforce-vulnerabilities'] }}", step.Env["ADMIN_VULNERABILITIES"])
					} else {
						require.Equal(t, "snapshot", step.With["operation"])
						require.NotContains(t, fmt.Sprint(step.Env), "secrets.")
					}

					isolated = true
					ownershipRestored = false
				}

				if strings.Contains(step.Run, "sudo chown") {
					require.True(t, isolated)
					require.Contains(t, step.If, "always()")
					require.Contains(t, step.If, "needs."+config.prepare+".outputs.builder == 'kaniko'")
					require.Contains(t, step.Run, "chown -hRP --", "permission repair must not traverse artifact symlinks")

					ownershipRestored = true
				}

				if isolated && (strings.HasPrefix(step.Uses, "actions/upload-artifact@") || strings.Contains(step.Run, "mv -T")) {
					require.True(t, ownershipRestored, "the host must be able to retain container-owned results, including failed scan evidence")
				}

				hostUpload = hostUpload || strings.HasPrefix(step.Uses, "actions/upload-artifact@")
				require.NotContains(t, step.Run, "forge-ci artifact upload", "GitHub runtime upload credentials belong to actions, not ordinary run steps")
			}

			require.True(t, prepared && isolated && hostUpload)

			selectorTested := false

			for _, step := range workflow.Jobs[config.prepare].Steps {
				if step.ID == "builder" {
					require.Equal(t, "${{ inputs['image-builder'] || vars.CI_IMAGE_BUILDER || 'buildah' }}", step.Env["BUILDER"])
					testGitHubBuilderAdmission(t, step.Run, config.file == "publish-container.yml")

					selectorTested = true
				}
			}

			require.True(t, selectorTested, "the workflow must admit its selected builder before scheduling builds")
		})
	}
}

func testGitHubBuilderAdmission(t *testing.T, script string, buildSecrets bool) {
	t.Helper()

	image := "registry.invalid/engine/kaniko@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, builder, image, secrets string
		valid                         bool
	}{
		{name: "Buildah default", builder: "buildah", valid: true},
		{name: "Buildah clears stale image", builder: "buildah", image: image, valid: true},
		{name: "Kaniko", builder: "kaniko", image: image, valid: true},
		{name: "unknown builder", builder: "other"},
		{name: "missing image", builder: "kaniko"},
		{name: "mutable image", builder: "kaniko", image: "registry.invalid/kaniko:latest"},
		{name: "output injection", builder: "kaniko", image: image + "\nbuilder=buildah"},
		{name: "unsupported build secrets", builder: "kaniko", image: image, secrets: "private", valid: !buildSecrets},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "output")
			require.NoError(t, os.WriteFile(output, nil, 0o600))
			command := exec.CommandContext(t.Context(), testenv.GNUExecutable(t, "bash"), "-e", "-c", script)
			command.Env = []string{"PATH=" + os.Getenv("PATH"), "BUILDER=" + tc.builder, "JOB_IMAGE=" + tc.image, "BUILD_SECRETS=" + tc.secrets, "GITHUB_OUTPUT=" + output}

			log, err := command.CombinedOutput()
			if !tc.valid {
				require.Error(t, err, "%s", log)

				body, readErr := os.ReadFile(output)
				require.NoError(t, readErr)
				require.Empty(t, body, "invalid selection must not release a build")

				return
			}

			require.NoError(t, err, "%s", log)

			body, readErr := os.ReadFile(output)
			require.NoError(t, readErr)
			require.Contains(t, string(body), "builder="+tc.builder+"\n")

			if tc.builder == "buildah" {
				require.Contains(t, string(body), "builder-job-image=\n")
			}
		})
	}
}

// A demo or example builds for the platforms its release selects, one native
// job per architecture, never for one it names itself.
func TestDemoContainerStepsFollowTheSelectedPlatforms(t *testing.T) {
	t.Parallel()

	for _, line := range shippedCILines(t, reporoot.Path(t), demoTrees) {
		if hardCodedArch.MatchString(line.text) {
			t.Errorf("%s:%d hard-codes a platform (%s); take it from RELEASE_PLATFORMS (release-platforms), a native job per architecture", line.path, line.number, strings.TrimSpace(line.text))
		}
	}
}

// A Forgejo demo or example job that builds a container prepares the
// selected builder, and a GitLab demo's build job builds none: its export
// jobs do, with the policy's image builder.
func TestDemoContainerBuildJobsPrepareTheSelectedBuilder(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)
	builds := 0

	for _, base := range demoTrees {
		require.NoError(t, filepath.WalkDir(filepath.Join(root, base), func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}

			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)

			if strings.HasSuffix(rel, "/.gitlab/build-release.sh") {
				body, readErr := os.ReadFile(path)
				require.NoError(t, readErr)

				if strings.Contains(string(body), "forge-ci container build ") {
					t.Errorf("%s runs a container build in the build job, which has no image builder to select; move it to .gitlab/export-release.sh, which the policy's export jobs run", rel)
				}

				return nil
			}

			if filepath.Ext(path) != ".yml" || !strings.Contains(rel, "forgejo") {
				return nil
			}

			body, readErr := os.ReadFile(path)
			require.NoError(t, readErr)

			var workflow struct {
				Jobs map[string]struct {
					Container any `yaml:"container"`
					Steps     []struct {
						Uses string `yaml:"uses"`
						Run  string `yaml:"run"`
					} `yaml:"steps"`
				} `yaml:"jobs"`
			}
			require.NoError(t, yaml.Unmarshal(body, &workflow), rel)

			for name, job := range workflow.Jobs {
				builds += checkDemoBuildJob(t, rel+" job "+name, job.Container, job.Steps)
			}

			return nil
		}))
	}

	require.Positive(t, builds, "no demo job builds a container; the guard checks nothing")
}

func checkDemoBuildJob(t *testing.T, where string, container any, steps []struct {
	Uses string `yaml:"uses"`
	Run  string `yaml:"run"`
},
) int {
	t.Helper()

	builds := slices.ContainsFunc(steps, func(step struct {
		Uses string `yaml:"uses"`
		Run  string `yaml:"run"`
	},
	) bool {
		return strings.Contains(step.Run, "forge-ci container build ") || strings.Contains(step.Run, "multiarch-image build-arch")
	})
	if !builds {
		return 0
	}

	prepared := slices.ContainsFunc(steps, func(step struct {
		Uses string `yaml:"uses"`
		Run  string `yaml:"run"`
	},
	) bool {
		return strings.Contains(step.Uses, "/setup-image-builder")
	})

	image, _ := container.(string)
	if !prepared || !strings.Contains(image, "builder_job_image") && !strings.Contains(image, "CI_IMAGE_BUILDER_JOB_IMAGE") {
		t.Errorf("%s builds a container without the selected builder: it needs a setup-image-builder step and, as its container, the builder's job image (empty for Buildah)", where)
	}

	return 1
}

type ciLine struct {
	path   string
	number int
	text   string
}

// shippedCILines returns every non-comment line of the YAML and shell files
// under the trees.
func shippedCILines(t *testing.T, root string, trees []string) []ciLine {
	t.Helper()

	var lines []ciLine

	for _, base := range trees {
		require.NoError(t, filepath.WalkDir(filepath.Join(root, base), func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}

			switch filepath.Ext(path) {
			case ".yml", ".yaml", ".sh":
			default:
				return nil
			}

			rel, _ := filepath.Rel(root, path)
			body, readErr := os.ReadFile(path)
			require.NoError(t, readErr)

			scanner := bufio.NewScanner(bytes.NewReader(body))
			for number := 1; scanner.Scan(); number++ {
				if text := scanner.Text(); !strings.HasPrefix(strings.TrimSpace(text), "#") {
					lines = append(lines, ciLine{path: filepath.ToSlash(rel), number: number, text: text})
				}
			}

			return scanner.Err()
		}))
	}

	return lines
}
