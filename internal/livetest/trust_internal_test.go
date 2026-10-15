// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"codefloe.com/itiquette/forge-lab-contract/labcontract"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func serverCertificatePEM(server *httptest.Server) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
}

func independentCAPEM(t *testing.T) []byte {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "unrelated test CA"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}

	certificate, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate})
}

func TestValidateCertificatePEM_CertificateOnlyGrammar(t *testing.T) {
	t.Parallel()

	certificate := string(independentCAPEM(t))
	for _, tc := range []struct{ name, body, reason string }{
		{name: "single", body: certificate},
		{name: "bundle", body: " \n\t" + certificate + "\n\t" + certificate + " \n"},
		{name: "crlf", body: strings.ReplaceAll(certificate, "\n", "\r\n")},
		{name: "empty", body: " \n", reason: "contains no certificates"},
		{name: "preamble", body: "NOT A CERTIFICATE\n" + certificate, reason: "must contain only PEM CERTIFICATE blocks"},
		{name: "between", body: certificate + "NOT A CERTIFICATE\n" + certificate, reason: "must contain only PEM CERTIFICATE blocks"},
		{name: "trailing", body: certificate + "NOT A CERTIFICATE\n", reason: "must contain only PEM CERTIFICATE blocks"},
		{name: "headers", body: strings.Replace(certificate, "-----BEGIN CERTIFICATE-----\n", "-----BEGIN CERTIFICATE-----\nHeader: value\n\n", 1), reason: "must contain only PEM CERTIFICATE blocks"},
		{name: "wrong_type", body: strings.ReplaceAll(certificate, "CERTIFICATE", "PUBLIC KEY"), reason: "must contain only PEM CERTIFICATE blocks"},
		{name: "malformed_prefix_block", body: "-----BEGIN CERTIFICATE-----\n!\n-----END CERTIFICATE-----\n" + certificate, reason: "must contain only PEM CERTIFICATE blocks"},
		{name: "invalid_der", body: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not DER")})), reason: "contains an invalid certificate"},
		{name: "missing_end_marker", body: strings.TrimSuffix(strings.TrimSpace(certificate), "-----END CERTIFICATE-----"), reason: "must contain only PEM CERTIFICATE blocks"},
		{name: "nested_begin", body: "-----BEGIN CERTIFICATE-----\n" + certificate, reason: "must contain only PEM CERTIFICATE blocks"},
		{name: "end_marker_only", body: certificate + "-----END CERTIFICATE-----\n", reason: "must contain only PEM CERTIFICATE blocks"},
		{name: "valid_then_invalid_der", body: certificate + string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not DER")})), reason: "contains an invalid certificate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCertificatePEM([]byte(tc.body))
			if tc.reason == "" {
				if err != nil {
					t.Fatal(err)
				}

				return
			}

			if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("got %v, want validation refusal %q", err, tc.reason)
			}
		})
	}
}

// labHost and labRegistryHost are names on the compiled disposable list. The
// tests below never resolve them: every dial goes to a loopback test server.
const (
	labHost         = "forgejo.compose.forgelab"
	labRegistryHost = "registry.gitlab.compose.forgelab"
)

// labTLSServer serves handler over TLS with a self-signed certificate for the
// lab names, and returns the server with that certificate as PEM.
func labTLSServer(t *testing.T, handler http.Handler) (*httptest.Server, []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: labHost},
		DNSNames:              []string{labHost, labRegistryHost},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	}
	server.StartTLS()
	t.Cleanup(server.Close)

	return server, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// dialRecorder sends every dial to one loopback server and records the
// addresses it was asked for.
type dialRecorder struct {
	to        string
	mu        sync.Mutex
	addresses []string
}

func (recorder *dialRecorder) dial(ctx context.Context, network, address string) (net.Conn, error) {
	recorder.mu.Lock()
	recorder.addresses = append(recorder.addresses, address)
	recorder.mu.Unlock()

	return (&net.Dialer{}).DialContext(ctx, network, recorder.to)
}

func (recorder *dialRecorder) dialled() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	return slices.Clone(recorder.addresses)
}

// labTransport is the base transport a target with caPEM gets, dialling
// server whatever host a request names.
func labTransport(t *testing.T, caPEM []byte, server *httptest.Server) (*http.Transport, *dialRecorder) {
	t.Helper()

	base, err := newLabTransport(caPEM)
	if err != nil {
		t.Fatal(err)
	}

	recorder := &dialRecorder{to: server.Listener.Addr().String()}
	base.DialContext = recorder.dial

	return base, recorder
}

