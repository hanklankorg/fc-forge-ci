// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/gitlabrender"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

const pinnedBuilderImage = "registry.example/kaniko-job@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// The request check refuses, before Prepare pushes anything, an export the
// export jobs would not carry out, and export commands without export-files,
// which would have no effect.
func TestReleaseExportCheckRefusesWhatTheJobsWouldNotCarryOut(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		args, commands, refusal string
	}{
		"off, no commands":          {args: "false '' '' '[]'", commands: "[]"},
		"commands without the flag": {args: "false '' '' '[]'", commands: `["bash .gitlab/export.sh"]`, refusal: "export-files is false"},
		"no builder job image":      {args: "true '' '' '[]'", commands: `["x"]`, refusal: "builder-job-image"},
		"amd64 by default":          {args: "true '" + pinnedBuilderImage + "' '' '[]'", commands: `["x"]`},
		"arm64 without its runner":  {args: "true '" + pinnedBuilderImage + "' 'linux/amd64,linux/arm64' '[]'", commands: `["x"]`, refusal: "build-runner-tags-linux-arm64"},
		"arm64 with its runner":     {args: "true '" + pinnedBuilderImage + "' 'linux/arm64' '[\"arm64\"]'", commands: `["x"]`},
		"a runner selects nothing":  {args: "true '" + pinnedBuilderImage + "' '' '[\"arm64\"]'", commands: `["x"]`},
		"an unknown export-files":   {args: "maybe '' '' '[]'", commands: "[]", refusal: "true or false"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			out, err := runReleaseFunction(t, t.TempDir(), ownerCLI+"rc_check_exports "+test.args+" <<'EOF'\n"+test.commands+"\nEOF\n", "OWNER_CLI_BINARY="+os.Args[0])
			if test.refusal == "" {
				if err != nil {
					t.Fatalf("refused: %v\n%s", err, out)
				}

				return
			}

			if err == nil || !strings.Contains(out, test.refusal) {
				t.Fatalf("err=%v, want a refusal naming %q:\n%s", err, test.refusal, out)
			}
		})
	}
}

// An export job of an architecture release-platforms leaves out exports
// nothing; every other runs. Empty selects linux/amd64, the platform a
// container declares when it names none: the arm64 runner serves an
// architecture and never selects one.
func TestReleaseExportJobRunsForTheSelectedArchitectures(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		arch, platforms string
		selected        bool
	}{
		{"amd64", "", true},
		{"arm64", "", false},
		{"amd64", "linux/arm64", false},
		{"arm64", "linux/amd64,linux/arm64", true},
		{"arm64", "linux/amd64", false},
	} {
		out, err := runReleaseFunction(t, t.TempDir(), ownerCLI+"rc_export_selected "+test.arch+" '"+test.platforms+"'", "OWNER_CLI_BINARY="+os.Args[0])
		if err != nil || strings.TrimSpace(out) != strconv.FormatBool(test.selected) {
			t.Errorf("%s with release-platforms %q: selected=%s, want %v: %v", test.arch, test.platforms, out, test.selected, err)
		}
	}
}

// Without release-platforms the images build what each container declares
// and the exports linux/amd64: the request check refuses a plan that would
// build an arm64 image then, so the two never build different
// architectures. Named, both build what release-platforms names.
func TestReleaseExportCheckKeepsTheExportsToTheImagesArchitectures(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		platforms, plan, refusal string
	}{
		"amd64 images":        {plan: `{"builds": [{"arch": "amd64"}]}`},
		"arm64 images":        {plan: `{"builds": [{"arch": "amd64"}, {"arch": "arm64"}]}`, refusal: "set release-platforms"},
		"arm64 images, named": {platforms: "linux/amd64,linux/arm64", plan: `{"builds": [{"arch": "amd64"}, {"arch": "arm64"}]}`},
		"no images, empty":    {},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			work := t.TempDir()
			if test.plan != "" {
				if err := os.WriteFile(filepath.Join(work, "release-images-plan.json"), []byte(test.plan), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			out, err := runReleaseFunction(t, work, ownerCLI+"rc_check_exports true '"+pinnedBuilderImage+"' '"+test.platforms+"' '[\"arm64\"]' <<'EOF'\n[\"x\"]\nEOF\n", "RC_WORK="+work, "OWNER_CLI_BINARY="+os.Args[0])
			if (err != nil) != (test.refusal != "") || !strings.Contains(out, test.refusal) {
				t.Fatalf("err=%v, want refusal %q:\n%s", err, test.refusal, out)
			}
		})
	}
}

