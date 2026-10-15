// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

// Exercise the command's real signer construction: an env:// key must still
// let both sign and attest find the job's registry login outside HOME.
func TestLedgerSign_EnvKeyPreservesDockerConfigWithoutUnrelatedSecrets(t *testing.T) {
	testenv.New(t)

	fsys := testfs.NewReal(t)
	t.Chdir(fsys.Root)

	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	// Only digest resolution reaches HTTP, and only this loopback fixture.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v2/":
		case "HEAD /v2/owner/app/manifests/" + digest:
			w.Header().Set("Docker-Content-Digest", digest)
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.Header().Set("Content-Length", "1")
		default:
			t.Errorf("unexpected registry request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	repository := strings.TrimPrefix(srv.URL, "http://") + "/owner/app"
	imageRef := repository + "@" + digest

	const authConfig = `{"auths":{"registry.example":{"username":"fixture","password":"fixture"}}}`
	fsys.WriteFile("work/.docker/config.json", []byte(authConfig))
	t.Setenv("DOCKER_CONFIG", fsys.Path("work/.docker"))
	t.Setenv("COSIGN_KEY", "fixture-signing-key")
	t.Setenv("COSIGN_PASSWORD", "fixture-passphrase")

	for _, name := range []string{"GITHUB_TOKEN", "FORGEJO_TOKEN", "CI_JOB_TOKEN", "REGISTRY_TOKEN", "REGISTRY_PASSWORD", "GPG_PRIVATE_KEY", "UNRELATED_SECRET"} {
		t.Setenv(name, "must-not-reach-cosign")
	}

	mock := mockbinary.New(t)
	mock.OnPath()
	mock.Add("cosign", fmt.Sprintf(`
set -eu
if [ "${DOCKER_CONFIG:-}" != %q ]; then
  printf 'ledger signer lost DOCKER_CONFIG\n' >&2
  exit 1
fi
[ "$(cat "$DOCKER_CONFIG/config.json")" = %q ]
[ "$COSIGN_KEY" = fixture-signing-key ]
[ "$COSIGN_PASSWORD" = fixture-passphrase ]
for name in GITHUB_TOKEN FORGEJO_TOKEN CI_JOB_TOKEN REGISTRY_TOKEN REGISTRY_PASSWORD GPG_PRIVATE_KEY UNRELATED_SECRET; do
  if [[ -v $name ]]; then
    printf 'unrelated secret reached cosign: %%s\n' "$name" >&2
    exit 1
  fi
done
`, fsys.Path("work/.docker"), authConfig))
	// A pinned, premade SBOM keeps this test on the ledger/Cosign boundary.
	sbom := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6","version":1}`)
	fsys.WriteFile("image.cyclonedx.json", sbom)
	ledger, err := imageledger.Marshal([]imageledger.Entry{{
		Ref: imageRef, Digest: digest, Role: "app", FinalTag: repository + ":v1.2.3",
		SBOM: "image.cyclonedx.json", SBOMSHA256: fmt.Sprintf("%x", sha256.Sum256(sbom)),
	}})
	require.NoError(t, err)

	ledgerPath := fsys.WriteFile("release-images.json", ledger)
	predicatePath := fsys.WriteFile("predicate.json", []byte(`{"buildDefinition":{"externalParameters":{}}}`))

	cmd := ledgerSignCmd()
	require.NoError(t, cmd.Run(t.Context(), []string{
		cmd.Name, "--ledger", ledgerPath, "--tag", "v1.2.3",
		"--provenance-predicate", predicatePath, "--method", "kms", "--key", "env://COSIGN_KEY",
	}))

	calls := mock.Invocations("cosign")
	require.Len(t, calls, 3)

	const attest = "attest"
	for idx, verb := range []string{"sign", attest, attest} {
		require.Equal(t, verb, calls[idx].Args[0])
		require.Contains(t, calls[idx].Args, "env://COSIGN_KEY")
		require.Contains(t, calls[idx].Args, imageRef)
	}

	require.Contains(t, calls[1].Args, "cyclonedx")
	require.Contains(t, calls[2].Args, "slsaprovenance1")
}
