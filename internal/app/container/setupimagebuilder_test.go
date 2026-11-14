// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
)

func TestSetupImageBuilderAdmitsKanikoWithoutBuildahEffects(t *testing.T) {
	t.Parallel()

	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			t.Parallel()

			digest := "sha256:" + strings.Repeat("a", 64)
			registry := &fakeUsableImageRegistry{digest: digest, arch: arch, labels: map[string]string{
				"org.opencontainers.image.source": "https://forge.example/owner/engine",
				"org.opencontainers.image.title":  "forge-ci kaniko job image",
			}}
			sink := fakeoutputsink.New(t)

			err := appcontainer.SetupImageBuilder(t.Context(), nil, nil, registry, sink, nil, io.Discard, appcontainer.SetupImageBuilderInput{
				Builder: "kaniko", JobImage: "registry.example/owner/kaniko@" + digest,
				EngineRepository: "https://forge.example/owner/engine.git", Arch: arch,
			})
			if err != nil {
				t.Fatal(err)
			}

			if len(registry.refs) != 1 || registry.archs[0] != arch || sink.Single("digest") != digest {
				t.Fatalf("native image inspection/output mismatch: %+v", registry)
			}
		})
	}
}

func TestSetupImageBuilderRefusesInvalidInputsBeforeEffects(t *testing.T) {
	t.Parallel()

	// What the refusal says, where a policy author reads it to fix the input.
	messages := map[string]string{
		"unknown builder":                      `image-builder must be buildah or kaniko, got "docker"`,
		"missing image":                        "kaniko needs CI_IMAGE_BUILDER_JOB_IMAGE pinned by digest",
		"mutable tag":                          "kaniko needs CI_IMAGE_BUILDER_JOB_IMAGE pinned by digest",
		"GitLab Buildah requires image":        `buildah needs builder-job-image pinned by digest, got ""`,
		"GitLab Buildah refuses mutable image": `buildah needs builder-job-image pinned by digest, got "registry.example/buildah:latest"`,
	}

	for name, change := range map[string]func(*appcontainer.SetupImageBuilderInput){
		"unknown builder": func(in *appcontainer.SetupImageBuilderInput) { in.Builder = "docker" },
		"missing image":   func(in *appcontainer.SetupImageBuilderInput) { in.JobImage = "" },
		"GitLab Buildah requires image": func(in *appcontainer.SetupImageBuilderInput) {
			in.Builder = "buildah"
			in.JobImage = ""
			in.RequireJobImage = true
		},
		"GitLab Buildah refuses mutable image": func(in *appcontainer.SetupImageBuilderInput) {
			in.Builder = "buildah"
			in.JobImage = "registry.example/buildah:latest"
			in.RequireJobImage = true
		},
		"mutable tag":      func(in *appcontainer.SetupImageBuilderInput) { in.JobImage = "registry.example/owner/kaniko:latest" },
		"newline image":    func(in *appcontainer.SetupImageBuilderInput) { in.JobImage += "\n" },
		"unsupported arch": func(in *appcontainer.SetupImageBuilderInput) { in.Arch = "riscv64" },
		"missing source":   func(in *appcontainer.SetupImageBuilderInput) { in.EngineRepository = "" },
		"source credentials": func(in *appcontainer.SetupImageBuilderInput) {
			in.EngineRepository = "https://secret@forge.example/owner/engine"
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			in := appcontainer.SetupImageBuilderInput{Builder: "kaniko", JobImage: "registry.example/owner/kaniko@sha256:" + strings.Repeat("a", 64), EngineRepository: "https://forge.example/owner/engine", Arch: "amd64"}
			change(&in)

			registry := &fakeUsableImageRegistry{}

			err := appcontainer.SetupImageBuilder(t.Context(), nil, nil, registry, nil, nil, io.Discard, in)
			if !errors.Is(err, errs.ErrUsage) || len(registry.refs) != 0 {
				t.Fatalf("invalid selection reached effects: err=%v refs=%v", err, registry.refs)
			}

			if !strings.Contains(err.Error(), messages[name]) {
				t.Fatalf("err = %v, want it to say %q", err, messages[name])
			}
		})
	}
}