// The build job takes every export's dist/ and refuses a path two of them
// both hold, or one that is not a regular file or directory.
func TestReleaseBuildGathersTheExportsWithoutOverwriting(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	work := filepath.Join(project, "consumer")

	for _, dir := range []string{work, filepath.Join(project, "_release-export-amd64/dist/linux-amd64"), filepath.Join(project, "_release-export-arm64/dist/linux-arm64")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	for _, arch := range []string{"amd64", "arm64"} {
		if err := os.WriteFile(filepath.Join(project, "_release-export-"+arch, "dist", "linux-"+arch, "hello-linux-"+arch), []byte(arch), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if out, err := runReleaseFunction(t, work, ownerCLI+"rc_gather_exports true", "CI_PROJECT_DIR="+project, "OWNER_CLI_BINARY="+os.Args[0]); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	for _, arch := range []string{"amd64", "arm64"} {
		if body, err := os.ReadFile(filepath.Join(work, "dist", "linux-"+arch, "hello-linux-"+arch)); err != nil || string(body) != arch {
			t.Fatalf("%s binary not gathered: %q %v", arch, body, err)
		}
	}

	if out, err := runReleaseFunction(t, work, ownerCLI+"rc_gather_exports true", "CI_PROJECT_DIR="+project, "OWNER_CLI_BINARY="+os.Args[0]); err == nil || !strings.Contains(out, "must not exist yet") {
		t.Fatalf("a second export of the same path was merged: %v\n%s", err, out)
	}

	link := filepath.Join(project, "_release-export-arm64", "dist", "linux-arm64", "escape")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatal(err)
	}

	fresh := filepath.Join(project, "fresh")
	if err := os.Mkdir(fresh, 0o755); err != nil {
		t.Fatal(err)
	}

	if out, err := runReleaseFunction(t, fresh, ownerCLI+"rc_gather_exports true", "CI_PROJECT_DIR="+project, "OWNER_CLI_BINARY="+os.Args[0]); err == nil || !strings.Contains(out, "regular files") {
		t.Fatalf("an export holding a link was gathered: %v\n%s", err, out)
	}

	if out, err := runReleaseFunction(t, fresh, ownerCLI+"rc_gather_exports false", "CI_PROJECT_DIR="+project, "OWNER_CLI_BINARY="+os.Args[0]); err != nil {
		t.Fatalf("without export-files nothing is gathered: %v\n%s", err, out)
	}
}

// emptyReleasePlatforms is what an empty release-platforms builds, stated
// alike wherever a release takes it.
const emptyReleasePlatforms = "Empty builds each container's declared platforms, linux/amd64 when it declares none"

// The GitLab release and the Forgejo release-images workflow describe an
// empty release-platforms alike, as the planner they share carries it out.
func TestEmptyReleasePlatformsIsDescribedAlikeOnEveryForge(t *testing.T) {
	t.Parallel()

	described := map[string]string{
		"templates/release.yml": fmt.Sprint(gitlabrender.InputSpec(t, "templates/release.yml")["release-platforms"]["description"]),
	}

	var workflow struct {
		On struct {
			WorkflowCall struct {
				Inputs map[string]struct {
					Description string `yaml:"description"`
				} `yaml:"inputs"`
			} `yaml:"workflow_call"`
		} `yaml:"on"`
	}
	if err := yaml.Unmarshal(reporoot.ReadFile(t, ".forgejo/workflows/release-images.yml"), &workflow); err != nil {
		t.Fatal(err)
	}

	described[".forgejo/workflows/release-images.yml"] = workflow.On.WorkflowCall.Inputs["release-platforms"].Description

	for path, description := range described {
		if !strings.Contains(description, emptyReleasePlatforms) {
			t.Errorf("%s: release-platforms is described as %q; want it to state %q", path, description, emptyReleasePlatforms)
		}
	}
}
