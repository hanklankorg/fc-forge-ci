// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package consumerinit_test

import (
	"errors"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/consumerinit"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// A comment that explains a placeholder is not one; every token on a code
// line is reported once with its line.
func TestRemainingSkipsCommentsAndReportsEachTokenOnce(t *testing.T) {
	t.Parallel()

	content := "# PIN_FORGE_CI_SHA is the engine commit\n" +
		"uses: FORGE_HOST/OWNER/forge-ci/x@PIN_FORGE_CI_SHA # PIN_FORGE_CI_SHA\n" +
		"bot: FIXME Release Bot\n"

	got := consumerinit.Remaining(content)
	want := []consumerinit.Placeholder{
		{Token: "FORGE_HOST", Line: 2}, {Token: "OWNER/forge-ci", Line: 2}, {Token: "PIN_FORGE_CI_SHA", Line: 2},
		{Token: "FIXME", Line: 3},
	}

	if len(got) != len(want) {
		t.Fatalf("Remaining = %v, want %v", got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Remaining[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestParseDefaultsRefusesUnknownKeys(t *testing.T) {
	t.Parallel()

	if _, err := consumerinit.ParseDefaults([]byte("forge: forgejo\nsigner: x\n")); !errors.Is(err, errs.ErrInvalidConfig) {
		t.Fatalf("err = %v, want an invalid-config error", err)
	}

	defaults, err := consumerinit.ParseDefaults([]byte("forge: gitlab\n"))
	if err != nil || defaults.Forge != "gitlab" {
		t.Fatalf("ParseDefaults = %+v, %v", defaults, err)
	}
}

func TestRemainingReportsProjectSpecificExampleValues(t *testing.T) {
	t.Parallel()

	for _, content := range []string{
		"main-package: ./cmd/my-cli", "name: my-service", "from: [my-app]",
		"package-name: com.example.myapp.demo",
	} {
		if got := consumerinit.Remaining(content); len(got) != 1 || got[0].Line != 1 {
			t.Errorf("Remaining(%q) = %v, want one customization on line 1", content, got)
		}
	}
}

// github.com needs no defaults file; any other host must name its forge.
func TestResolveForge(t *testing.T) {
	t.Parallel()

	if forge, err := consumerinit.ResolveForge("github.com", consumerinit.Defaults{}); err != nil || forge != provider.ForgeGitHub {
		t.Errorf("github.com = %s, %v", forge, err)
	}

	if _, err := consumerinit.ResolveForge("codefloe.com", consumerinit.Defaults{}); !errors.Is(err, errs.ErrInvalidConfig) {
		t.Errorf("codefloe.com without forge: err = %v, want invalid config", err)
	}

	if forge, err := consumerinit.ResolveForge("codefloe.com", consumerinit.Defaults{Forge: "forgejo"}); err != nil || forge != provider.ForgeForgejo {
		t.Errorf("codefloe.com forgejo = %s, %v", forge, err)
	}
}

// A trailing comment that explains a filled value is not a placeholder; a
// # inside quotes is not a comment.
func TestRemainingIgnoresTrailingComments(t *testing.T) {
	t.Parallel()

	content := "main-package: . # FIXME: your command's main package\n" +
		"image: \"a#b\" PIN_DIGEST\n" +
		"bot: FIXME Release Bot # FIXME\n"

	got := consumerinit.Remaining(content)
	want := []consumerinit.Placeholder{{Token: "PIN_DIGEST", Line: 2}, {Token: "FIXME", Line: 3}}

	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Remaining = %v, want %v", got, want)
	}
}

func TestProjectReplacementsFillWholeTokensOnly(t *testing.T) {
	t.Parallel()

	r := consumerinit.ProjectReplacements(consumerinit.Project{Name: "hello", MainPackage: "."})
	content := "main-package: ./cmd/my-cli\nbinary-name: my-cli\nattach: release-artifacts/*/my-cli-*\n" +
		"ARTIFACT_NAME: FIXME-app\nkeep: my-application FIXME-apple FIXME-release-bot@example.com\n" +
		"COPY dist/FIXME-service-linux-x /usr/local/bin/my-service\n"

	got := consumerinit.ApplyProject(content, r)
	want := "main-package: .\nbinary-name: hello\nattach: release-artifacts/*/hello-*\n" +
		"ARTIFACT_NAME: hello\nkeep: my-application FIXME-apple FIXME-release-bot@example.com\n" +
		"COPY dist/hello-linux-x /usr/local/bin/hello\n"

	if got != want {
		t.Fatalf("ApplyProject =\n%s\nwant\n%s", got, want)
	}

	// A name that would not survive as an unquoted YAML value fills nothing.
	if got := consumerinit.ApplyProject(content, consumerinit.ProjectReplacements(consumerinit.Project{Name: "bad name: x"})); got != content {
		t.Errorf("an unsafe name filled =\n%s", got)
	}

	// A name with capitals names the jar but cannot name the image; it is
	// left for the person, not lowercased into a jar path that does not exist.
	if got := consumerinit.ApplyProject(content, consumerinit.ProjectReplacements(consumerinit.Project{Name: "MyService"})); got != content {
		t.Errorf("a name with capitals filled =\n%s", got)
	}

	// Without a detected main package the example path stays for the person
	// to fill; the name does not turn it into a ./cmd/<name> guess.
	r = consumerinit.ProjectReplacements(consumerinit.Project{Name: "hello"})
	if got := consumerinit.ApplyProject("main-package: ./cmd/my-cli\nbinary-name: my-cli\n", r); got != "main-package: ./cmd/my-cli\nbinary-name: hello\n" {
		t.Errorf("undetected main package = %q", got)
	}

	// A name containing a placeholder replaces it once, not forever.
	if got := consumerinit.ApplyProject("name: my-app", consumerinit.ProjectReplacements(consumerinit.Project{Name: "my-app-x"})); got != "name: my-app-x" {
		t.Errorf("self-containing name = %q", got)
	}
}

func TestEveryPlaceholderHasAHint(t *testing.T) {
	t.Parallel()

	for _, token := range []string{
		"PIN_FORGE_CI_SHA", "PIN_SIGNER_IMAGE_DIGEST", "PIN_COSIGN_PUBLIC_KEY_SHA256", "PIN_DIGEST", "PIN_GO_IMAGE_DIGEST",
		"FIXME", "FIXME-release-bot", "FIXME-app", "FIXME-service", "FORGE_HOST", "OWNER/forge-ci",
		"gitlab.example.com", "com.example.myapp", "my-cli",
	} {
		if hint := consumerinit.Hint(token); hint == "" || hint == "a value for your project" {
			t.Errorf("Hint(%q) = %q, want a specific hint", token, hint)
		}
	}
}

// An escaped quote does not end a double-quoted string, so a # after it is
// still inside the string and a placeholder there is reported.
func TestRemainingKeepsEscapedQuotesInsideTheString(t *testing.T) {
	t.Parallel()

	got := consumerinit.Remaining(`run: "say \"hi\" # PIN_DIGEST"` + "\n")
	if len(got) != 1 || got[0].Token != "PIN_DIGEST" {
		t.Fatalf("Remaining = %v, want PIN_DIGEST", got)
	}
}
