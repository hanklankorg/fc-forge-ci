// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package buildah_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/buildah"
	"github.com/diggsweden/forge-ci/internal/domain/container"
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

	for _, want := range []string{"--pull=always", "--isolation chroot", "--network host", "--tls-verify=true", "--timestamp 1700000000", "--manifest localhost/app:list", "--platform linux/arm64", "--label org.opencontainers.image.revision=abc", "--build-arg VERSION=1.2.3 --build-arg COMMIT=abc", "REGISTRY_AUTH_FILE=/tmp/auth.json"} {
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
