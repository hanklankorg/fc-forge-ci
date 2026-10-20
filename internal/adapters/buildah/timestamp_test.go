// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package buildah_test

import (
	"os"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/buildah"
	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
)

func TestBuildTimestamp_SubprocessEnvironment(t *testing.T) {
	// The CLI obtains its request from this same inherited variable. Cover both
	// agreeing and disagreeing values: the request is authoritative either way.
	for _, inherited := range []string{"1700000000", "1792421899"} {
		t.Run(inherited, func(t *testing.T) {
			t.Setenv("SOURCE_DATE_EPOCH", inherited)
			t.Setenv("FORGE_CI_TIMESTAMP_TEST_KEEP", "keep")
			bins := mockbinary.New(t)
			bins.Add("buildah", `
printf 'ARGV %s\nEPOCH %s\nKEEP %s\nAUTH %s\n' "$*" "${SOURCE_DATE_EPOCH-<unset>}" "$FORGE_CI_TIMESTAMP_TEST_KEEP" "${REGISTRY_AUTH_FILE-<unset>}"
while [ "$#" -gt 0 ]; do
  if [ "$1" = --iidfile ]; then shift; printf 'sha256:test\n' > "$1"; fi
  shift
done
`)
			adapter := &buildah.Adapter{Bin: bins.Path("buildah"), Global: []string{"--storage-driver", "vfs"}}
			for _, mode := range []string{"load", "local", "layout", "manifest", "without-timestamp"} {
				t.Run(mode, func(t *testing.T) {
					req := container.BuildRequest{
						Context: ".", Mode: container.BuildModeLoad, ImageRef: "localhost/app:test",
						SourceDateEpoch: "1700000000", AuthFile: "/tmp/timestamp-auth.json",
						BuildArgs: []string{"VERSION=1.2.3"},
					}
					var log strings.Builder
					var err error
					switch mode {
					case "manifest":
						err = adapter.BuildManifest(t.Context(), container.BuildPushManifestBuildRequest{
							Context: req.Context, Containerfile: "Containerfile", Manifest: "localhost/app:list",
							Platform: "linux/amd64", SourceDateEpoch: req.SourceDateEpoch, AuthFile: req.AuthFile, BuildArgs: req.BuildArgs,
						}, &log)
					case "layout":
						req.Mode = container.BuildModePushByDigest
						err = adapter.BuildToLayout(t.Context(), req, t.TempDir(), &log)
					default:
						if mode == "local" {
							req.Mode, req.OutputDir = container.BuildModeLocal, t.TempDir()
						} else if mode == "without-timestamp" {
							req.SourceDateEpoch = ""
						}
						err = adapter.Build(t.Context(), req, &log)
					}
					if err != nil {
						t.Fatal(err)
					}
					// BuildToLayout also exports and cleans up; inspect just the build.
					lines := strings.Split(log.String(), "\n")
					wantEpoch := "EPOCH <unset>"
					if mode == "without-timestamp" {
						wantEpoch = "EPOCH " + inherited
						if strings.Contains(lines[0], "--timestamp") {
							t.Fatal("timestamp added to an unpinned build")
						}
					} else if !strings.Contains(lines[0], "--timestamp 1700000000") {
						t.Fatalf("timestamp missing: %s", lines[0])
					}
					for _, want := range []string{wantEpoch, "KEEP keep", "AUTH " + req.AuthFile} {
						if !strings.Contains(strings.Join(lines[:4], "\n"), want) {
							t.Errorf("missing %q in build subprocess: %s", want, log.String())
						}
					}
					if !strings.Contains(lines[0], "--build-arg VERSION=1.2.3") || strings.Contains(lines[0], "--source-date-epoch") {
						t.Fatalf("changed build arguments or timestamp mode: %s", lines[0])
					}
					if got := os.Getenv("SOURCE_DATE_EPOCH"); got != inherited {
						t.Fatalf("parent environment changed: %q", got)
					}
				})
			}
			// Non-build commands continue to inherit the caller's environment.
			got, err := adapter.InfoDriver(t.Context(), nil)
			if err != nil || !strings.Contains(got, "EPOCH "+inherited) {
				t.Fatalf("info environment: %q, %v", got, err)
			}
		})
	}
}
