// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	gitlabapi "gitlab.com/gitlab-org/api/client-go"
)

func TestPrepareGitLabTagsSeedsOnlyTheFirstRelease(t *testing.T) {
	t.Parallel()

	var requests []string

	seeded := false

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.Method+" "+request.URL.Path)

		response.Header().Set("Content-Type", "application/json")

		switch request.Method + " " + request.URL.Path {
		case "GET /api/v4/projects/trial/fci-retention/repository/branches/main":
			if !seeded {
				response.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(response, `{"message":"404 Branch Not Found"}`)

				return
			}

			_, _ = io.WriteString(response, `{"name":"main","commit":{"id":"abc"}}`)
		case "POST /api/v4/projects/trial/fci-retention/repository/files/README.md":
			if seeded {
				t.Error("second tag attempted to recreate the existing README")
				response.WriteHeader(http.StatusBadRequest)

				return
			}

			seeded = true

			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"file_path":"README.md","branch":"main"}`)
		case "POST /api/v4/projects/trial/fci-retention/repository/tags":
			var body struct {
				TagName string `json:"tag_name"`
				Ref     string `json:"ref"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Error(err)
			}

			if !seeded || body.Ref != "main" || (body.TagName != "v0.0.1" && body.TagName != "v0.0.2") {
				t.Errorf("tag not based on the seeded branch: %+v", body)
			}

			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"name":"`+body.TagName+`"}`)
		default:
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := gitlabapi.NewClient("inert", gitlabapi.WithBaseURL(server.URL+"/api/v4/"), gitlabapi.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}

	for _, tag := range []string{"v0.0.1", "v0.0.2"} {
		if err := prepareGitLabTag(t.Context(), client, "trial/fci-retention", tag); err != nil {
			t.Fatal(err)
		}
	}

	want := []string{
		"GET /api/v4/projects/trial/fci-retention/repository/branches/main",
		"POST /api/v4/projects/trial/fci-retention/repository/files/README.md",
		"POST /api/v4/projects/trial/fci-retention/repository/tags",
		"GET /api/v4/projects/trial/fci-retention/repository/branches/main",
		"POST /api/v4/projects/trial/fci-retention/repository/tags",
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%v; want %v", requests, want)
	}
}

func TestPrepareGitLabTagStopsOnBranchOrSeedFailure(t *testing.T) {
	t.Parallel()

	for _, scenario := range []struct {
		name        string
		seedFailure bool
	}{{"branch denied", false}, {"seed denied", true}} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()

			var requests []string

			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, r *http.Request) {
				requests = append(requests, r.Method)

				response.Header().Set("Content-Type", "application/json")

				if scenario.seedFailure && r.Method == http.MethodGet {
					response.WriteHeader(http.StatusNotFound)
				} else {
					response.WriteHeader(http.StatusForbidden)
				}

				_, _ = io.WriteString(response, `{"message":"fixture rejection"}`)
			}))
			defer server.Close()

			client, err := gitlabapi.NewClient("inert", gitlabapi.WithBaseURL(server.URL+"/api/v4/"), gitlabapi.WithHTTPClient(server.Client()))
			if err != nil {
				t.Fatal(err)
			}

			if err := prepareGitLabTag(t.Context(), client, "trial/fci-retention", "v0.0.1"); err == nil {
				t.Fatal("tag preparation ignored rejected branch/seed")
			}

			want := []string{"GET"}
			if scenario.seedFailure {
				want = append(want, "POST")
			}

			if !reflect.DeepEqual(requests, want) {
				t.Fatalf("requests=%v; want %v", requests, want)
			}
		})
	}
}

// A teardown's receipt is what a fresh read found. What the teardown owns and
// left behind fails it; a package beside the name is reported and not judged.
func TestScratchAbsenceReceipts(t *testing.T) {
	t.Parallel()

	const repo = "fci-consumer-trial"

	for name, test := range map[string]struct {
		exists   bool
		packages []OwnerPackage
		want     []string
		left     bool
	}{
		"everything is gone": {want: []string{
			"FORGE_CI_LIVE_CLEANUP repository forgeci/fci-consumer-trial absent",
			"FORGE_CI_LIVE_CLEANUP packages forgeci/fci-consumer-trial absent",
			"FORGE_CI_LIVE_CLEANUP packages-beside forgeci/fci-consumer-trial-* absent",
		}},
		"the repository survived": {exists: true, left: true, want: []string{
			"FORGE_CI_LIVE_CLEANUP repository forgeci/fci-consumer-trial present",
			"FORGE_CI_LIVE_CLEANUP packages forgeci/fci-consumer-trial absent",
			"FORGE_CI_LIVE_CLEANUP packages-beside forgeci/fci-consumer-trial-* absent",
		}},
		"its package survived": {
			packages: []OwnerPackage{{Type: "container", Name: repo, Version: "v0.0.1"}, {Type: "npm", Name: "unrelated", Version: "1.0.0"}},
			left:     true,
			want: []string{
				"FORGE_CI_LIVE_CLEANUP repository forgeci/fci-consumer-trial absent",
				"FORGE_CI_LIVE_CLEANUP package forgeci/container:fci-consumer-trial@v0.0.1 present",
				"FORGE_CI_LIVE_CLEANUP packages-beside forgeci/fci-consumer-trial-* absent",
			},
		},
		"a package linked to it survived": {
			packages: []OwnerPackage{{Type: "maven", Name: "org.example:library", Version: "1.0.0", Repository: repo}},
			left:     true,
			want: []string{
				"FORGE_CI_LIVE_CLEANUP repository forgeci/fci-consumer-trial absent",
				"FORGE_CI_LIVE_CLEANUP package forgeci/maven:org.example:library@1.0.0 present",
				"FORGE_CI_LIVE_CLEANUP packages-beside forgeci/fci-consumer-trial-* absent",
			},
		},
		"a package beside the name is another cleanup's": {
			packages: []OwnerPackage{{Type: "container", Name: repo + "-base", Version: "sha256-0"}, {Type: "container", Name: repo + "x", Version: "1"}},
			want: []string{
				"FORGE_CI_LIVE_CLEANUP repository forgeci/fci-consumer-trial absent",
				"FORGE_CI_LIVE_CLEANUP package-beside forgeci/container:fci-consumer-trial-base@sha256-0 present",
				"FORGE_CI_LIVE_CLEANUP packages forgeci/fci-consumer-trial absent",
			},
		},
	} {
		receipts, err := scratchAbsenceReceipts("forgeci", repo, test.exists, test.packages, true)
		if !reflect.DeepEqual(receipts, test.want) || (err != nil) != test.left {
			t.Errorf("%s: receipts are %q and the error %v, want %q and an error: %t", name, receipts, err, test.want, test.left)
		}
	}

	// Packages nobody listed are not reported absent.
	receipts, err := scratchAbsenceReceipts("forgeci", repo, false, nil, false)
	if want := []string{"FORGE_CI_LIVE_CLEANUP repository forgeci/fci-consumer-trial absent"}; !reflect.DeepEqual(receipts, want) || err != nil {
		t.Errorf("unlisted packages: receipts are %q and the error %v, want %q", receipts, err, want)
	}
}
