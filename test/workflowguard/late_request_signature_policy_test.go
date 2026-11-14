// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/adapters/git"
	forgecli "github.com/diggsweden/forge-ci/internal/cli"
	appvalidate "github.com/diggsweden/forge-ci/internal/app/validate"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeprovider"
)

// TestGuardCLIProcess is the forge-ci child of a workflow step under test: it
// runs the actual CLI command tree with the local provider, never a forge.
func TestGuardCLIProcess(t *testing.T) {
	if os.Getenv("FORGE_CI_GUARD_CLI_CHILD") != "1" {
		return
	}

	for i, arg := range os.Args {
		if arg == "--" {
			err := forgecli.New(forgecli.BuildInfo{}).Run(t.Context(), append([]string{"forge-ci", "--provider", "local", "--runner", "github"}, os.Args[i+1:]...))
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}

			os.Exit(0)
		}
	}

	t.Fatal("missing command separator")
}

// The late boundary must reach the prerequisite gate's decision on the
// request tag, and must still require a valid final-tag signature. Both sides
// run real code against one repository whose tags carry a dummy PGP armor
// block that no configured key can verify: the gate through Prerequisites, the late
// boundary by executing the workflow's own signature commands, with the
// step's own environment, through the actual CLI.
func TestLateRequestSignatureAgreesWithPrerequisiteGate(t *testing.T) {
	repo := unverifiableSignedRelease(t)
	t.Chdir(repo)

	executable, err := os.Executable()
	require.NoError(t, err)

	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "forge-ci"), []byte("#!/bin/bash\nexport FORGE_CI_GUARD_CLI_CHILD=1\nexec "+strconv.Quote(executable)+" -test.run='^TestGuardCLIProcess$' -- \"$@\"\n"), 0o700))

	step := lateAuthorizationStep(t)

	var requestLine, finalLine string

	for line := range strings.SplitSeq(step.Run, "\n") {
		line = strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(line, `forge-ci validate tag signature --tag "release-request/$TAG_NAME"`):
			requestLine = line
		case strings.HasPrefix(line, `forge-ci validate tag signature --tag "$TAG_NAME"`):
			finalLine = line
		}
	}

	require.NotEmpty(t, requestLine)
	require.NotEmpty(t, finalLine)

	for _, requireAllowlisted := range []bool{false, true} {
		t.Run("require-allowlisted-signer="+strconv.FormatBool(requireAllowlisted), func(t *testing.T) {
			gateErr := prerequisiteGateSignature(t, requireAllowlisted)
			lateOut, lateErr := runLateSignatureLine(t, bin, step, requestLine, requireAllowlisted)

			if requireAllowlisted {
				require.ErrorIs(t, gateErr, errs.ErrPermissionDenied)
				require.Error(t, lateErr, lateOut)
				require.Contains(t, lateOut, "no GPG allowlist is present")
			} else {
				require.NoError(t, gateErr, "the gate warns and passes an unverifiable request without a key")
				require.NoError(t, lateErr, "the late boundary must pass what the gate passed: "+lateOut)
				require.Contains(t, lateOut, "No GPG signer allowlist present")
			}

			// The bot's final tag has no permissive mode: without the
			// configured release key it cannot verify, so it is refused.
			finalOut, finalErr := runLateSignatureLine(t, bin, step, finalLine, requireAllowlisted)
			require.Error(t, finalErr, finalOut)
			require.Contains(t, finalOut, "could not be verified against the configured release GPG public key: permission denied")
		})
	}
}

// prerequisiteGateSignature returns the gate's own tag-signature outcome for
// the request tag, with no configured key and no committed allowlist.
func prerequisiteGateSignature(t *testing.T, requireAllowlisted bool) error {
	t.Helper()

	result, _ := appvalidate.Prerequisites(t.Context(), appvalidate.PrerequisitesDeps{
		GitRepo:  git.New(),
		Provider: fakeprovider.New(t).WithPlatform(provider.ForgeGitHub),
	}, &bytes.Buffer{}, output.Annotator{}, appvalidate.PrerequisitesInput{
		RefType:                  "tag",
		Tag:                      "release-request/v1.2.3",
		Ref:                      "refs/tags/release-request/v1.2.3",
		Branch:                   "main",
		Repository:               "owner/product",
		RequireAllowlistedSigner: requireAllowlisted,
	})

	for _, check := range result.Checks {
		if check.Name == "tag-signature" {
			require.False(t, check.Skipped)

			return check.Err
		}
	}

	t.Fatal("the gate did not run its tag-signature check")

	return nil
}

