// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package doctor_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/app/doctor"
	"github.com/diggsweden/forge-ci/internal/domain/release"
)

const setupCommit = "1234567890abcdef1234567890abcdef12345678"

func setupFile(t *testing.T, root, name, body string) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func setupItem(t *testing.T, report doctor.RequirementsReport, name string) doctor.SetupItem {
	t.Helper()

	for _, call := range report.Calls {
		for _, item := range call.Requirements {
			if item.Name == name {
				return item
			}
		}
	}

	t.Fatalf("requirement %s missing: %+v", name, report)

	return doctor.SetupItem{}
}

func TestSetupAllThreeDeliveryLayers(t *testing.T) {
	t.Parallel()

	for _, forge := range []string{"github", "gitlab", "forgejo"} {
		t.Run(forge, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()

			switch forge {
			case "gitlab":
				setupFile(t, root, ".gitlab-ci.yml", "include:\n  - component: gitlab.example/owner/engine/release@"+setupCommit+"\n    inputs:\n      sign-distro-packages: false\n")
			case "forgejo":
				setupFile(t, root, ".forgejo/workflows/release.yml", "jobs:\n  prepare:\n    uses: owner/engine/.forgejo/workflows/prepare-release.yml@"+setupCommit+"\n    with:\n      forge-ci-sha: "+setupCommit+"\n      sign-distro-packages: false\n      publish-container-images: false\n    secrets:\n      RELEASE_TOKEN: ${{ secrets.MY_RELEASE_TOKEN }}\n")
			case "github":
				setupFile(t, root, ".github/workflows/release.yml", "jobs:\n  release:\n    uses: owner/engine/.github/workflows/release-orchestrator.yml@"+setupCommit+"\n    secrets:\n      RELEASE_TOKEN: ${{ secrets.MY_RELEASE_TOKEN }}\n")
				setupFile(t, root, ".forge-ci/artifacts.yml", "artifacts:\n  - name: app\n    project-type: meta\nsign:\n  method: sigstore\ngit-signing:\n  method: ssh\n")
			}

			report := doctor.Requirements(root, "owner/engine", setupCommit)
			if len(report.Calls) != 1 || report.Calls[0].Forge != forge {
				t.Fatalf("wrong calls: %+v", report)
			}

			if got := setupItem(t, report, "RELEASE_GPG_PRIVATE_KEY").State; got != release.SetupUnused {
				t.Fatalf("GPG should not be needed: %s", got)
			}

			if got := setupItem(t, report, "RELEASE_SSH_SIGNING_KEY").State; got != release.SetupRequired {
				t.Fatalf("SSH should be needed: %s", got)
			}

			if forge != "gitlab" && setupItem(t, report, "RELEASE_TOKEN").Source != "secrets.MY_RELEASE_TOKEN" {
				t.Fatal("caller mapping was lost")
			}

			if forge == "gitlab" {
				if setupItem(t, report, "RELEASE_TOKEN").Scope != "release/publish" {
					t.Fatal("wrong GitLab environment scope")
				}
			}

			if forge == "github" && setupItem(t, report, "GITHUB_TOKEN").State != release.SetupAutomatic {
				t.Fatal("automatic token treated as user secret")
			}
		})
	}
}

// The GitLab release needs its registry token only where the registry
// access check runs: with release-images or a base-stages graph.
func TestSetupGitLabRegistryTokenFollowsImagePublication(t *testing.T) {
	t.Parallel()

	for inputs, want := range map[string]release.SetupState{
		"":                                      release.SetupUnused,
		"release-images: false":                 release.SetupUnused,
		"base-stages: ''":                       release.SetupUnused,
		"release-images: true":                  release.SetupRequired,
		"base-stages: tools":                    release.SetupRequired,
		"release-images: $IMAGES":               release.SetupConditional,
		"base-stages: $STAGES":                  release.SetupConditional,
		"release-images: true, base-stages: $S": release.SetupRequired,
	} {
		t.Run(inputs, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			setupFile(t, root, ".gitlab-ci.yml", "include:\n  - component: gitlab.example/owner/engine/release@"+setupCommit+"\n    inputs: {sign-distro-packages: false, "+inputs+"}\n")

			report := doctor.Requirements(root, "owner/engine", setupCommit)
			if got := setupItem(t, report, "RELEASE_REGISTRY_TOKEN").State; got != want {
				t.Fatalf("RELEASE_REGISTRY_TOKEN is %s, want %s", got, want)
			}
		})
	}
}

