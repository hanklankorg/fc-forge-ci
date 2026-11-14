// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appbaseimages "github.com/diggsweden/forge-ci/internal/app/baseimages"
	rootcli "github.com/diggsweden/forge-ci/internal/cli"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
)

// This test's final tag is fixture setup, not a production promotion guarantee.
// The separate promotion expectation unit is deliberately not required here.
func verifyChildCLI(t *testing.T, run func(...string) string, bins *mockbinary.Mock, parent appbaseimages.BaseImageParent, destination, id, keyHash string, common []string) {
	t.Helper()

	body, err := os.ReadFile("verified/dist/base-images-publication.json")
	require.NoError(t, err)
	entries, err := imageledger.Parse(body)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"ref": parent.Ref, "input_id": parent.InputID}, entries[0].Provenance["parent_base"])
	require.NoError(t, ociregistry.WithAuthFile("auth.json").CopyTag(t.Context(), entries[0].Ref, entries[0].FinalTag))
	require.NoError(t, os.WriteFile("flavors", []byte("opaque\n"), 0o600))

	args := append([]string{"container", "base-images", "validate", "--expected-workflow", "bases.yml",
		"--cosign-public-key-path", "trusted.pub", "--cosign-public-key-sha256", keyHash, "--flavors-file", "flavors"}, common[:len(common)-2]...)

	for _, scenario := range []string{"valid", "wrong parent ref", "wrong parent id"} {
		expected := parent
		if scenario == "wrong parent ref" {
			expected.Ref = destination + "@sha256:" + strings.Repeat("f", 64)
		}

		if scenario == "wrong parent id" {
			expected.InputID = strings.Repeat("f", 64)
		}

		inputs, marshalErr := json.Marshal([]appbaseimages.BaseInput{{Flavor: "opaque", BaseInputID: id, ParentBase: &expected}})
		require.NoError(t, marshalErr)

		call := append(append([]string{}, args...), "--base-inputs-json", string(inputs))
		if scenario == "valid" {
			output := run(call...)
			require.Contains(t, output, `"all_found": true`)
			require.Contains(t, output, entries[0].Ref)
		} else {
			command := rootcli.New(rootcli.BuildInfo{Version: "dev"})
			command.Writer, command.ErrWriter = io.Discard, io.Discard
			err := command.Run(t.Context(), append([]string{"forge-ci", "--provider", "forgejo", "--format", "json"}, call...))
			require.ErrorIs(t, err, errs.ErrValidation)
		}
	}

	for _, invocation := range bins.Invocations("buildah") {
		for index, arg := range invocation.Args {
			if arg == "--authfile" {
				body, err := os.ReadFile(invocation.Args[index+1])
				// Short-lived anonymous auth was already removed by the owner.
				require.True(t, os.IsNotExist(err) || string(body) == `{"auths":{}}`)
			}

			require.NotContains(t, arg, parent.Ref, "the builder receives a local imported image name, not the remote parent")
		}
	}
}

func refuseReplacedPreparedParent(t *testing.T, bins *mockbinary.Mock, buildArgs []string) {
	t.Helper()

	directory := "inputs/root-arch-amd64/PARENT_IMAGE"
	require.NoError(t, os.Rename(directory, "saved-parent"))

	config, err := empty.Image.ConfigFile()
	require.NoError(t, err)

	config.OS, config.Architecture = "linux", "amd64"
	config.Config.Labels = map[string]string{"replacement": "internally-valid-but-unadmitted"}
	image, err := mutate.ConfigFile(empty.Image, config)
	require.NoError(t, err)
	path, err := layout.Write(directory, empty.Index)
	require.NoError(t, err)
	require.NoError(t, path.AppendImage(image))
	manifest, err := image.RawManifest()
	require.NoError(t, err)
	// Replacing the retained source proof as well as the layout cannot replace
	// the parent digest independently admitted in the child's plan.
	require.NoError(t, os.WriteFile(directory+"/forge-ci-source-manifest.json", manifest, 0o600))

	command := rootcli.New(rootcli.BuildInfo{Version: "dev"})
	command.Writer, command.ErrWriter = io.Discard, io.Discard
	err = command.Run(t.Context(), append([]string{"forge-ci", "--provider", "forgejo", "--format", "json"}, buildArgs...))
	require.ErrorIs(t, err, errs.ErrValidation)
	require.ErrorContains(t, err, "source manifest differs from pinned digest")
	require.Empty(t, bins.Invocations("buildah"), "all prepared input pins precede any builder import or execution")
	require.NoDirExists(t, "layouts")
	require.NoError(t, os.Rename(directory, "rejected-parent"))
	require.NoError(t, os.Rename("saved-parent", directory))
}
