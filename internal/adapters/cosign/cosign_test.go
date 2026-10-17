// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package cosign_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/cosign"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
)

// refusingAdapter is the adapter for tests whose request must be refused
// before cosign runs. Its cosign is a recording stand-in, and the test fails if
// anything invoked it. These tests used cosign.New(), which runs whatever cosign
// is on PATH and reads the transparency setting from the environment, so a
// check that stopped refusing ran the host's cosign instead of failing -- and
// the errors.Is assertion alone could still pass on the error that run
// produced.
func refusingAdapter(t *testing.T) *cosign.Adapter {
	t.Helper()

	bins := mockbinary.New(t)
	bins.Add("cosign", ":")

	t.Cleanup(func() {
		if invs := bins.Invocations("cosign"); len(invs) != 0 {
			t.Errorf("cosign ran %d time(s) for a request that must be refused first: %+v", len(invs), invs)
		}
	})

	return &cosign.Adapter{Bin: bins.Path("cosign")}
}

func TestSignBlob_KeylessArgvShape(t *testing.T) {
	t.Parallel()

	bins := mockbinary.New(t)
	bins.Add("cosign", ":")

	a := &cosign.Adapter{Bin: bins.Path("cosign")}

	err := a.SignBlob(context.Background(), cosign.SignBlobInput{
		Artifact:   "app.tgz",
		BundlePath: "app.tgz.bundle",
		Keyless:    true,
		OIDCIssuer: "https://token.actions.githubusercontent.com",
	}, nil)
	if err != nil {
		t.Fatalf("SignBlob: %v", err)
	}

	invs := bins.Invocations("cosign")
	if len(invs) != 1 {
		t.Fatalf("expected 1 cosign invocation, got %d", len(invs))
	}

	// The issuer is named in the signing config, not on argv: cosign 3.x
	// deprecated --oidc-issuer and refuses it alongside a config.
	want := []string{
		"sign-blob", "--yes",
		"--bundle", "app.tgz.bundle",
		"--signing-config", invs[0].Args[slices.Index(invs[0].Args, "--signing-config")+1],
		"app.tgz",
	}
	if !slices.Equal(invs[0].Args, want) {
		t.Errorf("argv:\n got=%v\nwant=%v", invs[0].Args, want)
	}
}

func TestCopyImage_ArgvShape(t *testing.T) {
	t.Parallel()

	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	for _, source := range []string{
		"codeberg.org/o/r:staging-v1.2.3",
		"codeberg.org/o/r:staging-v1.2.3@" + digest,
		"codeberg.org/o/r@" + digest,
	} {
		t.Run(source, func(t *testing.T) {
			t.Parallel()

			bins := mockbinary.New(t)
			bins.Add("cosign", ":")
			adapter := &cosign.Adapter{Bin: bins.Path("cosign")}

			err := adapter.CopyImage(t.Context(), cosign.CopyImageInput{Source: source, Dest: "ghcr.io/o/r:release"}, nil)
			if err != nil {
				t.Fatalf("CopyImage: %v", err)
			}

			want := []string{"copy", "--force", source, "ghcr.io/o/r:release"}
			if got := bins.Invocations("cosign")[0].Args; !slices.Equal(got, want) {
				t.Errorf("argv:\n got=%v\nwant=%v", got, want)
			}
		})
	}
}

