// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"encoding/pem"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

func TestGitLabJobEvidenceBeforeTeardown(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", root)
	server, ca := labTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.HasSuffix(r.URL.Path, "/jobs"):
			_, _ = io.WriteString(w, `[{"id":7,"status":"success","pipeline":{"id":547}},{"id":8,"status":"failed","pipeline":{"id":548}}]`)
		case strings.HasSuffix(r.URL.Path, "/trace"):
			_, _ = io.WriteString(w, "ACCESS_JOB_EVIDENCE synthetic-signing-password\n"+strings.Repeat("x", jobLogTail+1))
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	original := http.DefaultTransport
	transport := original.(*http.Transport).Clone()
	transport.DialContext = (&dialRecorder{to: server.Listener.Addr().String()}).dial

	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = original }()
	defer transport.CloseIdleConnections()

	target := Target{Forge: provider.ForgeGitLab, Host: labHost, Owner: "fixture", CAFile: "captured", caPEM: ca, accepted: true}

	for _, failed := range []bool{false, true} {
		tb := &evidenceCleanupTB{TB: t}
		tb.Cleanup(func() {
			files, err := filepath.Glob(filepath.Join(root, "job-*.log"))
			if err != nil || len(files) != 2 {
				t.Fatalf("teardown reached without both successful and failed traces: %v", files)
			}

			for _, file := range files {
				body, err := os.ReadFile(file)
				if err != nil || !strings.Contains(string(body), "ACCESS_JOB_EVIDENCE") || strings.Contains(string(body), "synthetic-signing-password") || len(body) <= jobLogTail {
					t.Fatal("full redacted evidence missing before teardown")
				}
			}
		})
		RetainGitLabJobLogsAfter(tb, target, "fci-owned", map[string]string{"password": "synthetic-signing-password"})
		func() {
			defer func() { _ = recover() }()
			defer tb.finish()

			if failed {
				panic("scenario failed before assertions completed")
			}
		}()
	}
}

type evidenceCleanupTB struct {
	TB

	callbacks []func()
}

func (tb *evidenceCleanupTB) Cleanup(callback func()) { tb.callbacks = append(tb.callbacks, callback) }
func (tb *evidenceCleanupTB) finish() {
	for _, v := range slices.Backward(tb.callbacks) {
		v()
	}
}

func TestJobLogEvidenceFullBoundedRedacted(t *testing.T) {
	root := t.TempDir()
	target := Target{CredentialUsername: "fixture-user", Token: "target/credential+synthetic"}
	secrets := map[string]string{"PRIVATE_KEY": "synthetic private key\nsecond line", "PASSWORD": "synthetic/password+value"}

	var planted []string

	planted = append(planted, SecretForms(target.CredentialUsername, target.Token)...)
	for _, secret := range secrets {
		planted = append(planted, SecretForms(target.CredentialUsername, secret)...)
		for line := range strings.SplitSeq(secret, "\n") {
			if len(line) >= 16 {
				planted = append(planted, line)
			}
		}
	}

	log := "Failed to execute goal synthetic:plugin:1:check\n" + strings.Join(planted, "\n") + "\n"
	log += strings.Repeat("x", maxJobLog-len(log))
	redacted := redactJobLog(target, log, secrets)
	// Shortening the planted secrets leaves the redacted body below the cap.
	location, err := retainJobLog(root, "owner/repository", RunJob{RunID: 262, ID: 42, Attempt: 1}, redacted)
	if err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(location)
	if err != nil {
		t.Fatal(err)
	}

	if string(body) != redacted || !strings.HasPrefix(string(body), "Failed to execute goal") || len(body) <= jobLogTail {
		t.Fatal("full diagnostic was not retained")
	}

	for _, secret := range planted {
		if strings.Contains(string(body), secret) {
			t.Fatal("retained evidence contains a secret form")
		}
	}

	info, err := os.Stat(location)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("evidence is not private")
	}

	if _, err := retainJobLog(root, "owner/repository", RunJob{}, strings.Repeat("x", maxJobLog+1)); err == nil {
		t.Fatal("accepted oversized log")
	}

	location, err = retainJobLog(root, "../../untrusted/repository", RunJob{ID: 43}, strings.Repeat("x", maxJobLog))
	if err != nil {
		t.Fatal(err)
	}

	info, err = os.Stat(location)
	if err != nil || info.Size() != maxJobLog || filepath.Dir(location) != root {
		t.Fatal("exact-limit log was lost or escaped the evidence root")
	}
}

