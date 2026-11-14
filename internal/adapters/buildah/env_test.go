// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package buildah

import (
	"io"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
)

func TestEveryCommandRestrictsTheInheritedEnvironment(t *testing.T) {
	t.Setenv("SIGSTORE_ROOT_FILE", "untrusted")
	t.Setenv("TUF_ROOT", "untrusted")
	t.Setenv("COSIGN_REPOSITORY", "untrusted")
	t.Setenv("GPG_PRIVATE_KEY", "unrelated")
	t.Setenv("CONTAINERS_STORAGE_CONF", "/runner/storage.conf")
	t.Setenv("BUILDAH_ISOLATION", "chroot")
	t.Setenv("HTTPS_PROXY", "http://proxy.example")
	bins := mockbinary.New(t)
	bins.Add("buildah", `
[ -z "${SIGSTORE_ROOT_FILE+set}${TUF_ROOT+set}${COSIGN_REPOSITORY+set}${GPG_PRIVATE_KEY+set}" ] || exit 31
[ "${CONTAINERS_STORAGE_CONF-}" = /runner/storage.conf ] || exit 32
[ "${BUILDAH_ISOLATION-}" = chroot ] || exit 33
[ "${HTTPS_PROXY-}" = http://proxy.example ] || exit 34
`)
	adapter := &Adapter{Bin: bins.Path("buildah")}

	for _, mode := range []string{"run", "run-auth", "build", "output", "output-auth"} {
		t.Run(mode, func(t *testing.T) {
			var err error

			switch mode {
			case "run":
				err = adapter.run(t.Context(), io.Discard, "info")
			case "run-auth":
				err = adapter.runWithEnv(t.Context(), io.Discard, []string{"REGISTRY_AUTH_FILE=/runner/auth"}, "pull")
			case "build":
				err = adapter.runBuildWithEnv(t.Context(), io.Discard, nil, "1700000000", "build")
			case "output":
				_, err = adapter.outputBytesWithEnv(t.Context(), nil, "inspect")
			case "output-auth":
				_, err = adapter.outputBytesWithEnv(t.Context(), []string{"REGISTRY_AUTH_FILE=/runner/auth"}, "inspect")
			}

			if err != nil {
				t.Fatalf("builder subprocess environment: %v", err)
			}
		})
	}
}