func TestCopyImage_AuthConfigIsPrivateAndRemovedAfterTheChildExits(t *testing.T) {
	t.Parallel()

	for _, exit := range []int{0, 7} {
		t.Run(strconv.Itoa(exit), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()

			authFile, capture := filepath.Join(dir, "auth.json"), filepath.Join(dir, "used-config")
			if err := os.WriteFile(authFile, []byte(`{"auths":{}}`), 0o600); err != nil {
				t.Fatal(err)
			}

			bins := mockbinary.New(t)
			bins.Add("cosign", fmt.Sprintf(`
set -eu
test "$MARKER" = kept
cmp -s "$DOCKER_CONFIG/config.json" %q
# GNU and BSD stat use different format flags; check the real permission bits.
mode() { stat -c %%a "$1" 2>/dev/null || stat -f %%Lp "$1"; }
test "$(mode "$DOCKER_CONFIG")" = 700
test "$(mode "$DOCKER_CONFIG/config.json")" = 600
printf '%%s' "$DOCKER_CONFIG" > %q
exit %d
`, authFile, capture, exit))

			environment := []string{"PATH=" + os.Getenv("PATH"), "MARKER=kept", "DOCKER_CONFIG=" + dir}
			adapter := &cosign.Adapter{Bin: bins.Path("cosign"), AuthFile: authFile, Env: slices.Clone(environment)}

			err := adapter.CopyImage(t.Context(), cosign.CopyImageInput{Source: "127.0.0.1:1/source:tag", Dest: "127.0.0.1:1/dest:tag"}, nil)
			if (err == nil) != (exit == 0) {
				t.Fatalf("child exit %d: %v", exit, err)
			}

			used, readErr := os.ReadFile(capture)
			if readErr != nil {
				t.Fatal(readErr)
			}

			if _, statErr := os.Stat(string(used)); !os.IsNotExist(statErr) { //nolint:gosec // G703: the inert child records only this test's temporary config path.
				t.Fatalf("private auth config remains after child exit: %v", statErr)
			}

			if !slices.Equal(adapter.Env, environment) {
				t.Fatal("copy changed the adapter's captured environment")
			}
		})
	}
}

func TestCopyImage_RejectsEmptyRefs(t *testing.T) {
	t.Parallel()

	a := refusingAdapter(t)

	if err := a.CopyImage(context.Background(), cosign.CopyImageInput{Dest: "ghcr.io/o/r:release"}, nil); err == nil {
		t.Error("empty source must be rejected")
	}

	if err := a.CopyImage(context.Background(), cosign.CopyImageInput{Source: "codeberg.org/o/r:v1"}, nil); err == nil {
		t.Error("empty dest must be rejected")
	}
}

func TestSignBlob_KeylessNoIssuerOmitsFlag(t *testing.T) {
	t.Parallel()

	bins := mockbinary.New(t)
	bins.Add("cosign", ":")

	a := &cosign.Adapter{Bin: bins.Path("cosign")}

	if err := a.SignBlob(context.Background(), cosign.SignBlobInput{
		Artifact:   "app.tgz",
		BundlePath: "app.tgz.bundle",
		Keyless:    true,
	}, nil); err != nil {
		t.Fatalf("SignBlob: %v", err)
	}

	got := bins.Invocations("cosign")[0].Args
	if slices.Contains(got, "--oidc-issuer") {
		t.Errorf("empty OIDCIssuer must NOT produce --oidc-issuer flag; argv=%v", got)
	}
}

func TestSignBlob_KMSArgvShape(t *testing.T) {
	t.Parallel()

	bins := mockbinary.New(t)
	bins.Add("cosign", ":")

	a := &cosign.Adapter{Bin: bins.Path("cosign")}

	err := a.SignBlob(context.Background(), cosign.SignBlobInput{
		Artifact:   "app.tgz",
		BundlePath: "app.tgz.bundle",
		KeyRef:     "hashivault://transit/keys/release",
	}, nil)
	if err != nil {
		t.Fatalf("SignBlob: %v", err)
	}

	want := []string{
		"sign-blob", "--yes",
		"--bundle", "app.tgz.bundle",
		"--key", "hashivault://transit/keys/release",
		"app.tgz",
	}

	got := bins.Invocations("cosign")[0].Args
	if !slices.Equal(got, want) {
		t.Errorf("argv:\n got=%v\nwant=%v", got, want)
	}
}

