// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package maven

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func publicCATestPEM(t *testing.T) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	template := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestCallerTruststoreArgumentsRefusedBeforePreparation(t *testing.T) {
	for _, property := range []string{"trustStore=/private/store", "trustStorePassword=private", "trustStoreType=PKCS12"} {
		for _, route := range []string{"options-evaluate", "options-run", "arguments-run"} {
			t.Run(property+"/"+route, func(t *testing.T) {
				// An unreadable CA and nonexistent Maven prove conflict admission
				// happens before either trust preparation or process execution.
				a := &Adapter{MvnBin: "/nonexistent/mvn", Env: []string{"CI_SERVER_TLS_CA_FILE=/nonexistent/public-ca"}}
				arg := "-Djavax.net.ssl." + property

				var err error

				switch route {
				case "options-evaluate":
					a.Options = []string{arg}
					_, err = a.EvalExpression(t.Context(), "project.version")
				case "options-run":
					a.Options = []string{arg}
					err = a.RunInheritIn(t.Context(), "", io.Discard, io.Discard, "deploy")
				case "arguments-run":
					err = a.RunInheritIn(t.Context(), "", io.Discard, io.Discard, "deploy", arg)
				}

				if err == nil || !strings.HasPrefix(err.Error(), "explicit CA cannot be combined with a caller-selected JVM truststore:") {
					t.Fatalf("conflict = %v", err)
				}
			})
		}
	}
}

