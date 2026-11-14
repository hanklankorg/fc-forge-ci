// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/gitlabrender"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// releaseJobRoles is each release job's role, the literal its start opens
// with. A project variable can set CI_JOB_NAME, so the role is what the job
// may do.
var releaseJobRoles = map[string]string{
	"Check: signing access":              "signing-preflight",
	"Check: publishing access":           "publish-preflight",
	"Check: registry access":             "registry-preflight",
	"Check: release request":             "request",
	"Check: consumer checks":             "checks",
	"Prepare: changelog":                 "changelog",
	"Prepare: version java":              "version",
	"Prepare: version node":              "version",
	"Prepare: version rust":              "version",
	"Prepare: release commit and tag":    "prepare",
	"Build: java":                        "build",
	"Build: node":                        "build",
	"Build: go":                          "build",
	"Build: rust":                        "build",
	"Build: android":                     "build",
	"Build: release files":               "release-files",
	"Build: export amd64":                "export",
	"Build: export arm64":                "export",
	"Images: plan":                       "images-plan",
	"Images: build amd64":                "image-build",
	"Images: build arm64":                "image-build",
	"Images: publish":                    "images-publish",
	"Sign: files and images":             "sign",
	"Publish: images":                    "promote",
	"Publish: release":                   "publish",
	"Publish: tidy up":                   "cleanup",
	"Publish: npm packages":              "packages",
	"Summary: release":                   "summary",
	"Base: plan base":                    "base-plan",
	"Base: build base":                   "base-build",
	"Base: publish base":                 "base-publish",
	"Base: sign and promote base":        "base-promote",
	"Base: plan tools+debug":             "base-plan",
	"Base: parent tools+debug":           "base-parent",
	"Base: build tools+debug":            "base-build",
	"Base: publish tools+debug":          "base-publish",
	"Base: sign and promote tools+debug": "base-promote",
	"Base: all promoted":                 "base-gate",
}

// Each release job's start is its role, then the shared start, and each
// producer's after_script its hand-off directory, the one it uploads, then
// the shared hand-off. A job that hands nothing off runs no after_script.
func TestReleaseJobsOpenWithTheirLiteralRole(t *testing.T) {
	t.Parallel()

	body := releaseJobsBodyFor(t, "buildah")
	start := body[".forge-ci-release-start"].([]any)[0]
	handoff := body[".forge-ci-release-handoff"].([]any)[0]

	var jobs []string

	for name, raw := range body {
		job, isJob := raw.(map[string]any)
		if !isJob || strings.HasPrefix(name, ".") {
			continue
		}

		jobs = append(jobs, name)
		fields := gitlabrender.ResolveExtends(t, body, job)

		if before, _ := fields["before_script"].([]any); !slices.Equal(before, []any{"readonly rc_job_role=" + releaseJobRoles[name], start}) {
			t.Errorf("%s opens with %.60q, want its role %q and the shared start", name, before, releaseJobRoles[name])
		}

		var want []any
		if artifacts, ok := fields["artifacts"].(map[string]any); ok {
			want = []any{"rc_handoff=" + strings.TrimSuffix(yamlStrings(artifacts["paths"])[0], "/"), handoff}
		}

		if after, _ := fields["after_script"].([]any); !slices.Equal(after, want) {
			t.Errorf("%s hands off with %.60q, want %.60q", name, after, want)
		}
	}

	if len(jobs) != len(releaseJobRoles) {
		t.Errorf("the release jobs are %v; give each a role here", jobs)
	}
}

// What a job may do, its user, its work directory and its home, follows from
// its literal role, whatever a project variable names the job, or its role.
func TestReleaseJobStartFollowsItsRoleNotItsName(t *testing.T) {
	t.Parallel()

	ownHome := []string{"request", "checks", "changelog", "release-files", "version", "build", "export", "base-plan"}

	for _, builder := range []string{"buildah", "kaniko"} {
		body := releaseJobsBodyFor(t, builder)

		for job, role := range releaseJobRoles {
			fields := gitlabrender.ResolveExtends(t, body, body[job].(map[string]any))
			user := strings.ReplaceAll(fields["variables"].(map[string]any)["RC_JOB_USER"].(string), "$[[ inputs.job-user ]]", "65532:65532")
			start := joinedScript(fields["before_script"])

			for _, spoof := range []string{"Build: release files", "Images: build amd64", "Sign: files and images"} {
				t.Run(builder+"/"+job+"/as "+spoof, func(t *testing.T) {
					t.Parallel()

					home, work := runReleaseJobStart(t, start, user, spoof)
					if want := map[bool]string{true: "image home", false: "the work's home"}[slices.Contains(ownHome, role)]; home != want {
						t.Errorf("home in %s, want %s", home, want)
					}

					inProject := (role == "image-build" || role == "export" || role == "base-build") && builder == "kaniko"
					if want := map[bool]string{true: "project", false: "/tmp"}[inProject]; work != want {
						t.Errorf("work in %s, want %s", work, want)
					}
				})
			}
		}
	}
}