func TestSignBlob_RejectsKeylessWithKey(t *testing.T) {
	t.Parallel()

	a := refusingAdapter(t)

	err := a.SignBlob(context.Background(), cosign.SignBlobInput{
		Artifact:   "app.tgz",
		BundlePath: "app.tgz.bundle",
		Keyless:    true,
		KeyRef:     "awskms:///alias/X",
	}, nil)
	if !errors.Is(err, errs.ErrUsage) {
		t.Errorf("keyless + KeyRef should be rejected as ErrUsage, got %v", err)
	}
}

func TestSignBlob_RejectsNonKeylessWithoutKey(t *testing.T) {
	t.Parallel()

	a := refusingAdapter(t)

	err := a.SignBlob(context.Background(), cosign.SignBlobInput{
		Artifact:   "app.tgz",
		BundlePath: "app.tgz.bundle",
	}, nil)
	if !errors.Is(err, errs.ErrUsage) {
		t.Errorf("non-keyless without KeyRef should be rejected as ErrUsage, got %v", err)
	}
}

func TestSignBlob_RejectsIssuerInKMSMode(t *testing.T) {
	t.Parallel()

	a := refusingAdapter(t)

	err := a.SignBlob(context.Background(), cosign.SignBlobInput{
		Artifact:   "app.tgz",
		BundlePath: "app.tgz.bundle",
		OIDCIssuer: "https://example",
		KeyRef:     "awskms:///alias/X",
	}, nil)
	if !errors.Is(err, errs.ErrUsage) {
		t.Errorf("KMS + OIDCIssuer should be rejected as ErrUsage, got %v", err)
	}
}

func TestSignBlob_PropagatesCosignFailure(t *testing.T) {
	t.Parallel()

	bins := mockbinary.New(t)
	bins.Add("cosign", "exit 1")

	a := &cosign.Adapter{Bin: bins.Path("cosign")}

	err := a.SignBlob(context.Background(), cosign.SignBlobInput{
		Artifact:   "app.tgz",
		BundlePath: "app.tgz.bundle",
		Keyless:    true,
	}, nil)
	if err == nil {
		t.Fatal("expected non-nil error when cosign exits 1")
	}

	if !strings.Contains(err.Error(), "cosign") {
		t.Errorf("error must mention cosign for diagnosability; got %v", err)
	}
}

func TestSignBlob_RedactsKeyMaterialFromStderr(t *testing.T) {
	t.Parallel()

	bins := mockbinary.New(t)
	bins.Add("cosign", `cat <<EOF >&2
-----BEGIN OPENSSH PRIVATE KEY-----
NEVER-SHOULD-LEAK
-----END OPENSSH PRIVATE KEY-----
EOF
exit 1`)

	a := &cosign.Adapter{Bin: bins.Path("cosign")}

	var captured bytes.Buffer

	_ = a.SignBlob(context.Background(), cosign.SignBlobInput{
		Artifact:   "app.tgz",
		BundlePath: "app.tgz.bundle",
		Keyless:    true,
	}, &captured)

	if strings.Contains(captured.String(), "NEVER-SHOULD-LEAK") {
		t.Errorf("redactor missed PEM private-key block; captured:\n%s", captured.String())
	}

	if !strings.Contains(captured.String(), "redacted") {
		t.Errorf("redaction notice missing; captured:\n%s", captured.String())
	}
}

func TestVerifyBlob_KeylessArgvShape(t *testing.T) {
	t.Parallel()

	bins := mockbinary.New(t)
	bins.Add("cosign", ":")

	a := &cosign.Adapter{Bin: bins.Path("cosign")}

	err := a.VerifyBlob(context.Background(), cosign.VerifyBlobInput{
		Artifact:           "app.tgz",
		BundlePath:         "app.tgz.bundle",
		Keyless:            true,
		CertIdentityRegexp: "^https://github.com/diggsweden/",
		CertOIDCIssuer:     "https://token.actions.githubusercontent.com",
	}, nil)
	if err != nil {
		t.Fatalf("VerifyBlob: %v", err)
	}

	want := []string{
		"verify-blob",
		"--bundle", "app.tgz.bundle",
		"--new-bundle-format",
		"--certificate-identity-regexp", "^https://github.com/diggsweden/",
		"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com",
		"app.tgz",
	}

	got := bins.Invocations("cosign")[0].Args
	if !slices.Equal(got, want) {
		t.Errorf("verify argv:\n got=%v\nwant=%v", got, want)
	}
}

