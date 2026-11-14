// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	"bytes"
	"context"
	"os"
	"slices"
	"testing"
	"time"

	appbuild "github.com/diggsweden/forge-ci/internal/app/build"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
	"github.com/stretchr/testify/require"
)

func TestXcodeSigningCleanup_SurvivesOperationCancellation(t *testing.T) {
	for _, step := range []string{"setup", "archive", "-exportArchive"} {
		t.Run(step, func(t *testing.T) { checkXcodeCleanupContext(t, step, true) })
	}

	t.Run("failure without cancellation", func(t *testing.T) { checkXcodeCleanupContext(t, "archive", false) })
}

type xcodeCleanupContextKey struct{}

func checkXcodeCleanupContext(t *testing.T, step string, cancelOperation bool) {
	t.Helper()

	fsys, in := xcodeReleaseFixture(t)
	keychain := fsys.Path("TMPDIR/app-signing.keychain-db")
	login := fsys.WriteFile("HOME/login.keychain-db", []byte("unrelated keychain"))
	search := []string{login}

	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), xcodeCleanupContextKey{}, "operation value"))
	defer cancel()

	cause := errs.ErrValidation
	if cancelOperation {
		cause = context.Canceled
	}

	failed := false
	fail := func() {
		failed = true

		if cancelOperation {
			cancel()
		}
	}

	var (
		cleanupContexts []context.Context
		cleanupErrors   []error
		cleanupBudgets  []time.Duration
	)

	sec := &recordingXcodeSecurity{}
	sec.run = func(index int) (string, error) {
		callCtx, args := sec.contexts[index], sec.calls[index]
		if failed {
			cleanupContexts = append(cleanupContexts, callCtx)
			cleanupErrors = append(cleanupErrors, callCtx.Err())
			deadline, _ := callCtx.Deadline()
			cleanupBudgets = append(cleanupBudgets, time.Until(deadline))
		}

		// Like exec.CommandContext, a cancelled context cannot start security(1).
		if err := callCtx.Err(); err != nil {
			return "", err
		}

		switch args[0] {
		case "create-keychain":
			require.Equal(t, keychain, args[3])

			return "", os.WriteFile(keychain, []byte("synthetic imported keychain"), 0o600)
		case "set-keychain-settings":
			if step == "setup" {
				fail()

				return "", cause
			}
		case "list-keychain":
			if len(args) == 3 {
				return quotedKeychainList(search...), nil
			}

			search = slices.Clone(args[4:])
		case "delete-keychain":
			require.Equal(t, keychain, args[1])

			return "", os.Remove(keychain)
		}

		return "", nil
	}
	ops := &fakeXcodeBuild{}
	ops.run = func(args []string) {
		if args[0] == step {
			fail()

			ops.err = cause

			return
		}

		fsys.WriteFile("checkout/build/app.xcarchive/Info.plist", []byte(xcarchiveInfoPlist("Zebra")))
	}
	sink := fakeoutputsink.New(t)

	var stdout, stderr bytes.Buffer

	err := appbuild.XcodeReleaseBuild(ctx, sink, sec, ops, output.Annotator{}, &stdout, &stderr, in)
	require.ErrorIs(t, err, cause)
	require.Equal(t, errs.ExitCodeFromError(cause), errs.ExitCodeFromError(err))
	require.Empty(t, sink.Keys(), "a failed build must not publish success outputs")

	_, statErr := os.Stat(keychain)
	require.ErrorIs(t, statErr, os.ErrNotExist, "cleanup must actually remove the owned keychain")
	require.Equal(t, []string{login}, search, "restore the prior keychain search list")
	loginBody, readErr := os.ReadFile(login)
	require.NoError(t, readErr)
	require.Equal(t, "unrelated keychain", string(loginBody))

	wantCalls := 2
	if step == "setup" {
		wantCalls = 1 // the search list was never changed
	}

	require.Len(t, cleanupContexts, wantCalls)

	for index, cleanup := range cleanupContexts {
		require.NoError(t, cleanupErrors[index], "cleanup starts with a live context")
		require.Positive(t, cleanupBudgets[index], "cleanup must have a future deadline")
		require.LessOrEqual(t, cleanupBudgets[index], time.Minute, "detached cleanup must remain bounded")
		require.Equal(t, "operation value", cleanup.Value(xcodeCleanupContextKey{}))
		require.ErrorIs(t, cleanup.Err(), context.Canceled, "release the cleanup timer on return")
	}

	for _, path := range []string{"TMPDIR/certificate.p12", "TMPDIR/pp.mobileprovision", "HOME/Library/MobileDevice/Provisioning Profiles/pp.mobileprovision"} {
		_, statErr := os.Stat(fsys.Path(path))
		require.ErrorIs(t, statErr, os.ErrNotExist, path)
	}
}