func runLateSignatureLine(t *testing.T, bin string, step lateStep, line string, requireAllowlisted bool) (string, error) {
	t.Helper()

	metadata := filepath.Join(t.TempDir(), "release-request-metadata")
	resolved := map[string]string{
		"${{ github.token }}":                                                        "inert-read-token",
		"${{ inputs['checkout-ref'] }}":                                              strings.Repeat("b", 40),
		"${{ inputs['authorized-source-sha'] }}":                                     strings.Repeat("a", 40),
		"${{ inputs['request-is-release'] }}":                                        "false",
		"${{ inputs['release-name'] }}":                                              "v1.2.3",
		"${{ inputs['require-allowlisted-signer'] }}":                                strconv.FormatBool(requireAllowlisted),
		"${{ runner.temp }}/release-request-metadata/.forge-ci/allowed_signers":      filepath.Join(metadata, ".forge-ci/allowed_signers"),
		"${{ runner.temp }}/release-request-metadata/.forge-ci/allowed_gpg_keys.asc": filepath.Join(metadata, ".forge-ci/allowed_gpg_keys.asc"),
		"${{ secrets.RELEASE_GPG_PUBLIC_KEY }}":                                      "",
	}

	env := make([]string, 0, 5+len(step.Env))
	env = append(env, "PATH="+bin+":/usr/bin:/bin", "HOME="+t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GOMAXPROCS=2")

	for key, value := range step.Env {
		if strings.Contains(value, "${{") {
			replacement, ok := resolved[value]
			require.True(t, ok, "unmodelled late-step env %s=%s", key, value)

			value = replacement
		}

		env = append(env, key+"="+value)
	}

	cmd := exec.CommandContext(t.Context(), "bash", "--noprofile", "--norc", "-eu", "-o", "pipefail", "-c", line)
	cmd.Env = env
	body, err := cmd.CombinedOutput()

	return string(body), err
}

func lateAuthorizationStep(t *testing.T) lateStep {
	t.Helper()

	for _, step := range lateWorkflow(t, "release-create-github.yml")["sign-release"].Steps {
		if step.Name == "Revalidate request authority and final source at private boundary" {
			return step
		}
	}

	t.Fatal("late authorization step not found")

	return lateStep{}
}

// unverifiableSignedRelease builds a request commit, its release child on
// main, and annotated request and final tags that each carry a dummy PGP
// signature armor block, not a real signature. It is detected as a GPG
// signature, but no key exists for it, so it cannot be verified.
func unverifiableSignedRelease(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	run := func(stdin string, args ...string) string {
		t.Helper()

		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		cmd.Stdin = strings.NewReader(stdin)

		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_AUTHOR_NAME=Requester", "GIT_AUTHOR_EMAIL=requester@example.invalid", "GIT_AUTHOR_DATE=1700000000 +0000",
			"GIT_COMMITTER_NAME=Requester", "GIT_COMMITTER_EMAIL=requester@example.invalid", "GIT_COMMITTER_DATE=1700000000 +0000")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))

		return strings.TrimSpace(string(out))
	}

	run("", "init", "--quiet", "--initial-branch=main")
	run("", "commit", "--quiet", "--allow-empty", "-m", "request")
	request := run("", "rev-parse", "HEAD")
	run("", "commit", "--quiet", "--allow-empty", "-m", "release")
	release := run("", "rev-parse", "HEAD")

	for tag, commit := range map[string]string{"release-request/v1.2.3": request, "v1.2.3": release} {
		object := run("object "+commit+"\ntype commit\ntag "+tag+"\ntagger Requester <requester@example.invalid> 1700000000 +0000\n\n"+tag+"\n"+
			"-----BEGIN PGP SIGNATURE-----\n\niQEzBAABCAAdFiEEunverifiable\n=AAAA\n-----END PGP SIGNATURE-----\n", "hash-object", "-t", "tag", "-w", "--stdin")
		run("", "update-ref", "refs/tags/"+tag, object)
	}

	return dir
}