// A Forgejo declaration that says sign.method gpg or sign.files-method gpg
// signs its release files with the GPG key, pinned by its fingerprint, even
// without distro packages.
// One without a method, whose effective method is gpg, signs them with the
// Cosign key and needs no GPG secret. The signing call names the declaration
// by the prepare job's output, which is that job's input.
func TestSetupForgejoGPGReleaseFileSigning(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, input, sign string
		want              release.SetupState
	}{
		{name: "gpg declaration", input: "      artifacts-config: .forge-ci/release.yml\n", sign: "sign:\n  method: gpg\n", want: release.SetupRequired},
		{name: "gpg default declaration", sign: "sign:\n  method: gpg\n", want: release.SetupRequired},
		{name: "no method named", want: release.SetupUnused},
		{name: "cosign key", sign: "sign:\n  method: kms\n  key: env://COSIGN_KEY\n", want: release.SetupUnused},
		{name: "gpg files beside the cosign key", sign: "sign:\n  method: kms\n  key: env://COSIGN_KEY\n  files-method: gpg\n", want: release.SetupRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			declaration := ".forge-ci/artifacts.yml"

			if test.input != "" {
				declaration = ".forge-ci/release.yml"
			}

			setupFile(t, root, declaration, "artifacts:\n  - name: app\n    project-type: maven\n"+test.sign)
			setupFile(t, root, ".forgejo/workflows/release.yml", "jobs:\n"+
				"  prepare:\n    uses: owner/engine/.forgejo/workflows/prepare-release.yml@"+setupCommit+"\n    with:\n      forge-ci-sha: "+setupCommit+"\n      sign-distro-packages: false\n"+test.input+
				"  sign:\n    uses: owner/engine/.forgejo/workflows/sign-and-publish-release.yml@"+setupCommit+"\n    with:\n      forge-ci-sha: "+setupCommit+"\n      sign-distro-packages: false\n      artifacts-config: ${{ needs.prepare.outputs.artifacts-config }}\n")

			report := doctor.Requirements(root, "owner/engine", setupCommit)
			if len(report.Calls) != 2 {
				t.Fatalf("calls = %+v, want prepare and sign", report.Calls)
			}

			for _, call := range report.Calls {
				for _, item := range call.Requirements {
					if strings.HasPrefix(item.Name, "RELEASE_GPG_") && item.State != test.want {
						t.Errorf("%s: %s is %s, want %s", call.Workflow, item.Name, item.State, test.want)
					}
				}
			}

			for _, unknown := range report.Unknowns {
				if strings.Contains(unknown, "artifacts-config") || strings.Contains(unknown, "GPG release file signing") {
					t.Errorf("unresolved declaration: %s", unknown)
				}
			}
		})
	}
}

// The GitLab release signs its release files as the Forgejo release does:
// with the GPG key for a declaration that says sign.method gpg or
// sign.files-method gpg, also without distro packages, and with the Cosign
// key for any other. The component's artifacts-config names the declaration.
func TestSetupGitLabGPGReleaseFileSigning(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, input, sign string
		want              release.SetupState
	}{
		{name: "gpg declaration", input: ", artifacts-config: .forge-ci/release.yml", sign: "sign:\n  method: gpg\n", want: release.SetupRequired},
		{name: "gpg default declaration", sign: "sign:\n  method: gpg\n", want: release.SetupRequired},
		{name: "no method named", want: release.SetupUnused},
		{name: "cosign key", sign: "sign:\n  method: kms\n  key: env://COSIGN_KEY\n", want: release.SetupUnused},
		{name: "gpg files beside the cosign key", sign: "sign:\n  method: kms\n  key: env://COSIGN_KEY\n  files-method: gpg\n", want: release.SetupRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			declaration := ".forge-ci/artifacts.yml"

			if test.input != "" {
				declaration = ".forge-ci/release.yml"
			}

			setupFile(t, root, declaration, "artifacts:\n  - name: app\n    project-type: maven\n"+test.sign)
			setupFile(t, root, ".gitlab-ci.yml", "include:\n  - component: gitlab.example/owner/engine/release@"+setupCommit+
				"\n    inputs: {sign-distro-packages: false"+test.input+"}\n")

			report := doctor.Requirements(root, "owner/engine", setupCommit)
			for _, name := range []string{"RELEASE_GPG_PRIVATE_KEY", "RELEASE_GPG_PASSPHRASE", "RELEASE_GPG_FINGERPRINT"} {
				if got := setupItem(t, report, name).State; got != test.want {
					t.Errorf("%s is %s, want %s", name, got, test.want)
				}
			}

			for _, unknown := range report.Unknowns {
				if strings.Contains(unknown, "artifacts-config") || strings.Contains(unknown, "GPG release file signing") {
					t.Errorf("unresolved declaration: %s", unknown)
				}
			}
		})
	}
}

