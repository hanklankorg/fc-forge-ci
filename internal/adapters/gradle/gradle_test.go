// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gradle_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/gradle"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

// stubGradlew writes a fake ./gradlew script in the test's tmpdir and
// returns its absolute path. Tests pass that path via Adapter.Bin.
func stubGradlew(t *testing.T, body string) string {
	t.Helper()
	fsys := testfs.NewReal(t)

	path := fsys.Path("gradlew")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestRunInherit_PassesArgsAndStreamsOutput(t *testing.T) {
	bin := stubGradlew(t, `printf 'gradle running %s\n' "$*"; printf 'warn\n' >&2`)
	a := &gradle.Adapter{Bin: bin}

	var stdout, stderr bytes.Buffer
	if err := a.RunInherit(context.Background(), &stdout, &stderr, "build", "-x", "test"); err != nil {
		t.Fatalf("RunInherit: %v", err)
	}

	if got := strings.TrimSpace(stdout.String()); got != "gradle running build -x test" {
		t.Errorf("stdout = %q", got)
	}

	if got := strings.TrimSpace(stderr.String()); got != "warn" {
		t.Errorf("stderr = %q", got)
	}
}

func TestRunInherit_NonZeroExitWraps(t *testing.T) {
	bin := stubGradlew(t, `printf 'oops\n' >&2; exit 7`)
	a := &gradle.Adapter{Bin: bin}

	err := a.RunInherit(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, "cyclonedxBom")
	if !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("exit 7 = %v, want ErrValidation (the build was refused, not the toolchain missing)", err)
	}

	if !strings.Contains(err.Error(), "cyclonedxBom") {
		t.Errorf("error missing args: %v", err)
	}
}

// A wrapper that could not reach the host of its distribution ends as an
// unavailable dependency; the same exception from a program the build ran
// is the build's failure.
func TestRunInherit_AWrapperThatReachedNoHostIsAnUnavailableDependency(t *testing.T) {
	const unknown = `Exception in thread "main" java.net.UnknownHostException: services.gradle.org`

	for frame, want := range map[string]error{
		"org.gradle.wrapper.Download.downloadInternal(Download.java:129)": errs.ErrDependencyUnavailable,
		"com.example.Client.main(Client.java:12)":                         errs.ErrValidation,
	} {
		bin := stubGradlew(t, `echo 'Downloading https://services.gradle.org/distributions/gradle-9.1.0-bin.zip'; printf '%s\n\tat %s\n' '`+unknown+`' '`+frame+`' >&2; exit 1`)

		var stderr bytes.Buffer

		err := (&gradle.Adapter{Bin: bin}).RunInherit(context.Background(), &bytes.Buffer{}, &stderr, "test")
		if err == nil || !errors.Is(err, want) || errs.ExitCodeFromError(err) != errs.ExitCodeFromError(want) {
			t.Errorf("%s: err = %v, want %v", frame, err, want)
		}

		if !strings.Contains(stderr.String(), unknown) {
			t.Errorf("the wrapper's output did not reach the caller: %q", stderr.String())
		}
	}
}
