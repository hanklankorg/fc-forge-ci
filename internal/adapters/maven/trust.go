// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package maven

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/safeexec"
	"github.com/diggsweden/forge-ci/internal/secretenv"
	"github.com/diggsweden/forge-ci/internal/secrettext"
)

// trustedEnvironment bridges explicit PEM trust to Maven's separate JVM store.
// Nothing is installed into the image, host, or other Java processes.
func (a *Adapter) trustedEnvironment(ctx context.Context, dir string, args ...string) ([]string, func(), error) {
	env := a.Env
	if env == nil {
		env = os.Environ()
	}

	value := func(key string) string {
		for _, entry := range slices.Backward(env) {
			if v, ok := strings.CutPrefix(entry, key+"="); ok {
				return v
			}
		}

		return ""
	}
	noop := func() {}

	ca := value("CI_SERVER_TLS_CA_FILE")
	if ca == "" {
		ca = value("SSL_CERT_FILE")
	}

	if ca == "" {
		return a.Env, noop, nil
	}
	// A caller-selected store must never silently lose its roots or password.
	if callerMavenTruststore(args, env) {
		return nil, noop, fmt.Errorf("explicit CA cannot be combined with a caller-selected JVM truststore: %w", errs.ErrValidation)
	}

	certificates, err := readMavenCAs(ca)
	if err != nil {
		return nil, noop, err
	}

	home, err := mavenJavaHome(ctx, value("JAVA_HOME"), value("PATH"), dir, env...)
	if err != nil {
		return nil, noop, err
	}

	// JSSE prefers jssecacerts when present, then cacerts.
	roots, err := os.ReadFile(filepath.Join(home, "lib", "security", "jssecacerts")) //nolint:gosec // Public roots in the selected Java runtime.
	if os.IsNotExist(err) {
		roots, err = os.ReadFile(filepath.Join(home, "lib", "security", "cacerts")) //nolint:gosec // Public roots in the selected Java runtime.
	}

	if err != nil {
		return nil, noop, fmt.Errorf("read JVM trust roots: %w", err)
	}

	work, err := os.MkdirTemp("", "forge-ci-maven-trust-*")
	if err != nil {
		return nil, noop, err
	}

	cleanup := func() { _ = os.RemoveAll(work) }

	store := filepath.Join(work, "cacerts")
	if strings.ContainsAny(store, " \t\r\n\"'") {
		cleanup()

		return nil, noop, fmt.Errorf("maven truststore requires TMPDIR without whitespace or quotes: %w", errs.ErrValidation)
	}

	if err = os.WriteFile(store, roots, 0o600); err != nil { //nolint:gosec // G703: fixed file in our private MkdirTemp directory, not a caller path.
		cleanup()

		return nil, noop, err
	}

	if err = importMavenCAs(ctx, home, store, work, env, certificates); err != nil {
		cleanup()

		return nil, noop, err
	}

	options := value("MAVEN_OPTS") + " -Djavax.net.ssl.trustStore=" + store + " -Djavax.net.ssl.trustStorePassword=changeit"
	env = safeexec.EnvWithout(env, "MAVEN_OPTS")

	return append(env, "MAVEN_OPTS="+options), cleanup, nil
}

func callerMavenTruststore(args, env []string) bool {
	for index, arg := range args {
		property := strings.TrimPrefix(strings.TrimPrefix(arg, "-D"), "--define=")
		if (arg == "-D" || arg == "--define") && index+1 < len(args) {
			property = args[index+1]
		}

		if strings.HasPrefix(strings.TrimSpace(property), "javax.net.ssl.trustStore") {
			return true
		}
	}

	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		if slices.Contains([]string{"MAVEN_OPTS", "JAVA_TOOL_OPTIONS", "JDK_JAVA_OPTIONS", "_JAVA_OPTIONS", "MAVEN_ARGS"}, name) && strings.Contains(value, "javax.net.ssl.trustStore") {
			return true
		}
	}

	return false
}

