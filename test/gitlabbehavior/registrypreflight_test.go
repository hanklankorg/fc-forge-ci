// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/gitlabrender"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestRegistryPreflightUsesOnlyProtectedPublicationInputs(t *testing.T) {
	t.Parallel()
	_, jobs := releaseRouting(t, "templates/release-jobs.yml")
	rules := jobs["Check: registry access"].Rules
	if !reflect.DeepEqual(rules, []any{
		map[string]any{"if": `"$[[ inputs.release-images ]]" == "true"`},
		map[string]any{"if": `"$[[ inputs.base-images ]]" == "true"`},
	}) {
		t.Fatalf("registry preflight must depend only on protected publication inputs: %#v", rules)
	}

	for _, path := range []string{"templates/release.yml", "templates/release-jobs.yml"} {
		inputs := gitlabrender.InputSpec(t, path)
		for _, name := range []string{"release-images", "base-images", "export-files"} {
			if inputs[name]["type"] != "boolean" {
				t.Fatalf("%s: %s must remain a typed boolean", path, name)
			}
			if path == "templates/release.yml" && !strings.Contains(string(reporoot.ReadFile(t, path)), name+": $[[ inputs."+name+" ]]") {
				t.Fatalf("root policy does not forward %s literally", name)
			}
		}
	}

	for _, images := range []bool{false, true} {
		for _, bases := range []bool{false, true} {
			for _, exports := range []bool{false, true} {
				t.Run(fmt.Sprintf("images=%t/bases=%t/exports=%t", images, bases, exports), func(t *testing.T) {
					t.Parallel()
					inputs := map[string]any{"release-images": images, "base-images": bases, "export-files": exports}
					if images || exports {
						// Both paths require an explicitly configured builder job image.
						inputs["builder-job-image"], _ = gitlabrender.FixtureValue("builder-job-image")
					}
					var rendered []any
					for _, raw := range rules {
						expression := raw.(map[string]any)["if"].(string)
						rendered = append(rendered, map[string]any{"if": gitlabrender.Script(t, "templates/release-jobs.yml", expression, inputs)})
					}
					needed := images || bases
					if got := gitlabrender.RulesAdmit(rendered); got != needed {
						t.Fatalf("registry preflight scheduled=%t, want %t", got, needed)
					}
					if problems := gitlabrender.Problems(t, "templates/release.yml", inputs); len(problems) != 0 {
						t.Fatalf("optional preflight leaves an invalid pipeline: %v", problems)
					}

					for _, token := range []bool{false, true} {
						// Run only the job admitted by the actual YAML rules. No dummy
						// token is supplied for archive-only releases. The engine is inert.
						// Match the real job start: a missing token on the left of
						// the validation pipe must fail even with an inert engine.
						// Consume stdin before recording validation, so the stub
						// never reports empty token input as validated.
						script := `set -euo pipefail; source "$1"; rc_engine() {
if [ "$1 $2 $3" = 'validate auth token' ]; then
  local token; token=$(cat); [ -n "$token" ] || return 1
fi
printf 'engine %s\n' "$*"
}; `
						if gitlabrender.RulesAdmit(rendered) {
							script += "readonly rc_job_role=registry-preflight; rc_release_context\n" + strings.Join(jobs["Check: registry access"].Script, "\n") + "\n"
						} else {
							script += "readonly rc_job_role=request; rc_release_context\n"
						}
						command := exec.CommandContext(t.Context(), componentBash(t), "--noprofile", "--norc", "-eu", "-c", script, "registry-preflight", filepath.Join(reporoot.Path(t), ".gitlab/bin/release.sh"))
						command.Env = []string{"PATH=" + componentTools(t), "CI_COMMIT_TAG=release-request/v1.2.3", "CI_COMMIT_SHA=" + strings.Repeat("a", 40), "CI_DEFAULT_BRANCH=main", "RC_RELEASE_POLICY=stable", "RC_PUBLICATION_PROJECT=owner/publication",
							// Deliberately opposite consumer variables cannot alter scheduling.
							"RC_RELEASE_IMAGES=" + strconv.FormatBool(!images), "RC_BASE_IMAGES=" + strconv.FormatBool(!bases), "RC_EXPORT_FILES=" + strconv.FormatBool(!exports)}
						if token && needed {
							command.Env = append(command.Env, "RELEASE_REGISTRY_TOKEN=inert-canary-value")
						}
						out, err := command.CombinedOutput()
						if (err == nil) != (!needed || token) || strings.Contains(string(out), "validate auth token") != (needed && token) {
							t.Fatalf("token=%t: unexpected preflight result: %v\n%s", token, err, out)
						}
						if strings.Contains(string(out), "inert-canary-value") {
							t.Fatal("registry preflight exposed its token")
						}
						if needed && !token && !strings.Contains(string(out), "RELEASE_REGISTRY_TOKEN: parameter null or not set") {
							t.Fatalf("missing registry token was not explicitly refused: %v\n%s", err, out)
						}
					}

					command := exec.CommandContext(t.Context(), componentBash(t), "--noprofile", "--norc", "-eu", "-c",
						`source "$1"; rc_engine() { printf '%s\n' "$@"; }; rc_release_summary "$2" '[]'`, "summary", filepath.Join(reporoot.Path(t), ".gitlab/bin/release.sh"), strconv.FormatBool(images))
					command.Env = []string{"PATH=/usr/bin:/bin", "RC_BASE_IMAGES=" + strconv.FormatBool(bases), "RELEASE_TAG=v1.2.3", "RC_RELEASE_BRANCH=main"}
					out, err := command.CombinedOutput()
					if err != nil || strings.Contains(string(out), "Check: registry access=success") != needed ||
						!strings.Contains(string(out), "Check: signing access=success") || !strings.Contains(string(out), "Check: publishing access=success") {
						t.Fatalf("summary misrepresents scheduled access checks: %v\n%s", err, out)
					}
				})
			}
		}
	}
}
