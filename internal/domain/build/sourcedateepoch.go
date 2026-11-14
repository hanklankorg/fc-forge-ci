// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build

import (
	"fmt"
	"strconv"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// ParseSourceDateEpoch parses a SOURCE_DATE_EPOCH value (non-negative integer
// seconds since the UNIX epoch, reproducible-builds.org convention) into a UTC
// time. It refuses a value that is not such an integer or that RFC3339 cannot
// represent. Every reader of the variable and of a --source-date-epoch flag
// parses it here, so a value one verb accepts is never refused, or silently
// replaced by the wall clock, in another.
func ParseSourceDateEpoch(raw string) (time.Time, error) {
	secs, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || secs < 0 {
		return time.Time{}, fmt.Errorf("invalid SOURCE_DATE_EPOCH %q (expected non-negative integer seconds since UNIX epoch): %w", raw, errs.ErrUsage)
	}

	date := time.Unix(secs, 0).UTC()
	if _, err := time.Parse(time.RFC3339, date.Format(time.RFC3339)); err != nil {
		return time.Time{}, fmt.Errorf("SOURCE_DATE_EPOCH is outside the RFC3339 range: %w", errs.ErrUsage)
	}

	return date, nil
}