func TestVerifyBlob_KMSArgvShape(t *testing.T) {
	t.Parallel()

	bins := mockbinary.New(t)
	bins.Add("cosign", ":")

	a := &cosign.Adapter{Bin: bins.Path("cosign")}

	err := a.VerifyBlob(context.Background(), cosign.VerifyBlobInput{
		Artifact:   "app.tgz",
		BundlePath: "app.tgz.bundle",
		KeyRef:     "./release-pubkey.pem",
	}, nil)
	if err != nil {
		t.Fatalf("VerifyBlob: %v", err)
	}

	want := []string{
		"verify-blob",
		"--bundle", "app.tgz.bundle",
		"--new-bundle-format",
		"--key", "./release-pubkey.pem",
		"app.tgz",
	}

	got := bins.Invocations("cosign")[0].Args
	if !slices.Equal(got, want) {
		t.Errorf("verify argv:\n got=%v\nwant=%v", got, want)
	}
}

const testImageDigest = "ghcr.io/diggsweden/app@sha256:1111111111111111111111111111111111111111111111111111111111111111"

func TestSignImage_KeylessArgvShape(t *testing.T) {
	t.Parallel()

	bins := mockbinary.New(t)
	bins.Add("cosign", ":")

	a := &cosign.Adapter{Bin: bins.Path("cosign")}

	err := a.SignImage(context.Background(), cosign.SignImageInput{
		ImageRef:   testImageDigest,
		Recursive:  true,
		Keyless:    true,
		OIDCIssuer: "https://token.actions.githubusercontent.com",
	}, nil)
	if err != nil {
		t.Fatalf("SignImage: %v", err)
	}

	got := bins.Invocations("cosign")[0].Args

	want := []string{
		"sign", "--yes",
		"--signing-config", got[slices.Index(got, "--signing-config")+1],
		"--recursive",
		testImageDigest,
	}

	if !slices.Equal(got, want) {
		t.Errorf("argv:\n got=%v\nwant=%v", got, want)
	}
}

func TestSignImage_KMSArgvShape(t *testing.T) {
	t.Parallel()

	bins := mockbinary.New(t)
	bins.Add("cosign", ":")

	a := &cosign.Adapter{Bin: bins.Path("cosign")}

	err := a.SignImage(context.Background(), cosign.SignImageInput{
		ImageRef:  testImageDigest,
		Recursive: true,
		KeyRef:    "hashivault://transit/keys/release",
	}, nil)
	if err != nil {
		t.Fatalf("SignImage: %v", err)
	}

	want := []string{
		"sign", "--yes", "--recursive",
		"--key", "hashivault://transit/keys/release",
		testImageDigest,
	}

	got := bins.Invocations("cosign")[0].Args
	if !slices.Equal(got, want) {
		t.Errorf("argv:\n got=%v\nwant=%v", got, want)
	}
}

func TestSignImage_NonRecursiveOmitsFlag(t *testing.T) {
	t.Parallel()

	bins := mockbinary.New(t)
	bins.Add("cosign", ":")

	a := &cosign.Adapter{Bin: bins.Path("cosign")}

	if err := a.SignImage(context.Background(), cosign.SignImageInput{
		ImageRef: testImageDigest,
		KeyRef:   "awskms:///alias/X",
	}, nil); err != nil {
		t.Fatalf("SignImage: %v", err)
	}

	got := bins.Invocations("cosign")[0].Args
	if slices.Contains(got, "--recursive") {
		t.Errorf("Recursive=false must NOT add --recursive; argv=%v", got)
	}
}

