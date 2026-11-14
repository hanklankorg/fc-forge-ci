// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package buildah_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/buildah"
	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
)

func TestBuildManifest_ArgvShape(t *testing.T) {
	t.Parallel()
	bins := mockbinary.New(t)
	bins.Add("buildah", `printf 'ARGV %s\nREGISTRY_AUTH_FILE=%s\n' "$*" "${REGISTRY_AUTH_FILE:-<unset>}" >&2`)
	adapter := &buildah.Adapter{Bin: bins.Path("buildah")}

	var log strings.Builder

	err := adapter.BuildManifest(t.Context(), container.BuildPushManifestBuildRequest{
		AuthFile: "/tmp/auth.json", Manifest: "localhost/app:list", TLSVerify: true,
		SourceDateEpoch: "1700000000", Platform: "linux/arm64", Containerfile: "Containerfile",
		BuildArgs: []string{"VERSION=1.2.3", "COMMIT=abc"}, Labels: []string{"org.opencontainers.image.revision=abc"}, Context: ".",
	}, &log)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"--pull=always", "--isolation chroot", "--network host", "--retry 5 --retry-delay 15s", "--tls-verify=true", "--timestamp 1700000000", "--manifest localhost/app:list", "--platform linux/arm64", "--label org.opencontainers.image.revision=abc", "--build-arg VERSION=1.2.3 --build-arg COMMIT=abc", "REGISTRY_AUTH_FILE=/tmp/auth.json"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("missing %q: %s", want, log.String())
		}
	}
}

func TestPublicationExportsKeepAuthTLSAndLocalDestination(t *testing.T) {
	t.Parallel()

	for _, verify := range []bool{true, false} {
		for _, manifest := range []bool{true, false} {
			bins := mockbinary.New(t)
			bins.Add("buildah", `printf 'ARGV %s\nREGISTRY_AUTH_FILE=%s\n' "$*" "${REGISTRY_AUTH_FILE:-<unset>}" >&2`)
			adapter := &buildah.Adapter{Bin: bins.Path("buildah")}

			var (
				log strings.Builder
				err error
			)
			if manifest {
				err = adapter.ExportManifestToLayout(t.Context(), "/tmp/auth.json", verify, "localhost/app:list", "/tmp/layout", &log)
			} else {
				err = adapter.ExportImageToLayout(t.Context(), "/tmp/auth.json", verify, "localhost/app:arch", "/tmp/layout", &log)
			}

			if err != nil {
				t.Fatal(err)
			}

			tlsFlag := "--tls-verify=false"
			if verify {
				tlsFlag = "--tls-verify=true"
			}

			for _, want := range []string{tlsFlag, "--authfile /tmp/auth.json", "REGISTRY_AUTH_FILE=/tmp/auth.json", "oci:/tmp/layout:scan"} {
				if !strings.Contains(log.String(), want) {
					t.Errorf("missing %q: %s", want, log.String())
				}
			}

			if strings.Contains(log.String(), "docker://") || strings.Contains(log.String(), "--rm") || strings.Contains(log.String(), "--all") != manifest {
				t.Fatal("export published, removed, or lost a platform")
			}
		}
	}
}

func TestRemoveLocalManifestPropagatesFailure(t *testing.T) {
	t.Parallel()
	bins := mockbinary.New(t)
	bins.Add("buildah", "exit 7")

	if err := (&buildah.Adapter{Bin: bins.Path("buildah")}).RemoveLocalManifest(t.Context(), "localhost/app:list", io.Discard); err == nil {
		t.Fatal("cleanup failure swallowed")
	}
}

func TestExportLocalToLayout_ArgvShape(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		call func(*buildah.Adapter, *strings.Builder) error
		want string
	}{
		{func(a *buildah.Adapter, log *strings.Builder) error {
			return a.ExportLocalImageToLayout(context.Background(), "localhost/app:arch", "/tmp/layout", log)
		}, "push localhost/app:arch oci:/tmp/layout:scan"},
		{func(a *buildah.Adapter, log *strings.Builder) error {
			return a.ExportLocalManifestToLayout(context.Background(), "localhost/app:list", "/tmp/layout", log)
		}, "manifest push --all localhost/app:list oci:/tmp/layout:scan"},
	} {
		bins := mockbinary.New(t)
		bins.Add("buildah", `printf 'ARGV %s\n' "$*" >&2`)

		var log strings.Builder
		if err := test.call(&buildah.Adapter{Bin: bins.Path("buildah")}, &log); err != nil {
			t.Fatal(err)
		}

		if got := strings.TrimSpace(strings.TrimPrefix(log.String(), "ARGV")); got != test.want {
			t.Errorf("got %q, want %q", got, test.want)
		}
	}
}

