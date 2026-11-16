// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package buildah_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/buildah"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
	"github.com/stretchr/testify/require"
)

func TestRegistryInputImportUsesOnlyLocalOCITransport(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	m := mockbinary.New(t)
	m.Add("buildah", fmt.Sprintf(`printf '%%s\n' "$*" >> %q
case "$1" in
pull) printf '%%064d\n' 1 ;;
tag) exit 0 ;;
*) exit 91 ;;
esac`, log))
	adapter := &buildah.Adapter{Bin: m.Path("buildah")}
	local, err := adapter.LoadRegistryInput(t.Context(), "/workspace/retained parent", "sha256:"+strings.Repeat("a", 64), io.Discard)
	require.NoError(t, err)
	require.Equal(t, "localhost/forge-ci-input:"+strings.Repeat("a", 64), local)

	calls, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, []string{
		"pull --quiet oci:/workspace/retained parent:scan",
		"tag " + strings.Repeat("0", 63) + "1 localhost/forge-ci-input:" + strings.Repeat("a", 64),
	}, strings.Split(strings.TrimSpace(string(calls)), "\n"))
}