func readMavenCAs(path string) ([][]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read Maven public CA: %w", err)
	}

	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("maven public CA must be a regular file: %w", errs.ErrValidation)
	}

	file, err := os.Open(path) //nolint:gosec // Explicit public CA environment contract; bounded PEM-only read below.
	if err != nil {
		return nil, fmt.Errorf("read Maven public CA: %w", err)
	}

	defer func() { _ = file.Close() }()

	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("maven public CA must be a regular file: %w", errs.ErrValidation)
	}

	const maxCABytes = 1024 * 1024

	body, err := io.ReadAll(io.LimitReader(file, maxCABytes+1))
	if err != nil || len(body) > maxCABytes {
		return nil, fmt.Errorf("maven public CA must be readable and no larger than 1 MiB: %w", errs.ErrValidation)
	}

	return parseMavenCAs(body)
}

func parseMavenCAs(body []byte) ([][]byte, error) {
	var certificates [][]byte

	for len(strings.TrimSpace(string(body))) > 0 {
		if !strings.HasPrefix(strings.TrimSpace(string(body)), "-----BEGIN CERTIFICATE-----") {
			return nil, fmt.Errorf("maven public CA must contain only PEM certificates: %w", errs.ErrValidation)
		}

		block, rest := pem.Decode(body)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, fmt.Errorf("maven public CA must contain only PEM certificates: %w", errs.ErrValidation)
		}

		cert, parseErr := x509.ParseCertificate(block.Bytes)
		if parseErr != nil || !cert.BasicConstraintsValid || !cert.IsCA {
			return nil, fmt.Errorf("maven public CA must contain CA certificates: %w", errs.ErrValidation)
		}

		certificates = append(certificates, pem.EncodeToMemory(block))
		body = rest
	}

	if len(certificates) == 0 {
		return nil, fmt.Errorf("maven public CA is empty: %w", errs.ErrValidation)
	}

	return certificates, nil
}

func mavenJavaHome(ctx context.Context, home, path, dir string, environment ...string) (string, error) {
	if home != "" {
		return probeMavenJavaHome(ctx, filepath.Join(home, "bin", "java"), dir, environment)
	}

	// exec.LookPath("java") would use the parent's PATH, not Maven's.
	for _, entry := range filepath.SplitList(path) {
		candidate := filepath.Join(entry, "java")
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(dir, candidate)
		}

		candidate, err := filepath.Abs(candidate)
		if err != nil {
			return "", err
		}

		java, lookupErr := exec.LookPath(candidate)
		if lookupErr != nil {
			continue
		}

		return probeMavenJavaHome(ctx, java, dir, environment)
	}

	return "", safeexec.WrapError(exec.ErrNotFound, "java", "")
}

func probeMavenJavaHome(ctx context.Context, java, dir string, environment []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := safeexec.Command(ctx, java, "-XshowSettings:properties", "-version")
	// Keep the selected wrapper's HOME/XDG/Mise context exactly as admitted by
	// the caller. Discovery must not add trust or drop runtime-selection state.
	cmd.Env = safeexec.EnvWithout(environment, mavenRuntimeOmissions()...)
	cmd.Dir = dir

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}

	if err = cmd.Start(); err != nil {
		return "", safeexec.WrapError(err, "java", "runtime discovery")
	}

	const maxSettingsBytes = 64 * 1024

	settings, readErr := io.ReadAll(io.LimitReader(stderr, maxSettingsBytes+1))
	if readErr != nil || len(settings) > maxSettingsBytes {
		cancel()
	}

	waitErr := cmd.Wait()
	if waitErr != nil {
		wrapped := safeexec.WrapError(waitErr, "java", "runtime discovery")
		if readErr != nil || len(settings) > maxSettingsBytes {
			return "", fmt.Errorf("java runtime settings exceed readable bounds: %w", wrapped)
		}

		if ctx.Err() != nil {
			return "", fmt.Errorf("java runtime discovery interrupted: %w: %w", wrapped, ctx.Err())
		}

		return "", fmt.Errorf("%w\n%s", wrapped, mavenDiscoveryDiagnostic(settings, environment))
	}

	if readErr != nil || len(settings) > maxSettingsBytes {
		return "", fmt.Errorf("java runtime settings exceed readable bounds: %w", errs.ErrValidation)
	}

	if ctx.Err() != nil {
		return "", fmt.Errorf("java runtime discovery interrupted: %w", ctx.Err())
	}

	return parseMavenJavaHome(string(settings))
}