// runReleaseJobStart runs a release job's start, job-user 65532:65532, as
// 65532:65532 with RC_JOB_USER user, named spoof and given a role by a
// variable, up to the engine fetch. It returns where the job's home and work
// directory are: the image's home or the work's, the project or /tmp.
func runReleaseJobStart(t *testing.T, start, user, spoof string) (string, string) {
	t.Helper()

	dir := t.TempDir()
	imageHome, project := filepath.Join(dir, "home"), filepath.Join(dir, "project")

	for _, path := range []string{imageHome, project} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	const probe = `printf 'home=%s\nhelpers=%s\n' "${HOME}" "${RC_HELPERS_DIR}"; exit 3`

	pin := strings.Repeat("a", 40)
	script := strings.Replace(gitlabrender.Script(t, "templates/release-jobs.yml", start,
		map[string]any{"job-user": "65532:65532", "engine-sha": pin, "engine-reference": pin}), "object_format=sha1\n", probe+"\n", 1)

	ctx := commandContext(t)

	command := exec.CommandContext(ctx, componentBash(t), "--noprofile", "--norc", "-c", fakeID+script)
	command.Dir = dir
	command.Env = []string{
		"PATH=/usr/bin:/bin", "HOME=" + imageHome, "CI_PROJECT_DIR=" + project, "FAKE_UID=65532", "FAKE_GID=65532",
		"CI_JOB_NAME=" + spoof, "rc_job_role=sign", "RC_JOB_USER=" + user,
		"GIT_STRATEGY=empty", "GIT_SUBMODULE_STRATEGY=none",
		"CI_PROJECT_PATH=team/app", "CI_COMMIT_REF_PROTECTED=true",
		"CI_PIPELINE_SOURCE=parent_pipeline", "CI_COMMIT_TAG=release-request/v1.0.0",
		"CI_SERVER_URL=https://gitlab.invalid", "CI_REGISTRY=registry.gitlab.invalid",
	}

	output, err := command.CombinedOutput()

	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 || !strings.Contains(string(output), "job user: uid=65532 gid=65532\n") {
		t.Fatalf("the start did not reach the engine fetch: %v\n%s", err, output)
	}

	values := map[string]string{}

	for line := range strings.SplitSeq(string(output), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			values[key] = value
		}
	}

	work := filepath.Dir(values["helpers"])

	home := values["home"]
	switch home {
	case imageHome:
		home = "image home"
	case filepath.Join(work, "home"):
		home = "the work's home"
	}

	switch filepath.Dir(work) {
	case project:
		work = "project"
	case "/tmp":
		work = "/tmp"
	}

	return home, work
}

func joinedScript(lines any) string {
	return strings.Join(yamlStrings(lines), "\n")
}

// No trust decision of the GitLab templates or their scripts reads a job's
// name, stage or image: a project variable outranks the predefined ones.
func TestGitLabTrustDecisionsReadNoJobIdentityVariable(t *testing.T) {
	t.Parallel()

	identity := regexp.MustCompile(`CI_JOB_(NAME|NAME_SLUG|STAGE|IMAGE|GROUP_NAME)\b`)

	for _, dir := range []string{"templates", ".gitlab/bin"} {
		entries, err := os.ReadDir(filepath.Join(reporoot.Path(t), dir))
		if err != nil {
			t.Fatal(err)
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}

			path := dir + "/" + entry.Name()
			if match := identity.Find(reporoot.ReadFile(t, path)); match != nil {
				t.Errorf("%s reads %s; decide from a literal of the template", path, match)
			}
		}
	}
}
