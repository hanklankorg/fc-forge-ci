// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejo_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/forgejo"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestDeleteTag_DeletesContainerPackageVersion(t *testing.T) {
	t.Parallel()

	var deletedPath string

	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete {
			deletedPath = request.URL.Path

			response.WriteHeader(http.StatusNoContent)

			return
		}

		http.Error(response, "unexpected "+request.Method, http.StatusNotFound)
	})

	provider := &forgejo.Provider{
		Env:             envMap(map[string]string{"FORGEJO_TOKEN": "tok"}),
		HTTPClient:      inMemoryClient(handler),
		APIBaseOverride: "https://forgejo.invalid",
	}

	// A fully-qualified ref on the server's registry; the package API targets
	// owner/name/version.
	if err := provider.DeleteTag(context.Background(), "forgejo.invalid/examplescope/myapp:staging-v1.2.3"); err != nil {
		t.Fatalf("DeleteTag = %v", err)
	}

	const want = "/api/v1/packages/examplescope/container/myapp/staging-v1.2.3"
	if deletedPath != want {
		t.Errorf("DELETE path = %q, want %q", deletedPath, want)
	}
}

func TestDeleteTag_TreatsMissingPackageVersionAsAlreadyAbsent(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete {
			http.Error(response, "not found", http.StatusNotFound)

			return
		}

		http.Error(response, "unexpected "+request.Method, http.StatusNotFound)
	})

	provider := &forgejo.Provider{
		Env:             envMap(map[string]string{"FORGEJO_TOKEN": "tok"}),
		HTTPClient:      inMemoryClient(handler),
		APIBaseOverride: "https://forgejo.invalid",
	}

	if err := provider.DeleteTag(context.Background(), "forgejo.invalid/examplescope/myapp:staging-v1.2.3"); err != nil {
		t.Fatalf("DeleteTag 404 should be idempotent, got %v", err)
	}
}

// Only a 404 means the tag is already gone. A refused delete is a failure: read
// as success, a cleanup would report a tag removed that is still served.
func TestDeleteTag_ReportsARefusedDelete(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})

	provider := &forgejo.Provider{
		Env:             envMap(map[string]string{"FORGEJO_TOKEN": "tok"}),
		HTTPClient:      inMemoryClient(handler),
		APIBaseOverride: "https://forgejo.invalid",
	}

	err := provider.DeleteTag(t.Context(), "forgejo.invalid/examplescope/myapp:staging-v1.2.3")
	if !errors.Is(err, errs.ErrPermissionDenied) {
		t.Fatalf("a refused delete = %v, want ErrPermissionDenied", err)
	}
}

// A redirect that leaves the server's scheme or host is refused, and the
// request is not replayed there: followed, the delete (and for a plain-HTTP or
// other-port target the token) would go to the other place, and its answer
// would be read as this server's.
func TestDeleteTag_RefusesARedirectThatLeavesTheServer(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		location string
		want     error
		requests int
	}{
		"within the server": {location: "https://forgejo.invalid/api/v1/moved", requests: 2},
		"to plain HTTP":     {location: "http://forgejo.invalid/api/v1/moved", want: errs.ErrValidation, requests: 1},
		"to another port":   {location: "https://forgejo.invalid:8443/api/v1/moved", want: errs.ErrValidation, requests: 1},
		"to another host":   {location: "https://other.invalid/api/v1/moved", want: errs.ErrValidation, requests: 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			requests := 0
			provider := &forgejo.Provider{
				Env:             envMap(map[string]string{"FORGEJO_TOKEN": "tok"}),
				APIBaseOverride: "https://forgejo.invalid",
				HTTPClient: &http.Client{Transport: releaseAssetTransport(func(req *http.Request) (*http.Response, error) {
					requests++

					response := &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: req}
					if requests == 1 {
						response.StatusCode = http.StatusTemporaryRedirect
						response.Header.Set("Location", tc.location)
					}

					return response, nil
				})},
			}

			err := provider.DeleteTag(t.Context(), "forgejo.invalid/examplescope/myapp:staging-v1.2.3")
			if !errors.Is(err, tc.want) || requests != tc.requests {
				t.Fatalf("DeleteTag = %v after %d requests, want %v after %d", err, requests, tc.want, tc.requests)
			}

			if tc.want == nil {
				return
			}

			// The refusal says where the redirect led, so an operator sees a
			// server URL given as http, not a bare class.
			to := strings.TrimSuffix(tc.location, "/api/v1/moved")
			if !strings.Contains(err.Error(), "from https://forgejo.invalid to "+to) {
				t.Fatalf("DeleteTag = %v, want it to name both authorities", err)
			}
		})
	}
}

// The package API knows a package by owner and name only, so a ref on another
// registry would delete this server's same-named package tag. It is refused
// before any request.
func TestDeleteTag_RefusesAnImageOnAnotherRegistry(t *testing.T) {
	t.Parallel()

	requests := 0
	provider := &forgejo.Provider{
		Env:             envMap(map[string]string{"FORGEJO_TOKEN": "tok"}),
		APIBaseOverride: "https://forgejo.invalid",
		HTTPClient: inMemoryClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests++

			w.WriteHeader(http.StatusNoContent)
		})),
	}

	for _, ref := range []string{"ghcr.io/examplescope/myapp:staging-v1.2.3", "forgejo.invalid:8443/examplescope/myapp:staging-v1.2.3"} {
		if err := provider.DeleteTag(t.Context(), ref); !errors.Is(err, errs.ErrUsage) {
			t.Errorf("DeleteTag(%q) = %v, want ErrUsage", ref, err)
		}
	}

	if requests != 0 {
		t.Errorf("%d requests sent for a ref on another registry", requests)
	}
}

func TestDeleteTag_RejectsRefWithoutTag(t *testing.T) {
	t.Parallel()

	// A digest-pinned ref (no tag) cannot be untagged.
	err := forgejo.New().DeleteTag(context.Background(), "forge.example/examplescope/myapp@sha256:"+
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if !errors.Is(err, errs.ErrUsage) {
		t.Errorf("ref without a tag should be a usage error, got %v", err)
	}
}