func TestSignImage_RejectsMutableTag(t *testing.T) {
	t.Parallel()

	a := refusingAdapter(t)

	err := a.SignImage(context.Background(), cosign.SignImageInput{
		ImageRef: "ghcr.io/diggsweden/app:latest", // tag, not digest
		Keyless:  true,
	}, nil)
	if !errors.Is(err, errs.ErrUsage) {
		t.Errorf("mutable tag must be rejected as ErrUsage (cosign refuses to sign mutable tags), got %v", err)
	}
}

func TestSignImage_RejectsKeylessWithKey(t *testing.T) {
	t.Parallel()

	a := refusingAdapter(t)

	err := a.SignImage(context.Background(), cosign.SignImageInput{
		ImageRef: testImageDigest,
		Keyless:  true,
		KeyRef:   "awskms:///alias/X",
	}, nil)
	if !errors.Is(err, errs.ErrUsage) {
		t.Errorf("keyless + KeyRef should be rejected as ErrUsage, got %v", err)
	}
}

func TestSignImage_RejectsNonKeylessWithoutKey(t *testing.T) {
	t.Parallel()

	a := refusingAdapter(t)

	err := a.SignImage(context.Background(), cosign.SignImageInput{
		ImageRef: testImageDigest,
	}, nil)
	if !errors.Is(err, errs.ErrUsage) {
		t.Errorf("non-keyless without KeyRef should be rejected as ErrUsage, got %v", err)
	}
}

func TestVerifyImage_KeylessArgvShape(t *testing.T) {
	t.Parallel()

	bins := mockbinary.New(t)
	bins.Add("cosign", ":")

	a := &cosign.Adapter{Bin: bins.Path("cosign")}

	err := a.VerifyImage(context.Background(), cosign.VerifyImageInput{
		ImageRef:           testImageDigest,
		Keyless:            true,
		CertIdentityRegexp: "^https://github.com/diggsweden/",
		CertOIDCIssuer:     "https://token.actions.githubusercontent.com",
	}, nil)
	if err != nil {
		t.Fatalf("VerifyImage: %v", err)
	}

	want := []string{
		"verify",
		"--certificate-identity-regexp", "^https://github.com/diggsweden/",
		"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com",
		testImageDigest,
	}

	got := bins.Invocations("cosign")[0].Args
	if !slices.Equal(got, want) {
		t.Errorf("verify argv:\n got=%v\nwant=%v", got, want)
	}
}

func TestVerifyImage_KMSArgvShape(t *testing.T) {
	t.Parallel()

	bins := mockbinary.New(t)
	bins.Add("cosign", ":")

	a := &cosign.Adapter{Bin: bins.Path("cosign")}

	err := a.VerifyImage(context.Background(), cosign.VerifyImageInput{
		ImageRef: testImageDigest,
		KeyRef:   "./release-pubkey.pem",
	}, nil)
	if err != nil {
		t.Fatalf("VerifyImage: %v", err)
	}

	want := []string{
		"verify",
		"--key", "./release-pubkey.pem",
		testImageDigest,
	}

	got := bins.Invocations("cosign")[0].Args
	if !slices.Equal(got, want) {
		t.Errorf("verify argv:\n got=%v\nwant=%v", got, want)
	}
}

func TestVerifyImage_RejectsMutableTag(t *testing.T) {
	t.Parallel()

	a := refusingAdapter(t)

	err := a.VerifyImage(context.Background(), cosign.VerifyImageInput{
		ImageRef: "ghcr.io/diggsweden/app:v1.0.0",
		KeyRef:   "./pubkey.pem",
	}, nil)
	if !errors.Is(err, errs.ErrUsage) {
		t.Errorf("mutable tag must be rejected (verify must pin to digest), got %v", err)
	}
}

