// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package secrettext_test

import (
	"bytes"
	"github.com/diggsweden/forge-ci/internal/secrettext"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestWriterRedactsSplitAndOversizedDiagnostics(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	w := secrettext.NewWriter(&out, "synthetic-secret-token")
	for _, part := range []string{"token=synthetic-", "secret-token\n", strings.Repeat("x", 70<<10), "synthetic-secret-token\n", "done synthetic-secret-token"} {
		_, err := w.Write([]byte(part))
		require.NoError(t, err)
	}

	require.NoError(t, w.Close())
	require.Equal(t, "token=[redacted]\n[oversized diagnostic line suppressed]\ndone [redacted]", out.String())
}