func TestSetupImageBuilderKeepsExistingImageAdmissionRefusals(t *testing.T) {
	t.Parallel()

	for _, scenario := range []string{"wrong arch", "wrong source", "wrong title", "registry unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()

			registry := &fakeUsableImageRegistry{digest: "sha256:" + strings.Repeat("a", 64), arch: "amd64", labels: map[string]string{
				"org.opencontainers.image.source": "https://forge.example/owner/engine",
				"org.opencontainers.image.title":  "forge-ci kaniko job image",
			}}

			jobImage := "registry.example/owner/kaniko@" + registry.digest
			want := ""

			switch scenario {
			case "wrong arch":
				registry.arch = "arm64"
				want = "existing image is not reusable: " + jobImage + `: architecture "arm64" does not match required "amd64"`
			case "wrong source":
				registry.labels["org.opencontainers.image.source"] = "https://forge.example/other/engine"
				want = "existing image is not reusable: " + jobImage + ": label org.opencontainers.image.source does not match required value"
			case "wrong title":
				registry.labels["org.opencontainers.image.title"] = "other image"
				want = "existing image is not reusable: " + jobImage + ": label org.opencontainers.image.title does not match required value"
			case "registry unavailable":
				registry.err = io.ErrUnexpectedEOF
			}

			err := appcontainer.SetupImageBuilder(t.Context(), nil, nil, registry, nil, nil, io.Discard, appcontainer.SetupImageBuilderInput{
				Builder: "kaniko", JobImage: jobImage,
				EngineRepository: "https://forge.example/owner/engine", Arch: "amd64",
			})
			if err == nil || (scenario == "registry unavailable" && !errors.Is(err, io.ErrUnexpectedEOF)) {
				t.Fatalf("admission refusal was lost: %v", err)
			}

			if !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v, want it to say %q", err, want)
			}
		})
	}
}

func TestSetupImageBuilderReusesBuildahStorageAndCancellation(t *testing.T) {
	t.Parallel()

	scratch := t.TempDir()
	tool := &fakeBuildahSetupTool{commands: map[string]bool{"buildah": true, "fuse-overlayfs": true}}
	registry := &fakeUsableImageRegistry{}
	conf := filepath.Join(scratch, "storage.conf")

	err := appcontainer.SetupImageBuilder(t.Context(), tool, nil, registry, fakeoutputsink.New(t), nil, io.Discard, appcontainer.SetupImageBuilderInput{
		Buildah: appcontainer.SetupBuildahInput{ProbeBuild: true, RunnerTemp: scratch, StorageConf: conf, EnvFile: filepath.Join(scratch, "env")},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(conf); err != nil || len(tool.probeCalls) == 0 || len(registry.refs) != 0 {
		t.Fatalf("Buildah delegation/storage isolation changed: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := appcontainer.SetupImageBuilder(ctx, nil, nil, nil, nil, nil, io.Discard, appcontainer.SetupImageBuilderInput{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation must precede effects: %v", err)
	}
}

// An empty builder is Buildah, and the refusal names it.
func TestSetupImageBuilderNamesTheDefaultBuilder(t *testing.T) {
	t.Parallel()

	err := appcontainer.SetupImageBuilder(t.Context(), nil, nil, &fakeUsableImageRegistry{}, nil, nil, io.Discard, appcontainer.SetupImageBuilderInput{RequireJobImage: true})
	if !errors.Is(err, errs.ErrUsage) || !strings.HasPrefix(err.Error(), "buildah needs builder-job-image") {
		t.Fatalf("err = %v, want a usage error naming buildah", err)
	}
}
