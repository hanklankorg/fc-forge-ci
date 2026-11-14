// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/adapters/ghaoutput"
	appconfig "github.com/diggsweden/forge-ci/internal/app/config"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
)

// A fake sink accepts newlines in scalar values; the actual Forgejo output
// sink refuses them. Two images must cross the real multiline transport.
func TestEmitReleaseImagesPlan_MultipleNamesUseRunnerMultilineOutput(t *testing.T) {
	t.Parallel()

	in := pipeline.ReleaseImagesInput{ImageRepository: "ghcr.io/acme/app", Repository: "acme/app", ReleaseTag: "v1.2.3"}
	plan, err := pipeline.PlanReleaseImages([]pipeline.PlannedContainer{
		{Name: "steady", TagSuffix: "-steady", ContainerFile: "Containerfile", Context: ".", Platforms: "linux/amd64"},
		{Name: "changing", TagSuffix: "-changing", ContainerFile: "Containerfile", Context: ".", Platforms: "linux/amd64"},
	}, in)
	require.NoError(t, err)
	body, err := json.Marshal(plan)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "FORGEJO_OUTPUT")
	sink := ghaoutput.New(path)

	t.Cleanup(func() { require.NoError(t, sink.Close(t.Context())) })
	_, err = appconfig.EmitReleaseImagesPlan(t.Context(), sink, output.Annotator{}, appconfig.EmitReleaseImagesPlanInput{
		Images: in, ResolvedPlan: string(body),
	})
	require.NoError(t, err)
	transport, err := os.ReadFile(path)
	require.NoError(t, err)

	_, block, ok := strings.Cut(string(transport), "image-names<<")
	require.True(t, ok, "names must use the runner heredoc protocol")
	delimiter, value, ok := strings.Cut(block, "\n")
	require.True(t, ok)
	require.NotEmpty(t, delimiter)

	names := make([]string, 0, len(plan.Images))
	for _, image := range plan.Images {
		names = append(names, image.Name)
	}

	require.Equal(t, strings.Join(names, "\n")+"\n"+delimiter+"\n", value)
}
