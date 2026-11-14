// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestPreseededProvenanceFixtureUsesOnlyThePrivateConsumerIndex(t *testing.T) {
	t.Parallel()

	root, git := stageTestRepo(t, "sha1")
	original := "#!/bin/sh\nset -eu\nprintf original-build\n"
	stageTestWrite(t, root, GitLabConsumerBuildScript, original, 0o600)
	stageTestCommit(t, git, root)
	before := mustLayerGit(t, git, root, "rev-parse", "HEAD", "HEAD^{tree}")

	consumer, err := openConsumer(t.Context(), git, root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	fixture := consumer.WithPreseededGitLabProvenance(t)
	if fixture.Commit == consumer.Commit {
		t.Fatal("negative fixture did not create its own admitted tree")
	}

	backup := mustLayerGit(t, git, fixture.stage, "show", fixture.Commit+":"+gitlabPreseedOriginalBuild)

	wrapper := mustLayerGit(t, git, fixture.stage, "show", fixture.Commit+":"+GitLabConsumerBuildScript)
	if backup != strings.TrimSpace(original) || !strings.Contains(wrapper, "bash "+gitlabPreseedOriginalBuild+" \"$@\"") ||
		!strings.Contains(wrapper, "> dist/slsa-provenance.intoto.json") {
		t.Fatalf("fixture lost original build or negative handoff: backup=%s wrapper=%s", backup, wrapper)
	}

	if mustLayerGit(t, git, root, "rev-parse", "HEAD", "HEAD^{tree}") != before ||
		mustLayerGit(t, git, root, "show", "HEAD:"+GitLabConsumerBuildScript) != strings.TrimSpace(original) ||
		mustLayerGit(t, git, root, "status", "--porcelain") != "" {
		t.Fatal("negative fixture modified the source checkout, index or identity")
	}

	if _, err := fixture.withPreseededGitLabProvenance(t.Context()); !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("fixture silently overwrote its reserved source path: %v", err)
	}

	for name, source := range map[string]string{"successful original": original, "failed original": "#!/bin/sh\nexit 42\n"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			work := t.TempDir()
			stageTestWrite(t, work, gitlabPreseedOriginalBuild, source, 0o600)
			stageTestWrite(t, work, GitLabConsumerBuildScript, wrapper, 0o600)
			command := exec.CommandContext(t.Context(), "/bin/bash", GitLabConsumerBuildScript)
			command.Dir, command.Env = work, []string{"PATH=/usr/bin:/bin"}

			output, err := command.CombinedOutput()
			_, artifactErr := os.Stat(filepath.Join(work, "dist/slsa-provenance.intoto.json"))

			if name == "successful original" {
				if err != nil || string(output) != "original-build" || artifactErr != nil {
					t.Fatalf("fixture did not run original build before preseed: output=%s err=%v artifact=%v", output, err, artifactErr)
				}
			} else if err == nil || !os.IsNotExist(artifactErr) {
				t.Fatal("preseed ran after a failed original build")
			}
		})
	}
}

func TestGitLabConsumerReleaseScenarioAdmissionIsExactAndBounded(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", "TestGitLabRelease_ConsumerTrial", "TestGitLabRelease_RejectsPreseededProvenance", "TestGitLabRelease_ConsumerInterruptedRecovery"} {
		if _, err := GitLabConsumerReleaseScenario(name); err != nil {
			t.Fatalf("supported case refused: %v", err)
		}
	}

	for _, name := range []string{"Test.*", "^TestGitLabRelease_ConsumerTrial$", "TestOther", "$(id)"} {
		if _, err := GitLabConsumerReleaseScenario(name); !errors.Is(err, errs.ErrValidation) {
			t.Fatalf("unbounded scenario selector accepted: %q %v", name, err)
		}
	}

	for _, input := range []string{
		"scenario=TestGitLabRelease_ConsumerTrial\nscenario=TestGitLabRelease_ConsumerTrial",
		"scenario=TestGitLabRelease_ConsumerInterruptedRecovery",
		"scenario=TestGitLabRelease_ConsumerInterruptedRecovery\nversion=0.0.0",
		"scenario=TestGitLabRelease_RejectsPreseededProvenance\nversion=0.3.0\nnext-version=0.3.1",
	} {
		if _, err := ParseGitLabReleaseSettings(input); !errors.Is(err, errs.ErrValidation) {
			t.Fatalf("unsafe consumer case inputs accepted: %q %v", input, err)
		}
	}

	settings, err := ParseGitLabReleaseSettings("scenario=TestGitLabRelease_ConsumerInterruptedRecovery\nversion=0.3.0")
	if err != nil || len(settings.Variables) != 0 || settings.Version != "0.3.0" {
		t.Fatalf("controller case selection escaped into consumer variables: %+v %v", settings, err)
	}
}