func TestSetupLabelsUnknownRevisionAndRejectsExpression(t *testing.T) {
	t.Parallel()

	for _, ref := range []string{"main", "v0.93.0", strings.Repeat("a", 40), "${{ vars.ENGINE_REF }}"} {
		t.Run(ref, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			setupFile(t, root, ".forgejo/workflows/release.yml", "jobs:\n  prepare:\n    uses: owner/engine/.forgejo/workflows/prepare-release.yml@"+ref+"\n")

			report := doctor.Requirements(root, "owner/engine", setupCommit)
			if report.Complete {
				t.Fatalf("unknown contract reported complete: %+v", report)
			}

			if strings.Contains(ref, "$") {
				if len(report.Calls) != 0 {
					t.Fatal("expression interpreted as revision")
				}
			} else if len(report.Calls) != 1 || !strings.Contains(report.Calls[0].Contract, "unverified") {
				t.Fatal("reference guidance missing or not labelled")
			}
		})
	}
}

func TestSetupConditionalInputsAndRuntimeLedger(t *testing.T) {
	t.Parallel()

	for _, workflow := range []string{"prepare-release.yml", "sign-and-publish-release.yml"} {
		t.Run(workflow, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			setupFile(t, root, ".forgejo/workflows/release.yml", fmt.Sprintf("jobs:\n  release:\n    uses: owner/engine/.forgejo/workflows/%s@%s\n    with:\n      sign-distro-packages: ${{ vars.SIGN_PACKAGES }}\n      publish-container-images: ${{ vars.IMAGES }}\n", workflow, setupCommit))

			report := doctor.Requirements(root, "owner/engine", setupCommit)
			for _, name := range []string{"RELEASE_GPG_PRIVATE_KEY", "RELEASE_REGISTRY_TOKEN"} {
				if setupItem(t, report, name).State != release.SetupConditional {
					t.Fatalf("%s was guessed", name)
				}
			}

			if report.Complete {
				t.Fatal("conditional report marked complete")
			}
		})
	}
}

func TestSetupNeverReportsCredentialValues(t *testing.T) {
	t.Parallel()

	canary := "PRIVATE-CREDENTIAL-CANARY"
	for _, suffix := range []string{
		"    secrets:\n      RELEASE_TOKEN: " + canary + "\n",
		"    secrets:\n      RELEASE_TOKEN: [\"" + canary + "\n",
		"    secrets: &secret\n      RELEASE_TOKEN: " + canary + "\n",
		"    secrets: inherit\n",
	} {
		root := t.TempDir()
		setupFile(t, root, ".forgejo/workflows/release.yml", "jobs:\n  prepare:\n    uses: owner/engine/.forgejo/workflows/prepare-release.yml@"+setupCommit+"\n"+suffix)
		report := doctor.Requirements(root, "owner/engine", setupCommit)

		var text bytes.Buffer
		doctor.FormatRequirements(&text, report)

		encoded, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}

		if strings.Contains(text.String(), canary) || bytes.Contains(encoded, []byte(canary)) {
			t.Fatal("credential leaked into requirements report")
		}

		if report.Complete {
			t.Fatal("unresolved mapping reported complete")
		}
	}
}

func TestSetupGitHubGPGAndCosignChoices(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		method, key, git string
		gpg, cosign      release.SetupState
	}{
		{"gpg", "", "gpg", release.SetupRequired, release.SetupUnused},
		{"kms", "env://COSIGN_KEY", "ssh", release.SetupUnused, release.SetupRequired},
		{"sigstore", "", "ssh", release.SetupUnused, release.SetupUnused},
	} {
		t.Run(tc.method, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			setupFile(t, root, ".github/workflows/release.yml", "jobs:\n  release:\n    uses: owner/engine/.github/workflows/release-orchestrator.yml@"+setupCommit+"\n")

			cfg := fmt.Sprintf("artifacts:\n  - name: app\n    project-type: meta\nsign:\n  method: %s\ngit-signing:\n  method: %s\n", tc.method, tc.git)
			if tc.key != "" {
				cfg = strings.Replace(cfg, "git-signing:", "  key: "+tc.key+"\ngit-signing:", 1)
			}

			setupFile(t, root, ".forge-ci/artifacts.yml", cfg)

			report := doctor.Requirements(root, "owner/engine", setupCommit)
			if setupItem(t, report, "RELEASE_GPG_PRIVATE_KEY").State != tc.gpg || setupItem(t, report, "RELEASE_COSIGN_PRIVATE_KEY").State != tc.cosign {
				t.Fatalf("wrong signing requirements: %+v", report)
			}
		})
	}
}

