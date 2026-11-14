// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package plan_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	plancmd "github.com/diggsweden/forge-ci/internal/cli/commands/plan"
	"github.com/diggsweden/forge-ci/internal/testutil/ghaenv"
)

// Only a declaration that names gpg as its method or its files method gets
// GPG: one without a sign block, whose effective method is gpg, keeps the
// Cosign key, as does a release without a declaration.
func TestReleaseFileSigningCmd_GPGOnlyWhenDeclared(t *testing.T) { //nolint:paralleltest // the step outputs are the environment's.
	env := ghaenv.Setup(t)

	for _, test := range []struct {
		name, sign, want string
	}{
		{name: "no sign block", want: "cosign"},
		{name: "method gpg", sign: "sign:\n  method: gpg\n", want: "gpg"},
		{name: "cosign key", sign: "sign:\n  method: kms\n  key: env://COSIGN_KEY\n", want: "cosign"},
		{name: "files-method gpg", sign: "sign:\n  method: kms\n  key: env://COSIGN_KEY\n  files-method: gpg\n", want: "gpg"},
	} {
		file := filepath.Join(t.TempDir(), "artifacts.yml")
		require.NoError(t, os.WriteFile(file, []byte("artifacts:\n  - name: app\n    project-type: maven\n"+test.sign), 0o600))

		require.NoError(t, plancmd.New().Run(t.Context(), []string{"plan", "release-file-signing", "--file", file}), test.name)
		require.Equal(t, test.want, env.Output("release-file-signing"), test.name)
	}

	t.Chdir(t.TempDir())
	require.NoError(t, plancmd.New().Run(t.Context(), []string{"plan", "release-file-signing"}))
	require.Equal(t, "cosign", env.Output("release-file-signing"), "without a declaration")

	require.Error(t, plancmd.New().Run(t.Context(), []string{"plan", "release-file-signing", "--file", "absent.yml"}))
}
