// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/require"

	rootcli "github.com/diggsweden/forge-ci/internal/cli"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

const (
	pruneRegistry = "registry.example:5443"
	pruneBaseRepo = pruneRegistry + "/owner/nested/base"
	pruneSource   = "https://source.example/team/consumer"
	pruneWorkflow = ".forgejo/workflows/release.yml"
)

// These tests run the public CLI with a fake verifier and an in-memory HTTP
// transport. No listener, registry, credential helper or real cosign is used.
func TestBaseImagesPrune_EnforcesRequestedTrustBeforeInventory(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		want       error
		wantCalls  int
		structured bool
	}{
		{name: "wrong key pin", args: []string{"--cosign-public-key-sha256", strings.Repeat("f", 64)}, want: errs.ErrValidation},
		{name: "empty supplied key pin", args: []string{"--cosign-public-key-sha256", ""}, want: errs.ErrValidation},
		{name: "malformed key pin", args: []string{"--cosign-public-key-sha256", "not-a-digest"}, want: errs.ErrValidation},
		{name: "wrong source", args: []string{"--expected-source", "https://source.example/other/consumer"}, want: errs.ErrValidation, wantCalls: 1},
		{name: "wrong workflow", args: []string{"--expected-workflow", "other.yml"}, want: errs.ErrValidation, wantCalls: 1},
		{name: "wrong structured source", args: []string{"--expected-source", "https://source.example/other/consumer"}, want: errs.ErrValidation, wantCalls: 1, structured: true},
		{name: "wrong structured workflow", args: []string{"--expected-workflow", "other.yml"}, want: errs.ErrValidation, wantCalls: 1, structured: true},
		{name: "unsafe workflow", args: []string{"--expected-workflow", "../release.yml"}, want: errs.ErrUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPruneFixture(t, tc.structured)
			err := f.run(t, "prune", tc.args...)
			require.ErrorIs(t, err, tc.want)
			require.Len(t, f.cosign.Invocations("cosign"), tc.wantCalls)
			require.Empty(t, f.requests, "refusal must precede inventory and deletion")
		})
	}
}

func TestBaseImagesPrune_EnvironmentExpectationsAndFlagPrecedence(t *testing.T) {
	f := newPruneFixture(t, false)
	f.env.Setenv("COSIGN_PUBLIC_KEY_SHA256", strings.Repeat("f", 64))
	f.env.Setenv("EXPECTED_SOURCE", "https://source.example/other/consumer")
	f.env.Setenv("EXPECTED_WORKFLOW", "other.yml")
	require.ErrorIs(t, f.run(t, "prune"), errs.ErrValidation)
	require.Empty(t, f.cosign.Invocations("cosign"))
	require.Empty(t, f.requests)

	args := make([]string, 0, 6)
	args = append(args, "--cosign-public-key-sha256", f.keyHash)
	require.ErrorIs(t, f.run(t, "prune", args...), errs.ErrValidation)
	require.Len(t, f.cosign.Invocations("cosign"), 1)
	require.Empty(t, f.requests)

	args = append(args, "--expected-source", pruneSource)
	require.ErrorIs(t, f.run(t, "prune", args...), errs.ErrValidation)
	require.Len(t, f.cosign.Invocations("cosign"), 2)
	require.Empty(t, f.requests)

	args = append(args, "--expected-workflow", pruneWorkflow)
	require.NoError(t, f.run(t, "prune", args...))
	require.Len(t, f.cosign.Invocations("cosign"), 3)
	require.NotEmpty(t, f.requests)
}

func TestBaseImagesPrune_VerifiesBothProvenanceProfilesAndUsesExplicitAuth(t *testing.T) {
	for _, structured := range []bool{false, true} {
		t.Run(fmt.Sprintf("structured=%t", structured), func(t *testing.T) {
			f := newPruneFixture(t, structured)
			err := f.run(t, "prune", "--cosign-public-key-sha256", f.keyHash,
				"--expected-source", pruneSource, "--expected-workflow", pruneWorkflow)
			require.NoError(t, err)
			require.Len(t, f.cosign.Invocations("cosign"), 1)
			body, readErr := os.ReadFile(f.env.Path("seen-auth.json"))
			require.NoError(t, readErr)
			require.JSONEq(t, `{"auths":{}}`, string(body), "explicit auth must reach cosign instead of ambient Docker config")
			require.NotEmpty(t, f.requests)

			for _, request := range f.requests {
				require.Equal(t, http.MethodGet, request.method, "default is dry-run")
				require.Equal(t, pruneRegistry, request.host)
			}
		})
	}
}

func TestBaseImagesPrune_OmittedIdentityAllowsAnotherConsumerAndDeletesOnlyStaleTags(t *testing.T) {
	f := newPruneFixture(t, false)
	// The base repository is owner/nested/base; the attestation is from
	// team/consumer. No source/workflow expectation is inferred from the former.
	require.NoError(t, f.run(t, "prune", "--dry-run=false"))

	var deletes []string

	for _, request := range f.requests {
		require.Equal(t, pruneRegistry, request.host)

		if request.method == http.MethodDelete {
			deletes = append(deletes, request.path)
		}
	}

	require.Equal(t, []string{"/v2/owner/nested/base/manifests/" + strings.Repeat("b", 64) + "-go"}, deletes)
}

