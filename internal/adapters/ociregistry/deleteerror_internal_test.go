// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ociregistry

import (
	"fmt"
	"net/http"
	"syscall"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// A registry without tag deletion, such as GHCR, answers 405 or UNSUPPORTED:
// a capability it lacks, which callers keep and report. A refusal of this
// request stays what it was.
func TestClassifyDeleteError(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		err  error
		want error
	}{
		"405":            {err: &transport.Error{StatusCode: http.StatusMethodNotAllowed}, want: errs.ErrUnsupported},
		"UNSUPPORTED":    {err: &transport.Error{StatusCode: http.StatusBadRequest, Errors: []transport.Diagnostic{{Code: transport.UnsupportedErrorCode}}}, want: errs.ErrUnsupported},
		"wrapped 405":    {err: fmt.Errorf("delete: %w", &transport.Error{StatusCode: http.StatusMethodNotAllowed}), want: errs.ErrUnsupported},
		"denied":         {err: &transport.Error{StatusCode: http.StatusForbidden}, want: errs.ErrPermissionDenied},
		"already absent": {err: &transport.Error{StatusCode: http.StatusNotFound}, want: errs.ErrMissingInput},
		"unreachable":    {err: fmt.Errorf("dial: %w", syscall.ECONNREFUSED), want: errs.ErrDependencyUnavailable},
	} {
		require.ErrorIs(t, classifyDeleteError(tc.err), tc.want, name)
	}
}
