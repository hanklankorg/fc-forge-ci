// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	appsnapshot "github.com/diggsweden/forge-ci/internal/app/snapshotimage"
	buildcmd "github.com/diggsweden/forge-ci/internal/cli/commands/build"
	"github.com/diggsweden/forge-ci/internal/safeexec"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

// Execute the real build command in a child with exactly the producer's env and
// cwd, rather than mutating the test process's HOME or bypassing CLI isolation.
func TestSnapshotMavenBuildCommand(t *testing.T) {
	separator := slices.Index(os.Args, "--snapshot-maven-command")
	if separator < 0 {
		return
	}

	command := &cli.Command{Name: "forge-ci", Commands: []*cli.Command{buildcmd.New()}}
	ctx := t.Context()

	if slices.Contains(os.Args[:separator], "--snapshot-maven-cancel") {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()
	}

	require.NoError(t, command.Run(ctx, append([]string{"forge-ci"}, os.Args[separator+1:]...)))
}

func runtimeCA(t *testing.T) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	require.NoError(t, err)

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestSnapshotMavenRuntimeComposition(t *testing.T) {
	for _, scenario := range []string{"image selectors", "no selectors", "missing data", "missing config", "missing override", "JAVA_HOME precedence", "invalid CA", "cancel discovery"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)

			write := func(name, body string) {
				t.Helper()
				require.NoError(t, os.MkdirAll(filepath.Dir(name), 0o700))
				require.NoError(t, os.WriteFile(name, []byte(body), 0o700))
			}
			write("project/pom.xml", `<project><modelVersion>4.0.0</modelVersion><groupId>example</groupId><artifactId>service</artifactId><version>1.0.0-SNAPSHOT</version></project>`)
			write("project/.mise.toml", "[tools]\njava = 'temurin-25.0.4+7.0.LTS'\nmaven = '3.9.16'\n")
			write("image/config.toml", "[tools]\njava = 'temurin-25.0.3+9.0.LTS'\nmaven = '3.9.16'\n")
			write("ca.pem", string(runtimeCA(t)))

			if scenario == "invalid CA" {
				write("ca.pem", "not a public CA")
			}

			write("runtime/lib/security/cacerts", "image-public-roots")
			// Model only the observed image selector contract, not Mise's parser.
			selection := fmt.Sprintf(`
test -f .mise.toml
test "$HOME" != '%s'
test -d "$HOME"
test -z "${GITHUB_TOKEN-}${COSIGN_KEY-}${DEPENDENCY_TOKEN-}${JAVA_TOOL_OPTIONS-}${JDK_JAVA_OPTIONS-}${_JAVA_OPTIONS-}${MAVEN_ARGS-}${SKIP_TESTS-}${MISE_TRUSTED_CONFIG_PATHS-}${XDG_CONFIG_HOME-}${MISE_GITHUB_TOKEN-}${MISE_CACHE_DIR-}${MISE_STATE_DIR-}"
if [ -z "${JAVA_HOME-}" ]; then
  if [ "${MISE_DATA_DIR-}" != '%s/image' ] || [ "${MISE_CONFIG_DIR-}" != '%s/image' ] || [ "${MISE_OVERRIDE_CONFIG_FILENAMES-}" != '%s/image/config.toml' ]; then
    printf 'image runtime selectors lost; conflicting project runtime is unavailable\n' >&2
    exit 19
  fi
fi
`, root, root, root, root)
			write("shims/java", "#!/bin/sh\nset -eu\n"+selection+fmt.Sprintf("printf 'discovery\\n' >> '%s/calls'\nprintf 'java.home = %s/runtime\\n' >&2\n", root, root))
			write("explicit/bin/java", fmt.Sprintf("#!/bin/sh\nset -eu\ntest -n \"$JAVA_HOME\"\nprintf 'explicit\\n' >> '%s/calls'\nprintf 'java.home = %s/runtime\\n' >&2\n", root, root))
			write("runtime/bin/keytool", fmt.Sprintf(`#!/bin/sh
set -eu
test -z "${JAVA_TOOL_OPTIONS-}${GITHUB_TOKEN-}"
printf 'keytool\n' >> '%s/calls'
if [ "$1" = -list ]; then exit 0; fi
while [ "$#" -gt 0 ]; do
  case "$1" in -keystore) shift; store="$1";; esac
  shift
done
test "$(cat "$store")" = image-public-roots
printf '\nimported-ca' >> "$store"
`, root))
			write("shims/mvn", "#!/bin/sh\nset -eu\n"+selection+fmt.Sprintf(`
store="${MAVEN_OPTS#* -Djavax.net.ssl.trustStore=}"
store="${store%%%% *}"
test "$(cat "$store")" = "$(printf 'image-public-roots\nimported-ca')"
printf '%%s\n' "$store" >> '%s/stores'
printf 'maven\n' >> '%s/calls'
mkdir -p target
printf 'synthetic jar' > target/service.jar
`, root, root))

			if scenario == "cancel discovery" {
				write("shims/java", fmt.Sprintf("#!/bin/sh\nprintf 'started' > '%s/probe-started'\nprintf 'selector resolution blocked\\n' >&2\nexec /bin/sleep 30\n", root))
			}

			in := input(t, "artifacts:\n  - name: service\n    project-type: maven\n    working-directory: project\n    sboms: none\ncontainers:\n  - name: image\n    context: project\n    from: [service]\n    platforms: linux/amd64\n")
			plan, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
			require.NoError(t, err)
			require.NoError(t, os.Mkdir("scratch", 0o700))

			env := []string{"PATH=" + root + "/shims:/usr/bin:/bin", "HOME=" + root, "SSL_CERT_FILE=" + root + "/ca.pem", "TMPDIR=" + root + "/scratch",
				"GITHUB_TOKEN=secret",
				"COSIGN_KEY=secret",
				"DEPENDENCY_TOKEN=secret",
				"JAVA_TOOL_OPTIONS=injection",
				"JDK_JAVA_OPTIONS=injection",
				"_JAVA_OPTIONS=injection",
				"MAVEN_OPTS=injection",
				"MAVEN_ARGS=injection",
				"SKIP_TESTS=true",
				"MISE_TRUSTED_CONFIG_PATHS=/untrusted",
				"XDG_CONFIG_HOME=/untrusted",
				"MISE_GITHUB_TOKEN=secret",
				"MISE_CACHE_DIR=/untrusted",
				"MISE_STATE_DIR=/untrusted"}
			for _, selector := range []struct{ key, value, missing string }{
				{"MISE_DATA_DIR", root + "/image", "missing data"},
				{"MISE_CONFIG_DIR", root + "/image", "missing config"},
				{"MISE_OVERRIDE_CONFIG_FILENAMES", root + "/image/config.toml", "missing override"},
			} {
				if scenario != "no selectors" && scenario != selector.missing {
					env = append(env, selector.key+"="+selector.value)
				}
			}

			if scenario == "JAVA_HOME precedence" {
				env = append(env, "JAVA_HOME="+root+"/explicit")

				write("shims/java", "#!/bin/sh\nexit 99\n")
			}

			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()

			runner := &artifactRunner{run: func(request appsnapshot.ArtifactBuildRequest) error {
				args := []string{"-test.run=^TestSnapshotMavenBuildCommand$", "--"}
				if scenario == "cancel discovery" {
					args = append(args, "--snapshot-maven-cancel")
				}

				args = append(args, "--snapshot-maven-command")
				args = append(args, request.Args...)
				command := safeexec.Command(ctx, os.Args[0], args...)
				command.Dir, command.Env = request.Dir, request.Env

				body, runErr := command.CombinedOutput()
				if runErr != nil {
					return fmt.Errorf("build command: %w: %s", runErr, body)
				}

				return nil
			}}
			err = appsnapshot.BuildArtifact(ctx, &git{head: plan.SourceSHA}, runner, plan, appsnapshot.BuildArtifactInput{Name: "service", Environment: env})
			require.Len(t, runner.requests, 1)

			for _, entry := range runner.requests[0].Env {
				if home, ok := strings.CutPrefix(entry, "HOME="); ok {
					require.NotEqual(t, root, home)
					require.NoDirExists(t, home, "private HOME is removed on success, refusal and cancellation")
				}
			}

			switch scenario {
			case "image selectors", "JAVA_HOME precedence":
				require.NoError(t, err)

				calls, readErr := os.ReadFile("calls")
				require.NoError(t, readErr)
				require.Contains(t, string(calls), "keytool\nmaven\n")

				if scenario == "JAVA_HOME precedence" {
					require.Contains(t, string(calls), "explicit\n")
					require.NotContains(t, string(calls), "discovery")
				}

				stores, readErr := os.ReadFile("stores")
				require.NoError(t, readErr)

				for store := range strings.FieldsSeq(string(stores)) {
					require.NoFileExists(t, store, "trust snapshot removed after Maven")
				}
			case "invalid CA", "cancel discovery":
				require.Error(t, err)
				require.NoFileExists(t, "calls", "no keytool or Maven before runtime admission")

				if scenario == "cancel discovery" {
					require.FileExists(t, "probe-started")
					require.ErrorContains(t, err, "context deadline exceeded")
				}
			default:
				require.ErrorContains(t, err, "image runtime selectors lost")
				require.NoFileExists(t, "calls")
			}

			roots, readErr := os.ReadFile("runtime/lib/security/cacerts")
			require.NoError(t, readErr)
			require.Equal(t, "image-public-roots", string(roots))

			entries, readErr := os.ReadDir("scratch")
			require.NoError(t, readErr)
			require.Empty(t, entries, "no retained trust workspace")
		})
	}
}
