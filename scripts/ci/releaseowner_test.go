// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ci

import (
	"context"
	"encoding/json"
	"fmt"
	enginecli "github.com/diggsweden/forge-ci/internal/cli"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReleaseOwnerCLI(t *testing.T) {
	if os.Getenv("RELEASE_OWNER_HELPER") != "1" {
		return
	}

	i := slices.Index(os.Args, "--")
	if i < 0 {
		os.Exit(99)
	}

	if err := enginecli.New(enginecli.BuildInfo{}).Run(context.Background(), append([]string{"forge-ci"}, os.Args[i+1:]...)); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)

		os.Exit(1)
	}

	os.Exit(0)
}

func releaseOwnerStub(t *testing.T, bin string) {
	t.Helper()

	body := `#!/bin/sh
set -eu
if [ "$1 $2" = 'release sign' ]; then
  if [ -n "${SIGN_CALL:-}" ]; then printf '%s\n' "$@" >"$SIGN_CALL"; fi
  printf '{}\n' >"$7.bundle"
  exit 0
fi
RELEASE_OWNER_HELPER=1 exec "$RELEASE_OWNER_BINARY" -test.run='^TestReleaseOwnerCLI$' -- "$@"
`
	require.NoError(t, os.WriteFile(filepath.Join(bin, "forge-ci"), []byte(body), 0o700))
}

func releaseOwnerMetadata(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	var artifacts []map[string]string

	for _, entry := range entries {
		kind := "Archive"
		if entry.Name() == "checksums.txt" {
			kind = "Checksum"
		} else if strings.HasSuffix(entry.Name(), ".json") {
			kind = "SBOM"
		}

		artifacts = append(artifacts, map[string]string{"path": filepath.Join(filepath.Base(dir), entry.Name()), "type": kind})
	}

	body, err := json.Marshal(artifacts)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "artifacts.json"), body, 0o600))
}
