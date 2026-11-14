// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	publishcmd "github.com/diggsweden/forge-ci/internal/cli/commands/publish"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/ghaenv"
)

func runDistroPackages(t *testing.T, declaration, forge string) error {
	t.Helper()
	t.Setenv("FORGE_CI_PROVIDER", forge)

	file := filepath.Join(t.TempDir(), "artifacts.yml")
	require.NoError(t, os.WriteFile(file, []byte("artifacts:\n  - name: cli\n    project-type: go\n"+declaration), 0o600))

	return publishcmd.New().Run(t.Context(), []string{
		"publish", "forge-packages", "distro-packages",
		"--file", file, "--manifest", "dist/release-files.json", "--dist-dir", "dist",
	})
}

// A release that declares no distro-packages publishes none and succeeds,
// before any release file is read. One that declares them on a forge
// without the registries is refused, again before any file is read.
func TestDistroPackagesCmdPublishesOnlyWhatIsDeclaredWhereARegistryExists(t *testing.T) { //nolint:paralleltest // selects the forge by environment.
	require.NoError(t, runDistroPackages(t, "", "forgejo"))

	for _, forge := range []string{"github", "gitlab"} {
		err := runDistroPackages(t, "distro-packages: {}\n", forge)
		require.ErrorIs(t, err, errs.ErrUnsupported, forge)
	}
}

// Admission sends nothing and reads no token. It says whether the block is
// declared, refuses a declared block whose token is not configured, and
// refuses a release file that cannot be published, here an .apk that is no
// Alpine package, from the unsigned manifest.
func TestDistroPackagesCmdAdmitsBeforeTheReleaseIsPublic(t *testing.T) { //nolint:paralleltest // selects the forge by environment.
	const declared = "distro-packages: {}\n"

	env := ghaenv.Setup(t)

	admit := func(declaration string, args ...string) error {
		file := filepath.Join(t.TempDir(), "artifacts.yml")
		require.NoError(t, os.WriteFile(file, []byte("artifacts:\n  - name: cli\n    project-type: go\n"+declaration), 0o600))

		return publishcmd.New().Run(t.Context(), append([]string{"publish", "forge-packages", "distro-packages", "--admit", "--file", file}, args...))
	}

	require.NoError(t, admit("", "--token-configured", "false"))
	require.Equal(t, "false", env.Output("distro-packages"))

	// Refused before the output says the block is declared.
	require.ErrorIs(t, admit(declared, "--token-configured", "false"), errs.ErrPermissionDenied)
	require.Equal(t, "false", env.Output("distro-packages"))

	require.NoError(t, admit(declared, "--token-configured", "true"))
	require.Equal(t, "true", env.Output("distro-packages"))

	t.Chdir(t.TempDir())
	require.NoError(t, os.Mkdir("dist", 0o700))
	require.NoError(t, os.WriteFile("dist/app-release.apk", []byte("PK\x03\x04 an Android package"), 0o600))
	require.NoError(t, os.WriteFile("dist/release-files.json", []byte(`{"version":1,"assets":[{"path":"dist/app-release.apk","name":"app-release.apk","source":"declared"},`+
		`{"path":"dist/checksums.txt","name":"checksums.txt","source":"declared"}],`+
		`"checksums":[{"path":"dist/checksums.txt","name":"checksums.txt","source":"declared"}]}`), 0o600))
	require.NoError(t, os.WriteFile("dist/checksums.txt", []byte("checked by the signing job, not by admission\n"), 0o600))

	// With the release's files the forge's registry is known: one that is
	// not HTTPS is refused here, before the files are read.
	t.Setenv("FORGE_CI_PROVIDER", "forgejo")
	t.Setenv("FORGEJO_REPOSITORY", "owner/repository")
	t.Setenv("FORGEJO_SERVER_URL", "http://forge.example")
	require.ErrorIs(t, admit(declared, "--manifest", "dist/release-files.json", "--dist-dir", "dist"), errs.ErrInvalidConfig)

	t.Setenv("FORGEJO_SERVER_URL", "https://forge.example")
	require.ErrorIs(t, admit(declared, "--manifest", "dist/release-files.json", "--dist-dir", "dist"), errs.ErrMalformedInput)
}
