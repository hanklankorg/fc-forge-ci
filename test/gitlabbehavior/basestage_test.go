// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/gitlabrender"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

const (
	baseRepository = "registry.invalid/owner/publication/bases"
	baseProjection = `{"ref":"` + baseRepository + `@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","input_id":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}`
)

// A base stage's jobs, by where each runs and what it may hold. The jobs that
// run the project's planner and Containerfiles hold no release secret; the
// key is the signer job's alone, and the registry credential belongs to the
// jobs that run nothing of the project's.
func TestBaseStageJobsHoldOnlyTheSecretsOfTheirRole(t *testing.T) {
	t.Parallel()

	const (
		jobImage, builderImage, signerImage = "$[[ inputs.job-image ]]", "$[[ inputs.builder-job-image ]]", "$[[ inputs.signer-image ]]"
		ordinary, build, signer             = "$[[ inputs.runner-tags ]]", "$[[ inputs.build-runner-tags-linux-amd64 ]]", "$[[ inputs.signer-runner-tags ]]"
		registryToken                       = "RELEASE_REGISTRY_TOKEN"
	)

	script := filepath.Join(reporoot.Path(t), ".gitlab", "bin", "release.sh")
	body := releaseJobsBodyFor(t, "buildah")

	for _, tc := range []struct {
		job, image, tags, environment string
		secrets                       []string
	}{
		{"Base: plan tools+debug", jobImage, ordinary, "", nil},
		{"Base: parent tools+debug", signerImage, signer, "release/registry/promote", []string{registryToken}},
		{"Base: build tools+debug", builderImage, build, "", nil},
		{"Base: publish tools+debug", jobImage, signer, "release/registry/promote", []string{registryToken}},
		{"Base: sign and promote tools+debug", signerImage, signer, "release/registry/signing", []string{
			registryToken, "RELEASE_COSIGN_PRIVATE_KEY", "RELEASE_COSIGN_PASSPHRASE", "RELEASE_GPG_PRIVATE_KEY", "RELEASE_GPG_PASSPHRASE",
		}},
		{"Base: all promoted", jobImage, ordinary, "", nil},
	} {
		t.Run(tc.job, func(t *testing.T) {
			t.Parallel()

			fields := gitlabrender.ResolveExtends(t, body, body[tc.job].(map[string]any))

			image := fields["image"]
			if named, isMap := image.(map[string]any); isMap {
				image = named["name"]
			}

			environment, _ := fields["environment"].(map[string]any)
			if name, _ := environment["name"].(string); image != tc.image || fields["tags"] != tc.tags || name != tc.environment {
				t.Errorf("runs in %v on %v in environment %q, want %s on %s in %q", image, fields["tags"], name, tc.image, tc.tags, tc.environment)
			}

			for _, secret := range []string{
				"RELEASE_SSH_SIGNING_KEY", "RELEASE_COSIGN_PRIVATE_KEY", "RELEASE_COSIGN_PASSPHRASE", "RELEASE_GPG_PRIVATE_KEY",
				"RELEASE_GPG_PASSPHRASE", "RELEASE_TOKEN", registryToken, "RELEASE_PACKAGE_TOKEN", "COSIGN_KEY", "GITLAB_TOKEN",
			} {
				command := exec.CommandContext(commandContext(t), componentBash(t), "--noprofile", "--norc", "-c",
					`source "$1"; rc_engine() { :; }; rc_release_context`, "release-guard", script)
				command.Dir = t.TempDir()
				command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + command.Dir, "rc_job_role=" + releaseJobRoles[tc.job], secret + "=inert-canary-value",
					"CI_COMMIT_TAG=release-request/v1.2.3", "CI_COMMIT_SHA=" + strings.Repeat("a", 40), "CI_DEFAULT_BRANCH=main", "RC_RELEASE_POLICY=policy"}

				output, err := command.CombinedOutput()
				if refused := strings.Contains(string(output), secret+" is available to the wrong release job"); refused == slices.Contains(tc.secrets, secret) || refused == (err == nil) {
					t.Errorf("with %s: refused=%t, err=%v; the job holds %v alone", secret, refused, err, tc.secrets)
				}
			}
		})
	}
}

