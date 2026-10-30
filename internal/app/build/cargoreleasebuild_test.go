// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	appbuild "github.com/diggsweden/forge-ci/internal/app/build"
)

var errCycloneDXFailed = errors.New("cyclonedx failed")

// cargoSubcommands returns each recorded cargo invocation's first arg.
func cargoSubcommands(calls []appbuild.CargoRunInput) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		if len(c.Args) > 0 {
			out = append(out, c.Args[0])
		}
	}

	return out
}

func TestCargoReleaseBuild_RunsStepsInOrder(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		skipTests bool
		sbom      bool
		want      []string
	}{
		{
			name: "full sequence",
			sbom: true,
			want: []string{"metadata", "fetch", "test", "cyclonedx", "build"},
		},
		{
			name:      "skipping tests removes only the test step",
			skipTests: true,
			sbom:      true,
			want:      []string{"metadata", "fetch", "cyclonedx", "build"},
		},
		{
			name: "disabling the SBOM removes only the SBOM step",
			want: []string{"metadata", "fetch", "test", "build"},
		},
		{
			name:      "both skipped",
			skipTests: true,
			want:      []string{"metadata", "fetch", "build"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tool := &fakeCargoTool{metadata: sampleCargoMetadata, binaryNames: []string{"hello"}}

			var out bytes.Buffer

			err := appbuild.CargoReleaseBuild(context.Background(), &recordingSummarySink{}, tool, &out, &out, appbuild.CargoReleaseBuildInput{
				Dir: t.TempDir(), SkipTests: tc.skipTests, EnableBuildSBOM: tc.sbom,
				Version:   "1.2.3",
				Platforms: "linux/amd64",
			})
			if tc.skipTests {
				if err == nil || len(tool.calls) != 0 {
					t.Fatalf("test opt-out: err=%v calls=%v", err, tool.calls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}

			// The resolved metadata is what the build goes on to compile and
			// name its binaries with, so the log states it before any step.
			if want := "Cargo release build: binary=hello package=hello version=1.2.3\n"; !strings.HasPrefix(out.String(), want) {
				t.Errorf("output = %q, want it to open with %q", out.String(), want)
			}

			// The exact sequence. The previous assertion searched a joined
			// string for each subcommand, so it saw neither order nor
			// repetition -- and its skip cases were satisfied by a run in
			// which nothing happened at all.
			if got := cargoSubcommands(tool.calls); !slices.Equal(got, tc.want) {
				t.Errorf("cargo subcommands = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCargoReleaseBuild_UsesTheLockfileAsWritten asserts --locked on every
// step that resolves dependencies. Without it cargo may update Cargo.lock
// mid-release, so the artifact would not correspond to the committed
// lockfile -- and the build would still succeed, which is why this needs a
// test rather than a reviewer.
func TestCargoReleaseBuild_UsesTheLockfileAsWritten(t *testing.T) {
	t.Parallel()

	tool := &fakeCargoTool{metadata: sampleCargoMetadata, binaryNames: []string{"hello"}}

	var out bytes.Buffer

	err := appbuild.CargoReleaseBuild(context.Background(), &recordingSummarySink{}, tool, &out, &out, appbuild.CargoReleaseBuildInput{
		Dir:       t.TempDir(),
		Version:   "1.2.3",
		Platforms: "linux/amd64",
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, cfg := range tool.calls {
		if len(cfg.Args) == 0 || cfg.Args[0] == "metadata" {
			continue // metadata resolves nothing
		}

		if !slices.Contains(cfg.Args, "--locked") {
			t.Errorf("cargo %v ran without --locked", cfg.Args)
		}
	}
}

func TestCargoReleaseBuild_BuildSBOMFailureBlocksRelease(t *testing.T) {
	t.Parallel()

	tool := &fakeCargoTool{
		metadata:    sampleCargoMetadata,
		binaryNames: []string{"hello"},
		failOn:      "cyclonedx",
		runErr:      errCycloneDXFailed,
	}
	summary := &recordingSummarySink{}
	dir := t.TempDir()

	err := appbuild.CargoReleaseBuild(context.Background(), summary, tool, &bytes.Buffer{}, &bytes.Buffer{}, appbuild.CargoReleaseBuildInput{
		Dir: dir, EnableBuildSBOM: true,
		Version:   "1.2.3",
		Platforms: "linux/amd64",
	})
	if !errors.Is(err, errCycloneDXFailed) {
		t.Fatalf("Build SBOM error = %v, want wrapped tool error", err)
	}

	if !strings.Contains(summary.buf.String(), "release blocked") {
		t.Fatalf("failure summary = %q, want blocked release", summary.buf.String())
	}

	if len(tool.calls) != 4 || tool.calls[3].Args[0] != "cyclonedx" {
		t.Fatalf("work continued after the SBOM failure: %v", tool.calls)
	}

	if _, err := os.Stat(filepath.Join(dir, "dist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failure produced dist: %v", err)
	}
}
