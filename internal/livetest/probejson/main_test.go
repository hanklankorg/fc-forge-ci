// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunExtractsBoundedProbeFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode string
		body string
		want string
	}{
		{name: "id token", mode: "id-token", body: `{"value":"fixture-token","extra":true}`, want: "fixture-token\n"},
		{name: "Fulcio certificates", mode: "fulcio-certificates", body: `{"chains":[{"certificates":["cert-one","cert-two"]}]}`, want: "cert-one\ncert-two\n"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := run(t.Context(), []string{testCase.mode}, strings.NewReader(testCase.body), &output, nil); err != nil {
				t.Fatal(err)
			}

			if output.String() != testCase.want {
				t.Fatalf("output = %q, want %q", output.String(), testCase.want)
			}
		})
	}
}

func TestRunRejectsMalformedOrAmbiguousProbeJSON(t *testing.T) {
	t.Parallel()

	for _, body := range []string{"", `{}`, `{"value":"token"} {}`} {
		if err := run(t.Context(), []string{"id-token"}, strings.NewReader(body), &bytes.Buffer{}, nil); err == nil {
			t.Errorf("accepted %q", body)
		}
	}
}

func TestRunInputByteLimit(t *testing.T) {
	t.Parallel()

	const limit = 1024 * 1024

	for _, tc := range []struct{ mode, body, want string }{
		{mode: "id-token", body: `{"value":"fixture-token"}`, want: "fixture-token\n"},
		{mode: "fulcio-certificates", body: `{"chains":[{"certificates":["cert-one"]}]}`, want: "cert-one\n"},
	} {
		for _, extra := range []int{0, 1, 17} {
			t.Run(fmt.Sprintf("%s/extra=%d", tc.mode, extra), func(t *testing.T) {
				// A finite input keeps even a removed-limit regression bounded.
				body := tc.body + strings.Repeat(" ", limit-len(tc.body)+extra)
				input := &countingReader{Reader: strings.NewReader(body)}
				output := bytes.NewBufferString("unchanged\n")

				err := run(t.Context(), []string{tc.mode}, input, output, nil)
				if extra == 0 {
					if err != nil || output.String() != "unchanged\n"+tc.want || input.bytes != limit {
						t.Fatalf("at cap: err=%v output=%q read=%d", err, output.String(), input.bytes)
					}

					return
				}

				if !errors.Is(err, errInvalidProbeJSON) || err.Error() != "invalid probe JSON: input must be non-empty and at most 1048576 bytes" {
					t.Fatalf("over cap: got %v, want size refusal", err)
				}

				if output.String() != "unchanged\n" || input.bytes != limit+1 {
					t.Fatalf("over cap: output=%q read=%d, want unchanged output and %d bytes", output.String(), input.bytes, limit+1)
				}
			})
		}
	}
}

type countingReader struct {
	io.Reader
	bytes int
}

func (r *countingReader) Read(body []byte) (int, error) {
	n, err := r.Reader.Read(body)
	r.bytes += n

	return n, err
}

// The registry credential is found under the bare registry authority or a
// scheme-prefixed key, as clients spell it; an auth file without one fails.
func TestRunReadsTheStoredRegistryCredential(t *testing.T) {
	t.Parallel()

	for key, body := range map[string]string{
		"bare":   `{"auths":{"registry.lab:8443":{"auth":"c3RvcmVk"}}}`,
		"scheme": `{"auths":{"https://registry.lab:8443":{"auth":"c3RvcmVk"}}}`,
	} {
		var output bytes.Buffer
		if err := run(t.Context(), []string{"registry-auth", "registry.lab:8443"}, strings.NewReader(body), &output, nil); err != nil || output.String() != "c3RvcmVk\n" {
			t.Fatalf("%s: output %q, err %v", key, output.String(), err)
		}
	}

	for name, body := range map[string]string{
		"other registry": `{"auths":{"elsewhere:443":{"auth":"c3RvcmVk"}}}`,
		"empty auth":     `{"auths":{"registry.lab:8443":{"auth":""}}}`,
		"trailing data":  `{"auths":{}} {}`,
	} {
		if err := run(t.Context(), []string{"registry-auth", "registry.lab:8443"}, strings.NewReader(body), io.Discard, nil); !errors.Is(err, errInvalidProbeJSON) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}

	if err := run(t.Context(), []string{"registry-auth"}, strings.NewReader("{}"), io.Discard, nil); !errors.Is(err, errInvalidProbeJSON) {
		t.Fatalf("missing registry: err = %v", err)
	}
}

// registryDouble answers the OCI /v2/ challenge the way the lab forges do:
// Bearer with a token endpoint that takes the basic credential, or plain
// Basic. Only user:secret authenticates.
func registryDouble(t *testing.T, bearer bool) string {
	t.Helper()

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if username, password, ok := r.BasicAuth(); !ok || username != "user" || password != "secret" {
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		_, _ = io.WriteString(w, `{"token":"issued"}`)
	})
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		if bearer && r.Header.Get("Authorization") == "Bearer issued" {
			return
		}

		if username, password, ok := r.BasicAuth(); !bearer && ok && username == "user" && password == "secret" {
			return
		}

		if bearer {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+server.URL+`/token",service="double"`)
		} else {
			w.Header().Set("WWW-Authenticate", `Basic realm="double"`)
		}

		w.WriteHeader(http.StatusUnauthorized)
	})

	return strings.TrimPrefix(server.URL, "http://")
}

func authFile(registry, credential string) string {
	return `{"auths":{"` + registry + `":{"auth":"` + base64.StdEncoding.EncodeToString([]byte(credential)) + `"}}}`
}

// The stored credential authenticates through the OCI client against either
// challenge, and a wrong or malformed one fails without printing it.
func TestRunLogsInWithTheStoredRegistryCredential(t *testing.T) {
	t.Parallel()

	for _, bearer := range []bool{true, false} {
		registry := registryDouble(t, bearer)

		var output bytes.Buffer
		if err := run(t.Context(), []string{"registry-login", registry}, strings.NewReader(authFile(registry, "user:secret")), &output, http.DefaultTransport); err != nil || output.Len() != 0 {
			t.Fatalf("bearer=%v: output %q, err %v", bearer, output.String(), err)
		}

		err := run(t.Context(), []string{"registry-login", registry}, strings.NewReader(authFile(registry, "user:wrong")), io.Discard, http.DefaultTransport)
		if !errors.Is(err, errRegistryLogin) || strings.Contains(err.Error(), "wrong") {
			t.Fatalf("bearer=%v: wrong credential err = %v", bearer, err)
		}

		if err := run(t.Context(), []string{"registry-login", registry}, strings.NewReader(authFile(registry, "no-separator")), io.Discard, http.DefaultTransport); !errors.Is(err, errInvalidProbeJSON) {
			t.Fatalf("bearer=%v: malformed credential err = %v", bearer, err)
		}
	}
}