// baseJobStubs replace what a base job reaches outside itself: the checkout,
// the builder, the key and an engine that records each call, with a login's
// password, in ENGINE_LOG.
const baseJobStubs = `rc_release_checkout() {
  export RELEASE_SHA=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  mkdir -p "${RC_WORK}/consumer" && cd "${RC_WORK}/consumer"
  printf 'echo "planner: $* token=${CI_JOB_TOKEN-unset}" >>"${ENGINE_LOG}"; echo "{}"\n' >planner.sh
}
rc_public_key() { :; }; rc_image_builder() { :; }
syft() { :; }; install_trivy() { :; }; git() { echo 2026-01-01T00:00:00Z; }
rc_engine() {
  local call="$*" found=${BASE_FOUND-}
  if [ "$1 $2" = 'container login' ]; then call+=" stdin=$(cat)"; fi
  printf '%s\n' "${call}" >>"${ENGINE_LOG}"
  case "$*" in
  *'base-images promote '*) if [ -n "${BASE_PROMOTE_REFUSES-}" ]; then return 1; fi ;;
  *'base-images validate '*)
    if grep -q 'base-images promote ' "${ENGINE_LOG}"; then found=all; fi
    case "${found}" in
    all) printf 'ALL_FOUND=true\nPARENT_BASES_JSON={"tools":{},"debug":{}}\n' >>"${CI_OUTPUT}" ;;
    some) printf 'ALL_FOUND=false\nPARENT_BASES_JSON={"tools":{}}\n' >>"${CI_OUTPUT}" ;;
    none) printf 'ALL_FOUND=false\nPARENT_BASES_JSON={}\n' >>"${CI_OUTPUT}" ;;
    esac
    ;;
  *'base-images declared-inputs '*) echo '[]' ;;
  *'base-images authenticate-parent '*) cat -- "${CI_PROJECT_DIR}/_release-base-promote/base.json" ;;
  *'base-images select-parent '*) cat parent-bases.json ;;
  *'release validate-dist '*) mkdir -p verified-payload/dist && : >verified-payload/dist/base-images.json ;;
  *'child-base fetch '*) mkdir registry-inputs ;;
  *' publish '*) mkdir "${RC_WORK}/base-payload" ;;
  *' build '*) mkdir -p "${RC_WORK}/staged/tools-arch-amd64" ;;
  esac
}
`