func TestInfoDriver_IgnoresBuildahsStderrWarnings(t *testing.T) {
	t.Parallel()
	bins := mockbinary.New(t)
	bins.Add("buildah", `printf 'configuration warning\n' >&2; printf 'overlay\n'`)

	got, err := (&buildah.Adapter{Bin: bins.Path("buildah")}).InfoDriver(t.Context(), nil)
	if err != nil || got != "overlay" {
		t.Fatalf("driver=%q err=%v", got, err)
	}
}

// A failing `buildah info` is the storage diagnosis: its stderr names the
// cause, and the exit status alone names nothing.
func TestInfoDriver_FailureCarriesBuildahsStderr(t *testing.T) {
	t.Parallel()
	bins := mockbinary.New(t)
	bins.Add("buildah", `printf 'Error: mkdir /run/containers: read-only file system\n' >&2; exit 1`)

	_, err := (&buildah.Adapter{Bin: bins.Path("buildah")}).InfoDriver(t.Context(), nil)
	if err == nil {
		t.Fatal("want an error")
	}

	if !errors.Is(err, errs.ErrValidation) {
		t.Errorf("err=%v, want ErrValidation", err)
	}

	for _, want := range []string{"info: exit status 1", "read-only file system"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err=%q lacks %q", err.Error(), want)
		}
	}
}

func TestProbeBuild_BuildsAsTheRealBuildsDo(t *testing.T) {
	t.Parallel()
	bins := mockbinary.New(t)
	bins.Add("buildah", `printf 'ARGV %s\n' "$*" >&2`)

	var log strings.Builder
	if err := (&buildah.Adapter{Bin: bins.Path("buildah")}).ProbeBuild(t.Context(), nil, "/tmp/probe", "localhost/probe:latest", &log); err != nil {
		t.Fatal(err)
	}

	const want = "ARGV bud --isolation chroot --network host --pull=never --tag localhost/probe:latest /tmp/probe\nARGV rmi localhost/probe:latest\n"
	if log.String() != want {
		t.Errorf("probe=%q, want %q", log.String(), want)
	}
}

// A build ends in the class of the error buildah ended on, and the build
// log still reaches the caller.
func TestBuildManifestClassifiesTheErrorBuildahEndsOn(t *testing.T) {
	t.Parallel()

	const unreached = `Error: creating build container: initializing source docker://gcr.io/distroless/cc-debian13@sha256:e792ab3d241a468a4fd7519ddbbebe66b49b5f365771716ea688ad40b6c6f1c2: pinging container registry gcr.io: Get "https://gcr.io/v2/": dial tcp: lookup gcr.io on 10.201.0.1:53: server misbehaving`

	for said, want := range map[string]error{
		unreached: errs.ErrDependencyUnavailable,
		`Error: building at STEP "RUN ./fetch": while running runtime: exit status 1`: errs.ErrValidation,
	} {
		bins := mockbinary.New(t)
		bins.Add("buildah", `echo 'STEP 1/2: RUN ./fetch'; echo 'dial tcp: lookup mirror.example: i/o timeout' >&2; printf '%s\n' '`+said+`' >&2; exit 125`)
		adapter := &buildah.Adapter{Bin: bins.Path("buildah")}

		var log strings.Builder

		err := adapter.BuildManifest(t.Context(), container.BuildPushManifestBuildRequest{
			Manifest: "localhost/app:list", SourceDateEpoch: "1700000000", Platform: "linux/amd64", Containerfile: "Containerfile", Context: ".",
		}, &log)
		if err == nil || !errors.Is(err, want) || errs.ExitCodeFromError(err) != errs.ExitCodeFromError(want) {
			t.Errorf("%s: err = %v, want %v", said, err, want)
		}

		// The engine prints "Error: " and this; the live launcher's rerun
		// rule reads the host from that line.
		if errors.Is(want, errs.ErrDependencyUnavailable) {
			if line := "bud: exit status 125: " + strings.TrimPrefix(unreached, "Error: ") + ": dependency unavailable"; !strings.HasSuffix(err.Error(), line) {
				t.Errorf("err = %q, want it to end %q", err, line)
			}
		}

		if !strings.Contains(log.String(), "STEP 1/2") || !strings.Contains(log.String(), said) {
			t.Errorf("the build log lost output: %q", log.String())
		}
	}
}
