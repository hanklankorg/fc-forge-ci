// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

// This exercises the real CLI, OCI resolver and Cosign adapter, not Cosign's
// cryptography. The controlled verifier checks its auth/argv protocol and emits
// a synthetic verified envelope, as the app's fakeBaseImageVerifier does.
// Repeated validation models warm verification, not a cold-to-warm release.
func TestPrivateBaseValidationReuseAndRefusals(t *testing.T) {
	for _, mode := range []string{
		"verified", "bad-credentials", "forbidden", "missing-auth",
		"signature", "cyclonedx", "slsaprovenance1", "malformed",
		"source", "workflow", "flavor", "base_input_id",
		"parent-verified", "parent-wrong", "parent-missing",
	} {
		t.Run(mode, func(t *testing.T) { testPrivateBaseValidation(t, mode) })
	}
}

func testPrivateBaseValidation(t *testing.T, mode string) {
	t.Helper()
	env := testenv.New(t)
	t.Chdir(env.Temp)

	input := strings.Repeat("a", 64)
	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:` + strings.Repeat("c", 64) + `","size":2},"layers":[]}`
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(manifest)))

	var requests []string

	var requestsMu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requestsMu.Lock()

		requests = append(requests, request.Method+" "+request.URL.Path)
		requestsMu.Unlock()

		user, password, _ := request.BasicAuth()
		if user != "selected" || password != "fixture" {
			response.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			response.WriteHeader(http.StatusUnauthorized)

			return
		}

		if mode == "forbidden" {
			response.WriteHeader(http.StatusForbidden)

			return
		}

		response.Header().Set("Docker-Content-Digest", digest)
		response.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		response.Header().Set("Content-Length", strconv.Itoa(len(manifest)))
		response.WriteHeader(http.StatusOK)

		if request.Method == http.MethodGet {
			_, _ = response.Write([]byte(manifest))
		}
	}))
	t.Cleanup(server.Close)
	host := strings.TrimPrefix(server.URL, "http://")
	repo := host + "/owner/base"
	auth := fmt.Sprintf(`{"auths":{%q:{"username":"selected","password":"fixture"}}}`, host)
	// Valid ambient credentials must neither rescue bad/missing explicit auth nor
	// be required by the successful path (whose ambient credentials are invalid).
	ambient := env.MkdirAll("ambient")

	ambientAuth, selectedAuth := strings.ReplaceAll(auth, "fixture", "ambient-invalid"), auth
	if mode == "bad-credentials" || mode == "missing-auth" {
		ambientAuth, selectedAuth = auth, strings.ReplaceAll(auth, "fixture", "selected-invalid")
	}

	require.NoError(t, os.WriteFile(filepath.Join(ambient, "config.json"), []byte(ambientAuth), 0o600))
	env.Setenv("DOCKER_CONFIG", ambient)
	env.Setenv("REGISTRY_AUTH_FILE", filepath.Join(ambient, "config.json"))

	authFile := env.Path("auth.json")
	if mode != "missing-auth" {
		require.NoError(t, os.WriteFile(authFile, []byte(selectedAuth), 0o600))
	}

	key := []byte("synthetic public key; verifier protocol only\n")
	require.NoError(t, os.WriteFile("key.pub", key, 0o600))
	require.NoError(t, os.WriteFile("flavors", []byte("rust\n"), 0o600))
	envelope := privateBaseEnvelope(t, mode, input)
	require.NoError(t, os.WriteFile("lineage.json", envelope, 0o600))
	bins := mockbinary.New(t)
	bins.Add("cosign", fmt.Sprintf(`set -eu
cmp "$DOCKER_CONFIG/config.json" %q
case "$1" in
verify) step=signature ;;
verify-attestation) test "$2" = --type; step=$3 ;;
*) exit 99 ;;
esac
test "$step" != %q || exit 73
cat %q
`, authFile, mode, env.Path("lineage.json")))

	for _, tool := range []string{"buildah", "podman", "docker"} {
		bins.Add(tool, "exit 99")
	}

	bins.OnPath()

	args := []string{"base-images", "validate",
		"--expected-repository", repo, "--expected-source", "https://source.example/owner/app", "--expected-workflow", "base.yml",
		"--base-input-id", input, "--flavors-file", "flavors", "--cosign-public-key-path", "key.pub",
		"--cosign-public-key-sha256", fmt.Sprintf("%x", sha256.Sum256(key)), "--auth-file", authFile}
	if strings.HasPrefix(mode, "parent-") {
		args = append(args, "--base-inputs-json", fmt.Sprintf(`[{"flavor":"rust","base_input_id":%q,"parent_base":{"ref":%q,"input_id":%q}}]`, input,
			"other.example/shared/parent@sha256:"+strings.Repeat("b", 64), strings.Repeat("c", 64)))
	}

	for range 2 {
		command := &cli.Command{
			Name: "base-images", Flags: []cli.Flag{&cli.StringFlag{Name: "format", Value: "json"}},
			Commands: []*cli.Command{baseImagesVerifyCmd()},
		}

		var out bytes.Buffer

		command.Writer, command.ErrWriter = &out, &out

		err := command.Run(t.Context(), args)
		if mode == "verified" || mode == "parent-verified" {
			require.NoError(t, err)

			var outputs struct {
				AllFound bool   `json:"all_found"`
				Missing  string `json:"missing_flavors_json"`
				Images   string `json:"all_images_json"`
			}
			require.NoError(t, json.Unmarshal(out.Bytes(), &outputs))
			require.True(t, outputs.AllFound)
			require.Equal(t, "[]", outputs.Missing)
			require.JSONEq(t, fmt.Sprintf(`[{"flavor":"rust","tag":%q,"ref":%q,"candidate_tag":"","candidate_ref":"","base_input_id":%q}]`, repo+":"+input+"-rust", repo+"@"+digest, input), outputs.Images)
		} else {
			require.Error(t, err)

			switch mode {
			case "bad-credentials", "forbidden":
				require.ErrorIs(t, err, errs.ErrPermissionDenied)
			case "missing-auth":
				require.ErrorIs(t, err, errs.ErrDependencyUnavailable)
			default:
				require.ErrorIs(t, err, errs.ErrValidation)
			}
			// Refusal must not emit a missing-flavor decision inviting a rebuild.
			require.NotContains(t, out.String(), "missing_flavors_json")
			require.NotContains(t, out.String(), "all_found")
		}
	}

	assertPrivateBaseCalls(t, bins, mode, repo+"@"+digest)
	requestsMu.Lock()
	defer requestsMu.Unlock()

	for _, request := range requests {
		require.Contains(t, []string{"GET /v2/", "HEAD /v2/owner/base/manifests/" + input + "-rust", "GET /v2/owner/base/manifests/" + input + "-rust"}, request)
	}

	if mode == "missing-auth" {
		require.Empty(t, requests, "missing local auth must refuse before registry access")
	} else {
		require.NotEmpty(t, requests)
	}
}