// runBaseJob runs a base job's function with the stubs, in a policy of a
// root and two siblings on it, and returns its output, the engine's calls
// and the project directory. given are files of the hand-offs the job needs.
func runBaseJob(t *testing.T, call string, given map[string]string, env ...string) (string, string, string, error) {
	t.Helper()

	dir := t.TempDir()
	project, helpers := filepath.Join(dir, "project"), filepath.Join(dir, "helpers", "scripts", "bootstrap")

	given[filepath.Join(helpers, "install-trivy.sh")] = ""
	// The signing preflight is the script's own; only the toolchain check it
	// starts is empty here.
	given[filepath.Join(dir, "helpers", ".forgejo", "scripts", "verify-signer-toolchain.sh")] = ""

	for name, content := range given {
		if !filepath.IsAbs(name) {
			name = filepath.Join(project, name)
		}

		if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for _, path := range []string{project, filepath.Join(dir, "work")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	log := filepath.Join(dir, "engine.log")

	output, err := runReleaseFunction(t, dir, baseJobStubs+call, append([]string{
		"RC_WORK=" + filepath.Join(dir, "work"), "CI_PROJECT_DIR=" + project, "ENGINE_LOG=" + log, "CI_OUTPUT=" + filepath.Join(dir, "outputs.env"),
		"RC_HELPERS_DIR=" + filepath.Join(dir, "helpers"), "RC_BASE_STAGES=base tools+debug:base", "RC_BASE_PLAN=bash planner.sh",
		`RC_BUILD_RUNNER_TAGS_LINUX_AMD64=["build-amd64"]`, `RC_BUILD_RUNNER_TAGS_LINUX_ARM64=["build-arm64"]`,
		"RC_IMAGE_BUILDER=buildah", "RC_BUILDER_JOB_IMAGE=registry.invalid/team/builder@sha256:" + strings.Repeat("1", 64),
		"RC_PUBLICATION_PROJECT=owner/publication", "RC_PUBLICATION_IMAGE_REPOSITORY=registry.invalid/owner/publication",
		"RC_REGISTRY=registry.invalid", "RC_SERVER_URL=https://gitlab.invalid", "CI_PROJECT_PATH=owner/app",
		"RC_PUBLIC_KEY_SHA256=" + strings.Repeat("2", 64), "CI_JOB_TOKEN=job-token-canary", "RELEASE_REGISTRY_USER=release-bot",
		"RC_ADMIN_VULNERABILITIES=true",
	}, env...)...)

	calls, _ := os.ReadFile(log)

	return output, string(calls), project, err
}

// requireExpectedSource holds every verification among calls to the source
// the policy derives, as a flag: the engine would otherwise take the source
// a signed lineage must name from a variable a project can set.
func requireExpectedSource(t *testing.T, calls string) {
	t.Helper()

	verified := 0

	for call := range strings.SplitSeq(calls, "\n") {
		for _, verb := range []string{"authenticate-parent", "validate", "promote"} {
			if !strings.HasPrefix(call, "container base-images "+verb+" ") {
				continue
			}

			verified++

			if !strings.Contains(call, " --expected-source https://gitlab.invalid/owner/app ") {
				t.Errorf("a verification without the policy's expected source: %s", call)
			}
		}
	}

	if verified == 0 {
		t.Errorf("no verification among\n%s", calls)
	}
}

// A child is planned and built only on the parent a signer job verified: its
// plan reads the projection its parent's promotion handed on, and its build
// the parent job's copy. Without them, or with a stage the policy does not
// name, the job stops before the planner or the builder runs.
func TestBaseChildIsPlannedAndBuiltOnlyOnItsVerifiedParent(t *testing.T) {
	t.Parallel()

	const amd64BaseRunner = ` --runner-input build-runner-tags-linux-amd64=["build-amd64"]`

	for name, tc := range map[string]struct {
		call    string
		given   map[string]string
		refused string
		calls   []string
	}{
		"plan before the parent is promoted": {call: "rc_base_plan 'tools+debug' 'base'", refused: "must be a regular file"},
		"plan on a parent of another repository": {
			call:    "rc_base_plan 'tools+debug' 'base'",
			given:   map[string]string{"_release-base-promote/base.json": strings.Replace(baseProjection, "/bases@", "/other@", 1)},
			refused: "no verified parent projection",
		},
		"plan of a stage the policy does not name": {
			call: "rc_base_plan 'tools' 'base'", given: map[string]string{"_release-base-promote/base.json": baseProjection},
			refused: "base-stages names no stage 'tools:base'",
		},
		"plan of a child as a root": {call: "rc_base_plan 'tools+debug' ''", refused: "base-stages names no stage 'tools+debug'"},
		"plan on the promoted parent": {
			call: "rc_base_plan 'tools+debug' 'base'", given: map[string]string{"_release-base-promote/base.json": baseProjection},
			calls: []string{
				"planner: tools,debug " + baseRepository + " " + baseRepository + "@sha256:" + strings.Repeat("c", 64) + " " + strings.Repeat("d", 64) + " token=unset",
				"container base-images child-base plan --parent-base-json " + baseProjection + " --image-builder buildah --plan ",
				" --stage-flavors tools,debug ", amd64BaseRunner,
			},
		},
		"plan of the root":                   {call: "rc_base_plan 'base' ''", calls: []string{"planner: base " + baseRepository + " token=unset", "container base-images root-base plan --plan ", " --stage-flavors base ", amd64BaseRunner}},
		"build before the parent is fetched": {call: "rc_base_build 'tools+debug' 'base'", refused: "must be a regular file"},
		"build on the fetched parent": {
			call:  "rc_base_build 'tools+debug' 'base'",
			given: map[string]string{"_release-base-parent/parent-base.json": baseProjection, "_release-base-parent/registry-inputs/tools-arch-amd64/x": ""},
			calls: []string{"container base-images child-base build --parent-base-json " + baseProjection + " --inputs-dir registry-inputs --plan ", " --stage-flavors tools,debug "},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if tc.given == nil {
				tc.given = map[string]string{}
			}

			output, calls, _, err := runBaseJob(t, tc.call, tc.given)
			if tc.refused != "" {
				if err == nil || !strings.Contains(output, tc.refused) || strings.Contains(calls, "planner:") || strings.Contains(calls, "base-images") {
					t.Fatalf("want a refusal with %q before any planner or engine call: %v\n%s\n%s", tc.refused, err, output, calls)
				}

				return
			}

			if err != nil {
				t.Fatalf("%v\n%s", err, output)
			}

			for _, want := range tc.calls {
				if !strings.Contains(calls, want) {
					t.Errorf("no call %q in\n%s", want, calls)
				}
			}

			// The one base build job is amd64's: no other architecture's
			// runner input reaches admission, so its base is refused.
			if strings.Contains(calls, "linux-arm64") || strings.Contains(calls, "--runner-archs") {
				t.Errorf("a base plan names another build runner than amd64's:\n%s", calls)
			}
		})
	}
}

// The jobs that reach the registry log in with the release's registry
// credential, to the publication project's bases repository: the job token,
// which every job that runs the project's code holds, is never sent, and a
// job without the credential stops instead of falling back to it.
func TestBaseStageReachesTheRegistryWithTheReleaseCredentialAlone(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		call  string
		given map[string]string
	}{
		"publish a root":  {"rc_base_publish 'base' ''", map[string]string{}},
		"publish a child": {"rc_base_publish 'tools+debug' 'base'", map[string]string{"_release-base-parent/parent-base.json": baseProjection}},
		"fetch a parent":  {"rc_base_parent 'tools+debug' 'base'", map[string]string{"_release-base-promote/base.json": baseProjection}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			output, calls, _, err := runBaseJob(t, tc.call, tc.given, "RELEASE_REGISTRY_TOKEN=release-credential-canary")
			if err != nil {
				t.Fatalf("%v\n%s", err, output)
			}

			login := "container login --registry " + baseRepository + " --server-url https://gitlab.invalid --repository owner/app" +
				" --registry-username release-bot --registry-password-file - --auth-file "
			if !strings.Contains(calls, login) || !strings.Contains(calls, " stdin=release-credential-canary\n") ||
				strings.Contains(calls, "job-token-canary") || strings.Contains(calls, "gitlab-ci-token") {
				t.Errorf("the job did not log in with the release credential alone:\n%s", calls)
			}

			if name == "fetch a parent" {
				requireExpectedSource(t, calls)
			}

			// A publication applies the administrator's vulnerability setting,
			// as the release images' does.
			if strings.HasPrefix(name, "publish") && !strings.Contains(calls, "-base publish --enforce-vulnerabilities=true ") {
				t.Errorf("the base publication does not pass the vulnerability setting:\n%s", calls)
			}

			_, calls, _, err = runBaseJob(t, tc.call, tc.given)
			if err == nil || strings.Contains(calls, "container login") {
				t.Errorf("without the release credential the job went on: %v\n%s", err, calls)
			}
		})
	}
}