func TestJavaFallbackUsesChildPATH(t *testing.T) {
	parent, child := t.TempDir(), t.TempDir()
	for _, home := range []string{parent, child} {
		for _, dir := range []string{"bin", "lib/security"} {
			if err := os.MkdirAll(filepath.Join(home, dir), 0o700); err != nil {
				t.Fatal(err)
			}
		}

		for name, body := range map[string]string{
			"bin/java":             fmt.Sprintf("#!/bin/sh\nprintf '    java.home = %%s\\n' '%s' >&2\n", home),
			"bin/keytool":          "#!/bin/sh\nexit 0\n",
			"lib/security/cacerts": home,
		} {
			if err := os.WriteFile(filepath.Join(home, name), []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Setenv("PATH", filepath.Join(parent, "bin"))

	ca := filepath.Join(child, "public.pem")
	if err := os.WriteFile(ca, publicCATestPEM(t), 0o600); err != nil {
		t.Fatal(err)
	}

	a := &Adapter{Env: []string{"PATH=" + filepath.Join(child, "bin"), "SSL_CERT_FILE=" + ca}}

	env, cleanup, err := a.trustedEnvironment(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	for _, entry := range env {
		if opts, ok := strings.CutPrefix(entry, "MAVEN_OPTS="); ok {
			store := strings.Fields(strings.Split(opts, "-Djavax.net.ssl.trustStore=")[1])[0]

			got, readErr := os.ReadFile(store)
			if readErr != nil || string(got) != child {
				t.Fatalf("selected roots = %q, %v; want child runtime", got, readErr)
			}

			return
		}
	}

	t.Fatal("missing generated JVM options")
}

func TestPublicCATrustSnapshot(t *testing.T) {
	home := t.TempDir()
	write := func(path, body string, mode os.FileMode) {
		t.Helper()

		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}

	for _, dir := range []string{"bin", "lib/security"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	roots := filepath.Join(home, "lib/security/cacerts")
	write(roots, "original-public-roots", 0o600)
	write(filepath.Join(home, "bin/java"), fmt.Sprintf("#!/bin/sh\nprintf 'java.home = %%s\\n' '%s' >&2\n", home), 0o700)
	// The fake proves keytool receives a copy, not the JVM's installed store.
	write(filepath.Join(home, "bin/keytool"), `#!/bin/sh
set -eu
[ -z "${JAVA_TOOL_OPTIONS:-}" ]
if [ "$1" = -list ]; then exit 0; fi
while [ "$#" -gt 0 ]; do
  case "$1" in -keystore) shift; store="$1";; -file) shift; ca="$1";; esac
  shift
done
[ "$(cat "$store")" = original-public-roots ]
test -s "$ca"
printf '\nimported-ca' >> "$store"
`, 0o700)

	ca := filepath.Join(home, "public.pem")
	write(ca, string(publicCATestPEM(t)), 0o600)
	adapter := &Adapter{Env: []string{"PATH=/usr/bin:/bin", "JAVA_HOME=" + home, "CI_SERVER_TLS_CA_FILE=" + ca, "MAVEN_OPTS=-Xmx256m -Dprivate.option=retained", "JAVA_TOOL_OPTIONS=-Dfile.encoding=UTF-8"}}

	env, cleanup, err := adapter.trustedEnvironment(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	var opts string

	for _, entry := range env {
		if v, ok := strings.CutPrefix(entry, "MAVEN_OPTS="); ok {
			opts = v
		}
	}

	if !strings.HasPrefix(opts, "-Xmx256m -Dprivate.option=retained -Djavax.net.ssl.trustStore=") {
		t.Fatalf("options not retained: %q", opts)
	}

	_, storeOption, _ := strings.Cut(opts, "-Djavax.net.ssl.trustStore=")
	store, _, _ := strings.Cut(storeOption, " ")

	got, err := os.ReadFile(store)
	if err != nil || string(got) != "original-public-roots\nimported-ca" {
		t.Fatalf("snapshot = %q, %v", got, err)
	}

	got, err = os.ReadFile(roots)
	if err != nil || string(got) != "original-public-roots" {
		t.Fatalf("installed roots mutated: %q, %v", got, err)
	}

	cleanup()

	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Fatalf("snapshot retained: %v", err)
	}

	adapter.Env = append(adapter.Env, "JAVA_TOOL_OPTIONS=-Djavax.net.ssl.trustStore=/caller/store")
	if _, _, err := adapter.trustedEnvironment(t.Context(), ""); err == nil {
		t.Fatal("caller truststore overridden")
	}

	write(ca, "not a certificate", 0o600)

	adapter.Env = adapter.Env[:len(adapter.Env)-1]
	if _, _, err := adapter.trustedEnvironment(t.Context(), ""); err == nil {
		t.Fatal("invalid CA admitted")
	}
}

func TestMavenJavaWrapperReportsActualRuntime(t *testing.T) {
	launcher, runtime := t.TempDir(), t.TempDir()
	java := filepath.Join(launcher, "java")

	script := fmt.Sprintf(`#!/bin/sh
set -eu
test "$#" = 2
test "$1" = -XshowSettings:properties
test "$2" = -version
test -z "${JAVA_TOOL_OPTIONS-}"
test -z "${JDK_JAVA_OPTIONS-}"
test -z "${_JAVA_OPTIONS-}"
test -z "${MAVEN_OPTS-}"
test -z "${RELEASE_GPG_PRIVATE_KEY-}"
test -z "${DEPENDENCY_TOKEN-}"
printf '    java.home = %%s\n' '%s' >&2
`, runtime)
	if err := os.WriteFile(java, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"JAVA_TOOL_OPTIONS", "JDK_JAVA_OPTIONS", "_JAVA_OPTIONS", "MAVEN_OPTS", "RELEASE_GPG_PRIVATE_KEY", "DEPENDENCY_TOKEN"} {
		t.Setenv(name, "synthetic-private-option")
	}

	got, err := mavenJavaHome(t.Context(), "", launcher, "")
	if err != nil || got != runtime {
		t.Fatalf("wrapper runtime = %q, %v; want %q", got, err, runtime)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err = mavenJavaHome(ctx, "", launcher, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestInvalidJavaHomeRefusesBeforeTrustWorkspace(t *testing.T) {
	root := t.TempDir()
	ca := filepath.Join(root, "ca.pem")
	java := filepath.Join(root, "java")

	if err := os.WriteFile(ca, publicCATestPEM(t), 0o600); err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()
	t.Setenv("TMPDIR", work)

	for _, report := range []string{"no home", "java.home = relative", "java.home = /one\njava.home = /two", "java.home = /one/../two", "java.home = /"} {
		if err := os.WriteFile(java, []byte("#!/bin/sh\nprintf '%s\\n' '"+report+"' >&2\n"), 0o700); err != nil {
			t.Fatal(err)
		}

		adapter := &Adapter{Env: []string{"PATH=" + root, "SSL_CERT_FILE=" + ca}}
		_, cleanup, err := adapter.trustedEnvironment(t.Context(), "")
		cleanup()

		if err == nil {
			t.Fatalf("accepted runtime settings %q", report)
		}

		entries, readErr := os.ReadDir(work)
		if readErr != nil || len(entries) != 0 {
			t.Fatalf("workspace created on rejected runtime: %v, %v", entries, readErr)
		}
	}
}

func TestMavenJavaDiscoveryCancellationAndOutputBounds(t *testing.T) {
	root := t.TempDir()

	java := filepath.Join(root, "java")
	if err := os.WriteFile(java, []byte("#!/bin/sh\nwhile :; do :; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	if _, err := mavenJavaHome(ctx, "", root, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("running probe cancellation = %v", err)
	}

	script := "#!/bin/sh\nprintf '%s' '" + strings.Repeat("x", 64*1024+1) + "' >&2\n"
	if err := os.WriteFile(java, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := mavenJavaHome(t.Context(), "", root, ""); err == nil || !strings.Contains(err.Error(), "bounds") {
		t.Fatalf("oversized runtime settings = %v", err)
	}
}

func TestPublicCAFileKindsAndSplitTrustArguments(t *testing.T) {
	root := t.TempDir()
	if _, err := readMavenCAs(root); err == nil {
		t.Fatal("accepted directory as public CA")
	}

	ca, link := filepath.Join(root, "ca.pem"), filepath.Join(root, "ca-link.pem")
	if err := os.WriteFile(ca, publicCATestPEM(t), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(ca, link); err != nil {
		t.Fatal(err)
	}

	if _, err := readMavenCAs(link); err != nil {
		t.Fatalf("public CA symlink to regular file: %v", err)
	}

	for _, args := range [][]string{{"-D", "javax.net.ssl.trustStore=/private"}, {"--define", "javax.net.ssl.trustStoreType=JKS"}, {"--define=javax.net.ssl.trustStorePassword=private"}} {
		if !callerMavenTruststore(args, nil) {
			t.Fatalf("missed caller truststore: %v", args)
		}
	}
}

func TestNoExplicitCALeavesEnvironmentUntouched(t *testing.T) {
	a := &Adapter{Env: []string{"MAVEN_OPTS=-Djavax.net.ssl.trustStore=/caller/store"}}

	env, cleanup, err := a.trustedEnvironment(t.Context(), "")
	defer cleanup()

	if err != nil || len(env) != 1 || env[0] != a.Env[0] {
		t.Fatalf("environment = %v, %v", env, err)
	}
}