// TestDisposableTransport_RefusesNonLabHosts sends credential-bearing requests
// through the target client to a lab host that answers with a redirect or a
// registry token realm. A target off the lab, a public Sigstore host included,
// is refused before any dial; a redirect to another lab host is followed, as
// release read-back and web URLs need.
func TestDisposableTransport_RefusesNonLabHosts(t *testing.T) {
	t.Parallel()

	port := ""
	server, caPEM := labTLSServer(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v2/":
			response.Header().Set("WWW-Authenticate", `Bearer realm="https://hostile.example.invalid:`+port+`/token",service="hostile"`)
			response.WriteHeader(http.StatusUnauthorized)
		case "/to-public":
			http.Redirect(response, request, "https://rekor.sigstore.dev/api/v1/log", http.StatusTemporaryRedirect)
		case "/to-loopback":
			http.Redirect(response, request, "https://127.0.0.1:"+port+"/collect", http.StatusTemporaryRedirect)
		case "/to-lab":
			http.Redirect(response, request, "https://"+labRegistryHost+":"+port+"/landed", http.StatusTemporaryRedirect)
		case "/landed":
			response.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("request reached %s", request.URL)
		}
	}))
	_, port, _ = net.SplitHostPort(server.Listener.Addr().String())
	labOrigin := "https://" + labHost + ":" + port

	base, recorder := labTransport(t, caPEM, server)
	client := disposableHTTPClient(base, 5*time.Second)

	send := func(target string) error {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}

		request.Header.Set("Authorization", "Basic fixture-secret")

		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
		}

		return err
	}

	for _, refused := range []string{
		"https://rekor.sigstore.dev/api/v1/log",
		"https://tuf-repo-cdn.sigstore.dev/root.json",
		"https://fulcio.sigstore.dev/api/v2/signingCert",
		"https://example.com/",
		"https://127.0.0.1:" + port + "/",
		"https://user@" + labHost + ":" + port + "/",
		"http://" + labHost + ":" + port + "/",
		labOrigin + "/to-public",
		labOrigin + "/to-loopback",
	} {
		if err := send(refused); !errors.Is(err, labcontract.ErrHost) {
			t.Errorf("GET %s = %v, want a refusal", refused, err)
		}
	}

	if err := send(labOrigin + "/to-lab"); err != nil {
		t.Errorf("a redirect between lab hosts was not followed: %v", err)
	}

	repository, err := name.NewRepository(labHost + ":" + port + "/fixture")
	if err != nil {
		t.Fatal(err)
	}

	_, err = remote.List(repository,
		remote.WithTransport(labcontract.DisposableTransport(base)),
		remote.WithAuth(&authn.Basic{Username: "fixture-user", Password: "fixture-secret"}),
	)
	if !errors.Is(err, labcontract.ErrHost) {
		t.Errorf("a token realm off the lab = %v, want a refusal", err)
	}

	for _, address := range recorder.dialled() {
		if labcontract.Disposable(address) != nil {
			t.Errorf("dialled %s, which is not a lab host", address)
		}
	}
}

// TestCredentialProxy_TunnelsOnlyToLabHosts drives the proxy over a real
// loopback socket: a CONNECT to a lab host is tunnelled to it, and a CONNECT
// to a public Sigstore host or any other host off the lab is refused without a
// dial.
func TestCredentialProxy_TunnelsOnlyToLabHosts(t *testing.T) {
	t.Parallel()

	upstream, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = upstream.Close() }()

	go func() {
		for {
			connection, acceptErr := upstream.Accept()
			if acceptErr != nil {
				return
			}

			go func() {
				defer func() { _ = connection.Close() }()

				_, _ = io.Copy(connection, connection)
			}()
		}
	}()

	recorder := &dialRecorder{to: upstream.Addr().String()}
	proxy := newCredentialProxy(recorder.dial)

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	proxy.serve(listener)
	defer func() { _ = proxy.Close() }()

	connect := func(authority string) (int, net.Conn, *bufio.Reader) {
		connection, dialErr := (&net.Dialer{}).DialContext(t.Context(), "tcp", listener.Addr().String())
		if dialErr != nil {
			t.Fatal(dialErr)
		}

		if _, writeErr := fmt.Fprintf(connection, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", authority, authority); writeErr != nil {
			t.Fatal(writeErr)
		}

		reader := bufio.NewReader(connection)

		response, readErr := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
		if readErr != nil {
			t.Fatal(readErr)
		}

		_ = response.Body.Close()

		return response.StatusCode, connection, reader
	}

	for _, refused := range []string{
		"rekor.sigstore.dev:443",
		"tuf-repo-cdn.sigstore.dev:443",
		"fulcio.sigstore.dev:443",
		"ctfe.sigstore.dev:443",
		"example.com:443",
		"127.0.0.1:443",
	} {
		status, connection, _ := connect(refused)
		_ = connection.Close()

		if status != http.StatusForbidden {
			t.Errorf("CONNECT %s: status %d, want %d", refused, status, http.StatusForbidden)
		}
	}

	status, connection, reader := connect(labHost + ":443")
	defer func() { _ = connection.Close() }()

	if status != http.StatusOK {
		t.Fatalf("CONNECT to a lab host: status %d", status)
	}

	if _, err := connection.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}

	if line, err := reader.ReadString('\n'); err != nil || line != "ping\n" {
		t.Fatalf("tunnel carried %q, %v", line, err)
	}

	if got := recorder.dialled(); !slices.Equal(got, []string{labHost + ":443"}) {
		t.Fatalf("dialled %q, want only the lab host", got)
	}
}

