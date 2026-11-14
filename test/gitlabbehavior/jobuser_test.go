// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/gitlabrender"
)

type jobUserCase struct {
	// job is the release job whose start runs; spoof the name a project
	// variable gives it.
	job, spoof, input, variable, uid, gid string
	// wantError is the refusal; without one the job prints its receipt.
	wantError string
}

// A project variable outranks the job's RC_JOB_USER, so each job holds the
// variable to the literal job-user input, and its own user to that, and
// says which user it runs as.
func TestCheckJobHoldsItsUserToJobUser(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]jobUserCase{
		"the image's user":           {uid: "0", gid: "0"},
		"job-user":                   {input: "65532:65532", variable: "65532:65532", uid: "65532", gid: "65532"},
		"job-user UID alone":         {input: "65532", variable: "65532", uid: "65532", gid: "7"},
		"a variable instead of none": {variable: "0", uid: "0", gid: "0", wantError: "RC_JOB_USER is '0', not job-user ''"},
		"a variable over job-user":   {input: "65532:65532", variable: "0", uid: "0", gid: "0", wantError: "RC_JOB_USER is '0'"},
		"another UID":                {input: "65532", variable: "65532", uid: "0", gid: "0", wantError: "runs as uid=0 gid=0, not job-user 65532"},
		"another GID":                {input: "65532:65532", variable: "65532:65532", uid: "65532", gid: "0", wantError: "not job-user 65532:65532"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runJobUserCheck(t, checkBootstrapScript(t), tc,
				"$[[ component.sha ]]", strings.Repeat("a", 40), "$[[ component.reference ]]", "main")
		})
	}
}

func TestReleaseJobsHoldTheirUserToTheirImage(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]jobUserCase{
		"a job-image job as job-user":     {job: "Check: release request", spoof: "Build: release files", input: "65532:65532", variable: "65532:65532", uid: "65532", gid: "65532"},
		"a job-image job as root":         {job: "Prepare: changelog", input: "65532:65532", variable: "65532:65532", uid: "0", gid: "0", wantError: "not job-user 65532:65532"},
		"a variable over job-user":        {job: "Images: plan", input: "65532:65532", variable: "0", uid: "0", gid: "0", wantError: "RC_JOB_USER is '0'"},
		"a job-image job named a signer":  {job: "Check: consumer checks", spoof: "Sign: files and images", input: "65532:65532", variable: "0", uid: "0", gid: "0", wantError: "RC_JOB_USER is '0'"},
		"a job-image job named the build": {job: "Images: plan", spoof: "Build: release files", input: "65532:65532", uid: "0", gid: "0", wantError: "RC_JOB_USER is '', not '65532:65532'"},
		"the build as its image's":        {job: "Build: java", spoof: "Check: release request", input: "65532:65532", uid: "0", gid: "0"},
		"a variable on the build":         {job: "Build: java", input: "65532:65532", variable: "65532:65532", uid: "65532", gid: "65532", wantError: "RC_JOB_USER is '65532:65532', not ''"},
		"a variable on an image build":    {job: "Images: build arm64", variable: "1000", uid: "1000", gid: "1000", wantError: "RC_JOB_USER is '1000', not ''"},
		"a signer job as its image's":     {job: "Sign: files and images", input: "65532:65532", uid: "1000", gid: "1000"},
		"a variable on a signer job":      {job: "Publish: release", spoof: "Prepare: changelog", input: "65532:65532", variable: "0", uid: "0", gid: "0", wantError: "RC_JOB_USER is '0', not ''"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runJobUserCheck(t, releaseJobStart(t, tc.job, "buildah"), tc,
				"$[[ inputs.engine-sha ]]", strings.Repeat("a", 40), "$[[ inputs.engine-reference ]]", "main")
		})
	}
}

// Every release job's start holds RC_JOB_USER to the value its own YAML gives
// it, job-user for the job-image jobs and empty for the others, and refuses
// any other.
func TestEveryReleaseJobHoldsItsUserToItsOwnVariable(t *testing.T) {
	t.Parallel()

	for _, builder := range []string{"buildah", "kaniko"} {
		body := releaseJobsBodyFor(t, builder)

		for job := range releaseJobRoles {
			fields := gitlabrender.ResolveExtends(t, body, body[job].(map[string]any))
			variable := strings.ReplaceAll(fields["variables"].(map[string]any)["RC_JOB_USER"].(string), "$[[ inputs.job-user ]]", "65532:65532")
			start := joinedScript(fields["before_script"])

			for name, tc := range map[string]jobUserCase{
				"its own":   {input: "65532:65532", variable: variable, uid: "65532", gid: "65532"},
				"another's": {input: "65532:65532", variable: "1:1", uid: "65532", gid: "65532", wantError: "RC_JOB_USER is '1:1'"},
			} {
				t.Run(builder+"/"+job+"/"+name, func(t *testing.T) {
					t.Parallel()

					tc.job, tc.spoof = job, "Sign: files and images"
					runJobUserCheck(t, start, tc,
						"$[[ inputs.engine-sha ]]", strings.Repeat("a", 40), "$[[ inputs.engine-reference ]]", "main")
				})
			}
		}
	}
}

// releaseJobStart is a release job's before_script as GitLab runs it, its
// lines joined, with the builder chosen and release images on.
func releaseJobStart(t *testing.T, job, builder string) string {
	t.Helper()

	body := releaseJobsBodyFor(t, builder)

	return joinedScript(gitlabrender.ResolveExtends(t, body, body[job].(map[string]any))["before_script"])
}

// fakeID is id as a shell function, answering FAKE_UID and FAKE_GID. A
// function, not a file: a parallel test's fork can hold a file just written
// open, and executing it then fails.
const fakeID = `id() { case $1 in -u) echo "$FAKE_UID" ;; -g) echo "$FAKE_GID" ;; *) return 2 ;; esac; }` + "\n"

// runJobUserCheck runs a component's job start as user uid:gid, with the
// pin's old, new pairs, which stop it right after the user check.
func runJobUserCheck(t *testing.T, script string, tc jobUserCase, pin ...string) {
	t.Helper()

	dir := t.TempDir()

	replacements := append([]string{"$[[ inputs.job-user ]]", tc.input}, pin...)

	ctx := commandContext(t)

	command := exec.CommandContext(ctx, componentBash(t), "--noprofile", "--norc", "-c", fakeID+strings.NewReplacer(replacements...).Replace(script))
	command.Dir = dir
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "FAKE_UID=" + tc.uid, "FAKE_GID=" + tc.gid,
		"CI_JOB_NAME=" + tc.spoof, "CI_PROJECT_DIR=" + dir, "RC_JOB_USER=" + tc.variable}

	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("the pin did not stop the job:\n%s", output)
	}

	receipt := "job user: uid=" + tc.uid + " gid=" + tc.gid + "\n"
	if tc.wantError != "" {
		if !strings.Contains(string(output), "ERROR: ") || !strings.Contains(string(output), tc.wantError) || strings.Contains(string(output), receipt) {
			t.Fatalf("want the refusal %q and no receipt:\n%s", tc.wantError, output)
		}

		return
	}

	if !strings.Contains(string(output), receipt) || strings.Contains(string(output), "ERROR: RC_JOB_USER") || strings.Contains(string(output), "not job-user") {
		t.Fatalf("want the receipt %q:\n%s", receipt, output)
	}
}