func TestSetupEngineBinaryWithoutCommitStillGivesLabelledGuidance(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	setupFile(t, root, ".forgejo/workflows/release.yml", "jobs:\n  prepare:\n    uses: owner/engine/.forgejo/workflows/prepare-release.yml@"+setupCommit+"\n")

	report := doctor.Requirements(root, "owner/engine", "none")
	if len(report.Calls) != 1 || report.Complete || !strings.Contains(report.Calls[0].Contract, "unverified") {
		t.Fatalf("unusable or overconfident report: %+v", report)
	}

	var out bytes.Buffer
	doctor.FormatRequirements(&out, report)

	if !strings.Contains(out.String(), "reference guidance") || !strings.Contains(out.String(), "RELEASE_TOKEN") {
		t.Fatal("missing usable guidance or confidence label")
	}
}

func TestSetupRejectsMultipleDocumentsAndSymlinkEscape(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	setupFile(t, root, ".gitlab-ci.yml", "include: []\n---\ninclude: []\n")

	if doctor.Requirements(root, "owner/engine", setupCommit).Complete {
		t.Fatal("multiple documents ignored")
	}

	outside := t.TempDir()
	setupFile(t, outside, "release.yml", "jobs: {}\n")

	if err := os.MkdirAll(filepath.Join(root, ".forgejo"), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(outside, filepath.Join(root, ".forgejo/workflows")); err != nil {
		t.Skip("symlinks unavailable")
	}

	report := doctor.Requirements(root, "owner/engine", setupCommit)
	if !strings.Contains(strings.Join(report.Unknowns, " "), "regular YAML file") {
		t.Fatal("outside workflow directory followed")
	}
}

func TestSetupRequiredCosignTakesPrecedenceOverContainerUncertainty(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	setupFile(t, root, ".github/workflows/release.yml", "jobs:\n  release:\n    uses: owner/engine/.github/workflows/release-orchestrator.yml@"+setupCommit+"\n")
	setupFile(t, root, ".forge-ci/artifacts.yml", "artifacts:\n  - name: app\n    project-type: meta\ncontainers:\n  - name: image\n    from: [app]\nsign:\n  method: kms\n  key: env://COSIGN_KEY\ngit-signing:\n  method: ssh\n")

	report := doctor.Requirements(root, "owner/engine", setupCommit)
	for _, name := range []string{"RELEASE_COSIGN_PRIVATE_KEY", "RELEASE_COSIGN_PASSPHRASE"} {
		if setupItem(t, report, name).State != release.SetupRequired {
			t.Fatalf("required %s downgraded: %+v", name, report)
		}
	}
}

func TestSetupSkipsUnrelatedCallsButReportsUnknownReleasePaths(t *testing.T) {
	t.Parallel()

	for _, forge := range []string{"github", "forgejo", "gitlab"} {
		t.Run(forge, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			file := "." + forge + "/workflows/release.yml"
			body := "jobs:\n  check:\n    uses: another/project/check.yml@main\n  release:\n    uses: another/project/release-custom.yml@main\n"

			if forge == "gitlab" {
				file = ".gitlab-ci.yml"
				body = "include:\n  - component: gitlab.example/another/project/check@main\n  - component: gitlab.example/another/project/release-custom@main\n"
			}

			setupFile(t, root, file, body)
			report := doctor.Requirements(root, "owner/engine", setupCommit)
			// One unknown release call plus the no-recognized-calls summary;
			// the unrelated check component contributes no warning.
			if len(report.Unknowns) != 2 || report.Complete {
				t.Fatalf("noisy or incomplete classification: %+v", report)
			}
		})
	}
}

func TestSetupGitHubRuntimeOverrideInvalidatesMatchedContract(t *testing.T) {
	t.Parallel()

	for _, override := range []string{setupCommit, "v0.93.0", "${{ vars.ENGINE }}", ""} {
		t.Run(override, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			body := "jobs:\n  release:\n    uses: owner/engine/.github/workflows/release-orchestrator.yml@" + setupCommit + "\n    with:\n      forge-ci-binary-ref: '" + override + "'\n"
			setupFile(t, root, ".github/workflows/release.yml", body)

			report := doctor.Requirements(root, "owner/engine", setupCommit)
			if len(report.Calls) != 1 {
				t.Fatal("call not recognized")
			}

			wantUnverified := override != setupCommit
			if strings.Contains(report.Calls[0].Contract, "override unverified") != wantUnverified {
				t.Fatalf("wrong override confidence: %+v", report)
			}
		})
	}
}