func TestBaseImagesCleanup_LocalInventoryUsesConfiguredRegistry(t *testing.T) {
	f := newPruneFixture(t, false)
	require.NoError(t, f.run(t, "cleanup"))
	require.Empty(t, f.cosign.Invocations("cosign"))
	require.NotEmpty(t, f.requests)

	for _, request := range f.requests {
		require.Equal(t, http.MethodGet, request.method)
		require.Equal(t, pruneRegistry, request.host)
	}
}

type pruneRequest struct {
	method string
	host   string
	path   string
}

type pruneFixture struct {
	env      *testenv.Env
	cosign   *mockbinary.Mock
	keyHash  string
	requests []pruneRequest
}

type pruneTransport func(*http.Request) (*http.Response, error)

func (f pruneTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newPruneFixture(t *testing.T, structured bool) *pruneFixture {
	t.Helper()
	env := testenv.New(t)
	t.Chdir(env.Temp)

	key := []byte("synthetic public key for pin validation; cryptography is stubbed\n")
	require.NoError(t, os.WriteFile(env.Path("cosign.pub"), key, 0o600))
	require.NoError(t, os.WriteFile(env.Path("auth.json"), []byte(`{"auths":{}}`), 0o600))
	env.Setenv("DOCKER_CONFIG", env.MkdirAll("ambient"))
	require.NoError(t, os.WriteFile(env.Path("ambient", "config.json"), []byte(`{"auths":{},"fixture":"ambient"}`), 0o600))

	f := &pruneFixture{env: env, cosign: mockbinary.New(t), keyHash: fmt.Sprintf("%x", sha256.Sum256(key))}
	f.cosign.Add("cosign", fmt.Sprintf("cat \"${DOCKER_CONFIG:?}/config.json\" > %q\nprintf '%%s\\n' '%s'\n", env.Path("seen-auth.json"), pruneEnvelope(t, structured)))
	f.cosign.OnPath()

	original := remote.DefaultTransport

	t.Cleanup(func() { remote.DefaultTransport = original })

	remote.DefaultTransport = pruneTransport(f.roundTrip)

	return f
}

func pruneEnvelope(t *testing.T, structured bool) string {
	t.Helper()

	baseID, digest := strings.Repeat("a", 64), strings.Repeat("c", 64)
	baseRef := pruneBaseRepo + "@sha256:" + digest

	params := map[string]any{
		"source": "git+" + pruneSource, "workflow": pruneWorkflow,
		"base": map[string]string{"ref": baseRef, "input_id": baseID},
	}
	if structured {
		delete(params, "source")
		params["workflow"] = map[string]string{"repository": pruneSource, "path": pruneWorkflow, "ref": "refs/tags/v1"}
	}

	statement, err := json.Marshal(map[string]any{
		"predicateType": "https://slsa.dev/provenance/v1",
		"predicate": map[string]any{"buildDefinition": map[string]any{
			"externalParameters": params,
			"resolvedDependencies": []any{map[string]any{
				"uri": "oci://" + baseRef, "digest": map[string]string{"sha256": digest},
				"annotations": map[string]string{"base_input_id": baseID},
			}},
		}},
	})
	require.NoError(t, err)
	envelope, err := json.Marshal(map[string]string{"payload": base64.StdEncoding.EncodeToString(statement)})
	require.NoError(t, err)

	return string(envelope)
}

func (f *pruneFixture) run(t *testing.T, verb string, extra ...string) error {
	t.Helper()

	args := []string{"forge-ci", "--provider", "local", "--format", "text", "container", "base-images", verb,
		"--server-url", "https://forge.example", "--repository", "owner/builder",
		"--expected-repository", pruneBaseRepo, "--local-registry", "--auth-file", "auth.json"}
	if verb == "prune" {
		args = append(args, "--cosign-public-key-path", "cosign.pub", "--release-images", pruneRegistry+"/team/release@sha256:"+strings.Repeat("d", 64))
	}

	command := rootcli.New(rootcli.BuildInfo{Version: "dev"})
	command.Writer, command.ErrWriter = io.Discard, io.Discard

	return command.Run(t.Context(), append(args, extra...))
}

func (f *pruneFixture) roundTrip(r *http.Request) (*http.Response, error) {
	f.requests = append(f.requests, pruneRequest{method: r.Method, host: r.URL.Host, path: r.URL.Path})
	kept, stale := strings.Repeat("a", 64)+"-go", strings.Repeat("b", 64)+"-go"
	header, status, body := make(http.Header), http.StatusOK, ""

	switch r.Method + " " + r.URL.Path {
	case "GET /v2/":
	case "GET /v2/owner/nested/base/tags/list":
		body = fmt.Sprintf(`{"name":"owner/nested/base","tags":[%q,%q]}`, kept, stale)
	case "HEAD /v2/owner/nested/base/manifests/" + kept, "HEAD /v2/owner/nested/base/manifests/" + stale:
		header.Set("Docker-Content-Digest", "sha256:"+strings.Repeat("c", 64))

		if strings.HasSuffix(r.URL.Path, stale) {
			header.Set("Docker-Content-Digest", "sha256:"+strings.Repeat("e", 64))
		}

		header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		header.Set("Content-Length", "1")
	case "DELETE /v2/owner/nested/base/manifests/" + stale:
		status = http.StatusAccepted
	default:
		return nil, fmt.Errorf("unexpected fixture request %s %s: %w", r.Method, r.URL, errs.ErrValidation)
	}

	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}
