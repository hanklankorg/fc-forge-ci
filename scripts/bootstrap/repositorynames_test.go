// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package bootstrap

import (
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/repositorycases"
)

func TestInstallerRepositoryNamesMatchAdapterAddressing(t *testing.T) {
	t.Parallel()

	for _, tc := range repositorycases.OwnerName() {
		t.Run(tc.Path, func(t *testing.T) {
			t.Parallel()

			_, stderr, err := runSourcedScript(t, "install-forge-ci.sh", "check_forge_ci_source", map[string]string{"FORGE_CI_REPOSITORY": tc.Path})
			if (err == nil) != tc.Valid {
				t.Fatalf("err=%v, want valid=%v: %s", err, tc.Valid, stderr)
			}
		})
	}
}
