// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"

	"github.com/google/go-github/v91/github"
	"golang.org/x/crypto/nacl/box"
)

func TestGitHubReleaseCredential(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name, contents string
		mode           os.FileMode
		wantOK         bool
	}{
		{"fine-grained", "github_pat_inert-release-role\n", 0o600, true},
		{"classic", "ghp_inert-packages-role", 0o600, false},
		{"empty", "", 0o600, false},
		{"prefix-only", "github_pat_", 0o600, false},
		{"multiline", "github_pat_private\nprivate-sentinel", 0o600, false},
		{"oversize", "github_pat_" + strings.Repeat("x", 4096), 0o600, false},
		{"public", "github_pat_private-sentinel", 0o644, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name)
			if err := os.WriteFile(path, []byte(tc.contents), tc.mode); err != nil {
				t.Fatal(err)
			}

			// CI's private umask must not silently turn the public-file refusal
			// fixture into an admissible private file.
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatal(err)
			}

			token, err := GitHubReleaseToken(path)
			if (err == nil) != tc.wantOK {
				t.Fatalf("admission success=%v, want %v", err == nil, tc.wantOK)
			}

			if tc.wantOK && token != strings.TrimSpace(tc.contents) {
				t.Fatal("release credential was not independently selected")
			}

			if err != nil && (token != "" || strings.Contains(err.Error(), "private-sentinel") || strings.Contains(err.Error(), path)) {
				t.Fatal("private input leaked")
			}
		})
	}

	for _, path := range []string{"", filepath.Join(dir, "missing-private-sentinel"), dir} {
		if token, err := GitHubReleaseToken(path); err == nil || token != "" || strings.Contains(err.Error(), "private-sentinel") {
			t.Fatal("absent or invalid file must refuse without private diagnostics")
		}
	}

	link := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(dir, "fine-grained"), link); err != nil {
		t.Fatal(err)
	}

	if _, err := GitHubReleaseToken(link); err == nil {
		t.Fatal("symlink admitted")
	}
}

func TestGitHubReleaseCredentialUnderPrivateUmask(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	// A child owns the umask change; do not mutate the test process's global
	// umask or depend on the invoking developer/runner's shell configuration.
	cmd := exec.CommandContext(t.Context(), "sh", "-c",
		`umask 077; exec "$1" -test.run '^TestGitHubReleaseCredential$' -test.count=1`,
		"credential-umask-fixture", binary)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("credential admission under private CI umask: %v\n%s", err, output)
	}
}

func TestGitHubReleaseSecretErrorRedaction(t *testing.T) {
	const release = "github_pat_inert-private-release-sentinel"

	public, private, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	puts := 0

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, httpRequest *http.Request) {
		if httpRequest.Method == http.MethodGet {
			_ = json.NewEncoder(writer).Encode(map[string]string{"key_id": "inert", "key": base64.StdEncoding.EncodeToString(public[:])})

			return
		}

		puts++

		if httpRequest.Method != http.MethodPut || httpRequest.URL.Path != "/repos/fixture/fc-scratch/actions/secrets/RELEASE_TOKEN" {
			t.Error("unexpected secret destination")
		}

		var request struct {
			EncryptedValue string `json:"encrypted_value"`
		}
		if err := json.NewDecoder(httpRequest.Body).Decode(&request); err != nil {
			t.Error("invalid secret request")
		}

		sealed, err := base64.StdEncoding.DecodeString(request.EncryptedValue)
		if err != nil {
			t.Error("invalid sealed value")
		}

		plain, ok := box.OpenAnonymous(nil, sealed, public, private)
		if !ok || string(plain) != release {
			t.Error("uploaded credential must be the explicit release role, not the classic session role")
		}

		writer.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(writer).Encode(map[string]string{"message": release})
	}))
	defer server.Close()

	api, err := github.NewClient(github.WithHTTPClient(server.Client()), github.WithURLs(new(server.URL+"/"), nil))
	if err != nil {
		t.Fatal(err)
	}

	tb := &recordingTB{}
	trial := GitHubTrial{tb: tb, api: api, org: "fixture", token: "ghp_inert-management"}
	trial.SetSecret("fc-scratch", "RELEASE_TOKEN", release)

	if puts != 1 || tb.fatal == "" || strings.Contains(tb.fatal, release) || !strings.Contains(tb.fatal, "[redacted]") {
		t.Fatal("secret API errors must preserve refusal without leaking the release credential")
	}
}

func TestGitHubConsumerReleaseCredentialWiring(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(reporoot.Path(t), "test/live/githubconsumer/consumertrial_test.go"))
	if err != nil {
		t.Fatal(err)
	}

	source := string(raw)
	if !strings.Contains(source, `"RELEASE_TOKEN":           releaseToken,`) || strings.Contains(source, "trial.Token()") {
		t.Fatal("RELEASE_TOKEN must use the explicit release credential, never the session token")
	}

	admission := strings.Index(source, "releaseToken, err = livetest.GitHubReleaseToken(")
	if admission < 0 || admission > strings.Index(source, "trial := livetest.OpenGitHubTrial(") {
		t.Fatal("release file must be admitted before any hosted trial")
	}

	if !strings.Contains(source, `(settings.Event == livetest.TrialRelease || settings.Event == livetest.TrialLifecycle) && os.Getenv("FORGE_CI_LIVE_ENGINE_MODE") != "prepare"`) {
		t.Fatal("non-releasing events and SDK-only preparation must not require release authority")
	}
}
