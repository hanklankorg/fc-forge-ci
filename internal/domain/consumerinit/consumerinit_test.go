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
		{Token: "FORGE_HOST", Line: 2}, {Token: "OWNER/forge-ci", Line: 2}, {Token: "PIN_FORGE_CI_SHA", Line: 2}, //nolint:gosec // G101: placeholder names, no credential.
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
