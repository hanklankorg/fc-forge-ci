// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	appversion "github.com/diggsweden/forge-ci/internal/app/version"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/stretchr/testify/require"
)

func TestAndroidLibraryBumpsPublicationVersionOnly(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	file := filepath.Join(directory, "gradle.properties")
	require.NoError(t, os.WriteFile(file, []byte("version=1.0.0\nversionName=9.9.9\nversionCode=7\n"), 0o600))
	require.NoError(t, appversion.Bump(t.Context(), appversion.BumpOps{}, io.Discard, io.Discard, output.Annotator{}, appversion.BumpInput{ProjectType: projecttype.GradleAndroid, Library: true, WorkingDir: directory, Version: "1.1.0"}))

	body, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, "version=1.1.0\nversionName=9.9.9\nversionCode=7\n", string(body))
}
