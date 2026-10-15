// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestSelectPlatforms(t *testing.T) {
	t.Parallel()

	declared := []string{"linux/amd64", "linux/arm64"}

	for _, tc := range []struct {
		selection string
		want      []string
	}{
		{"", declared},
		{"linux/amd64", []string{"linux/amd64"}},
		{"linux/arm64", []string{"linux/arm64"}},
		// The declared order wins over the order asked for.
		{"linux/arm64,linux/amd64", declared},
	} {
		got, err := container.SelectPlatforms(declared, tc.selection)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("SelectPlatforms(%q) = %v, %v; want %v", tc.selection, got, err, tc.want)
		}
	}

	for _, selection := range []string{"linux/riscv64", "linux/amd64,linux/amd64", " linux/amd64", "linux", "linux/arm/v7", "linux/amd64,"} {
		_, err := container.SelectPlatforms(declared, selection)
		if !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), "declared: linux/amd64,linux/arm64") {
			t.Errorf("SelectPlatforms(%q) error = %v, want a usage error naming the declared set", selection, err)
		}
	}
}
