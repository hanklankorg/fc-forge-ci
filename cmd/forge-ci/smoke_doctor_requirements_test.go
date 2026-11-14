//go:build smoke

// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package main_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

func TestBinary_Doctor_Requirements(t *testing.T) {
	_ = testenv.New(t)
	bin := buildBinary(t)

	const revision = "1234567890abcdef1234567890abcdef12345678"

	for _, forge := range []string{"github", "gitlab", "forgejo"} {
		t.Run(forge, func(t *testing.T) {
			fsys := testfs.NewReal(t)
			fsys.WriteFile(".forge-ci/artifacts.yml", []byte("artifacts:\n  - name: app\n    project-type: meta\n"))

			switch forge {
			case "gitlab":
				fsys.WriteFile(".gitlab-ci.yml", []byte("include:\n  - component: gitlab.example/owner/engine/release@"+revision+"\n    inputs:\n      sign-distro-packages: false\n"))
			case "github", "forgejo":
				workflow := "release-orchestrator.yml"
				if forge == "forgejo" {
					workflow = "prepare-release.yml"
				}

				fsys.WriteFile("."+forge+"/workflows/release.yml", []byte("jobs:\n  release:\n    uses: owner/engine/."+forge+"/workflows/"+workflow+"@"+revision+"\n    secrets:\n      RELEASE_TOKEN: ${{ secrets.MY_RELEASE_TOKEN }}\n      RELEASE_GPG_PRIVATE_KEY: inline-credential-must-not-appear\n"))
			}

			args := []string{"doctor", "--root", fsys.Root, "--forge-ci-repository", "owner/engine"}
			baseline, baselineErr, baselineCode := runBinary(t, bin, args...)
			if baselineCode != 0 || baselineErr != "" || strings.Contains(baseline, "Release setup requirements") {
				t.Fatalf("unexpected ordinary doctor report: exit=%d, stderr=%q\n%s", baselineCode, baselineErr, baseline)
			}

			stdout, stderr, code := runBinary(t, bin, append(args, "--requirements")...)
			if code != baselineCode || stderr != baselineErr || !strings.HasPrefix(stdout, baseline) {
				t.Fatalf("requirements changed normal checks: exit=%d, stderr=%q\n%s", code, stderr, stdout)
			}

			for _, want := range []string{"Release setup requirements (not remote readiness)", forge, "RELEASE_TOKEN", "unverified", "see forge-ci docs setup-requirements"} {
				if !strings.Contains(stdout, want) {
					t.Errorf("text report missing %q:\n%s", want, stdout)
				}
			}

			if strings.Contains(stdout, "inline-credential-must-not-appear") {
				t.Error("text report exposed an inline credential")
			}

			for _, format := range []string{"--json", "--format=json"} {
				t.Run(format, func(t *testing.T) {
					jsonArgs := append([]string{format}, args...)
					stdout, stderr, code := runBinary(t, bin, append(jsonArgs, "--requirements")...)
					if code != baselineCode || stderr != "" {
						t.Fatalf("JSON: exit=%d, stderr=%q\n%s", code, stderr, stdout)
					}

					var report struct {
						Failures     int `json:"failures"`
						Requirements struct {
							Complete bool `json:"complete"`
							Calls    []struct {
								Forge string `json:"forge"`
							} `json:"calls"`
							Unknowns []string `json:"unknowns"`
						} `json:"requirements"`
					}
					if err := json.Unmarshal([]byte(stdout), &report); err != nil {
						t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
					}

					if report.Failures != 0 || report.Requirements.Complete || len(report.Requirements.Unknowns) == 0 || len(report.Requirements.Calls) != 1 || report.Requirements.Calls[0].Forge != forge {
						t.Fatalf("wrong coverage or checks: %+v", report)
					}

					if forge != "gitlab" && !strings.Contains(stdout, "secrets.MY_RELEASE_TOKEN") {
						t.Error("renamed caller secret mapping missing")
					}

					if strings.Contains(stdout, "inline-credential-must-not-appear") {
						t.Error("JSON report exposed an inline credential")
					}
				})
			}
		})
	}
}

func TestBinary_Doctor_RequirementsPreservesFailure(t *testing.T) {
	_ = testenv.New(t)
	bin := buildBinary(t)
	fsys := testfs.NewReal(t)

	for _, requirements := range []bool{false, true} {
		args := []string{"--json", "doctor", "--root", fsys.Root}
		if requirements {
			args = append(args, "--requirements")
		}

		stdout, stderr, code := runBinary(t, bin, args...)
		if code != 1 || !strings.Contains(stderr, "failing check(s)") {
			t.Fatalf("requirements=%t: exit=%d, stderr=%q", requirements, code, stderr)
		}

		var report map[string]json.RawMessage
		if err := json.Unmarshal([]byte(stdout), &report); err != nil {
			t.Fatal(err)
		}

		if _, present := report["requirements"]; present != requirements {
			t.Errorf("requirements=%t: unexpected requirements field presence: %t", requirements, present)
		}
	}
}
