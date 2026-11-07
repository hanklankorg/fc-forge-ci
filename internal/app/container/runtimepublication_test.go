// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/stretchr/testify/require"
)

func TestRuntimeTargetProjectionReusesCompletePlanAndMetadataAdmission(t *testing.T) {
	t.Parallel()

	for _, scenario := range []string{"valid", "platform mismatch", "unselected target", "unselected arch", "invalid other row", "bad final metadata", "missing final metadata"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()

			roster := `[{"target":"go","name":"runtime-go","archs":["amd64","arm64"]}]`
			if scenario == "invalid other row" {
				roster = strings.TrimSuffix(roster, "]") + `,{"target":"other","name":"runtime-other","archs":["s390x"]}]`
			}

			path := filepath.Join(root, "images.json")
			require.NoError(t, os.WriteFile(path, []byte(roster), 0o600))
			in := appcontainer.RuntimePublicationInput{
				RosterFile: path, PlatformMatrix: `[{"platform":"linux/amd64","suffix":"amd64"},{"platform":"linux/arm64","suffix":"arm64"}]`,
				Namespace: "registry.example/acme", SourceSHA: strings.Repeat("a", 40), CheckedOutSHA: strings.Repeat("a", 40), RunID: "123-1", Ref: "refs/heads/main",
			}
			target, selected := "go", ""

			switch scenario {
			case "platform mismatch":
				in.PlatformMatrix = `[{"platform":"linux/arm64","suffix":"amd64"}]`
			case "unselected target":
				target = "other"
			case "unselected arch":
				selected = "s390x"
			}

			for _, arch := range []string{"amd64", "arm64"} {
				if scenario == "missing final metadata" && arch == "arm64" {
					continue
				}

				tag := "registry.example/acme/runtime-go:staging-123-1-aaaaaaaaaaaa-" + arch
				if scenario == "bad final metadata" && arch == "arm64" {
					tag = "registry.example/acme/runtime-go:stale"
				}

				dir := filepath.Join(root, "runtime-go-arch-"+arch)
				require.NoError(t, os.Mkdir(dir, 0o700))

				body := fmt.Sprintf(`{"arch":%q,"tag":%q,"digest":"sha256:%s","ref":"registry.example/acme/runtime-go@sha256:%s"}`, arch, tag, strings.Repeat("b", 64), strings.Repeat("b", 64))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "runtime-go-"+arch+".json"), []byte(body), 0o600))
			}

			var out bytes.Buffer

			err := appcontainer.PlanRuntimeTarget(t.Context(), &out, in, target, selected, root)
			if scenario != "valid" {
				require.Error(t, err)
				require.Empty(t, out.String(), "a rejected late candidate must not emit an earlier projection")

				return
			}

			require.NoError(t, err)

			var got struct {
				Repository string `json:"repository"`
				Candidates []struct {
					Arch string `json:"arch"`
					Ref  string `json:"ref"`
				} `json:"candidates"`
			}
			require.NoError(t, json.Unmarshal(out.Bytes(), &got))
			require.Equal(t, "registry.example/acme/runtime-go", got.Repository)
			require.Len(t, got.Candidates, 2)
		})
	}
}

func TestRuntimePublicationPlanPublishesOnlyAfterCompleteAdmission(t *testing.T) {
	t.Parallel()

	const roster = `[{"target":"go","name":"runtime-go","archs":["amd64","arm64"]}]`
	for _, test := range []struct {
		name, roster, matrix string
		refused              bool
	}{
		{"valid", roster, `[{"platform":"linux/arm64","suffix":"arm64"}]`, false},
		{"unknown field", `[{"target":"go","name":"runtime-go","archs":["arm64"],"unexpected":true}]`, `[]`, true},
		{"trailing roster", roster + ` []`, `[]`, true},
		{"trailing platforms", roster, `[] []`, true},
		{"platform mismatch", roster, `[{"platform":"linux/amd64","suffix":"arm64"}]`, true},
		{"null matrix", roster, `null`, true},
		{"empty matrix", roster, `[]`, true},
		{"non-array matrix", roster, `{}`, true},
		{"malformed matrix", roster, `invalid-json`, true},
		{"unsupported platform", roster, `[{"platform":"linux/s390x","suffix":"s390x"}]`, true},
		{"duplicate platforms", roster, `[{"platform":"linux/amd64","suffix":"amd64"},{"platform":"linux/amd64","suffix":"amd64"}]`, true},
		{"mixed platforms", roster, `[{"platform":"linux/amd64","suffix":"amd64"},{"platform":"linux/armv7","suffix":"armv7"}]`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "images.json")
			require.NoError(t, os.WriteFile(path, []byte(test.roster), 0o600))
			sink := fakeoutputsink.New(t)
			sha := strings.Repeat("a", 40)

			err := appcontainer.PlanRuntimePublication(t.Context(), sink, appcontainer.RuntimePublicationInput{
				RosterFile: path, PlatformMatrix: test.matrix, Namespace: "registry.example/acme",
				SourceSHA: sha, CheckedOutSHA: sha, RunID: "1-1", Ref: "refs/heads/main",
			})
			if test.refused {
				require.Error(t, err)
				require.Empty(t, sink.Keys())

				return
			}

			require.NoError(t, err)
			require.JSONEq(t, `{"include":[{"target":"go","name":"runtime-go","arch":"arm64"}]}`, sink.Single("build-matrix"))
			require.JSONEq(t, `{"include":[{"target":"go","name":"runtime-go","archs":["arm64"]}]}`, sink.Single("image-matrix"))
		})
	}
}

func TestRuntimePublicationPlanCancellationPrecedesReadsAndOutputs(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	sink := fakeoutputsink.New(t)
	err := appcontainer.PlanRuntimePublication(ctx, sink, appcontainer.RuntimePublicationInput{RosterFile: "must-not-be-opened"})
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, sink.Keys())
}

// The product roster is intentionally tested separately from generic domain
// admission: adding a product target must not require changing engine policy.
func TestRuntimePublicationProductRosterNativeCounts(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, matrix   string
		builds, images int
	}{
		{"amd64", `[{"platform":"linux/amd64","suffix":"amd64"}]`, 7, 7},
		{"arm64", `[{"platform":"linux/arm64","suffix":"arm64"}]`, 6, 6},
		{"both", `[{"platform":"linux/amd64","suffix":"amd64"},{"platform":"linux/arm64","suffix":"arm64"}]`, 13, 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			sink := fakeoutputsink.New(t)
			sha := strings.Repeat("a", 40)

			err := appcontainer.PlanRuntimePublication(t.Context(), sink, appcontainer.RuntimePublicationInput{
				RosterFile:     filepath.Join(reporoot.Path(t), "containers/runtime/images.json"),
				PlatformMatrix: test.matrix, Namespace: "registry.example/acme",
				SourceSHA: sha, CheckedOutSHA: sha, RunID: "123-1", Ref: "refs/heads/main",
			})
			require.NoError(t, err)

			for _, expected := range []struct {
				key   string
				count int
			}{{"build-matrix", test.builds}, {"image-matrix", test.images}} {
				var matrix struct {
					Include []struct {
						Target string `json:"target"`
						Arch   string `json:"arch"`
					} `json:"include"`
				}

				require.NoError(t, json.Unmarshal([]byte(sink.Single(expected.key)), &matrix))
				require.Len(t, matrix.Include, expected.count)

				for _, leg := range matrix.Include {
					if expected.key == "build-matrix" && leg.Target == "android" {
						require.Equal(t, "amd64", leg.Arch)
					}
				}
			}
		})
	}
}