func privateBaseEnvelope(t *testing.T, mode, input string) []byte {
	t.Helper()

	params := map[string]any{"source": "git+https://source.example/owner/app", "workflow": "base.yml", "flavor": "rust", "base_input_id": input}
	if _, exists := params[mode]; exists {
		params[mode] = "wrong-lineage"
	}

	if mode == "parent-verified" || mode == "parent-wrong" {
		id := strings.Repeat("c", 64)
		if mode == "parent-wrong" {
			id = strings.Repeat("d", 64)
		}

		params["parent_base"] = map[string]any{"ref": "other.example/shared/parent@sha256:" + strings.Repeat("b", 64), "input_id": id}
	}

	statement, err := json.Marshal(map[string]any{
		"predicateType": "https://slsa.dev/provenance/v1",
		"predicate":     map[string]any{"buildDefinition": map[string]any{"externalParameters": params}},
	})
	require.NoError(t, err)
	envelope, err := json.Marshal(map[string]string{"payload": base64.StdEncoding.EncodeToString(statement)})
	require.NoError(t, err)

	if mode == "malformed" {
		return []byte("not-json")
	}

	return envelope
}

func assertPrivateBaseCalls(t *testing.T, bins *mockbinary.Mock, mode, ref string) {
	t.Helper()

	for _, tool := range []string{"buildah", "podman", "docker"} {
		require.Empty(t, bins.Invocations(tool), "validation must not build or publish")
	}

	callsPerRun := 3

	switch mode {
	case "bad-credentials", "forbidden", "missing-auth":
		callsPerRun = 0
	case "signature":
		callsPerRun = 1
	case "cyclonedx":
		callsPerRun = 2
	}

	want := [][]string{
		{"verify", "--key", "key.pub", ref},
		{"verify-attestation", "--type", "cyclonedx", "--key", "key.pub", ref},
		{"verify-attestation", "--type", "slsaprovenance1", "--key", "key.pub", ref},
	}
	calls := bins.Invocations("cosign")
	require.Len(t, calls, callsPerRun*2)

	for i, call := range calls {
		require.Equal(t, want[i%callsPerRun], call.Args)
	}
}

