// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	"errors"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/build"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestParseSourceDateEpoch_ParsesSecondsAndRefusesTheRest(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ raw, want string }{
		{raw: "0", want: "1970-01-01T00:00:00Z"},
		{raw: "1700000000", want: "2023-11-14T22:13:20Z"},
		{raw: "253402300799", want: "9999-12-31T23:59:59Z"},
	} {
		got, err := build.ParseSourceDateEpoch(tc.raw)
		if err != nil || got.Format(time.RFC3339) != tc.want || got.Location() != time.UTC {
			t.Errorf("ParseSourceDateEpoch(%q) = %v, %v; want %s in UTC", tc.raw, got, err, tc.want)
		}
	}

	for _, raw := range []string{"", "yesterday", "1.5", " 0", "-1", "253402300800", "9223372036854775807"} {
		if got, err := build.ParseSourceDateEpoch(raw); !errors.Is(err, errs.ErrUsage) || !got.IsZero() {
			t.Errorf("ParseSourceDateEpoch(%q) = %v, %v; want a usage refusal", raw, got, err)
		}
	}
}
