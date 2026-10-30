// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package genericcli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/livetest"
	"gopkg.in/yaml.v3"
)

func TestForgejoNativeGraphAndSDKPin(t *testing.T) {
	body, err := os.ReadFile("forgejo-release.yml")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(body), "ENGINE_SHA", strings.Repeat("a", 40))
	text = strings.ReplaceAll(text, "VERSION_BUMP", "cargo")
	text = strings.ReplaceAll(text, "BUMP_IMAGE", "${{ vars.CI_JOB_IMAGE }}")
	text = strings.ReplaceAll(text, "IS_GO", "false")
	if _, err := livetest.ReadConsumerPin(map[string]string{"release.yml": text}); err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Needs   any               `yaml:"needs"`
			RunsOn  string            `yaml:"runs-on"`
			Uses    string            `yaml:"uses"`
			Secrets map[string]string `yaml:"secrets"`
			Steps   []struct {
				Name string            `yaml:"name"`
				Run  string            `yaml:"run"`
				Uses string            `yaml:"uses"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
			Container struct {
				Image string `yaml:"image"`
			} `yaml:"container"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(text), &workflow); err != nil {
		t.Fatal(err)
	}
	jobs := workflow.Jobs
	if jobs["checks"].Needs != "authorize" || jobs["prepare"].Needs != "checks" || jobs["build"].Needs != "prepare" {
		t.Fatal("authorization/prepare ordering lost")
	}
	if jobs["authorize"].RunsOn != "${{ fromJSON(vars.CI_RUNNER_LABEL) }}" || !strings.Contains(jobs["build"].RunsOn, "CI_RUNNER_LABEL_CONTAINER") || !strings.Contains(jobs["checks"].RunsOn, "CI_RUNNER_LABEL_CONTAINER") {
		t.Fatal("runner capability boundary lost")
	}
	for _, name := range []string{"authorize", "checks", "build"} {
		if len(jobs[name].Secrets) != 0 {
			t.Fatalf("source job %s receives signing secrets", name)
		}
	}
	if !strings.Contains(jobs["sign-and-publish"].Uses, "sign-and-publish-release.yml") || !strings.Contains(text, "--allowed-signers-file .forgejo/release-allowed-signers") || strings.Contains(text, "--skip-signature") {
		t.Fatal("shared signer/request authorization missing")
	}
	if !strings.Contains(text, "bash .gitlab/check-release.sh") || !strings.Contains(text, "bash .gitlab/build-release.sh") || strings.Contains(text, "matrix:") {
		t.Fatal("shared native file producer lost")
	}
	verified := false
	for _, step := range jobs["build"].Steps {
		if step.Name == "Revalidate signed request and prepared source" {
			if step.Env["EXPECTED"] != "${{ forgejo.sha }}" || step.Env["REQUEST"] != "${{ forgejo.ref_name }}" ||
				!strings.Contains(step.Run, "verify-tag \"$REQUEST\"") ||
				!strings.Contains(step.Run, "gpg.ssh.allowedSignersFile=.forgejo/release-allowed-signers") ||
				!strings.Contains(step.Run, "refs/tags/${REQUEST}^{commit}") ||
				!strings.Contains(step.Run, "--expected-parent \"$EXPECTED\" --changelog-only") {
				t.Fatal("fresh source job must reverify request signature, event commit and prepared-source changes")
			}
			verified = true
		}
		if (strings.Contains(step.Uses, "setup-buildah") || strings.Contains(step.Run, "bash .gitlab/check-release.sh")) && !verified {
			t.Fatal("source commands or Buildah setup precede fresh signed source verification")
		}
	}
	if !verified {
		t.Fatal("fresh source job has no signed request verification")
	}
	verified = false
	checked := false
	for _, step := range jobs["checks"].Steps {
		if step.Run == "forge-ci platform checkout" && step.Env["CHECKOUT_REF"] != "${{ forgejo.sha }}" {
			t.Fatal("original checks must checkout the immutable event request, not prepared source")
		}
		if step.Name == "Revalidate signed original source" {
			if step.Env["EXPECTED"] != "${{ forgejo.sha }}" || !strings.Contains(step.Run, "test \"$(git rev-parse HEAD)\" = \"$EXPECTED\"") || !strings.Contains(step.Run, "verify-tag \"$REQUEST\"") || !strings.Contains(step.Run, "--expected-commit \"$EXPECTED\"") {
				t.Fatal("original checks lost fresh HEAD/event/tag/signature binding")
			}
			verified = true
		}
		if strings.Contains(step.Uses, "setup-buildah") || strings.Contains(step.Run, "bash .gitlab/check-release.sh") {
			if !verified {
				t.Fatal("original source execution precedes fresh authentication")
			}
			if strings.Contains(step.Run, "bash .gitlab/check-release.sh") {
				checked = true
			}
		}
	}
	if !checked {
		t.Fatal("full original gate not run before prepare")
	}
	for _, step := range jobs["build"].Steps {
		if strings.Contains(step.Run, "check-release.sh") {
			t.Fatal("original repository guards repeated on version-prepared source")
		}
		if step.Run == "forge-ci platform checkout" && step.Env["CHECKOUT_REF"] != "${{ needs.prepare.outputs.release-sha }}" {
			t.Fatal("release producer not bound to prepared source")
		}
	}
}