// TestCredentialProxy_RefusesBeforeDialing drives the proxy handler in memory:
// a non-CONNECT method and an authority that is empty, portless or carries
// userinfo are answered without a dial, and no refusal echoes a credential.
func TestCredentialProxy_RefusesBeforeDialing(t *testing.T) {
	t.Parallel()

	proxy := newCredentialProxy(func(_ context.Context, _, address string) (net.Conn, error) {
		t.Errorf("dialled %s for a refused request", address)

		return nil, net.ErrClosed
	})

	for name, tc := range map[string]struct {
		method, host string
		status       int
	}{
		"plain GET":          {method: http.MethodGet, host: labHost + ":443", status: http.StatusMethodNotAllowed},
		"public host":        {method: http.MethodConnect, host: "rekor.sigstore.dev:443", status: http.StatusForbidden},
		"userinfo authority": {method: http.MethodConnect, host: "user:secret@" + labHost + ":443", status: http.StatusBadRequest},
		"userinfo, no colon": {method: http.MethodConnect, host: "secret@" + labHost + ":443", status: http.StatusForbidden},
		"no port":            {method: http.MethodConnect, host: labHost, status: http.StatusBadRequest},
		"empty authority":    {method: http.MethodConnect, host: "", status: http.StatusBadRequest},
	} {
		request := httptest.NewRequestWithContext(t.Context(), tc.method, "https://placeholder.invalid/", nil)
		request.Host = tc.host
		recorder := httptest.NewRecorder()

		proxy.ServeHTTP(recorder, request)

		if recorder.Code != tc.status {
			t.Errorf("%s: status %d, want %d (%s)", name, recorder.Code, tc.status, recorder.Body)
		}

		if strings.Contains(recorder.Body.String(), "secret") {
			t.Errorf("%s: refusal echoes the credential: %s", name, recorder.Body)
		}
	}
}

// TestTargetHTTPClient_UsesSelectedCAInsteadOfAmbientTrust sends through the
// client a target gets, dialling a lab-named TLS server: the CA frozen at
// acceptance is trusted even after the file changes, ambient trust is not,
// and a transient 503 on a read is retried.
func TestTargetHTTPClient_UsesSelectedCAInsteadOfAmbientTrust(t *testing.T) {
	t.Parallel()

	var busy sync.Once

	server, caPEM := labTLSServer(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		status := http.StatusNoContent

		busy.Do(func() {
			response.Header().Set("Retry-After", "0")

			status = http.StatusServiceUnavailable
		})
		response.WriteHeader(status)
	}))
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())

	caFile := filepath.Join(t.TempDir(), "target-ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	target, err := captureTargetCA(Target{Host: labHost + ":" + port, CAFile: caFile})
	if err != nil {
		t.Fatal(err)
	}

	send := func(method string, target Target) (int, error) {
		base, err := targetBaseTransport(target)
		if err != nil {
			return 0, err
		}

		base.DialContext = (&dialRecorder{to: server.Listener.Addr().String()}).dial

		request, err := http.NewRequestWithContext(t.Context(), method, target.BaseURL(), nil)
		if err != nil {
			return 0, err
		}

		response, err := disposableHTTPClient(base, 5*time.Second).Do(request)
		if err != nil {
			return 0, err
		}

		_ = response.Body.Close()

		return response.StatusCode, nil
	}

	// The first answer is a 503, so this also proves reads are retried.
	if status, err := send(http.MethodGet, target); err != nil || status != http.StatusNoContent {
		t.Fatalf("selected CA was not used: status %d, %v", status, err)
	}

	// The CA was captured at acceptance, so a file replaced mid-run neither
	// changes what later clients trust nor makes them fail.
	if err := os.WriteFile(caFile, independentCAPEM(t), 0o600); err != nil {
		t.Fatal(err)
	}

	if status, err := send(http.MethodGet, target); err != nil || status != http.StatusNoContent {
		t.Fatalf("captured CA was not used after the file changed: status %d, %v", status, err)
	}

	// Without the lab CA the server is untrusted: ambient trust is not used.
	// A POST is not retried, so the failure comes back from the one attempt.
	var unknownAuthority x509.UnknownAuthorityError
	if _, err := send(http.MethodPost, Target{Host: target.Host}); !errors.As(err, &unknownAuthority) {
		t.Fatalf("a target without the lab CA = %v, want an unknown-authority failure", err)
	}
}