func mavenRuntimeOmissions() []string {
	return secretenv.Names("JAVA_TOOL_OPTIONS", "JDK_JAVA_OPTIONS", "_JAVA_OPTIONS", "MAVEN_OPTS", "MAVEN_ARGS")
}

// Only failed discovery emits the bounded stderr. Redact known credentials and
// option-injection values, preserving nonsecret runtime paths in diagnostics;
// key/JWT detection also covers material printed from wrapper configuration.
func mavenDiscoveryDiagnostic(stderr []byte, environment []string) string {
	values := make([]string, 0, len(environment))
	for _, entry := range environment {
		if name, value, ok := strings.Cut(entry, "="); ok && value != "" && slices.Contains(mavenRuntimeOmissions(), name) {
			values = append(values, value)
		}
	}

	return secrettext.Redact(string(safeexec.RedactKeyMaterial(stderr)), values...)
}

func parseMavenJavaHome(settings string) (string, error) {
	var home string

	for line := range strings.SplitSeq(settings, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.TrimSpace(key) != "java.home" {
			continue
		}

		value = strings.TrimSpace(value)
		if home != "" || !filepath.IsAbs(value) || filepath.Clean(value) != value || value == string(filepath.Separator) || strings.ContainsRune(value, '\x00') {
			return "", fmt.Errorf("java runtime reported an invalid or ambiguous java.home: %w", errs.ErrValidation)
		}

		home = value
	}

	if home == "" {
		return "", fmt.Errorf("java runtime did not report java.home: %w", errs.ErrValidation)
	}

	return home, nil
}

func importMavenCAs(ctx context.Context, home, store, work string, env []string, certificates [][]byte) error {
	// Lab runners supply a bundle with public roots as well as their CA.
	// Import only roots missing from Java, avoiding one JVM launch per public root.
	keytoolEnv := safeexec.EnvWithout(env, mavenRuntimeOmissions()...)
	list := safeexec.Command(ctx, filepath.Join(home, "bin", "keytool"), "-list", "-rfc", "-keystore", store, "-storepass", "changeit")
	list.Env = keytoolEnv

	installed, err := list.Output()
	if err != nil {
		return safeexec.WrapError(err, "keytool", "list")
	}

	known := make(map[string]bool)

	for {
		block, rest := pem.Decode(installed)
		if block == nil {
			break
		}

		if block.Type == "CERTIFICATE" {
			known[string(block.Bytes)] = true
		}

		installed = rest
	}

	for i, cert := range certificates {
		block, _ := pem.Decode(cert)
		if known[string(block.Bytes)] {
			continue
		}

		file := filepath.Join(work, "ca.pem")
		if err = os.WriteFile(file, cert, 0o600); err != nil {
			return err
		}

		if err = importMavenCA(ctx, home, store, file, fmt.Sprintf("forge-ci-public-ca-%d", i), keytoolEnv); err != nil {
			return err
		}

		known[string(block.Bytes)] = true
	}

	return nil
}

func importMavenCA(ctx context.Context, home, store, file, alias string, env []string) error {
	cmd := safeexec.Command(ctx, filepath.Join(home, "bin", "keytool"), "-importcert", "-noprompt", "-alias", alias, "-keystore", store, "-storepass", "changeit", "-file", file)
	cmd.Env = env

	return safeexec.WrapError(cmd.Run(), "keytool", "importcert")
}
