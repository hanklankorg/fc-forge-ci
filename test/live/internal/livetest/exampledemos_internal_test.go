// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"flag"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/consumerinit"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

var updateDemos = flag.Bool("update-demos", false, "rewrite the example demos' CI files from their examples")

// exampleDemos are the demos that show an example: their source is their
// own, and every CI file is the example's, filled in by exampleDemoFiles.
var exampleDemos = map[string]string{
	"hello-base-images": "base-images",
	"hello-go-cli":      "go-cli",
	"hello-go-service":  "go-service",
	"hello-gradle":      "gradle-app",
	"hello-meta":        "no-build",
	"hello-monorepo":    "monorepo",
}

// exampleDemoPin stands where a consumer pins Forge CI; the live tier puts
// the layer under test in its place, as it does for every demo.
const exampleDemoPin = "2f4e845cee96973d1082bf00a5a18689c1482148"

// exampleDemoValues fill an example's placeholders the way an adopter
// would, longest first; the image inputs are the ones the GitLab gate trial
// binds to its own images.
var exampleDemoValues = strings.NewReplacer(
	"FORGE_HOST/OWNER/forge-ci-signer@sha256:PIN_SIGNER_IMAGE_DIGEST", "forgejo.example/itiquette/forge-ci-signer@sha256:"+unrunDigest,
	"PIN_COSIGN_PUBLIC_KEY_SHA256", strings.Repeat("1", 64),
	"PIN_FORGE_CI_SHA", exampleDemoPin,
	"https://FORGE_HOST/", "https://codefloe.com/",
	"OWNER/forge-ci", "itiquette/forge-ci",
	"FIXME Release Bot", "Hello Go Release Bot",
	"FIXME-release-bot@example.com", "hello-release-bot@lab.invalid",
	"FIXME-app", "my-cli",
	"FIXME-service", "my-service",
	`"ghcr.io/itiquette/nanolinter:go@sha256:PIN_DIGEST"`, "$CI_LINT_IMAGE",
	`"ghcr.io/itiquette/nanolinter:kotlin@sha256:PIN_DIGEST"`, "$CI_LINT_IMAGE",
	`"ghcr.io/itiquette/nanolinter:core@sha256:PIN_DIGEST"`, "$CI_LINT_IMAGE",
	// The GitHub gate's lint image, a mirror the trial replaces with its own.
	"ghcr.io/itiquette/nanolinter:go@sha256:PIN_DIGEST", "codefloe.com/itiquette/nanolinter:go@sha256:"+unrunDigest,
	"ghcr.io/itiquette/nanolinter:kotlin@sha256:PIN_DIGEST", "codefloe.com/itiquette/nanolinter:kotlin@sha256:"+unrunDigest,
	"ghcr.io/itiquette/nanolinter:core@sha256:PIN_DIGEST", "codefloe.com/itiquette/nanolinter:core@sha256:"+unrunDigest,
	// The Gradle release builds in the JDK image the Maven demos pin.
	"docker.io/library/maven:3.9-eclipse-temurin-25@sha256:PIN_MAVEN_IMAGE_DIGEST", "docker.io/library/maven:3.9-eclipse-temurin-25@sha256:96f175708f766ea049ab6c794a85335d238894c75f8eb364bddd98086a5b7cfd",
	// The monorepo's Forgejo release builds its frontend in the Node image the
	// npm service fixture pins.
	"docker.io/library/node:24.17.0-trixie@sha256:PIN_NODE_IMAGE_DIGEST", "docker.io/library/node:24.17.0-trixie@sha256:61db8992b5c481488fe236ea69fe94035ba73df76a474051ed2e9713f3a15e5a",
	"registry.example.com/team/check-tools@sha256:PIN_IMAGE_DIGEST", gitlabJobPlaceholder,
	// The monorepo checks and builds each part in its runtime image: Java in
	// the trial's job image, Node in a further one.
	"ghcr.io/diggsweden/forge-ci-runtime-java-25@sha256:PIN_JAVA_IMAGE_DIGEST", gitlabJobPlaceholder,
	"ghcr.io/diggsweden/forge-ci-runtime-node-24@sha256:PIN_NODE_IMAGE_DIGEST", gitlabJobPlaceholder+"_NODE",
	// A Go build runs in the trial's job image too.
	"ghcr.io/diggsweden/forge-ci-runtime-go-1.27@sha256:PIN_GO_IMAGE_DIGEST", gitlabJobPlaceholder,
	"server-url: https://gitlab.example.com", "server-url: "+gitlabServerPlaceholder,
	// No trial runs the demo's copy of the snapshot policy, so its registry
	// is a host of no instance rather than a variable a trial would bind.
	"registry: registry.gitlab.example.com", "registry: registry.gitlab.example",
	// The Go CLI's snapshot of files builds in the trial's job image.
	"REGISTRY/OWNER/go-build@sha256:PIN_GO_BUILD_DIGEST", "${{ vars.CI_JOB_IMAGE }}",
	"gitlab.example.com/team/ci/forge-ci", "$CI_SERVER_FQDN/itiquette/forge-ci",
	"team/ci/forge-ci", "itiquette/forge-ci",
)

