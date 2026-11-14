// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
)

// refusingBuildahSetupTool is Buildah on a runner whose job container may not
// create a user namespace: `buildah info` dies the same way for every driver.
type refusingBuildahSetupTool struct {
	fakeBuildahSetupTool

	stderr string
}

func (f *refusingBuildahSetupTool) InfoDriver(context.Context, []string) (string, error) {
	return "", errors.New("buildah info: exit status 1: " + errs.ErrValidation.Error() + "\n" + f.stderr)
}

// Every storage driver failing identically is not a storage problem. The
// error names the runner, the variable that moves the job and the builder
// that needs no user namespace.
func TestSetupBuildah_NamesTheRunnerWhenUserNamespacesAreDenied(t *testing.T) {
	t.Parallel()

	scratch := t.TempDir()
	tool := &refusingBuildahSetupTool{
		commands: map[string]bool{"buildah": true, "fuse-overlayfs": true},
		stderr:   "Error during unshare(CLONE_NEWUSER): Operation not permitted",
	}

	var log strings.Builder

	_, err := appcontainer.SetupBuildah(context.Background(), tool, nil, fakeoutputsink.New(t), &fakeSummarySink{}, &log, appcontainer.SetupBuildahInput{
		StorageConf: filepath.Join(scratch, "storage.conf"),
		StorageRoot: filepath.Join(scratch, "root"),
		TmpDir:      filepath.Join(scratch, "tmp"),
		RunnerTemp:  scratch,
	})
	if err == nil {
		t.Fatal("want an error")
	}

	for _, want := range []string{"unshare(CLONE_NEWUSER)", "denies user namespaces", "CI_RUNNER_LABEL_CONTAINER", "request-runner", "CI_IMAGE_BUILDER=kaniko"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err=%q lacks %q", err.Error(), want)
		}
	}
}

// Any other storage failure keeps Buildah's own words and gets no hint.
func TestSetupBuildah_OtherStorageFailuresGetNoRunnerHint(t *testing.T) {
	t.Parallel()

	scratch := t.TempDir()
	tool := &refusingBuildahSetupTool{
		commands: map[string]bool{"buildah": true},
		stderr:   "Error: mkdir /run/containers: read-only file system",
	}

	_, err := appcontainer.SetupBuildah(context.Background(), tool, nil, fakeoutputsink.New(t), &fakeSummarySink{}, io.Discard, appcontainer.SetupBuildahInput{
		StorageConf: filepath.Join(scratch, "storage.conf"),
		StorageRoot: filepath.Join(scratch, "root"),
		TmpDir:      filepath.Join(scratch, "tmp"),
		RunnerTemp:  scratch,
	})
	if err == nil || !strings.Contains(err.Error(), "read-only file system") || strings.Contains(err.Error(), "denies user namespaces") {
		t.Fatalf("err=%v", err)
	}
}
