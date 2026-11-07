// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package releasesetup

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	appdoctor "github.com/diggsweden/forge-ci/internal/app/doctor"
	appenforcement "github.com/diggsweden/forge-ci/internal/app/enforcement"
	"github.com/diggsweden/forge-ci/internal/docslink"
)

// StepState is how far one setup step is.
type StepState string

// Step states. Manual steps are ones the binary cannot see from the
// repository (a secret on the forge, a merged pull request).
const (
	StepDone   StepState = "done"
	StepTodo   StepState = "todo"
	StepManual StepState = "manual"
)

// Step is one entry of the path from no Forge CI files to a first release.
type Step struct {
	Title   string    `json:"title"`
	State   StepState `json:"state"`
	Detail  string    `json:"detail"`
	Command string    `json:"command,omitempty"`
}

// Checklist reports the setup steps for the repository at root, in order,
// with the state the repository shows for each. It reads the same checks as
// forge-ci doctor, so the two never disagree about what is done.
func Checklist(ctx context.Context, root, repo, commit string) ([]Step, error) {
	checks, err := appdoctor.Run(appdoctor.Input{Root: root, RepoSlug: repo})
	if err != nil {
		return nil, err
	}

	ok := func(name string) (bool, string) {
		c, found := appdoctor.Find(checks, name)

		// Done is OK only: a warning (release values left) is still to do.
		return found && c.Severity == appdoctor.SeverityOK, c.Message
	}

	steps := make([]Step, 0, 8)

	filesDone, filesDetail := ok(appdoctor.CheckCIWorkflows)
	steps = append(steps, step("Write the Forge CI files", filesDone, filesDetail,
		"forge-ci init "+docslink.Repository+" --dry-run   (or your organization's mirror URL)"))

	placeholdersDone, placeholdersDetail := ok(appdoctor.CheckPlaceholders)
	pathsDone, pathsDetail := ok(appdoctor.CheckArtifactPaths)

	if !pathsDone && placeholdersDone {
		placeholdersDetail = pathsDetail
	}

	steps = append(steps, step("Fill in the example values", placeholdersDone && pathsDone, placeholdersDetail,
		"forge-ci doctor   (lists each value with file:line and how to find it)"))

	steps = append(steps, signerStep(root, checks))

	secrets := Secrets(root, repo, commit)
	names := make([]string, 0, len(secrets.Secrets))

	for _, s := range secrets.Secrets {
		names = append(names, s.Name)
	}

	secretsDetail := "the forge's secrets are not visible from here; create them, then check off this step yourself"
	if len(names) > 0 {
		secretsDetail = "create " + strings.Join(names, ", ") + " on the forge (not visible from here); " + keyTools
	}

	steps = append(steps,
		Step{Title: "Create the release secrets", State: StepManual, Detail: secretsDetail, Command: "forge-ci setup secrets"},
		step("Pass forge-ci doctor", appdoctor.CountFailures(checks) == 0,
			fmt.Sprintf("%d failing check(s)", appdoctor.CountFailures(checks)), "forge-ci doctor"),
		enforcementStep(ctx),
		Step{
			Title: "Commit, open a pull request and merge", State: StepManual,
			Detail: "the pull-request workflow runs on it; the release runs from the merged default branch",
		},
		Step{
			Title: "Request the release", State: StepManual,
			Detail:  "push a signed release-request/vX.Y.Z tag; the release workflow builds, signs and creates vX.Y.Z",
			Command: "forge-ci setup release-request v0.1.0   (checks your signing setup, prints the git commands)",
		},
	)

	return steps, nil
}

// enforcementStep states what the effective policy blocks, which the person
// decides with their administrator. A policy the gates would refuse is the
// failing doctor check, not a reason to stop the checklist.
func enforcementStep(ctx context.Context) Step {
	s := Step{Title: "Decide what blocks a merge", State: StepManual, Command: "forge-ci docs admin-policy"}

	policy, err := appenforcement.Resolve(ctx)
	if err != nil {
		s.State, s.Detail, s.Command = StepTodo, err.Error(), "forge-ci doctor"
	} else {
		s.Detail = appenforcement.Summary(policy)
	}

	return s
}

func step(title string, done bool, detail, command string) Step {
	s := Step{Title: title, State: StepTodo, Detail: detail, Command: command}
	if done {
		s.State, s.Command = StepDone, ""
	}

	return s
}

// signerStep is done when the release's allowed-signers file has an entry.
// A GitLab-only repository passes the text as a pipeline input instead.
func signerStep(root string, checks []appdoctor.Check) Step {
	const title = "List who may sign releases"

	command := "forge-ci setup signer --principal <your email> --public-key ~/.ssh/id_ed25519.pub   (and again for the release bot)"

	if regularFile(filepath.Join(root, ".gitlab-ci.yml")) && !dirExists(filepath.Join(root, ".github", "workflows")) &&
		!dirExists(filepath.Join(root, ".forgejo", "workflows")) {
		return Step{
			Title: title, State: StepManual,
			Detail:  "GitLab reads the request-allowed-signers input; paste the line this prints into it",
			Command: "forge-ci setup signer --dry-run --principal <your email> --public-key ~/.ssh/id_ed25519.pub",
		}
	}

	file := DefaultSignersFile(root)

	if c, found := appdoctor.Find(checks, appdoctor.CheckSSHAllowlist); found && c.Severity == appdoctor.SeverityFail {
		return Step{Title: title, State: StepTodo, Detail: c.Message, Command: command}
	}

	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file))) //nolint:gosec // repository allowlist under root.
	if err != nil || !hasAnyEntry(body) {
		return Step{Title: title, State: StepTodo, Detail: file + " has no signer yet", Command: command}
	}

	return Step{Title: title, State: StepDone, Detail: file + " lists at least one signer"}
}

func hasAnyEntry(body []byte) bool {
	for line := range strings.SplitSeq(string(body), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			return true
		}
	}

	return false
}

func regularFile(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.Mode().IsRegular()
}

// FormatChecklist prints the steps with a mark each and, for the first step
// still to do, says it is the next one.
func FormatChecklist(out io.Writer, steps []Step) {
	_, _ = fmt.Fprintln(out, "Forge CI setup: from no files to a first release")
	_, _ = fmt.Fprintln(out)

	marks := map[StepState]string{StepDone: "[x]", StepTodo: "[ ]", StepManual: "[?]"}
	next := -1

	for i, s := range steps {
		_, _ = fmt.Fprintf(out, "%s %d. %s\n", marks[s.State], i+1, s.Title)

		if s.Detail != "" {
			_, _ = fmt.Fprintf(out, "       %s\n", s.Detail)
		}

		if s.Command != "" {
			_, _ = fmt.Fprintf(out, "       run: %s\n", s.Command)
		}

		if next < 0 && s.State == StepTodo {
			next = i
		}
	}

	_, _ = fmt.Fprintln(out, "\n[x] done   [ ] to do   [?] not visible from the repository; check it yourself")

	if next >= 0 {
		_, _ = fmt.Fprintf(out, "Next: step %d, %s.\n", next+1, strings.ToLower(steps[next].Title[:1])+steps[next].Title[1:])
	} else {
		_, _ = fmt.Fprintln(out, "Next: everything visible from here is done; finish the [?] steps, then request the release.")
	}
}
