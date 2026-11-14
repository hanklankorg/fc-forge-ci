// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package safeexec_test

import (
	"slices"
	"testing"

	"github.com/diggsweden/forge-ci/internal/safeexec"
)

func TestEnvWithout_DropsOnlyTheNamedVariables(t *testing.T) {
	t.Parallel()

	env := []string{"PATH=/bin", "TOKEN=secret", "TOKEN_HINT=x", "malformed"}

	got := safeexec.EnvWithout(env, "TOKEN")
	if want := []string{"PATH=/bin", "TOKEN_HINT=x"}; !slices.Equal(got, want) {
		t.Errorf("EnvWithout = %q, want %q", got, want)
	}
}

func TestEnvKeep_KeepsOnlyAcceptedNames(t *testing.T) {
	t.Parallel()

	env := []string{"PATH=/bin", "GNUPGHOME=/g", "GPG_PASSPHRASE=secret"}

	got := safeexec.EnvKeep(env, func(name string) bool { return name != "GPG_PASSPHRASE" })
	if want := []string{"PATH=/bin", "GNUPGHOME=/g"}; !slices.Equal(got, want) {
		t.Errorf("EnvKeep = %q, want %q", got, want)
	}
}
