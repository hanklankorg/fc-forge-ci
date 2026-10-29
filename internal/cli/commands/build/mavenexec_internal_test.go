// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
)

func TestMavenExecForwardsArgumentsDirectoryAndScopedEnvironment(t *testing.T) {
	mock := mockbinary.New(t)
	mock.OnPath()
	mock.Add("mvn", `test "$MAVEN_OPTS" = '-Xmx256m -Dfile.encoding=UTF-8'
test -z "${RELEASE_GPG_PRIVATE_KEY-}"
test -z "${MAVEN_CENTRAL_PASSWORD-}"
test "$DEPENDENCY_TOKEN" = read-only-token
test "$DEPENDENCY_USERNAME" = reader
`)

	for key, value := range map[string]string{
		"SSL_CERT_FILE": "", "CI_SERVER_TLS_CA_FILE": "", "PRIVATE_DEPENDENCIES": "false",
		"MAVEN_OPTS": "-Xmx256m -Dfile.encoding=UTF-8", "RELEASE_GPG_PRIVATE_KEY": "synthetic-private",
		"MAVEN_CENTRAL_PASSWORD": "synthetic-publication", "DEPENDENCY_TOKEN": "read-only-token",
	} {
		t.Setenv(key, value)
	}

	dir := t.TempDir()
	args := []string{"--batch-mode", "-Dmaven.repo.local=/fixture/fresh repo", "dependency:get", "-Dartifact=org.example:library:1-SNAPSHOT:jar", "-Dliteral=$(not-evaluated)"}
	require.NoError(t, mavenCmd().Run(t.Context(), append([]string{"maven", "exec", "--private-dependencies", "--dependency-username", "reader", "--working-dir", dir, "--"}, args...)))

	calls := mock.Invocations("mvn")
	require.Len(t, calls, 1)
	require.Equal(t, args, calls[0].Args)
	require.Equal(t, dir, calls[0].Cwd)
}

func TestMavenExecTrustAdmissionAndUsage(t *testing.T) {
	mock := mockbinary.New(t)
	mock.OnPath()
	mock.Add("mvn", "exit 99")
	t.Setenv("PRIVATE_DEPENDENCIES", "false")
	t.Setenv("CI_SERVER_TLS_CA_FILE", "/nonexistent/public-ca")

	for _, property := range []string{"trustStore=/private", "trustStorePassword=private", "trustStoreType=PKCS12"} {
		err := mavenCmd().Run(t.Context(), []string{"maven", "exec", "--", "dependency:get", "-Djavax.net.ssl." + property})
		require.ErrorContains(t, err, "caller-selected JVM truststore")
	}

	err := mavenCmd().Run(t.Context(), []string{"maven", "exec", "--", "dependency:get"})
	require.ErrorContains(t, err, "read Maven public CA")
	require.Empty(t, mock.Invocations("mvn"))

	err = mavenCmd().Run(t.Context(), []string{"maven", "exec"})
	require.ErrorIs(t, err, errs.ErrUsage)
}

func TestMavenExecWithoutPrivateAccessScrubsDependencyToken(t *testing.T) {
	mock := mockbinary.New(t)
	mock.OnPath()
	mock.Add("mvn", `test -z "${DEPENDENCY_TOKEN-}"`)

	for _, key := range []string{"SSL_CERT_FILE", "CI_SERVER_TLS_CA_FILE", "PRIVATE_DEPENDENCIES"} {
		t.Setenv(key, "")
	}

	t.Setenv("DEPENDENCY_TOKEN", "must-not-reach-project")
	require.NoError(t, mavenCmd().Run(t.Context(), []string{"maven", "exec", "--", "validate"}))
	require.Len(t, mock.Invocations("mvn"), 1)
	// The wrapper does not consume or replace the caller's JVM options.
	require.NotContains(t, os.Getenv("MAVEN_OPTS"), "forge-ci-maven-trust-")
}
