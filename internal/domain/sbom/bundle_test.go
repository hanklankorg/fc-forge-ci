// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package sbom_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/sbom"
)

func TestBundleName_CanonicalShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		project string
		version string
		want    string
	}{
		{"simple", "my-app", "1.2.3", "my-app-1.2.3-sboms.tar.gz"},
		{"scoped_npm", "@org/pkg", "0.1.0", "org-pkg-0.1.0-sboms.tar.gz"},
		{"prerelease_with_v_prefix", "lib", "v2.0.0-rc.1", "lib-v2.0.0-rc.1-sboms.tar.gz"},
		{"empty_version_keeps_separator", "x", "", "x--sboms.tar.gz"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, testCase.want, sbom.BundleName(testCase.project, testCase.version))
		})
	}
}
