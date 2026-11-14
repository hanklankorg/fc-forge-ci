// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package cosign_test

import (
	"os"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/cosign"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
)

func TestVerificationDoesNotInheritTrustOrDestinationOverrides(t *testing.T) {
	for _, name := range []string{"SIGSTORE_ROOT_FILE", "TUF_ROOT", "COSIGN_REPOSITORY", "COSIGN_PUBLIC_KEY", "GPG_PRIVATE_KEY"} {
		t.Setenv(name, "untrusted")
	}

	t.Setenv("SSL_CERT_FILE", "/runner/ca.pem")
	t.Setenv("DOCKER_CONFIG", "/runner/auth")

	for _, explicit := range []bool{false, true} {
		for _, verb := range verifyVerbs() {
			t.Run(verb.name+map[bool]string{false: "/ambient", true: "/explicit"}[explicit], func(t *testing.T) {
				bins := mockbinary.New(t)
				bins.Add("cosign", `
[ -z "${SIGSTORE_ROOT_FILE+set}${TUF_ROOT+set}${COSIGN_REPOSITORY+set}${COSIGN_PUBLIC_KEY+set}${GPG_PRIVATE_KEY+set}" ] || exit 31
[ "${SSL_CERT_FILE-}" = /runner/ca.pem ] || exit 32
[ "${DOCKER_CONFIG-}" = /runner/auth ] || exit 33
`)

				adapter := cosign.New()
				adapter.Bin = bins.Path("cosign")

				if explicit {
					adapter.Env = os.Environ()
				}

				if err := verb.call(adapter); err != nil {
					t.Fatalf("verification subprocess environment: %v", err)
				}
			})
		}
	}
}

func TestVerificationKeepsOnlyTheSelectedKeyAuthentication(t *testing.T) {
	for _, name := range []string{"PUBLIC_KEY", "AWS_SESSION_TOKEN", "GOOGLE_APPLICATION_CREDENTIALS", "AZURE_CLIENT_SECRET", "VAULT_TOKEN", "COSIGN_PKCS11_PIN", "SIGSTORE_ROOT_FILE"} {
		t.Setenv(name, "test-value")
	}

	for _, tc := range []struct {
		key, keep string
	}{
		{"env://PUBLIC_KEY", "PUBLIC_KEY"},
		{"awskms:///alias/test", "AWS_SESSION_TOKEN"},
		{"gcpkms://projects/test", "GOOGLE_APPLICATION_CREDENTIALS"},
		{"azurekms://vault/key", "AZURE_CLIENT_SECRET"},
		{"hashivault://transit/keys/test", "VAULT_TOKEN"},
		{"pkcs11:object=test", "COSIGN_PKCS11_PIN"},
	} {
		t.Run(tc.keep, func(t *testing.T) {
			bins := mockbinary.New(t)

			var script strings.Builder

			for _, name := range []string{"PUBLIC_KEY", "AWS_SESSION_TOKEN", "GOOGLE_APPLICATION_CREDENTIALS", "AZURE_CLIENT_SECRET", "VAULT_TOKEN", "COSIGN_PKCS11_PIN", "SIGSTORE_ROOT_FILE"} {
				if name == tc.keep {
					script.WriteString(`[ "${` + name + `-}" = test-value ] || exit 31` + "\n")
				} else {
					script.WriteString(`[ -z "${` + name + `+set}" ] || exit 32` + "\n")
				}
			}

			bins.Add("cosign", strings.TrimSpace(script.String()))

			adapter := &cosign.Adapter{Bin: bins.Path("cosign"), Transparency: domainrelease.TransparencyNone}

			if err := adapter.VerifyBlob(t.Context(), cosign.VerifyBlobInput{
				Artifact: "app.tgz", BundlePath: "app.tgz.bundle", KeyRef: tc.key,
			}, nil); err != nil {
				t.Fatalf("selected authentication: %v", err)
			}
		})
	}
}