func TestVerifyBlob_RejectsKeylessWithoutIdentity(t *testing.T) {
	t.Parallel()

	a := refusingAdapter(t)

	err := a.VerifyBlob(context.Background(), cosign.VerifyBlobInput{
		Artifact:   "app.tgz",
		BundlePath: "app.tgz.bundle",
		Keyless:    true,
	}, nil)
	if !errors.Is(err, errs.ErrUsage) {
		t.Errorf("keyless verify without identity constraints must reject as ErrUsage, got %v", err)
	}
}

// A self-hosted Sigstore is reached through the signing config, which is the
// only channel cosign 3.x still listens on.
//
// --fulcio-url and --oidc-issuer are deprecated in cosign 3.x and refused
// alongside a signing config, which this adapter passes whenever the
// transparency log is off. The combination a self-hosted deployment runs cannot
// go through those flags at all, so the argv must not carry them.
func TestSignBlob_KeylessSelfHostedSigstoreArgvShape(t *testing.T) {
	t.Parallel()

	captured := filepath.Join(t.TempDir(), "signing-config.json")

	bins := mockbinary.New(t)
	// The adapter removes the document when the call returns, which is correct
	// and makes it unreadable afterwards -- so the fake cosign keeps a copy
	// while it exists, the same way a real one would read it.
	bins.Add("cosign", `while [ $# -gt 0 ]; do
  if [ "$1" = "--signing-config" ]; then cp "$2" `+captured+`; fi
  shift
done`)

	a := &cosign.Adapter{Bin: bins.Path("cosign")}

	err := a.SignBlob(context.Background(), cosign.SignBlobInput{
		Artifact:   "app.tgz",
		BundlePath: "app.tgz.bundle",
		Keyless:    true,
		OIDCIssuer: "https://gitlab.example.internal",
		FulcioURL:  "https://fulcio.example.internal",
	}, nil)
	if err != nil {
		t.Fatalf("SignBlob: %v", err)
	}

	got := bins.Invocations("cosign")[0].Args

	// The deprecated flags must not appear at all: cosign rejects the run
	// outright when they accompany a config.
	for _, deprecated := range []string{"--fulcio-url", "--rekor-url", "--oidc-issuer"} {
		if slices.Contains(got, deprecated) {
			t.Errorf("argv carries %s, which cosign 3.x refuses alongside --signing-config: %v", deprecated, got)
		}
	}

	if !slices.Contains(got, "--signing-config") {
		t.Fatalf("no --signing-config in argv: %v", got)
	}

	// The document is the assertion, not the flag: a config naming none of the
	// services would satisfy the flag and send the run to public Sigstore.
	document, readErr := os.ReadFile(captured)
	if readErr != nil {
		t.Fatalf("read signing config: %v", readErr)
	}

	for _, want := range []string{"https://fulcio.example.internal", "https://gitlab.example.internal"} {
		if !strings.Contains(string(document), want) {
			t.Errorf("signing config does not name %s:\n%s", want, document)
		}
	}
}

// With every service at its default and the log on, cosign's own configuration
// is correct, so the run must carry no config at all -- passing one could only
// diverge from what cosign would have done.
func TestSignBlob_KeylessWithoutEndpointsOmitsThem(t *testing.T) {
	t.Parallel()

	bins := mockbinary.New(t)
	bins.Add("cosign", ":")

	a := &cosign.Adapter{Bin: bins.Path("cosign"), Transparency: domainrelease.TransparencyPublic}

	if err := a.SignBlob(context.Background(), cosign.SignBlobInput{
		Artifact:   "app.tgz",
		BundlePath: "app.tgz.bundle",
		Keyless:    true,
	}, nil); err != nil {
		t.Fatalf("SignBlob: %v", err)
	}

	got := bins.Invocations("cosign")[0].Args
	for _, flag := range []string{"--fulcio-url", "--rekor-url", "--oidc-issuer", "--signing-config"} {
		if slices.Contains(got, flag) {
			t.Errorf("argv carries %s with nothing configured: %v", flag, got)
		}
	}
}

