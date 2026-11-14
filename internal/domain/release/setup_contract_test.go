// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"gopkg.in/yaml.v3"
)

// Exercise the actual pre-checkout shell with presence booleans, not keys. This
// catches changes to conditional groups and defaults without moving preflight
// behind an engine checkout or duplicating its shell in a test fixture.
func TestSetupMatchesForgejoPresencePreflight(t *testing.T) {
	t.Parallel()

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is required for workflow contract test")
	}

	body, err := os.ReadFile(filepath.Join(reporoot.Path(t), ".forgejo/workflows/prepare-release.yml"))
	if err != nil {
		t.Fatal(err)
	}

	var workflow struct {
		On struct {
			Call struct {
				Inputs map[string]struct {
					Default string `yaml:"default"`
				} `yaml:"inputs"`
			} `yaml:"workflow_call"`
		} `yaml:"on"`
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Run  string            `yaml:"run"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"sign-distro-packages", "publish-container-images"} {
		if workflow.On.Call.Inputs[name].Default != "true" {
			t.Fatalf("review setup default: %s", name)
		}
	}

	found := false

	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if step.Name != "Check required secrets" {
				continue
			}

			found = true

			for _, distro := range []bool{false, true} {
				for _, images := range []bool{false, true} {
					state := func(v bool) release.SetupState {
						if v {
							return release.SetupRequired
						}

						return release.SetupUnused
					}

					expected := map[string]bool{}
					for _, item := range release.KeyReleaseSetup("forgejo", true, state(distro), release.SetupUnused, state(images)) {
						expected[item.Name] = item.State == release.SetupRequired
					}

					for missing := range expected {
						cmd := exec.CommandContext(t.Context(), bash, "--noprofile", "--norc", "-c", step.Run)

						cmd.Env = []string{"PATH=" + filepath.Dir(bash), fmt.Sprintf("SIGN_DISTRO_PACKAGES=%t", distro), fmt.Sprintf("PUBLISH_CONTAINER_IMAGES=%t", images)}
						for name := range step.Env {
							if strings.HasPrefix(name, "SECRET_") {
								cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%t", name, name != "SECRET_"+missing))
							}
						}

						output, runErr := cmd.CombinedOutput()
						if (runErr != nil) != expected[missing] {
							t.Fatalf("distro=%t images=%t missing=%s: %v %s", distro, images, missing, runErr, output)
						}
					}

					for name := range step.Env {
						if secret, hasPrefix := strings.CutPrefix(name, "SECRET_"); hasPrefix {
							if _, ok := expected[secret]; !ok {
								t.Fatalf("new secret not modeled: %s", name)
							}
						}
					}
				}
			}
		}
	}

	if !found {
		t.Fatal("presence preflight moved; review setup contract")
	}
}

// The prepare job's GPG check runs as the setup model says: a release whose
// declaration signs its files with GPG stops on each missing GPG secret, one
// signed with the Cosign key on none, and a plan that said neither stops.
func TestSetupMatchesForgejoGPGReleaseFilePreflight(t *testing.T) {
	t.Parallel()

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is required for workflow contract test")
	}

	body, err := os.ReadFile(filepath.Join(reporoot.Path(t), ".forgejo/workflows/prepare-release.yml"))
	if err != nil {
		t.Fatal(err)
	}

	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Run  string            `yaml:"run"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		t.Fatal(err)
	}

	var run string

	for _, step := range workflow.Jobs["prepare"].Steps {
		if step.Name == "Check the GPG release file signing secrets" {
			run = step.Run
		}
	}

	if run == "" {
		t.Fatal("the prepare job has no GPG release file check; review setup contract")
	}

	check := func(signing, missing string) error {
		cmd := exec.CommandContext(t.Context(), bash, "--noprofile", "--norc", "-c", run)
		cmd.Env = []string{"PATH=" + filepath.Dir(bash), "RELEASE_FILE_SIGNING=" + signing}

		for _, name := range []string{"RELEASE_GPG_PRIVATE_KEY", "RELEASE_GPG_FINGERPRINT", "RELEASE_GPG_PASSPHRASE"} {
			cmd.Env = append(cmd.Env, fmt.Sprintf("SECRET_%s=%t", name, name != missing))
		}

		return cmd.Run()
	}

	for signing, files := range map[string]release.SetupState{"cosign": release.SetupUnused, "gpg": release.SetupRequired} {
		for _, item := range release.KeyReleaseSetup("forgejo", true, release.SetupUnused, files, release.SetupUnused) {
			if !strings.HasPrefix(item.Name, "RELEASE_GPG_") {
				continue
			}

			if err := check(signing, item.Name); (err != nil) != (item.State == release.SetupRequired) {
				t.Errorf("signing %s without %s: refused %t, the model says %s", signing, item.Name, err != nil, item.State)
			}
		}
	}

	if check("", "") == nil {
		t.Error("the check passed a plan that said nothing about release file signing")
	}
}
