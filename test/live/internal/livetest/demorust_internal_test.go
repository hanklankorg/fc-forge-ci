// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/require"
)

// The built-in demos use the prepared Rust runtime for native checks. A
// different channel makes rustup install another toolchain as the job user,
// even though the runtime already has everything these demos need. Keep the
// local Mise tools and container builders on that same compiler too.
func TestRustDemosMatchPreparedRuntime(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)
	runtime, err := os.ReadFile(filepath.Join(root, "containers", "runtime", "Containerfile"))
	require.NoError(t, err)

	version := regexp.MustCompile(`(?m)^ARG RUST_VERSION=(\d+\.\d+\.\d+)$`).FindSubmatch(runtime)
	require.Len(t, version, 2, "runtime must declare one exact Rust version")
	want := string(version[1])

	pins, err := filepath.Glob(filepath.Join(root, "test", "live", "fixtures", "consumers", "*", "consumer", "rust-toolchain.toml"))
	require.NoError(t, err)
	require.NotEmpty(t, pins, "no Rust demos checked")

	for _, pin := range pins {
		t.Run(filepath.Base(filepath.Dir(filepath.Dir(pin))), func(t *testing.T) {
			t.Parallel()

			consumer := filepath.Dir(pin)

			var toolchain struct {
				Toolchain struct {
					Channel string `toml:"channel"`
				} `toml:"toolchain"`
			}
			readRustDemoTOML(t, pin, &toolchain)
			require.Equal(t, want, toolchain.Toolchain.Channel, "rust-toolchain.toml would select a toolchain absent from the prepared runtime")

			var mise struct {
				Tools struct {
					Rust string `toml:"rust"`
				} `toml:"tools"`
			}
			readRustDemoTOML(t, filepath.Join(consumer, ".mise.toml"), &mise)
			require.Equal(t, want, mise.Tools.Rust, "Mise must select the demo's Rust compiler")

			var lock struct {
				Tools struct {
					Rust []struct {
						Version    string   `toml:"version"`
						Specifiers []string `toml:"specifiers"`
					} `toml:"rust"`
				} `toml:"tools"`
			}
			readRustDemoTOML(t, filepath.Join(consumer, "mise.lock"), &lock)
			require.Len(t, lock.Tools.Rust, 1)
			require.Equal(t, want, lock.Tools.Rust[0].Version, "Mise lock must match the demo's Rust compiler")
			require.Equal(t, []string{want}, lock.Tools.Rust[0].Specifiers)

			// A demo that declares no container has no builder to pin; one
			// that declares any must ship the Containerfile.
			declared, err := os.ReadFile(filepath.Join(consumer, DefaultArtifactsConfig))
			require.NoError(t, err)

			declaration, err := config.Parse(declared)
			require.NoError(t, err)

			if len(declaration.Containers) == 0 {
				return
			}

			container, err := os.ReadFile(filepath.Join(consumer, "Containerfile"))
			require.NoError(t, err)

			builder := regexp.MustCompile(`(?m)^FROM docker\.io/library/rust:(\d+\.\d+\.\d+)-slim-trixie@sha256:[a-f0-9]{64} AS builder$`).FindSubmatch(container)
			require.Len(t, builder, 2, "demo builder must keep an exact version and digest pin")
			require.Equal(t, want, string(builder[1]), "container builder must match the demo's Rust compiler; resolve its digest when updating")
		})
	}
}

func readRustDemoTOML(t *testing.T, path string, target any) {
	t.Helper()

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, toml.Unmarshal(body, target), path)
}