func TestBaseValidationUsesExplicitAuthForRegistryAndCosign(t *testing.T) {
	testenv.New(t)
	t.Chdir(t.TempDir())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, _ := r.BasicAuth()
		if user != "selected" || password != "fixture" {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		w.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("b", 64))
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	host := strings.TrimPrefix(server.URL, "http://")
	repository := host + "/owner/base"
	input := strings.Repeat("a", 64)
	auth := fmt.Sprintf(`{"auths":{%q:{"username":"selected","password":"fixture"}}}`, host)
	authFile := filepath.Join(t.TempDir(), "auth.json")
	require.NoError(t, os.WriteFile(authFile, []byte(auth), 0o600))
	ambient := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(ambient, "config.json"), []byte(auth), 0o600))
	t.Setenv("DOCKER_CONFIG", ambient)
	t.Setenv("REGISTRY_AUTH_FILE", authFile)
	require.NoError(t, os.WriteFile("flavors", []byte("rust\n"), 0o600))

	key := []byte("fixture-public-key\n")
	require.NoError(t, os.WriteFile("key.pub", key, 0o600))
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "cosign-auth")
	// No real verifier or external registry: prove both adapters receive the
	// selected credentials, then deliberately refuse signature verification.
	script := fmt.Sprintf("#!/bin/sh\nset -eu\ncp \"$DOCKER_CONFIG/config.json\" %q\necho fixture-signature-refusal >&2\nexit 73\n", marker)
	require.NoError(t, os.WriteFile(filepath.Join(bin, "cosign"), []byte(script), 0o700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	args := []string{"validate", "--expected-repository", repository, "--expected-source", "https://source.example/owner/app", "--expected-workflow", "base.yml", "--base-input-id", input, "--flavors-file", "flavors", "--cosign-public-key-path", "key.pub", "--cosign-public-key-sha256", fmt.Sprintf("%x", sha256.Sum256(key))}
	// Ambient credentials do not turn the anonymous discovery route private.
	require.NoError(t, baseImagesVerifyCmd().Run(t.Context(), args))
	require.NoFileExists(t, marker)
	err := baseImagesVerifyCmd().Run(t.Context(), append(args, "--auth-file", authFile))
	require.Error(t, err, "a bad signature must still refuse authenticated reuse")
	body, err := os.ReadFile(marker)
	require.NoError(t, err)
	require.Equal(t, auth, string(body))
	// A selected missing file must not fall back to valid ambient credentials.
	require.NoError(t, os.Remove(marker))
	err = baseImagesVerifyCmd().Run(t.Context(), append(args, "--auth-file", filepath.Join(t.TempDir(), "absent")))
	require.Error(t, err)
	require.NoFileExists(t, marker)
}
