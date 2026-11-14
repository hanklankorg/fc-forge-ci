// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	appbaseimages "github.com/diggsweden/forge-ci/internal/app/baseimages"
	rootcli "github.com/diggsweden/forge-ci/internal/cli"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provenance"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

// The CLI wiring alone, against a loopback registry and a Cosign stand-in: the
// parent projection is printed only for an authenticated, pinned-key
// verification, from private snapshots of the auth file and the key. What a
// statement must say is held at the app level, in TestAuthenticateBaseParent.
func TestBaseImagesAuthenticateParentCLI(t *testing.T) {
	for _, mode := range []string{"child", "no-auth", "multiple", "key-pin"} {
		t.Run(mode, func(t *testing.T) { runAuthenticateParentScenario(t, mode) })
	}
}

func runAuthenticateParentScenario(t *testing.T, mode string) {
	t.Helper()
	env := testenv.New(t)
	t.Chdir(env.Temp)

	id, digest := strings.Repeat("a", 64), "sha256:"+strings.Repeat("e", 64)

	var requests atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests.Add(1)

		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			t.Errorf("unexpected registry write: %s", request.Method)
			response.WriteHeader(http.StatusBadRequest)

			return
		}

		user, password, _ := request.BasicAuth()
		if user != "selected" || password != "synthetic-password" {
			response.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			response.WriteHeader(http.StatusUnauthorized)

			return
		}

		response.Header().Set("Docker-Content-Digest", digest)
		response.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		response.Header().Set("Content-Length", "1")
	}))
	t.Cleanup(server.Close)

	host := strings.TrimPrefix(server.URL, "http://")
	repository := host + "/owner/base"
	ref := repository + "@" + digest
	auth := fmt.Sprintf(`{"auths":{%q:{"username":"selected","password":"synthetic-password"}}}`, host)
	key := []byte("synthetic public key; no cryptographic qualification\n")

	for _, file := range []string{"key.pub", "frozen-key.pub"} {
		require.NoError(t, os.WriteFile(file, key, 0o600))
	}

	for _, file := range []string{"auth.json", "frozen-auth.json"} {
		require.NoError(t, os.WriteFile(file, []byte(auth), 0o600))
	}

	ambient := env.MkdirAll("ambient")
	require.NoError(t, os.WriteFile(ambient+"/config.json", []byte(auth), 0o600))
	t.Setenv("DOCKER_CONFIG", ambient)
	t.Setenv("REGISTRY_AUTH_FILE", env.Path("auth.json"))
	t.Setenv("COSIGN_KEY", "synthetic-signing-secret")

	parent := &appbaseimages.BaseImageParent{Ref: "other.example/shared/base@sha256:" + strings.Repeat("b", 64), InputID: strings.Repeat("c", 64)}
	expected := []appbaseimages.BaseInput{{Flavor: "leaf", BaseInputID: id, ParentBase: parent}}
	params := map[string]any{"source": "https://source.example/owner/app", "workflow": "bases.yml", "flavor": "leaf", "base_input_id": id, "parent_base": parent}

	statement, err := json.Marshal(map[string]any{"predicateType": provenance.PredicateTypeV1, "predicate": map[string]any{"buildDefinition": map[string]any{"externalParameters": params}}})
	require.NoError(t, err)

	payload, err := json.Marshal([]map[string]string{{"payload": base64.StdEncoding.EncodeToString(statement)}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile("lineage.json", payload, 0o600))

	// Cosign sees the snapshots, not the caller's files: it replaces both
	// originals once it has compared its own copies with the frozen ones.
	bins := mockbinary.New(t)
	bins.Add("cosign", fmt.Sprintf(`set -eu
test -z "${COSIGN_KEY-}"
cmp "$DOCKER_CONFIG/config.json" %q
while [ "$1" != --key ]; do shift; done
shift
cmp "$1" %q
printf replaced > %q
printf replaced > %q
cat %q
`, env.Path("frozen-auth.json"), env.Path("frozen-key.pub"), env.Path("key.pub"), env.Path("auth.json"), env.Path("lineage.json")))
	bins.OnPath()

	if mode == "multiple" {
		expected = append(expected, expected[0])
	}

	inputs, err := json.Marshal(expected)
	require.NoError(t, err)

	pin := fmt.Sprintf("%x", sha256.Sum256(key))
	if mode == "key-pin" {
		pin = strings.Repeat("f", 64)
	}

	args := []string{"forge-ci", "--provider", "forgejo", "container", "base-images", "authenticate-parent",
		"--expected-repository", repository, "--expected-source", "https://source.example/owner/app", "--expected-workflow", "bases.yml",
		"--expected-ref", ref, "--base-inputs-json", string(inputs), "--cosign-public-key-path", "key.pub", "--cosign-public-key-sha256", pin}

	if mode != "no-auth" {
		args = append(args, "--auth-file", env.Path("auth.json"))
	}

	command := rootcli.New(rootcli.BuildInfo{Version: "dev"})

	var output, diagnostics bytes.Buffer

	command.Writer, command.ErrWriter = &output, &diagnostics
	err = command.Run(t.Context(), args)

	for _, text := range []string{fmt.Sprint(err), output.String(), diagnostics.String()} {
		require.NotContains(t, text, "synthetic-password")
		require.NotContains(t, text, "synthetic-signing-secret")
	}

	// The temporary config is always removed and the caller environment restored.
	require.Equal(t, ambient, os.Getenv("DOCKER_CONFIG"))

	if mode != "child" {
		require.Error(t, err)
		require.Empty(t, output.String(), "no parent or success output on refusal")
		require.Zero(t, requests.Load())
		require.Empty(t, bins.Invocations("cosign"))

		if mode == "key-pin" {
			require.ErrorIs(t, err, errs.ErrValidation)
		} else {
			require.ErrorIs(t, err, errs.ErrUsage)
		}

		return
	}

	require.NoError(t, err)

	var got appbaseimages.BaseImageParent

	require.NoError(t, json.Unmarshal(output.Bytes(), &got))
	require.Equal(t, appbaseimages.BaseImageParent{Ref: ref, InputID: id}, got)
	require.Len(t, bins.Invocations("cosign"), 3)

	for _, invocation := range bins.Invocations("cosign") {
		for i, argument := range invocation.Args {
			if argument == "--key" {
				require.NoFileExists(t, invocation.Args[i+1], "remove the private pinned key on every path")
			}
		}
	}
}

// select-parent hands on only what the verifying job wrote for the flavor.
func TestBaseImagesSelectParentCLI(t *testing.T) {
	t.Parallel()

	file := t.TempDir() + "/parents.json"
	pinned := "registry.example/owner/base@sha256:" + strings.Repeat("a", 64)
	require.NoError(t, os.WriteFile(file, []byte(`{"base":{"ref":"`+pinned+`","input_id":"`+strings.Repeat("b", 64)+`"},"loose":{"ref":"registry.example/owner/base:tag","input_id":"x"}}`), 0o600))

	for flavor, want := range map[string]error{"base": nil, "tools": errs.ErrValidation, "loose": errs.ErrValidation} {
		err := rootcli.New(rootcli.BuildInfo{}).Run(t.Context(), []string{"forge-ci", "container", "base-images", "select-parent", "--parent-bases-file", file, "--flavor", flavor})
		require.ErrorIs(t, err, want, flavor)
	}
}