func TestJobLogEvidenceRedactsOverlappingAndTimestampedSecrets(t *testing.T) {
	// Construct an inert PEM-shaped fixture; no cryptographic key is stored.
	key := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("synthetic-key-payload-line-one\nsynthetic-key-payload-line-two")}))
	lines := strings.Split(strings.TrimSpace(key), "\n")
	secrets := map[string]string{"key": key, "short": "synthetic-password", "long": "synthetic-password-extra"}
	log := "timestamp " + lines[1] + "\ntimestamp " + lines[2] + "\nsynthetic-password-extra"

	redacted := redactJobLog(Target{}, log, secrets)
	if strings.Contains(redacted, lines[1]) || strings.Contains(redacted, lines[2]) || strings.Contains(redacted, "password") || strings.Contains(redacted, "-extra") {
		t.Fatal("partial secret survived redaction")
	}
}

func TestJobLogEvidenceDestinationRefusals(t *testing.T) {
	parent := testfs.NewReal(t).Root

	consumer := filepath.Join(parent, "consumer")
	if err := os.Mkdir(consumer, 0700); err != nil {
		t.Fatal(err)
	}

	t.Setenv("FORGE_CI_LIVE_CONSUMER", consumer)

	for _, root := range []string{"relative", consumer, filepath.Join(consumer, "new", "evidence")} {
		if _, err := retainJobLog(root, "owner/repository", RunJob{}, "log"); err == nil {
			t.Fatal("accepted non-external destination")
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := retainJobLog(filepath.Join(cwd, "evidence"), "owner/repository", RunJob{}, "log"); err == nil {
		t.Fatal("accepted engine checkout destination")
	}

	link := filepath.Join(parent, "link")
	if err := os.Symlink(consumer, link); err != nil {
		t.Fatal(err)
	}

	if _, err := retainJobLog(filepath.Join(link, "escape"), "owner/repository", RunJob{}, "log"); err == nil {
		t.Fatal("followed evidence ancestor symlink")
	}

	if _, err := os.Stat(filepath.Join(consumer, "escape")); !os.IsNotExist(err) {
		t.Fatal("created evidence through rejected symlink")
	}
}

func TestJobLogEvidenceEnvironment(t *testing.T) {
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE", "/fallback")
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", "")

	if snapshotEvidenceRoot() != "/fallback" {
		t.Fatal("lost legacy evidence environment")
	}

	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", "/preferred")

	if snapshotEvidenceRoot() != "/preferred" {
		t.Fatal("preferred evidence environment was not selected")
	}
}

func TestJobLogEvidenceFetchFailureFailsAcceptance(t *testing.T) {
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", t.TempDir())
	server, ca := labTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/jobs") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"id":7,"status":"success","pipeline":{"id":547}}]`)
			return
		}

		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "synthetic-secret-response-body")
	}))
	original := http.DefaultTransport
	transport := original.(*http.Transport).Clone()
	transport.DialContext = (&dialRecorder{to: server.Listener.Addr().String()}).dial
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = original }()
	defer transport.CloseIdleConnections()

	for _, forge := range []provider.ForgeAPI{provider.ForgeForgejo, provider.ForgeGitLab} {
		t.Run(string(forge), func(t *testing.T) {
			target := Target{Forge: forge, Host: labHost, Owner: "fixture", CAFile: "captured", caPEM: ca, accepted: true}
			recorder := &recordingTB{}
			if forge == provider.ForgeGitLab {
				tb := &evidenceCleanupTB{TB: recorder}
				RetainGitLabJobLogsAfter(tb, target, "fci-owned")
				tb.finish()
			} else if tail := RetainJobLog(recorder, target, "fci-owned", RunJob{ID: 7}); tail != "" {
				t.Fatal("failed log fetch returned a tail")
			}

			if len(recorder.errors) != 1 || !strings.Contains(recorder.errors[0], "no full evidence retained") {
				t.Fatalf("missing evidence did not fail acceptance: %+v", recorder)
			}
			if strings.Contains(strings.Join(append(recorder.logs, recorder.errors...), "\n"), "synthetic-secret-response-body") {
				t.Fatal("log fetch failure exposed the response body")
			}
		})
	}
}

func TestJobLogEvidenceRedactedBeforePersistenceAndTail(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", root)
	const secret = "synthetic-boundary-secret"
	// The secret crosses the console tail boundary, and the early marker must
	// survive in durable evidence even though the console tail drops it.
	log := "full-evidence-marker\n" + secret + strings.Repeat("x", jobLogTail-len(secret)/2)
	tail := retainJobLogText(t, Target{}, "owner/repository", RunJob{ID: 7}, log, map[string]string{"token": secret})
	files, err := filepath.Glob(filepath.Join(root, "job-*.log"))
	if err != nil || len(files) != 1 {
		t.Fatalf("missing persisted log: %v, %v", files, err)
	}

	body, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), secret) || !strings.Contains(string(body), redactedTargetSecret) || !strings.HasPrefix(string(body), "full-evidence-marker\n") {
		t.Fatal("full log was truncated or persisted before redaction")
	}
	if len(tail) > jobLogTail || strings.Contains(tail, "boundary-secret") || strings.Contains(tail, "full-evidence-marker") {
		t.Fatal("tail was formed before redaction or was not bounded")
	}
}
