// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// The one credential from outside the lab a consumer trial may carry: a token
// for an external image registry, such as GHCR, where a consumer publishes
// its images while its release stays on the lab's forge. It is read from a
// file the caller owns, never from a setting, and reaches the run only as the
// consumer's registry secret.
const (
	externalRegistryTokenFileEnv = "FORGE_CI_LIVE_EXTERNAL_REGISTRY_TOKEN_FILE" //nolint:gosec // G101: env var names, not values.
	externalRegistryUserEnv      = "FORGE_CI_LIVE_EXTERNAL_REGISTRY_USER"
)

// ExternalRegistry is the user and token an external image registry takes.
type ExternalRegistry struct {
	User  string
	Token string
}

// ExternalRegistryCredential reads the external registry's credential when
// FORGE_CI_LIVE_EXTERNAL_REGISTRY_TOKEN_FILE names one. The file must be a regular
// file only its owner can read or write, and FORGE_CI_LIVE_EXTERNAL_REGISTRY_USER
// must name the user; anything else stops the trial before it creates a
// resource.
func ExternalRegistryCredential(tb TB) (ExternalRegistry, bool) {
	tb.Helper()

	path := os.Getenv(externalRegistryTokenFileEnv)
	if path == "" {
		return ExternalRegistry{}, false
	}

	credential, err := readExternalRegistry(path, os.Getenv(externalRegistryUserEnv))
	if err != nil {
		tb.Fatalf("livetest: %s: %v", externalRegistryTokenFileEnv, err)
	}

	return credential, true
}

func readExternalRegistry(path, user string) (ExternalRegistry, error) {
	if strings.TrimSpace(user) == "" {
		return ExternalRegistry{}, fmt.Errorf("%s must name the registry user: %w", externalRegistryUserEnv, errs.ErrUsage)
	}

	info, err := os.Lstat(path) //nolint:gosec // G703: the caller's own token file, named by the caller.
	if err != nil {
		return ExternalRegistry{}, fmt.Errorf("read the token file: %w", err)
	}

	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return ExternalRegistry{}, fmt.Errorf("%s must be a regular file readable by its owner only (0600), not %s: %w", path, info.Mode(), errs.ErrUsage)
	}

	raw, err := os.ReadFile(path) //nolint:gosec // the caller's own token file, checked above.
	if err != nil {
		return ExternalRegistry{}, fmt.Errorf("read the token file: %w", err)
	}

	token := strings.TrimSpace(string(raw))
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return ExternalRegistry{}, fmt.Errorf("%s must hold one token on one line: %w", path, errs.ErrUsage)
	}

	return ExternalRegistry{User: strings.TrimSpace(user), Token: token}, nil
}

// VerifyExternalImage verifies image, a tagged reference on the external
// registry, against publicKey with Cosign. The signatures stay out of the
// public log, as the lab's releases sign. Cosign runs with only PATH, a
// private home and a Docker config holding this registry's login: the lab's
// containment and trust settings are for the lab's own endpoints.
func VerifyExternalImage(tb TB, registry ExternalRegistry, publicKey, image string) error {
	tb.Helper()

	host, err := externalImageHost(image)
	if err != nil {
		return err
	}

	dir, err := os.MkdirTemp("", "livetest-external-registry-")
	if err != nil {
		tb.Fatalf("livetest: create a registry config directory: %v", err)
	}

	defer func() { _ = os.RemoveAll(dir) }()

	auth := base64.StdEncoding.EncodeToString([]byte(registry.User + ":" + registry.Token))

	config, err := json.Marshal(map[string]any{"auths": map[string]any{host: map[string]string{"auth": auth}}})
	if err != nil {
		return fmt.Errorf("encode the registry config: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "config.json"), config, 0o600); err != nil {
		tb.Fatalf("livetest: write the registry config: %v", err)
	}

	ctx, cancel := context.WithTimeout(Context(tb), timeoutAPI)
	defer cancel()

	cmd := exec.CommandContext(ctx, "cosign", "verify", "--key", publicKey, "--insecure-ignore-tlog=true", image) //nolint:gosec // G204: kit-authored arguments.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "DOCKER_CONFIG=" + dir}

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("cosign verify %s: %w: %s", image, err, strings.ReplaceAll(strings.TrimSpace(string(out)), registry.Token, "[redacted]"))
	}

	return nil
}

// externalImageHost is the registry an image reference lives on, whether it
// names a repository, a tag or a digest: the login is per registry host.
func externalImageHost(image string) (string, error) {
	host, err := container.RegistryHost(container.StripTagOrDigest(image))
	if err != nil {
		return "", fmt.Errorf("image %s: %w", image, err)
	}

	return host, nil
}

// TrialImageReference is the reference an image= setting verifies: the
// setting as written when it carries a tag, else its v<version> tag. A colon
// after the last slash is a tag; one before it is the registry's port.
func TrialImageReference(image, version string) string {
	if strings.Contains(image[strings.LastIndex(image, "/")+1:], ":") {
		return image
	}

	return image + ":v" + version
}
