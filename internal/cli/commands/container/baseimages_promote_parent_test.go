// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
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
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provenance"
)

// Inert adapter integration only: local HTTP registry and a mock cosign binary,
// not an assertion that these synthetic statements have valid signatures.
func TestBaseImagesPromoteParentCLI(t *testing.T) {
	for _, mode := range []string{"correct", "wrong", "missing", "split", "unknown valid", "unknown valid no parent", "full batch", "missing batch", "no map"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newBaseSignFixture(t)
			t.Setenv("BASE_INPUTS_JSON", "")
			require.NoError(t, os.Unsetenv("BASE_INPUTS_JSON"))

			id := strings.Repeat("a", 64)
			digest := "sha256:" + strings.Repeat("e", 64)

			var registryCalls atomic.Int64

			registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				registryCalls.Add(1)

				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v2/":
				case r.Method == http.MethodHead && strings.Contains(r.URL.Path, "/manifests/"):
					w.Header().Set("Docker-Content-Digest", digest)
					w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
					w.Header().Set("Content-Length", "1")
				default:
					t.Errorf("unexpected registry mutation/request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			t.Cleanup(registry.Close)
			repository := strings.TrimPrefix(registry.URL, "http://") + "/owner/app-base"
			parent := &appbaseimages.BaseImageParent{Ref: "other.example/shared/parent@sha256:" + strings.Repeat("b", 64), InputID: strings.Repeat("c", 64)}
			params := map[string]any{"source": registry.URL + "/" + baseSlug, "workflow": baseWorkflow, "flavor": "child", "base_input_id": id, "parent_base": parent}
			statements := []map[string]any{params}

			switch mode {
			case "wrong":
				params["parent_base"] = &appbaseimages.BaseImageParent{Ref: parent.Ref, InputID: strings.Repeat("d", 64)}
			case "missing", "no map":
				delete(params, "parent_base")
			case "split":
				delete(params, "parent_base")
				statements = append(statements, map[string]any{"source": params["source"], "workflow": baseWorkflow, "flavor": "child", "base_input_id": strings.Repeat("d", 64), "parent_base": parent})
			case "full batch", "missing batch":
				statements = append(statements, map[string]any{"source": params["source"], "workflow": baseWorkflow, "flavor": "other", "base_input_id": id, "parent_base": parent})
			}

			envelopes := make([]map[string]string, 0, len(statements))
			for _, fields := range statements {
				statement, err := json.Marshal(map[string]any{"predicateType": provenance.PredicateTypeV1, "predicate": map[string]any{"buildDefinition": map[string]any{"externalParameters": fields}}})
				require.NoError(t, err)

				envelopes = append(envelopes, map[string]string{"payload": base64.StdEncoding.EncodeToString(statement)})
			}

			payload, err := json.Marshal(envelopes)
			require.NoError(t, err)
			fixture.cosign.Add("cosign", fmt.Sprintf("printf '%%s' '%s'", payload))

			key := []byte("inert key")
			require.NoError(t, os.WriteFile("key.pub", key, 0o600))

			targets := []appbaseimages.BaseImageMetadata{{Flavor: "child", Tag: repository + ":final", Ref: repository + "@" + digest, BaseInputID: id}}

			expected := []appbaseimages.BaseInput{{Flavor: "child", BaseInputID: id, ParentBase: parent}}
			if mode == "full batch" || mode == "missing batch" {
				targets = append(targets, appbaseimages.BaseImageMetadata{Flavor: "other", Tag: repository + ":other", Ref: repository + "@" + digest, BaseInputID: id})
			}

			if mode == "full batch" || mode == "unknown valid" {
				expected = append(expected, appbaseimages.BaseInput{Flavor: "other", BaseInputID: id, ParentBase: parent})
			}

			if mode == "unknown valid no parent" {
				expected = append(expected, appbaseimages.BaseInput{Flavor: "unused", BaseInputID: strings.Repeat("d", 64)})
			}

			images, err := json.Marshal(targets)
			require.NoError(t, err)
			inputs, err := json.Marshal(expected)
			require.NoError(t, err)

			args := []string{"container", "base-images", "promote", "--all-images-json", string(images), "--server-url", registry.URL, "--repository", baseSlug, "--expected-repository", repository, "--expected-workflow", baseWorkflow, "--cosign-public-key-path", "key.pub", "--cosign-public-key-sha256", fmt.Sprintf("%x", sha256.Sum256(key)), "--registry-username", "fixture"}
			if mode != "no map" {
				args = append(args, "--base-inputs-json", string(inputs))
			}

			err = fixture.runErr(t, args...)

			switch mode {
			case "correct", "full batch":
				require.NoError(t, err)
			case "no map":
				// The map is mandatory; there is no legacy path without it.
				require.ErrorContains(t, err, "base-inputs-json")
			default:
				require.ErrorIs(t, err, errs.ErrValidation)
			}

			if strings.HasPrefix(mode, "unknown valid") || mode == "missing batch" || mode == "no map" {
				require.Zero(t, registryCalls.Load(), "including login, reads and copies")
				require.Empty(t, fixture.cosign.Invocations("cosign"))
				scratch, readErr := os.ReadDir(fixture.env.Path("runner-temp"))
				require.NoError(t, readErr)
				require.Empty(t, scratch)
			} else {
				require.NotEmpty(t, fixture.cosign.Invocations("cosign"))
			}
		})
	}
}