func TestTrustLabCA_Base64EncodesValidatedCertificates(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()

	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, serverCertificatePEM(server), 0o400); err != nil {
		t.Fatal(err)
	}

	shell := TrustLabCA(Target{CAFile: path, accepted: true})
	if strings.Contains(shell, "BEGIN CERTIFICATE") || strings.Contains(shell, "LAB_CA_PEM") ||
		!strings.Contains(shell, "base64 -d") {
		t.Fatalf("unsafe CA shell rendering:\n%s", shell)
	}
}

func TestTrustLabCA_RejectsShellTextAfterCertificate(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()

	path := filepath.Join(t.TempDir(), "ca.pem")

	body := append(serverCertificatePEM(server), []byte("\nLAB_CA_PEM\ntouch /tmp/exfiltrated\n")...)
	if err := os.WriteFile(path, body, 0o400); err != nil {
		t.Fatal(err)
	}

	defer func() {
		if recover() == nil {
			t.Fatal("TrustLabCA accepted non-certificate shell text")
		}
	}()

	_ = TrustLabCA(Target{CAFile: path, accepted: true})
}

// TestCaptureTargetCA_FreezesTheBytesForTheRun captures the CA once, then
// replaces the file: every later load still returns the captured certificate,
// and mutating a returned copy changes neither the capture nor the next copy.
func TestCaptureTargetCA_FreezesTheBytesForTheRun(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "ca.pem")

	original := independentCAPEM(t)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	captured, err := captureTargetCA(Target{CAFile: path})
	if err != nil {
		t.Fatal(err)
	}

	if writeErr := os.WriteFile(path, independentCAPEM(t), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	first, err := loadTargetCAPEM(captured)
	if err != nil || !bytes.Equal(first, original) {
		t.Fatalf("captured CA changed with the file: err=%v", err)
	}

	for i := range first {
		first[i] = 'X'
	}

	second, err := loadTargetCAPEM(captured)
	if err != nil || !bytes.Equal(second, original) {
		t.Fatalf("a mutated returned copy changed the captured CA: err=%v", err)
	}

	if _, err := captureTargetCA(Target{CAFile: filepath.Join(t.TempDir(), "absent.pem")}); err == nil {
		t.Fatal("an unreadable ca_file was captured")
	}

	if uncaptured, err := captureTargetCA(Target{}); err != nil || uncaptured.caPEM != nil {
		t.Fatalf("a target without a ca_file captured %q, %v", uncaptured.caPEM, err)
	}
}

func TestCLIEnv_UsesContractCANotAmbientCA(t *testing.T) {
	t.Setenv("SSL_CERT_FILE", "/ambient/ca.pem")

	target := Target{Forge: "forgejo", Host: "forgejo.compose.forgelab", Owner: "owner", CAFile: "/contract/ca.pem"}
	environment := cliEnv(t, target, "repo")

	joined := strings.Join(environment, "\n")
	for _, key := range []string{"SSL_CERT_FILE", "CURL_CA_BUNDLE", "GIT_SSL_CAINFO", "REQUESTS_CA_BUNDLE", "NODE_EXTRA_CA_CERTS"} {
		if !strings.Contains(joined, key+"=/contract/ca.pem") {
			t.Errorf("%s does not use the contract CA: %s", key, joined)
		}
	}

	if strings.Contains(joined, "/ambient/ca.pem") {
		t.Fatalf("ambient CA leaked into product environment: %s", joined)
	}
}

func TestCLIEnv_UsesAPrivateHomeNotTheOperators(t *testing.T) {
	operatorHome := t.TempDir()
	t.Setenv("HOME", operatorHome)

	target := Target{Forge: "forgejo", Host: "forgejo.compose.forgelab", Owner: "owner"}

	var home string

	for _, entry := range cliEnv(t, target, "repo") {
		if value, found := strings.CutPrefix(entry, "HOME="); found {
			home = value
		}
	}

	if home == "" || home == operatorHome {
		t.Fatalf("product HOME = %q, want a directory of the test's own, not the operator's %q", home, operatorHome)
	}
}
