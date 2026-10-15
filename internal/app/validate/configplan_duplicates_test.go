// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate_test

import (
	"io/fs"
	"os"
	"strings"
	"testing"

	appvalidate "github.com/diggsweden/forge-ci/internal/app/validate"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
	"github.com/stretchr/testify/require"
)

func TestPrerequisiteConsumers_ConfigPlanDuplicateMembers(t *testing.T) {
	const (
		cargo = `{"name":"crate","project_type":"cargo","working_directory":".","sboms":"none","cargo_build_mode":"artifact-first","build_artifact_name":"crate-cargo-build-artifacts","build_sbom_artifact_name":"crate-cargo-build-sbom","cargo":{"skip_tests":false}}`
		maven = `{"name":"lib","project_type":"maven","working_directory":".","sboms":"none","build_artifact_name":"lib-build-artifacts","build_sbom_artifact_name":"lib-build-sbom","maven":{"settings_path":"settings.xml"}}`
		raw   = `{"version":1,"artifacts":{"all":[` + cargo + `,` + maven + `],"cargo":[` + cargo + `],"cargo_artifact_first":[` + cargo + `],"maven":[` + maven + `],"npm":[]},"pipeline_sboms":"none","fallback_project_type":"cargo","any_require_authorization":false,"sign":{"method":"gpg","imports_gpg_key":true},"git_signing":{"method":"gpg","imports_gpg_key":true},"future":{"version":1,"version":2}}`
	)

	// pipeline.DecodeConfigPlan owns which members and spellings count as
	// duplicates; these consumers show only that a refused plan has no effect.
	const (
		member    = `"settings_path":"settings.xml"`
		duplicate = `"settings_path":"other.xml"`
	)

	cases := []struct{ name, raw, path string }{
		{"control", raw, ""},
		{"duplicate", strings.Replace(raw, member, member+","+duplicate, 1), "artifacts.all[1].maven.settings_path"},
	}

	for _, kind := range []string{"cargo", "jvm"} {
		for _, tc := range cases {
			for _, seed := range []string{"", "existing\n"} {
				t.Run(kind+"/"+tc.name+"/"+seed, func(t *testing.T) {
					fsys := testfs.NewReal(t)
					fsys.Chdir()
					fsys.WriteFile("Cargo.lock", []byte("literal lock"))
					fsys.WriteFile("rust-toolchain", []byte("1.90.0"))
					fsys.WriteFile("pom.xml", []byte(`<project><properties><project.build.outputTimestamp>literal-time</project.build.outputTimestamp></properties></project>`))
					before := duplicatePlanTree(t, fsys.Root)

					var out, annot configPlanWriter

					_, _ = out.buf.WriteString(seed)
					_, _ = annot.buf.WriteString(seed)
					calls := 0

					var err error
					if kind == "cargo" {
						err = appvalidate.CargoPrerequisites(t.Context(), fakeCargoTool{version: "cargo literal", calls: &calls}, &out, output.NewAnnotator(&annot, output.FormatGitHub), appvalidate.CargoPrerequisitesInput{
							ConfigPlanJSON: tc.raw, PublishStagePlanJSON: `{"version":1,"stage":"publish","targets":{"cargo_container_first":{"runs":false}}}`,
						})
					} else {
						err = appvalidate.JVMReproducibility(t.Context(), &out, output.NewAnnotator(&annot, output.FormatGitHub), appvalidate.JVMReproducibilityInput{ConfigPlanJSON: tc.raw})
					}

					require.Equal(t, before, duplicatePlanTree(t, fsys.Root))

					if tc.path == "" {
						require.NoError(t, err)
						require.Positive(t, out.attempts)
						require.Positive(t, annot.attempts)

						if kind == "cargo" {
							require.Equal(t, 1, calls)
						} else {
							require.Contains(t, out.String(), "project.build.outputTimestamp=literal-time")
						}

						return
					}

					require.Zero(t, calls)
					require.Zero(t, out.attempts)
					require.Zero(t, annot.attempts)
					require.Equal(t, seed, out.String())
					require.Equal(t, seed, annot.String())
					require.ErrorIs(t, err, errs.ErrInvalidConfig)
					require.ErrorContains(t, err, "config-plan."+tc.path+" has duplicate consumed member")
				})
			}
		}
	}
}

func duplicatePlanTree(t *testing.T, root string) map[string]string {
	t.Helper()

	tree := map[string]string{}

	require.NoError(t, fs.WalkDir(os.DirFS(root), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			tree[path+"/"] = "directory"

			return nil
		}

		body, err := fs.ReadFile(os.DirFS(root), path)
		tree[path] = string(body)

		return err
	}))

	return tree
}