// A cosign that exits nonzero is a failed verification for every verify entry
// point. Each caller treats a nil error as "verified", so an adapter that
// swallowed the exit status would pass unsigned artifacts.
func TestVerify_EveryVerifySubcommandReportsACosignFailure(t *testing.T) {
	t.Parallel()

	for _, verb := range verifyVerbs() {
		t.Run(verb.name, func(t *testing.T) {
			t.Parallel()

			bins := mockbinary.New(t)
			bins.Add("cosign", "exit 1")

			if err := verb.call(&cosign.Adapter{Bin: bins.Path("cosign")}); err == nil {
				t.Fatalf("%s reported success although cosign exited 1", verb.name)
			}

			if invs := bins.Invocations("cosign"); len(invs) != 1 || invs[0].Args[0] != verb.name {
				t.Errorf("invocations = %+v, want one %s", invs, verb.name)
			}
		})
	}
}

// An env:// key isolates the child for real: the allowed key arrives and an
// unrelated secret does not. The constructor tests read Adapter.Env; this one
// reads what the subprocess was given.
func TestForKeyRef_ChildReceivesOnlyTheIsolatedEnvironment(t *testing.T) {
	// No t.Parallel(): t.Setenv is incompatible with parallel tests.
	t.Setenv("MY_SIGNING_KEY", "k")
	t.Setenv("GPG_PRIVATE_KEY", "leak-me")

	bins := mockbinary.New(t)
	bins.Add("cosign", `[ "${MY_SIGNING_KEY-}" = k ] || { echo "key missing" >&2; exit 3; }
[ -z "${GPG_PRIVATE_KEY+set}" ] || { echo "unrelated secret present" >&2; exit 4; }`)

	signer := cosign.ForKeyRef("env://MY_SIGNING_KEY")
	signer.Bin = bins.Path("cosign")

	var stderr bytes.Buffer

	err := signer.SignBlob(t.Context(), cosign.SignBlobInput{
		Artifact: "app.tgz", BundlePath: "app.tgz.bundle", KeyRef: "env://MY_SIGNING_KEY",
	}, &stderr)
	if err != nil {
		t.Fatalf("SignBlob: %v: %s", err, stderr.String())
	}
}

// A slow Rekor makes cosign's client retry an upload the log has already
// accepted, and the retry fails with a conflict against cosign's own entry.
// That once failed four runtime-image attestations at the same moment, and a
// timestamp authority timeout failed another. The adapter signs once more
// after such a transient failure, and only after one.
func TestAttestImage_SignsAgainOnlyAfterATransientFailure(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		message string
		runs    int
		ok      bool
	}{
		{"conflict", `[POST /api/v1/log/entries][409] createLogEntryConflict {"code":409}`, 2, true},
		// The v0.90.0 runtime image signing lost a run to this.
		{"timestamp authority timeout", `error signing bundle: Post "https://timestamp.sigstore.dev/api/v1/timestamp": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`, 2, true},
		{"rekor unavailable", `[POST /api/v1/log/entries][503] createLogEntryDefault`, 2, true},
		{"other failure", "error: rejected by the registry", 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			state := filepath.Join(t.TempDir(), "failed-once")
			bins := mockbinary.New(t)
			bins.Add("cosign", "if [ ! -e '"+state+"' ]; then : > '"+state+"'; printf '%s\\n' '"+test.message+"' >&2; exit 1; fi")

			a := &cosign.Adapter{Bin: bins.Path("cosign")}

			err := a.AttestImage(t.Context(), cosign.AttestImageInput{
				ImageRef: "ghcr.io/o/app@sha256:" + strings.Repeat("a", 64), KeyRef: "k.key",
				PredicateType: "cyclonedx", PredicatePath: "sbom.json",
			}, nil)
			if (err == nil) != test.ok {
				t.Fatalf("err = %v, want success %t", err, test.ok)
			}

			if runs := len(bins.Invocations("cosign")); runs != test.runs {
				t.Fatalf("cosign ran %d times, want %d", runs, test.runs)
			}
		})
	}
}
