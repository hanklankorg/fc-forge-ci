// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// cleanupTB is a recordingTB that keeps its cleanups, to run them as the end
// of the test would.
type cleanupTB struct {
	recordingTB

	cleanups []func()
}

func (tb *cleanupTB) Cleanup(cleanup func()) { tb.cleanups = append(tb.cleanups, cleanup) }

// Only a private consumer's image verification gets the trial's registry
// credential: a directory of its own with the token for ghcr.io, gone when the
// test ends. A public one keeps the environment it was started in, and no
// log line carries the token.
func TestCheckImageGivesOnlyPrivateVerificationTheTrialCredential(t *testing.T) {
	const (
		token   = "ghp_inert-packages-role"
		ambient = "/ambient/docker-config"
	)

	encoded := base64.StdEncoding.EncodeToString([]byte("trial-actor:" + token))
	record := filepath.Join(t.TempDir(), "docker-config")
	tools := t.TempDir()

	// The stand-in for cosign records the directory it was given and refuses,
	// so the trial logs its output.
	script := "#!/bin/sh\nprintf '%s' \"$DOCKER_CONFIG\" > " + record + "\necho stand-in refuses\nexit 1\n"
	if err := os.WriteFile(filepath.Join(tools, "cosign"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DOCKER_CONFIG", ambient)

	for _, private := range []bool{false, true} {
		tb := &cleanupTB{}
		trial := &GitHubTrial{tb: tb, actor: "trial-actor", token: token}
		trial.CheckImage("consumer", "service", "v1.2.3", GitHubEngine{Owner: "Owner"}, CosignTrust{key: "cosign.pub"}, private)

		given, err := os.ReadFile(record)
		if err != nil || tb.fatal != "" {
			t.Fatalf("private=%t: the verification did not run: %v %+v", private, err, tb)
		}

		dir := string(given)

		if !private {
			if dir != ambient {
				t.Fatalf("public verification ran with DOCKER_CONFIG=%q, want the unchanged %q", dir, ambient)
			}

			continue
		}

		info, err := os.Stat(dir)
		if err != nil || dir == ambient || info.Mode().Perm() != 0o700 {
			t.Fatalf("private verification ran with DOCKER_CONFIG=%q (%v, %v), want a 0700 directory of the trial's own", dir, info, err)
		}

		config := filepath.Join(dir, "config.json")

		body, err := os.ReadFile(config)
		if err != nil || string(body) != `{"auths":{"ghcr.io":{"auth":"`+encoded+`"}}}` {
			t.Fatalf("config.json = %q (%v), want the token for ghcr.io alone", body, err)
		}

		if info, err := os.Stat(config); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("config.json mode = %v (%v), want 0600", info, err)
		}

		said := strings.Join(append(append([]string{}, tb.logs...), tb.errors...), "\n")
		if !strings.Contains(said, "stand-in refuses") || strings.Contains(said, token) || strings.Contains(said, encoded) {
			t.Fatalf("the trial must log the refusal and never the token: %q", said)
		}

		for _, cleanup := range tb.cleanups {
			cleanup()
		}

		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("the credential directory %s outlives the test: %v", dir, err)
		}
	}
}

// The snapshot oracle reads a private consumer's images as the context's
// actor and a public consumer's with no credential, the host's Docker
// configuration included: one that names the registry is not sent.
func TestSnapshotRegistryAuthenticatesOnlyAPrivateConsumer(t *testing.T) {
	const token = "ghp_inert-packages-role"

	var sent []string

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v2/" {
			response.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			response.WriteHeader(http.StatusUnauthorized)

			return
		}

		sent = append(sent, request.Header.Get("Authorization"))
		_, _ = response.Write([]byte(`{"name":"owner/consumer","tags":[]}`))
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")

	ambient, err := dockerAuthConfig(host, "host-user", "host-password")
	if err != nil {
		t.Fatal(err)
	}

	config := t.TempDir()
	if err := os.WriteFile(filepath.Join(config, "config.json"), ambient, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DOCKER_CONFIG", config)

	repository, err := name.NewRepository(host + "/owner/consumer")
	if err != nil {
		t.Fatal(err)
	}

	for private, want := range map[bool]string{
		false: "",
		true:  "Basic " + base64.StdEncoding.EncodeToString([]byte("trial-actor:"+token)),
	} {
		trial := &GitHubTrial{tb: t, org: "Owner", actor: "trial-actor", token: token}
		registry := trial.SnapshotRegistry("consumer", private)

		if registry.Image() != "ghcr.io/owner/consumer" {
			t.Fatalf("snapshot image repository = %s", registry.Image())
		}

		// The oracle's own options, pointed at the test's registry.
		registry.repository, sent = repository, nil

		if _, err := remote.List(registry.repository, registry.remote(t.Context())...); err != nil || len(sent) != 1 || sent[0] != want {
			t.Fatalf("private=%t: the registry was sent %q (%v), want %q", private, sent, err, want)
		}
	}
}
