// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

const maxInputBytes = 1024 * 1024

const (
	modeIDToken            = "id-token"
	modeFulcioCertificates = "fulcio-certificates"
	// modeRegistryAuth reads a registry auth file and prints the stored
	// credential for one registry, as registries key it with or without a
	// scheme. The job images carry no jq, so this is the one JSON reader
	// the probes bring.
	modeRegistryAuth = "registry-auth"
	// modeRegistryLogin reads the same auth file and proves its credential
	// for one registry with go-containerregistry's OCI client: the /v2/
	// challenge, any token exchange, then an authenticated /v2/. It needs
	// no container runtime, so it works in a job that runs as a non-root
	// user without user namespaces, where podman and buildah cannot start.
	// It honours SSL_CERT_FILE and the proxy variables the probe exports.
	modeRegistryLogin    = "registry-login"
	modeRegistryBoundary = "registry-boundary"
)

var (
	errInvalidProbeJSON = errors.New("invalid probe JSON")
	errRegistryLogin    = errors.New("registry login failed")
	errRegistryBoundary = errors.New("registry authorization boundary failed")
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, http.DefaultTransport)

	stop()

	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func run(ctx context.Context, args []string, input io.Reader, output io.Writer, roundTripper http.RoundTripper) error { //nolint:cyclop // Four bounded modes share input and output enforcement.
	valid := len(args) == 1 && (args[0] == modeIDToken || args[0] == modeFulcioCertificates) ||
		len(args) == 2 && (args[0] == modeRegistryAuth || args[0] == modeRegistryLogin) && args[1] != "" ||
		len(args) == 4 && args[0] == modeRegistryBoundary
	if !valid {
		return fmt.Errorf("%w: usage: probe-json id-token|fulcio-certificates|registry-auth <registry>|registry-login <registry>", errInvalidProbeJSON)
	}

	body, err := io.ReadAll(io.LimitReader(input, maxInputBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxInputBytes {
		return fmt.Errorf("%w: input must be non-empty and at most %d bytes", errInvalidProbeJSON, maxInputBytes)
	}

	var values []string

	switch args[0] {
	case modeIDToken:
		value, decodeErr := decodeIDToken(body)
		if decodeErr != nil {
			return decodeErr
		}

		values = []string{value}
	case modeFulcioCertificates:
		values, err = decodeFulcioCertificates(body)
		if err != nil {
			return err
		}
	case modeRegistryAuth:
		value, decodeErr := decodeRegistryAuth(body, args[1])
		if decodeErr != nil {
			return decodeErr
		}

		values = []string{value}
	case modeRegistryLogin:
		if loginErr := registryLogin(ctx, body, args[1], roundTripper); loginErr != nil {
			return loginErr
		}
	case modeRegistryBoundary:
		if len(args) != 4 {
			return errInvalidProbeJSON
		}

		return registryBoundary(ctx, body, args[1], args[2], args[3], roundTripper)
	}

	for _, value := range values {
		if _, err = fmt.Fprintln(output, value); err != nil {
			return fmt.Errorf("write probe JSON result: %w", err)
		}
	}

	return nil
}

// registryBoundary proves that a job can write its own staging repository but
// cannot overwrite or directly delete an existing publication-project image.
// It never prints credentials or token-service response bodies.
func registryBoundaryReferences(sourceText, destinationText, digest string) (name.Repository, name.Repository, error) {
	source, err := name.NewRepository(sourceText, name.StrictValidation)
	if err != nil {
		return source, name.Repository{}, errInvalidProbeJSON
	}

	destination, err := name.NewRepository(destinationText, name.StrictValidation)
	if err != nil || source.Name() == destination.Name() || source.RegistryStr() != destination.RegistryStr() {
		return source, destination, errInvalidProbeJSON
	}

	if _, err := v1.NewHash(digest); err != nil {
		return source, destination, errInvalidProbeJSON
	}

	return source, destination, nil
}

func registryBoundary(ctx context.Context, body []byte, sourceText, destinationText, digest string, roundTripper http.RoundTripper) error {
	source, destination, err := registryBoundaryReferences(sourceText, destinationText, digest)
	if err != nil {
		return err
	}

	credential, err := storedBasicCredential(body, source.RegistryStr())
	if err != nil {
		return err
	}

	options := []remote.Option{remote.WithContext(ctx), remote.WithTransport(roundTripper), remote.WithAuth(credential)}

	image, err := random.Image(1024, 1)
	if err != nil {
		return err
	}

	if err := remote.Write(source.Tag("staging-ci-boundary"), image, options...); err != nil {
		return fmt.Errorf("%w: job token could not write its own staging repository", errRegistryBoundary)
	}

	if _, err := remote.Head(destination.Digest(digest), options...); err != nil {
		return fmt.Errorf("%w: job token could not read the publication sentinel", errRegistryBoundary)
	}

	if !registryWriteDenied(remote.Write(destination.Tag("v0.0.0-sentinel"), image, options...)) {
		return fmt.Errorf("%w: job token publication push was not denied", errRegistryBoundary)
	}

	if !registryWriteDenied(remote.Delete(destination.Digest(digest), options...)) {
		return fmt.Errorf("%w: job token publication manifest deletion was not denied", errRegistryBoundary)
	}

	return nil
}

func registryWriteDenied(err error) bool {
	var denied *transport.Error

	return errors.As(err, &denied) && (denied.StatusCode == http.StatusUnauthorized || denied.StatusCode == http.StatusForbidden)
}

func decodeIDToken(body []byte) (string, error) {
	var response struct {
		Value string `json:"value"`
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	if decodeErr := decoder.Decode(&response); decodeErr != nil || response.Value == "" {
		return "", fmt.Errorf("%w: response has no id token", errInvalidProbeJSON)
	}

	if eofErr := requireJSONEOF(decoder); eofErr != nil {
		return "", eofErr
	}

	return response.Value, nil
}

func decodeFulcioCertificates(body []byte) ([]string, error) {
	var response struct {
		Chains []struct {
			Certificates []string `json:"certificates"`
		} `json:"chains"`
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	if decodeErr := decoder.Decode(&response); decodeErr != nil || len(response.Chains) == 0 || len(response.Chains[0].Certificates) == 0 {
		return nil, fmt.Errorf("%w: response has no Fulcio certificates", errInvalidProbeJSON)
	}

	if eofErr := requireJSONEOF(decoder); eofErr != nil {
		return nil, eofErr
	}

	for _, certificate := range response.Chains[0].Certificates {
		if certificate == "" {
			return nil, fmt.Errorf("%w: response has an empty Fulcio certificate", errInvalidProbeJSON)
		}
	}

	return response.Chains[0].Certificates, nil
}

func decodeRegistryAuth(body []byte, registry string) (string, error) {
	var file struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	if decodeErr := decoder.Decode(&file); decodeErr != nil {
		return "", fmt.Errorf("%w: auth file does not decode", errInvalidProbeJSON)
	}

	if eofErr := requireJSONEOF(decoder); eofErr != nil {
		return "", eofErr
	}

	for _, key := range []string{registry, "https://" + registry, "http://" + registry} {
		if entry, found := file.Auths[key]; found && entry.Auth != "" {
			return entry.Auth, nil
		}
	}

	return "", fmt.Errorf("%w: auth file stores no credential for the registry", errInvalidProbeJSON)
}

// registryLogin authenticates the auth file's stored credential against the
// registry. Errors name the registry, never the credential.
func registryLogin(ctx context.Context, body []byte, registry string, roundTripper http.RoundTripper) error {
	credential, err := storedBasicCredential(body, registry)
	if err != nil {
		return err
	}

	reference, err := name.NewRegistry(registry, name.StrictValidation)
	if err != nil {
		return fmt.Errorf("%w: %q is not a registry authority", errInvalidProbeJSON, registry)
	}

	authenticated, err := transport.NewWithContext(ctx, reference, credential, roundTripper, nil)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", errRegistryLogin, registry, err)
	}

	// The URL is the validated registry authority the probe was given; the
	// probe's credential proxy confines where the connection may go.
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, reference.Scheme()+"://"+reference.RegistryStr()+"/v2/", http.NoBody) //nolint:gosec // G704: see above.
	if err != nil {
		return fmt.Errorf("%w: %s: %w", errRegistryLogin, registry, err)
	}

	response, err := (&http.Client{Transport: authenticated}).Do(request) //nolint:gosec // G704: see above.
	if err != nil {
		return fmt.Errorf("%w: %s: %w", errRegistryLogin, registry, err)
	}

	_ = response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %s answered /v2/ with %s", errRegistryLogin, registry, response.Status)
	}

	return nil
}

// storedBasicCredential is the auth file's credential for the registry as a
// username and password.
func storedBasicCredential(body []byte, registry string) (*authn.Basic, error) {
	stored, err := decodeRegistryAuth(body, registry)
	if err != nil {
		return nil, err
	}

	decoded, err := base64.StdEncoding.DecodeString(stored)
	username, password, found := strings.Cut(string(decoded), ":")

	if err != nil || !found || username == "" || password == "" {
		return nil, fmt.Errorf("%w: the stored credential is not a basic username:password pair", errInvalidProbeJSON)
	}

	return &authn.Basic{Username: username, Password: password}, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing JSON data", errInvalidProbeJSON)
	}

	return nil
}
