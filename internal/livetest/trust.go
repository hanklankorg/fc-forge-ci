// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"slices"
	"time"

	"codefloe.com/itiquette/forge-lab-contract/labcontract"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/diggsweden/forge-ci/internal/adapters/httpretry"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

const (
	certificatePEMType = "CERTIFICATE"
	envSSLCAFile       = "SSL_CERT_FILE"
	envCurlCABundle    = "CURL_CA_BUNDLE"
	envGitSSLCAInfo    = "GIT_SSL_CAINFO"
	envRequestsBundle  = "REQUESTS_CA_BUNDLE"
	envNodeExtraCA     = "NODE_EXTRA_CA_CERTS"
)

// newLabTransport trusts exactly caPEM, the CA frozen for the run, and
// ignores any ambient proxy. It checks no host itself: every caller wraps it
// in labcontract.DisposableTransport.
func newLabTransport(caPEM []byte) (*http.Transport, error) {
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("default HTTP transport has unexpected type: %w", errs.ErrValidation)
	}

	base := defaultTransport.Clone()
	base.Proxy = nil
	base.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}

	if len(caPEM) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("build target CA pool: %w", errs.ErrValidation)
		}

		base.TLSClientConfig.RootCAs = pool
	}

	return base, nil
}

// CredentialProxy is a loopback-only CONNECT proxy that tunnels only to Forge
// Lab hosts. A child process that holds a credential is pointed at it, so any
// connection it opens to another host (a public Sigstore, Rekor or TUF
// service most of all) is refused rather than made. It never parses or logs
// tunneled bytes.
type CredentialProxy struct {
	dial   func(ctx context.Context, network, address string) (net.Conn, error)
	server *http.Server
	url    string
	done   chan error
}

// StartCredentialProxy starts the proxy on a loopback port.
func StartCredentialProxy() (*CredentialProxy, error) {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for credential containment proxy: %w", err)
	}

	proxy := newCredentialProxy((&net.Dialer{Timeout: 10 * time.Second}).DialContext)
	proxy.serve(listener)

	return proxy, nil
}

func newCredentialProxy(dial func(context.Context, string, string) (net.Conn, error)) *CredentialProxy {
	proxy := &CredentialProxy{dial: dial, done: make(chan error, 1)}
	proxy.server = &http.Server{
		Handler:           proxy,
		ReadHeaderTimeout: 5 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
	}

	return proxy
}

// URL returns the loopback HTTP proxy URL.
func (proxy *CredentialProxy) URL() string { return proxy.url }

// Done reports the terminal serve result.
func (proxy *CredentialProxy) Done() <-chan error { return proxy.done }

// Close stops the listener. Open tunnels end when their client or the process
// does.
func (proxy *CredentialProxy) Close() error { return proxy.server.Close() }

func (proxy *CredentialProxy) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodConnect {
		http.Error(response, "only HTTPS CONNECT is permitted", http.StatusMethodNotAllowed)

		return
	}

	if host, port, err := net.SplitHostPort(request.Host); err != nil || host == "" || port == "" {
		http.Error(response, "invalid CONNECT authority", http.StatusBadRequest)

		return
	}

	if labcontract.Disposable(request.Host) != nil {
		http.Error(response, "CONNECT host is not a Forge Lab host", http.StatusForbidden)

		return
	}

	upstream, err := proxy.dial(request.Context(), "tcp", request.Host)
	if err != nil {
		http.Error(response, "lab host is unavailable", http.StatusBadGateway)

		return
	}

	hijacker, ok := response.(http.Hijacker)
	if !ok {
		_ = upstream.Close()

		http.Error(response, "CONNECT tunnelling is unavailable", http.StatusInternalServerError)

		return
	}

	downstream, buffered, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()

		return
	}

	tunnel(downstream, buffered, upstream)
}

func (proxy *CredentialProxy) serve(listener net.Listener) {
	proxy.url = "http://" + listener.Addr().String()

	go func() {
		serveErr := proxy.server.Serve(listener)
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}

		proxy.done <- serveErr

		close(proxy.done)
	}()
}

// tunnel copies bytes both ways until either side ends, then closes both.
func tunnel(downstream net.Conn, buffered *bufio.ReadWriter, upstream net.Conn) {
	defer func() {
		_ = downstream.Close()
		_ = upstream.Close()
	}()

	if _, err := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}

	if err := buffered.Flush(); err != nil {
		return
	}

	done := make(chan struct{}, 2)

	go func() {
		_, _ = io.Copy(upstream, buffered)

		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(downstream, upstream)

		done <- struct{}{}
	}()

	<-done
}

// targetHTTPClient follows redirects, which release read-back and web URLs
// need, but only to Forge Lab hosts.
func targetHTTPClient(target Target, timeout time.Duration) (*http.Client, error) {
	base, err := targetBaseTransport(target)
	if err != nil {
		return nil, err
	}

	return disposableHTTPClient(base, timeout), nil
}

// disposableHTTPClient retries safe reads through base, with the host check
// outermost: labcontract.DisposableTransport sees every request, each
// redirect hop included, and refuses one off the lab, over plain http or with
// userinfo before a retry or a dial.
func disposableHTTPClient(base *http.Transport, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: labcontract.DisposableTransport(httpretry.NewTransport(httpretry.Config{Inner: base})),
	}
}

// HTTPClient returns a client that trusts the run's frozen lab CA and reaches
// only Forge Lab hosts. It is exported for the independent live oracle
// package.
func HTTPClient(tb TB, target Target, timeout time.Duration) *http.Client {
	tb.Helper()

	client, err := targetHTTPClient(target, timeout)
	if err != nil {
		tb.Fatalf("livetest: configure target HTTP client: %v", err)
	}

	return client
}