// exampleDemoFiles maps a demo's CI files to the release files they are
// filled from: the example's whole directory, where a project keeps each
// file, and the changelog configuration `forge-ci init` writes.
func exampleDemoFiles(example string) map[string]string {
	dir := path.Join("examples", example)

	// The base-image graph is a release alone, with the planner and the build
	// contexts the example ships beside it. Its GitLab release is a policy's:
	// the example holds the gate and the two commands that policy names.
	if example == "base-images" {
		files := map[string]string{
			".forge-ci/artifacts.yml":                path.Join(dir, "artifacts.yml"),
			".forge-ci/gitcliff/keepachangelog.toml": "templates/gitcliff/keepachangelog.toml",
			".forge-ci/gitcliff/minimal.toml":        "templates/gitcliff/minimal.toml",
			".forgejo/workflows/release.yml":         path.Join(dir, "forgejo/release.yml"),
			GitLabGateFile:                           path.Join(dir, "gitlab/.gitlab-ci.yml"),
			"nanolinter.toml":                        path.Join(dir, "nanolinter.toml"),
			".gitlab/check-release.sh":               path.Join(dir, "gitlab/check-release.sh"),
			GitLabConsumerBuildScript:                path.Join(dir, "gitlab/build-release.sh"),
		}
		for _, file := range []string{"base-plan.sh", "images/base/Containerfile", "images/base/base.txt", "images/tools/Containerfile", "images/tools/tools.txt", "images/debug/Containerfile", "images/debug/debug.txt"} {
			files[file] = path.Join(dir, file)
		}

		return files
	}

	files := map[string]string{
		".forge-ci/artifacts.yml":                path.Join(dir, "artifacts.yml"),
		".forge-ci/gitcliff/keepachangelog.toml": "templates/gitcliff/keepachangelog.toml",
		".forge-ci/gitcliff/minimal.toml":        "templates/gitcliff/minimal.toml",
		GitLabGateFile:                           path.Join(dir, "gitlab/.gitlab-ci.yml"),
		"nanolinter.toml":                        path.Join(dir, "nanolinter.toml"),
	}

	files[".forgejo/workflows/pullrequest.yml"] = path.Join(dir, "forgejo/pullrequest.yml")
	files[".forgejo/workflows/release.yml"] = path.Join(dir, "forgejo/release.yml")

	files[githubDemoWorkflows+"/pullrequest.yml"] = path.Join(dir, "github/pullrequest-workflow.yml")
	files[githubDemoWorkflows+"/release.yml"] = path.Join(dir, "github/release-workflow.yml")

	// The monorepo's GitLab release is a policy's, as the base-image graph's:
	// the example holds the two commands that policy names.
	if example == "monorepo" {
		files[".gitlab/check-release.sh"] = path.Join(dir, "gitlab/check-release.sh")
		files[GitLabConsumerBuildScript] = path.Join(dir, "gitlab/build-release.sh")
	}

	// The Go CLI shows the snapshot of files: the Forgejo caller, and the
	// include a GitLab policy project holds, kept beside the gate.
	if example == "go-cli" {
		files[".forgejo/workflows/snapshot-files.yml"] = path.Join(dir, "forgejo/snapshot-files.yml")
		files[".gitlab/snapshot-files-policy.yml"] = path.Join(dir, "gitlab/snapshot-files-policy.yml")

		// And the release of distro packages, the example's GoReleaser
		// alternative: kept as fixtures, each file where a project copies it
		// to, because a second release workflow would run on the same tag.
		// The trial stages them over the root (WithForgejoFixtures). The
		// locked mise tools beside them are the demo's own.
		files[ForgejoPackagesFixtures+"/.forgejo/workflows/release.yml"] = path.Join(dir, "forgejo/release-goreleaser.yml")
		files[ForgejoPackagesFixtures+"/.forge-ci/artifacts.yml"] = path.Join(dir, "artifacts-forgejo-packages.yml")
		files[ForgejoPackagesFixtures+"/.goreleaser.yml"] = path.Join(dir, ".goreleaser.yml")
	}

	if example == "go-service" {
		files[".forge-ci/images.yml"] = path.Join(dir, "artifacts-forgejo.yml")
		files["Containerfile"] = path.Join(dir, "Containerfile.example")
		files["Containerfile.forgejo"] = path.Join(dir, "forgejo/Containerfile.example")
	}

	return files
}