// The promotion signs and promotes a stage none of whose final tags exist,
// and reuses one whose tags all verify, signing nothing. A stage whose
// promotion stopped between two tags is completed without signing again,
// by the promotion that refuses a tag of another digest; when that refuses,
// nothing is handed on. What the job hands on is verified under the final
// tags every time.
func TestBasePromotionSignsOnlyAStageNotYetPromoted(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		found           string
		refuses         bool
		signs, promotes bool
		validated       int
	}{
		"not promoted":                {found: "none", signs: true, promotes: true, validated: 2},
		"promoted":                    {found: "all", validated: 1},
		"half promoted by this stage": {found: "some", promotes: true, validated: 2},
		"half promoted by another":    {found: "some", refuses: true, promotes: true, validated: 1},
		"not promoted, and refused":   {found: "none", refuses: true, signs: true, promotes: true, validated: 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env := []string{"BASE_FOUND=" + tc.found, "RELEASE_REGISTRY_TOKEN=release-credential-canary",
				"RELEASE_COSIGN_PRIVATE_KEY=inert-key", "RELEASE_COSIGN_PASSPHRASE=inert-passphrase",
			}
			if tc.refuses {
				env = append(env, "BASE_PROMOTE_REFUSES=1")
			}

			// The component's default, read-only as the job's start makes it:
			// a release signs its distro packages with GPG, and a base
			// stage, which has none, needs no GPG key.
			output, calls, project, err := runBaseJob(t, "readonly RC_SIGN_DISTRO_PACKAGES=true; rc_base_promote 'tools+debug' 'base'", map[string]string{"_release-base-publish/digest": ""}, env...)

			if signed, promoted := strings.Contains(calls, "base-images sign "), strings.Contains(calls, "base-images promote "); signed != tc.signs || promoted != tc.promotes {
				t.Errorf("signed %t and promoted %t, want %t and %t\n%s", signed, promoted, tc.signs, tc.promotes, calls)
			}

			_, statErr := os.Stat(filepath.Join(project, "_release-base-promote", "debug.json"))
			if handed := statErr == nil; handed == tc.refuses || handed != (err == nil) {
				t.Errorf("handed on: %t, err=%v\n%s", handed, err, output)
			}

			if named := strings.Contains(output, "promoted or reused whole"); named != (tc.refuses && tc.found == "some") {
				t.Errorf("a half-promoted stage of another digest is refused by name, and nothing else: %t\n%s", named, output)
			}

			requireExpectedSource(t, calls)

			// What a child's plan reads is what the last verification wrote,
			// selected by flavor, and nothing a build job handed over.
			if handed, readErr := os.ReadFile(filepath.Join(project, "_release-base-promote", "debug.json")); readErr == nil &&
				(strings.TrimSpace(string(handed)) != `{"tools":{},"debug":{}}` ||
					!strings.Contains(calls, "base-images select-parent --parent-bases-file parent-bases.json --flavor debug\n")) {
				t.Errorf("the hand-off %q is not selected from the verified parents\n%s", handed, calls)
			}

			if verified := strings.Count(calls, "base-images validate "); verified != tc.validated {
				t.Errorf("verified under the final tags %d times, want %d\n%s", verified, tc.validated, calls)
			}
		})
	}
}