func TestSetupGitHubContainerBuildSecretEnvelope(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, containers, mapping string
		state                     release.SetupState
		complete                  bool
	}{
		{"no build secrets", "", "", release.SetupUnused, true},
		{"unmapped envelope", "containers:\n  - name: image\n    from: [app]\n    build-secrets: [NPMRC]\n", "", release.SetupRequired, false},
		{"mapped envelope", "containers:\n  - name: image\n    from: [app]\n    build-secrets: [NPMRC]\n", "      FORGE_CI_BUILD_SECRETS_JSON: ${{ secrets.BUILD_CREDENTIALS }}\n", release.SetupRequired, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			setupFile(t, root, ".github/workflows/release.yml", "jobs:\n  release:\n    uses: owner/engine/.github/workflows/release-orchestrator.yml@"+setupCommit+"\n    with:\n      forge-ci-binary-ref: "+setupCommit+"\n    secrets:\n      RELEASE_TOKEN: ${{ secrets.RELEASE_TOKEN }}\n      RELEASE_SSH_SIGNING_KEY: ${{ secrets.RELEASE_SSH_SIGNING_KEY }}\n"+tc.mapping)
			setupFile(t, root, ".forge-ci/artifacts.yml", "artifacts:\n  - name: app\n    project-type: meta\nsign:\n  method: sigstore\ngit-signing:\n  method: ssh\n"+tc.containers)

			report := doctor.Requirements(root, "owner/engine", setupCommit)
			item := setupItem(t, report, "FORGE_CI_BUILD_SECRETS_JSON")

			if item.State != tc.state || report.Complete != tc.complete {
				t.Fatalf("wrong envelope requirement or coverage: %+v", report)
			}

			if tc.mapping != "" && item.Source != "secrets.BUILD_CREDENTIALS" {
				t.Fatalf("renamed envelope mapping lost: %+v", item)
			}
		})
	}
}

func TestSetupPreparationPublicSettings(t *testing.T) {
	t.Parallel()

	for _, forge := range []string{"forgejo", "gitlab"} {
		t.Run(forge, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			file := ".forgejo/workflows/release.yml"
			body := "jobs:\n  prepare:\n    uses: owner/engine/.forgejo/workflows/prepare-release.yml@" + setupCommit + "\n"

			if forge == "gitlab" {
				file = ".gitlab-ci.yml"
				body = "include:\n  - component: gitlab.example/owner/engine/release@" + setupCommit + "\n"
			}

			setupFile(t, root, file, body)
			report := doctor.Requirements(root, "owner/engine", setupCommit)
			settings := map[string]doctor.SetupSetting{}

			for _, setting := range report.Calls[0].Settings {
				settings[setting.Name] = setting
			}

			for _, name := range []string{"bot-name", "bot-email", "forge-ci-repository"} {
				if _, ok := settings[name]; !ok {
					t.Errorf("missing public setting %s", name)
				}
			}

			for _, name := range []string{"bot-name", "bot-email"} {
				if !strings.Contains(strings.Join(report.Unknowns, " "), "public input "+name+" is absent or unresolved") {
					t.Errorf("missing coverage warning for %s", name)
				}
			}

			if forge == "forgejo" && !strings.Contains(settings["forge-ci-repository"].Source, "vars.FORGE_CI_REPOSITORY") {
				t.Fatal("helper repository fallback not inventoried")
			}
		})
	}
}

func TestSetupForgejoHelperRevisionInvalidatesMatchedContract(t *testing.T) {
	t.Parallel()

	for _, workflow := range []string{"prepare-release.yml", "sign-and-publish-release.yml"} {
		for _, override := range []string{setupCommit, strings.Repeat("b", 40), "${{ vars.ENGINE }}", ""} {
			t.Run(workflow+"/"+override, func(t *testing.T) {
				t.Parallel()
				root := t.TempDir()
				setupFile(t, root, ".forgejo/workflows/release.yml", "jobs:\n  release:\n    uses: owner/engine/.forgejo/workflows/"+workflow+"@"+setupCommit+"\n    with:\n      forge-ci-sha: '"+override+"'\n")

				report := doctor.Requirements(root, "owner/engine", setupCommit)
				wantUnverified := override != setupCommit

				if len(report.Calls) != 1 || strings.Contains(report.Calls[0].Contract, "helper revision unverified") != wantUnverified {
					t.Fatalf("wrong helper confidence: %+v", report)
				}
			})
		}
	}
}