// yamlComment is a comment after a value; a FIXME there explains the value
// and is kept, as an adopter would keep it.
var yamlComment = regexp.MustCompile(`(?m)[ \t]+#.*$`)

// An example demo's CI files are its example's, never assembled by hand, so
// a demo run proves the example a person copies. Rewrite them after changing
// an example with
//
//	go test ./test/live/internal/livetest -run TestExampleDemosAreTheirExamples -args -update-demos
func TestExampleDemosAreTheirExamples(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)

	for demo, example := range exampleDemos {
		t.Run(demo, func(t *testing.T) {
			t.Parallel()

			for file, from := range exampleDemoFiles(example) {
				body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(from)))
				if err != nil {
					t.Fatal(err)
				}

				want := exampleDemoValues.Replace(string(body))

				// Init's own detection, less the program names the demos
				// build under on purpose.
				for _, left := range consumerinit.Remaining(yamlComment.ReplaceAllString(want, "")) {
					if left.Token != "my-cli" && left.Token != "my-service" {
						t.Errorf("%s:%d keeps the placeholder %s", file, left.Line, left.Token)
					}
				}

				target := filepath.Join(root, "test", "live", "fixtures", "consumers", demo, "consumer", filepath.FromSlash(file))
				if *updateDemos {
					if err = os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
						t.Fatal(err)
					}

					if err = os.WriteFile(target, []byte(want), 0o644); err != nil {
						t.Fatal(err)
					}

					continue
				}

				got, err := os.ReadFile(target)
				if err != nil || string(got) != want {
					t.Errorf("%s is not %s filled in (%v); rewrite it with -args -update-demos", file, from, err)
				}
			}
		})
	}
}

// An example demo serves every forge its example has a pull-request
// workflow for: the example's GitHub workflows once passed no runtime image,
// which the GitHub renderer requires, and their demos had no GitHub variant.
func TestExampleDemosServeEveryForgeTheirExampleDoes(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)
	forges := map[string]string{
		"github/pullrequest-workflow.yml": githubDemoWorkflows + "/pullrequest.yml",
		"forgejo/pullrequest.yml":         ".forgejo/workflows/pullrequest.yml",
		"gitlab/.gitlab-ci.yml":           GitLabGateFile,
	}

	for demo, example := range exampleDemos {
		files := exampleDemoFiles(example)

		for workflow, file := range forges {
			if _, err := os.Stat(filepath.Join(root, "examples", example, filepath.FromSlash(workflow))); err != nil {
				continue
			}

			if files[file] != path.Join("examples", example, workflow) {
				t.Errorf("%s shows %s, which has %s, but its demo has no %s", demo, example, workflow, file)
			}
		}
	}
}
