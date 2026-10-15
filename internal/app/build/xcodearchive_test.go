// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	"context"
	"io"
)

type fakeXcodeBuild struct {
	calls    [][]string
	exitCode int
	err      error
	run      func([]string)
}

func (f *fakeXcodeBuild) RunInherit(_ context.Context, _, _ io.Writer, args ...string) (int, error) {
	f.calls = append(f.calls, args)
	if f.run != nil {
		f.run(args)
	}

	if f.err != nil {
		return -1, f.err
	}

	return f.exitCode, nil
}