// A checkout by prepared SHA has no local tags. Exercise the workflow's actual
// fetch prefix against native Git, so signature/prepared-source verification
// gets both annotated objects without changing HEAD or converting object format.
func TestForgejoFreshSourceFetchesRequestAndFinalTag(t *testing.T) {
	body, err := os.ReadFile("forgejo-release.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		t.Fatal(err)
	}
	prefix := ""
	for _, step := range workflow.Jobs["build"].Steps {
		if step.Name == "Revalidate signed request and prepared source" {
			var found bool
			prefix, _, found = strings.Cut(step.Run, "\ntest ")
			if !found {
				t.Fatal("verification has no event-commit binding after fetch")
			}
		}
	}
	if prefix == "" {
		t.Fatal("fresh source verification not found")
	}
	requestPrefix := ""
	for _, step := range workflow.Jobs["checks"].Steps {
		if step.Name == "Revalidate signed original source" {
			var found bool
			requestPrefix, _, found = strings.Cut(step.Run, "\ngit -c ")
			if !found {
				t.Fatal("original-source verification must bind HEAD/event/tag before SSH verification")
			}
		}
	}
	if requestPrefix == "" {
		t.Fatal("original source verification missing")
	}
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			source, checkout := filepath.Join(root, "source"), filepath.Join(root, "checkout")
			env := append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@localhost", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@localhost")
			run := func(input string, args ...string) string {
				t.Helper()
				cmd := exec.CommandContext(t.Context(), "git", args...)
				cmd.Env, cmd.Stdin = env, strings.NewReader(input)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			run("", "init", "--quiet", "--object-format="+format, source)
			tree := run("", "-C", source, "mktree")
			request := run("request\n", "-C", source, "commit-tree", tree)
			prepared := run("prepared\n", "-C", source, "commit-tree", tree, "-p", request)
			run("", "-C", source, "update-ref", "HEAD", prepared)
			run("", "-C", source, "-c", "tag.gpgsign=false", "tag", "-a", "-m", "request", "release-request/v0.8.6", request)
			run("", "-C", source, "-c", "tag.gpgsign=false", "tag", "-a", "-m", "release", "v0.8.6", prepared)
			run("", "clone", "--quiet", "--no-checkout", "--no-tags", source, checkout)
			run("", "-C", checkout, "checkout", "--quiet", "--detach", prepared)
			missing := exec.CommandContext(t.Context(), "git", "-C", checkout, "rev-parse", "--verify", "refs/tags/v0.8.6")
			missing.Env = env
			if err := missing.Run(); err == nil {
				t.Fatal("regression fixture unexpectedly already has final tag")
			}
			cmd := exec.CommandContext(t.Context(), "bash", "-c", prefix)
			cmd.Dir, cmd.Env = checkout, append(env, "REQUEST=release-request/v0.8.6", "RELEASE_TAG=v0.8.6")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("workflow fetch prefix: %v\n%s", err, out)
			}
			for tag, want := range map[string]string{"release-request/v0.8.6": request, "v0.8.6": prepared} {
				if run("", "-C", checkout, "cat-file", "-t", "refs/tags/"+tag) != "tag" || run("", "-C", checkout, "rev-parse", "refs/tags/"+tag+"^{commit}") != want {
					t.Fatalf("workflow did not materialize exact annotated tag %s", tag)
				}
			}
			if run("", "-C", checkout, "rev-parse", "HEAD") != prepared || run("", "-C", checkout, "rev-parse", "--show-object-format") != format {
				t.Fatal("tag fetch changed the prepared checkout or object format")
			}
			// Execute the original-check job's actual identity checks on the
			// request and on the prepared child: only the former is admissible.
			for _, head := range []string{request, prepared} {
				run("", "-C", checkout, "checkout", "--quiet", "--detach", head)
				cmd := exec.CommandContext(t.Context(), "bash", "-c", requestPrefix)
				cmd.Dir, cmd.Env = checkout, append(env, "REQUEST=release-request/v0.8.6", "EXPECTED="+request)
				out, err := cmd.CombinedOutput()
				if (err == nil) != (head == request) {
					t.Fatalf("original-source identity accepted wrong revision: head=%s error=%v\n%s", head, err, out)
				}
			}
		})
	}
}