func registryRemoteOptions(target Target) ([]remote.Option, error) {
	base, err := targetBaseTransport(target)
	if err != nil {
		return nil, err
	}

	return []remote.Option{
		remote.WithAuth(&authn.Basic{Username: target.CredentialUsername, Password: target.Token}),
		remote.WithTransport(labcontract.DisposableTransport(base)),
	}, nil
}

// targetBaseTransport is the target's frozen-CA transport, without retries or
// the host check: go-containerregistry retries on its own, and each caller
// adds the check.
func targetBaseTransport(target Target) (*http.Transport, error) {
	caPEM, err := loadTargetCAPEM(target)
	if err != nil {
		return nil, err
	}

	return newLabTransport(caPEM)
}

func loadTargetCAPEM(target Target) ([]byte, error) {
	if target.CAFile == "" {
		return nil, nil
	}

	if len(target.caPEM) > 0 {
		return bytes.Clone(target.caPEM), nil
	}

	captured, err := captureTargetCA(target)
	if err != nil {
		return nil, err
	}

	return bytes.Clone(captured.caPEM), nil
}

// captureTargetCA reads the contract's CA once into the target, so every
// client built from it trusts the same bytes for the whole run.
func captureTargetCA(target Target) (Target, error) {
	if target.CAFile == "" {
		return target, nil
	}

	body, err := readCAFile(target.CAFile)
	if err != nil {
		return Target{}, err
	}

	target.caPEM = body

	return target, nil
}

func validateCertificatePEM(body []byte) error {
	rest := bytes.TrimSpace(body)
	certificates := 0

	for len(rest) > 0 {
		end := leadingCertificateBlockEnd(rest)
		if end == 0 {
			return fmt.Errorf("ca_file must contain only PEM CERTIFICATE blocks: %w", errs.ErrValidation)
		}

		block, remaining := pem.Decode(rest[:end])
		if block == nil || block.Type != certificatePEMType || len(block.Headers) != 0 || len(remaining) != 0 {
			return fmt.Errorf("ca_file must contain only PEM CERTIFICATE blocks: %w", errs.ErrValidation)
		}

		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return fmt.Errorf("ca_file contains an invalid certificate: %w: %w", err, errs.ErrValidation)
		}

		certificates++
		rest = bytes.TrimSpace(rest[end:])
	}

	if certificates == 0 {
		return fmt.Errorf("ca_file contains no certificates: %w", errs.ErrValidation)
	}

	return nil
}

// leadingCertificateBlockEnd returns the length of the CERTIFICATE block rest
// starts with, or 0 when it does not start with exactly one BEGIN before its
// END marker. Only that first block is decoded afterwards: pem.Decode skips a
// malformed leading block, even within the slice, so a stray BEGIN line before
// a certificate would otherwise pass.
func leadingCertificateBlockEnd(rest []byte) int {
	const endMarker = "-----END CERTIFICATE-----"

	end := bytes.Index(rest, []byte(endMarker)) + len(endMarker)
	if !bytes.HasPrefix(rest, []byte("-----BEGIN CERTIFICATE-----")) || end < len(endMarker) ||
		bytes.Count(rest[:end], []byte("-----BEGIN ")) != 1 {
		return 0
	}

	return end
}

func targetTrustEnvironment(target Target) map[string]string {
	if target.CAFile == "" {
		return map[string]string{}
	}

	return map[string]string{
		envSSLCAFile:      target.CAFile,
		envCurlCABundle:   target.CAFile,
		envGitSSLCAInfo:   target.CAFile,
		envRequestsBundle: target.CAFile,
		envNodeExtraCA:    target.CAFile,
	}
}

// ToolEnvironment returns a closed, target-scoped environment for a helper that
// can read target credentials. HTTPS is forced through a per-test proxy that
// tunnels only to Forge Lab hosts.
func ToolEnvironment(tb TB, target Target, extra map[string]string) []string {
	tb.Helper()
	requireAccepted(tb, target)

	values := targetTrustEnvironment(target)
	for key, value := range credentialProxyEnvironment(tb) {
		values[key] = value
	}

	for key, value := range runtimeEnvironment(tb) {
		values[key] = value
	}

	for key, value := range extra {
		if isTrustEnvironmentKey(key) {
			tb.Fatalf("livetest: helper environment may not override containment variable %s", key)
		}

		values[key] = value
	}

	environment := make([]string, 0, len(values))
	for key, value := range values {
		environment = append(environment, key+"="+value)
	}

	slices.Sort(environment)

	return environment
}

func credentialProxyEnvironment(tb TB) map[string]string {
	tb.Helper()

	proxy, err := StartCredentialProxy()
	if err != nil {
		tb.Fatalf("livetest: start credential containment proxy: %v", err)
	}

	tb.Cleanup(func() { _ = proxy.Close() })

	return map[string]string{
		"HTTP_PROXY":  proxy.URL(),
		"HTTPS_PROXY": proxy.URL(),
		"http_proxy":  proxy.URL(),
		"https_proxy": proxy.URL(),
		"NO_PROXY":    "",
		"no_proxy":    "",
	}
}

func isTrustEnvironmentKey(key string) bool {
	switch key {
	case envSSLCAFile, envCurlCABundle, envGitSSLCAInfo, envRequestsBundle, envNodeExtraCA,
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy":
		return true
	default:
		return false
	}
}
