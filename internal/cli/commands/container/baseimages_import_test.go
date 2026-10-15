// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	rootcli "github.com/diggsweden/forge-ci/internal/cli"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

func TestBaseImportCLIRejectsBeforeCredentialAccess(t *testing.T) {
	const source = "registry.example/owner/source/bases"

	const destination = "registry.example/owner/publication/bases"

	baseID, digest := strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)

	for _, scenario := range []string{"foreign destination", "release entry", "moving tag", "preseeded output", "same ledger", "valid admission"} {
		t.Run(scenario, func(t *testing.T) {
			env := testenv.New(t)
			t.Chdir(env.Temp)
			require.NoError(t, os.Mkdir("dist", 0o700))

			entry := imageledger.Entry{ImageKind: imageledger.ImageKindBase, Role: "base", Flavor: "go", BaseInputID: baseID,
				Ref: source + "@" + digest, Digest: digest, FinalTag: source + ":" + baseID + "-go", CandidateTag: source + ":staging-" + baseID + "-go"}
			output, repository := "dist/imported.json", destination

			switch scenario {
			case "foreign destination":
				repository = "registry.example/other/bases"
			case "release entry":
				entry.ImageKind = imageledger.ImageKindRelease
			case "moving tag":
				entry.MovingTag = source + ":latest"
			case "preseeded output":
				require.NoError(t, os.WriteFile(output, []byte("preserve me"), 0o600))
			case "same ledger":
				output = "dist/source.json"
			}

			body, err := json.Marshal([]imageledger.Entry{entry})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile("dist/source.json", body, 0o600))
			err = rootcli.New(rootcli.BuildInfo{}).Run(t.Context(), []string{"forge-ci", "container", "base-images", "import",
				"--server-url", "https://gitlab.example", "--registry", "registry.example", "--repository", "owner/source",
				"--expected-repository", repository, "--source-repository", source, "--ledger", "dist/source.json", "--output-ledger", output,
				"--registry-username", "fixture", "--registry-password-file", env.Path("absent-credential")})
			require.Error(t, err)

			if scenario == "valid admission" {
				require.ErrorIs(t, err, os.ErrNotExist, "positive control must reach the absent credential, without any network")
			} else {
				require.True(t, strings.Contains(err.Error(), "base") || strings.Contains(err.Error(), "imageledger"))
				require.False(t, os.IsNotExist(err), "invalid requests must refuse before reading credentials")

				if scenario == "same ledger" {
					require.ErrorIs(t, err, errs.ErrUsage)
				}
			}

			unchanged, err := os.ReadFile("dist/source.json")
			require.NoError(t, err)
			require.Equal(t, body, unchanged)

			if scenario == "preseeded output" {
				unchanged, err = os.ReadFile(output)
				require.NoError(t, err)
				require.Equal(t, "preserve me", string(unchanged))
			}
		})
	}
}
