// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/stretchr/testify/require"
)

func runtimePlanInput() container.RuntimePublicationInput {
	sha := strings.Repeat("a", 40)

	return container.RuntimePublicationInput{
		Images: []container.RuntimeImage{
			{Target: "minimal", Name: "other-runtime", Archs: []string{"amd64", "arm64"}},
			{Target: "tools", Name: "other-tools", Archs: []string{"amd64"}},
		},
		Architectures: []string{"amd64", "arm64"}, Namespace: "registry.example:5000/acme",
		SourceSHA: sha, CheckedOutSHA: sha, RunID: "42-2", Ref: "refs/heads/main",
	}
}

func TestRuntimePublicationPlanSupportsProductRostersAndNativeSubsets(t *testing.T) {
	t.Parallel()

	in := runtimePlanInput()
	in.Architectures = []string{"arm64"}
	plan, err := container.PlanRuntimePublication(in)
	require.NoError(t, err)
	require.Equal(t, "staging-42-2-aaaaaaaaaaaa", plan.CandidateTag)
	require.Equal(t, []container.RuntimeBuildLeg{{Target: "minimal", Name: "other-runtime", Arch: "arm64"}}, plan.Builds)
	require.Equal(t, []container.RuntimeImage{{Target: "minimal", Name: "other-runtime", Archs: []string{"arm64"}}}, plan.Images)
	plan.Images[0].Archs[0] = "changed"
	require.Equal(t, []string{"amd64", "arm64"}, in.Images[0].Archs)
}

func TestRuntimePublicationPlanRefusesIncompleteOrAmbiguousAuthority(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(*container.RuntimePublicationInput){
		"source mismatch":         func(in *container.RuntimePublicationInput) { in.CheckedOutSHA = strings.Repeat("b", 40) },
		"short source":            func(in *container.RuntimePublicationInput) { in.SourceSHA = "abc" },
		"not main":                func(in *container.RuntimePublicationInput) { in.Ref = "refs/heads/feature" },
		"invalid run":             func(in *container.RuntimePublicationInput) { in.RunID = "42\n2" },
		"namespace credentials":   func(in *container.RuntimePublicationInput) { in.Namespace = "user:secret@registry.example/acme" },
		"namespace traversal":     func(in *container.RuntimePublicationInput) { in.Namespace = "registry.example/acme/../other" },
		"empty roster":            func(in *container.RuntimePublicationInput) { in.Images = nil },
		"empty selection":         func(in *container.RuntimePublicationInput) { in.Architectures = nil },
		"foreign architecture":    func(in *container.RuntimePublicationInput) { in.Architectures = []string{"armv7"} },
		"duplicate selection":     func(in *container.RuntimePublicationInput) { in.Architectures = []string{"arm64", "arm64"} },
		"duplicate target":        func(in *container.RuntimePublicationInput) { in.Images[1].Target = in.Images[0].Target },
		"duplicate image":         func(in *container.RuntimePublicationInput) { in.Images[1].Name = in.Images[0].Name },
		"target path":             func(in *container.RuntimePublicationInput) { in.Images[0].Target = "../outside" },
		"empty declared legs":     func(in *container.RuntimePublicationInput) { in.Images[0].Archs = nil },
		"duplicate declared legs": func(in *container.RuntimePublicationInput) { in.Images[0].Archs = []string{"amd64", "amd64"} },
		"invalid unselected row": func(in *container.RuntimePublicationInput) {
			in.Architectures = []string{"arm64"}
			in.Images[1].Archs = []string{"invalid"}
		},
		"empty result": func(in *container.RuntimePublicationInput) {
			in.Images = in.Images[1:]
			in.Architectures = []string{"arm64"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			in := runtimePlanInput()
			mutate(&in)
			plan, err := container.PlanRuntimePublication(in)
			require.Error(t, err)
			require.Empty(t, plan.Builds)
			require.Empty(t, plan.Images)
		})
	}
}
