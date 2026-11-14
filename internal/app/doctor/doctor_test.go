// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package doctor_test

import (
	"bytes"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/app/doctor"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// writeRepo lays out a minimal repo under root with the supplied
// artifacts.yml. Optional extra files via files map (relative path → body).
// Unless files brings its own CI entry point, the repo gets a minimal
// .github/workflows/ci.yml: doctor fails a repository with no CI at all.
func writeRepo(t *testing.T, artifactsYAML string, files map[string]string) string {
	t.Helper()

	root := t.TempDir()

	if !hasCIFile(files) {
		files = withFile(files, ".github/workflows/ci.yml", "on: push\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps: []\n")
	}

	if err := os.MkdirAll(filepath.Join(root, ".forge-ci"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, ".forge-ci", "artifacts.yml"), []byte(artifactsYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	for rel, body := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	return root
}

func hasCIFile(files map[string]string) bool {
	for rel := range files {
		if strings.HasPrefix(rel, ".github/workflows/") || strings.HasPrefix(rel, ".forgejo/workflows/") || rel == ".gitlab-ci.yml" {
			return true
		}
	}

	return false
}

func withFile(files map[string]string, rel, body string) map[string]string {
	out := map[string]string{rel: body}
	maps.Copy(out, files)

	return out
}

func findCheck(checks []doctor.Check, name string) *doctor.Check {
	for i := range checks {
		if checks[i].Name == name {
			return &checks[i]
		}
	}

	return nil
}

func TestRun_HappyPath_GPGDefault(t *testing.T) {
	t.Parallel()

	root := writeRepo(t, `
artifacts:
  - name: fixture-app
    project-type: meta
`, nil)

	checks, err := doctor.Run(doctor.Input{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	if exit := doctor.ExitCode(checks); exit != 0 {
		var buf bytes.Buffer
		doctor.FormatText(&buf, checks)
		t.Errorf("expected exit 0, got %d\n%s", exit, buf.String())
	}
}

func TestRun_ReportsUnsupportedGitHubEnterpriseReleaseWorkflow(t *testing.T) {
	t.Parallel()

	root := writeRepo(t, `
artifacts:
  - name: fixture-app
    project-type: meta
`, nil)

	checks, err := doctor.Run(doctor.Input{
		Root:      root,
		ForgeAPI:  provider.ForgeGitHub,
		ServerURL: "https://github.example.test",
	})
	if err != nil {
		t.Fatal(err)
	}

	check := findCheck(checks, "release workflow provider support")
	if check == nil || check.Severity != doctor.SeverityFail {
		t.Fatalf("provider check = %#v, want fail", check)
	}
}

func TestRun_MissingArtifactsYML_Fails(t *testing.T) {
	t.Parallel()

	root := t.TempDir() // no artifacts.yml

	checks, err := doctor.Run(doctor.Input{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	check := findCheck(checks, "artifacts.yml present")
	if check == nil || check.Severity != doctor.SeverityFail {
		// Fatalf, not Errorf: the remediation check below dereferences c,
		// so a missing check would panic instead of reporting.
		t.Fatalf("missing artifacts.yml must produce a FAIL check; got %+v", check)
	}

	// The fix names a command the binary's user can run, not a directory of
	// the source tree they may not have.
	if !strings.Contains(check.Remediation, "forge-ci init") {
		t.Errorf("remediation must point at forge-ci init; got %q", check.Remediation)
	}
}

func TestRun_MissingArtifactsYML_AutoDerivesSingleManifest(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/acme/widget\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, ".github", "workflows", "ci.yml"), []byte("on: push\njobs: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	checks, err := doctor.Run(doctor.Input{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	// The check says the configuration was derived; it no longer claims an
	// absent artifacts.yml is "present".
	if c := findCheck(checks, "artifacts.yml present"); c != nil {
		t.Errorf("an absent artifacts.yml must not be reported as present; got %+v", c)
	}

	c := findCheck(checks, "configuration auto-derives")
	if c == nil || c.Severity != doctor.SeverityOK || !strings.Contains(c.Message, "go artifact \"widget\" derived from the root manifest") {
		t.Errorf("single manifest should auto-derive a usable config; got %+v", c)
	}

	if exit := doctor.ExitCode(checks); exit != 0 {
		var buf bytes.Buffer
		doctor.FormatText(&buf, checks)
		t.Errorf("expected exit 0, got %d\n%s", exit, buf.String())
	}
}

func TestRun_ExplicitMissingArtifactsPathDoesNotAutoDerive(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/acme/widget\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	missing := filepath.Join(root, "custom.yml")

	checks, err := doctor.Run(doctor.Input{Root: root, ArtifactsPath: missing})
	if err != nil {
		t.Fatal(err)
	}

	c := findCheck(checks, "artifacts.yml present")
	if c == nil || c.Severity != doctor.SeverityFail {
		t.Errorf("an explicit missing path must not auto-derive; got %+v", c)
	}
}

func TestRun_RequireAuthorizationWithoutAllowlist_Fails(t *testing.T) {
	t.Parallel()

	root := writeRepo(t, `
artifacts:
  - name: fixture-app
    project-type: meta
    require-authorization: true
`, nil)

	checks, err := doctor.Run(doctor.Input{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	c := findCheck(checks, "release-authorization allowlist present")
	if c == nil || c.Severity != doctor.SeverityFail {
		t.Errorf("require-authorization without allowlist must fail; got %+v", c)
	}
}

func TestRun_RequireAuthorizationWithAllowlist_Passes(t *testing.T) {
	t.Parallel()

	root := writeRepo(t, `
artifacts:
  - name: fixture-app
    project-type: meta
    require-authorization: true
`, map[string]string{
		".forge-ci/allowed_signers": "user@example.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFKw6+NIKWOtsi2O+WmFbxgYTmak1rpcEPrvx23EQ3yJ\n", // gitleaks:allow — throwaway test key
	})

	checks, err := doctor.Run(doctor.Input{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	c := findCheck(checks, "release-authorization allowlist present")
	if c == nil || c.Severity != doctor.SeverityOK {
		t.Errorf("allowlist present must pass; got %+v", c)
	}
}

func TestRun_SigstoreWithoutIDTokenPermission_Fails(t *testing.T) {
	t.Parallel()

	root := writeRepo(t, `
artifacts:
  - name: fixture-app
    project-type: meta
sign:
  method: sigstore
`, map[string]string{
		".github/workflows/release.yml": "name: release\non: push\npermissions:\n  contents: write\n",
	})

	checks, err := doctor.Run(doctor.Input{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	check := findCheck(checks, "workflow id-token permission")
	if check == nil || check.Severity != doctor.SeverityFail {
		// Fatalf, not Errorf: the remediation check below dereferences c.
		t.Fatalf("sigstore + no id-token must fail; got %+v", check)
	}

	if !strings.Contains(check.Remediation, "id-token: write") {
		t.Errorf("remediation must mention id-token: write; got %q", check.Remediation)
	}
}

func TestRun_SigstoreWithIDTokenPermission_Passes(t *testing.T) {
	t.Parallel()

	root := writeRepo(t, `
artifacts:
  - name: fixture-app
    project-type: meta
sign:
  method: sigstore
`, map[string]string{
		".github/workflows/release.yml": "name: release\non: push\npermissions:\n  contents: write\n  id-token: write\n",
	})

	checks, err := doctor.Run(doctor.Input{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	c := findCheck(checks, "workflow id-token permission")
	if c == nil || c.Severity != doctor.SeverityOK {
		t.Errorf("sigstore + id-token present must pass; got %+v", c)
	}
}

// Forgejo grants the OIDC id-token via `enable-openid-connect` under
// .forgejo/workflows (it ignores `permissions: id-token: write`), so the
// check must accept that convention too — not only GitHub's.
func TestRun_SigstoreWithForgejoOpenIDConnect_Passes(t *testing.T) {
	t.Parallel()

	root := writeRepo(t, `
artifacts:
  - name: fixture-app
    project-type: meta
sign:
  method: sigstore
`, map[string]string{
		".forgejo/workflows/release.yml": "name: release\non: push\njobs:\n  release:\n    enable-openid-connect: true\n",
	})

	checks, err := doctor.Run(doctor.Input{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	c := findCheck(checks, "workflow id-token permission")
	if c == nil || c.Severity != doctor.SeverityOK {
		t.Errorf("sigstore + Forgejo enable-openid-connect must pass; got %+v", c)
	}
}

func TestRun_FloatingForgeCIRef_Warns(t *testing.T) {
	t.Parallel()

	root := writeRepo(t, `
artifacts:
  - name: fixture-app
    project-type: meta
`, map[string]string{
		".github/workflows/release.yml": "name: release\non: push\njobs:\n  release:\n    uses: diggsweden/forge-ci/.github/workflows/release-orchestrator.yml@main\n",
	})

	checks, err := doctor.Run(doctor.Input{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	c := findCheck(checks, "workflows pin forge-ci to a tag")
	if c == nil || c.Severity != doctor.SeverityWarn {
		t.Errorf("@main pin must produce a WARN; got %+v", c)
	}

	if exit := doctor.ExitCode(checks); exit != 0 {
		// Warnings must NOT change exit code — pinned design contract.
		t.Errorf("warning-only checks must not change exit code; got exit %d", exit)
	}
}

func TestRun_KMSWithBadKey_FailsAtSignBlock(t *testing.T) {
	t.Parallel()

	root := writeRepo(t, `
artifacts:
  - name: fixture-app
    project-type: meta
sign:
  method: kms
  key: /etc/secrets/key
`, nil)

	checks, err := doctor.Run(doctor.Input{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	// Config.Validate runs the SignConfig.Validate, which rejects
	// /etc/... — so this comes back as the parses+validates check
	// failing, not as a separate "sign block valid" check.
	c := findCheck(checks, "artifacts.yml validates")
	if c == nil || c.Severity != doctor.SeverityFail {
		t.Errorf("bad sign.key URI must fail validation; got %+v", c)
	}
}

func TestFormatText_RendersAllSeverities(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	doctor.FormatText(&buf, []doctor.Check{
		{Name: "a", Severity: doctor.SeverityOK, Message: "fine"},
		{Name: "b", Severity: doctor.SeverityWarn, Message: "weird", Remediation: "rethink"},
		{Name: "c", Severity: doctor.SeverityFail, Message: "broken", Remediation: "patch"},
	})

	out := buf.String()
	for _, want := range []string{"[OK] a:", "[WARN] b:", "[FAIL] c:", "fix: rethink", "fix: patch"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
}

const recommendationCheck = "signing method recommendation"

func TestRun_KeylessRecommendation_AppearsForGPGOnKeylessForge(t *testing.T) {
	t.Parallel()

	root := writeRepo(t, `
artifacts:
  - name: fixture-app
    project-type: meta
`, nil) // no sign block → gpg default

	checks, err := doctor.Run(doctor.Input{Root: root, KeylessAvailable: true})
	if err != nil {
		t.Fatal(err)
	}

	check := findCheck(checks, recommendationCheck)
	if check == nil {
		t.Fatal("expected a keyless recommendation on gpg + keyless-capable forge")
	}

	if check.Severity != doctor.SeverityOK {
		t.Errorf("recommendation must be non-failing (OK), got %s", check.Severity)
	}

	if doctor.ExitCode(checks) != 0 {
		t.Error("recommendation must not change exit code")
	}
}

// GPG for files beside Cosign for images: the GitHub, Forgejo and GitLab
// releases sign the files with GPG, so doctor neither warns nor refuses.
func TestRun_SignBlockFollowsTheForgesFilesMethodSupport(t *testing.T) {
	t.Parallel()

	root := writeRepo(t, `
artifacts:
  - name: fixture-app
    project-type: meta
sign:
  method: kms
  key: env://COSIGN_KEY
  files-method: gpg
`, nil)

	for forge, want := range map[provider.ForgeAPI]doctor.Severity{
		provider.ForgeGitHub:  doctor.SeverityOK,
		provider.ForgeGitLab:  doctor.SeverityOK,
		provider.ForgeForgejo: doctor.SeverityOK,
	} {
		checks, err := doctor.Run(doctor.Input{Root: root, ForgeAPI: forge})
		if err != nil {
			t.Fatal(err)
		}

		check := findCheck(checks, "sign block valid")
		if check == nil || check.Severity != want || !strings.Contains(check.Message, "files-method") {
			t.Errorf("%s: sign block check = %+v, want %s naming files-method", forge, check, want)
		}
	}
}

// Doctor warns, and does not fail, where the forge's release signs with
// another method or key than the declaration names, or decides the
// transparency by its own input, as that release's plan does.
func TestRun_SignBlockWarnsAboutSigningTheReleaseDoesNotUse(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		sign, message string
		want          map[provider.ForgeAPI]doctor.Severity
	}{
		{sign: "  method: sigstore\n", want: map[provider.ForgeAPI]doctor.Severity{
			provider.ForgeGitHub: doctor.SeverityOK, provider.ForgeForgejo: doctor.SeverityWarn, provider.ForgeGitLab: doctor.SeverityWarn,
		}},
		{sign: "  method: kms\n  key: awskms:///alias/release\n", want: map[provider.ForgeAPI]doctor.Severity{
			provider.ForgeGitHub: doctor.SeverityOK, provider.ForgeForgejo: doctor.SeverityWarn, provider.ForgeGitLab: doctor.SeverityWarn,
		}},
		{sign: "  method: gpg\n", want: map[provider.ForgeAPI]doctor.Severity{
			provider.ForgeGitHub: doctor.SeverityOK, provider.ForgeForgejo: doctor.SeverityOK, provider.ForgeGitLab: doctor.SeverityOK,
		}},
		{sign: "  method: kms\n  key: env://COSIGN_KEY\n", want: map[provider.ForgeAPI]doctor.Severity{
			provider.ForgeGitHub: doctor.SeverityOK, provider.ForgeForgejo: doctor.SeverityOK, provider.ForgeGitLab: doctor.SeverityOK,
		}},
		{sign: "  method: kms\n  key: env://COSIGN_KEY\n  transparency: none\n", message: "transparency-log", want: map[provider.ForgeAPI]doctor.Severity{
			provider.ForgeGitHub: doctor.SeverityOK, provider.ForgeForgejo: doctor.SeverityWarn, provider.ForgeGitLab: doctor.SeverityWarn,
		}},
	} {
		root := writeRepo(t, "artifacts:\n  - name: fixture-app\n    project-type: meta\nsign:\n"+tc.sign, nil)

		for forge, want := range tc.want {
			checks, err := doctor.Run(doctor.Input{Root: root, ForgeAPI: forge})
			if err != nil {
				t.Fatal(err)
			}

			message := tc.message
			if message == "" {
				message = "RELEASE_COSIGN_PRIVATE_KEY"
			}

			check := findCheck(checks, "sign block valid")
			if check == nil || check.Severity != want || (want == doctor.SeverityWarn && !strings.Contains(check.Message, message)) {
				t.Errorf("%s with %q: sign block check = %+v, want %s", forge, tc.sign, check, want)
			}
		}
	}
}

func TestRun_KeylessRecommendation_SilentWhenForgeLacksKeyless(t *testing.T) {
	t.Parallel()

	root := writeRepo(t, `
artifacts:
  - name: fixture-app
    project-type: meta
`, nil)

	checks, err := doctor.Run(doctor.Input{Root: root, KeylessAvailable: false})
	if err != nil {
		t.Fatal(err)
	}

	if findCheck(checks, recommendationCheck) != nil {
		t.Error("no recommendation expected when the forge cannot do keyless")
	}
}

func TestRun_KeylessRecommendation_SilentForMavenCentral(t *testing.T) {
	t.Parallel()

	// Maven Central requires a PGP .asc, so gpg is correct — no nudge.
	root := writeRepo(t, `
artifacts:
  - name: lib
    project-type: maven
    publish-to: [maven-central]
`, nil)

	checks, err := doctor.Run(doctor.Input{Root: root, KeylessAvailable: true})
	if err != nil {
		t.Fatal(err)
	}

	if findCheck(checks, recommendationCheck) != nil {
		t.Error("no recommendation expected for a Maven Central (PGP-native) publish")
	}
}

func TestRun_KeylessRecommendation_SilentWhenAlreadySigstore(t *testing.T) {
	t.Parallel()

	root := writeRepo(t, `
artifacts:
  - name: fixture-app
    project-type: meta
sign:
  method: sigstore
`, nil)

	checks, err := doctor.Run(doctor.Input{Root: root, KeylessAvailable: true})
	if err != nil {
		t.Fatal(err)
	}

	if findCheck(checks, recommendationCheck) != nil {
		t.Error("no recommendation expected when already on sigstore")
	}
}

const sshAllowlistCheck = "ssh git-signing allowlist"

// A missing allowlist warns: only the release reads it, so a pull request
// passes without it. The warning names the command that writes it.
func TestRun_SSHGitSigning_WarnsWhenAllowedSignersMissing(t *testing.T) {
	t.Parallel()

	root := writeRepo(t, `
artifacts:
  - name: fixture-app
    project-type: meta
git-signing:
  method: ssh
`, nil) // no allowed_signers file

	checks, err := doctor.Run(doctor.Input{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	c := findCheck(checks, sshAllowlistCheck)
	if c == nil || c.Severity != doctor.SeverityWarn {
		t.Fatalf("ssh git-signing without allowed_signers should WARN; got %+v", c)
	}

	if !strings.Contains(c.Remediation, "forge-ci setup signer") {
		t.Errorf("remediation must name the command that writes the file; got %q", c.Remediation)
	}
}

func TestRun_SSHGitSigning_OKWhenAllowedSignersPresent(t *testing.T) {
	t.Parallel()

	root := writeRepo(t, `
artifacts:
  - name: fixture-app
    project-type: meta
git-signing:
  method: ssh
`, map[string]string{
		".forge-ci/allowed_signers": "bot@org.example ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFKw6+NIKWOtsi2O+WmFbxgYTmak1rpcEPrvx23EQ3yJ\n", // gitleaks:allow — throwaway test key
	})

	checks, err := doctor.Run(doctor.Input{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	c := findCheck(checks, sshAllowlistCheck)
	if c == nil || c.Severity != doctor.SeverityOK {
		t.Errorf("ssh git-signing with allowed_signers should be OK; got %+v", c)
	}
}

func TestRun_GPGGitSigning_NoSSHAllowlistCheck(t *testing.T) {
	t.Parallel()

	root := writeRepo(t, `
artifacts:
  - name: fixture-app
    project-type: meta
`, nil) // git-signing defaults to gpg

	checks, err := doctor.Run(doctor.Input{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	if findCheck(checks, sshAllowlistCheck) != nil {
		t.Error("no ssh-allowlist check expected for the gpg default")
	}
}

func TestRun_SSHGitSigning_FailsWhenAllowedSignersMalformed(t *testing.T) {
	t.Parallel()

	root := writeRepo(t, `
artifacts:
  - name: fixture-app
    project-type: meta
git-signing:
  method: ssh
`, map[string]string{
		// Missing principal — the exact bug that caused CI to reject
		// the tag signature with "signer is not in allowed_signers".
		".forge-ci/allowed_signers": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFKw6+NIKWOtsi2O+WmFbxgYTmak1rpcEPrvx23EQ3yJ fixture@example.invalid\n", // gitleaks:allow — throwaway test key
	})

	checks, err := doctor.Run(doctor.Input{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	c := findCheck(checks, sshAllowlistCheck)
	if c == nil || c.Severity != doctor.SeverityFail {
		t.Fatalf("malformed allowed_signers should FAIL; got %+v", c)
	}

	if c.Remediation == "" {
		t.Error("FAIL check should carry a remediation")
	}
}

// A root that is not a directory is a mistyped --root, not a repository
// without Forge CI files: doctor refuses it as missing input instead of
// failing every check and proposing init.
func TestRunRefusesARootThatIsNotADirectory(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, root := range []string{filepath.Join(t.TempDir(), "absent"), file} {
		checks, err := doctor.Run(doctor.Input{Root: root})
		if !errors.Is(err, errs.ErrMissingInput) || checks != nil || !strings.Contains(err.Error(), "--root") {
			t.Errorf("root %s: checks = %v, err = %v, want a missing-input refusal naming --root", root, checks, err)
		}
	}
}

// The Forgejo examples turn the request-tag signature check off until the
// allowed-signers file is committed. Doctor warns with the line while it is
// off, passes once it is on, and says nothing where no Forgejo workflow sets it.
func TestRequestSignatureCheckFollowsTheForgejoInput(t *testing.T) {
	t.Parallel()

	const meta = "artifacts:\n  - name: x\n    project-type: meta\n"

	for name, tc := range map[string]struct {
		workflow string
		want     doctor.Severity
		listed   bool
	}{
		"off, quoted with a comment": {"      verify-release-request-signature: 'false' # until signers are committed\n", doctor.SeverityWarn, true},
		"off, bare":                  {"      verify-release-request-signature: false\n", doctor.SeverityWarn, true},
		"on":                         {"      verify-release-request-signature: 'true'\n", doctor.SeverityOK, true},
		"not set":                    {"      bot-name: Release Bot\n", "", false},
		"only in a comment":          {"      # verify-release-request-signature: 'true'\n", "", false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			body := "on: push\njobs:\n  release:\n    uses: ./.forgejo/workflows/x.yml\n    with:\n" + tc.workflow
			root := writeRepo(t, meta, map[string]string{".forgejo/workflows/release.yml": body})

			checks, err := doctor.Run(doctor.Input{Root: root})
			if err != nil {
				t.Fatal(err)
			}

			got := findCheck(checks, doctor.CheckRequestSignature)
			switch {
			case !tc.listed && got != nil:
				t.Errorf("check reported without the input: %+v", *got)
			case tc.listed && (got == nil || got.Severity != tc.want):
				t.Errorf("check = %+v, want severity %q", got, tc.want)
			case tc.want == doctor.SeverityWarn && !strings.Contains(got.Message, ".forgejo/workflows/release.yml:6"):
				t.Errorf("warning %q does not name the line", got.Message)
			}
		})
	}
}
