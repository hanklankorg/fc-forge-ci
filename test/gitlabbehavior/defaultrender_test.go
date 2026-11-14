// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/gitlabrender"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestComponentsRenderAValidConfigurationWithTheirDefaults(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(filepath.Join(reporoot.Path(t), "templates"))
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yml" {
			continue
		}

		path := "templates/" + entry.Name()
		t.Run(entry.Name(), func(t *testing.T) {
			t.Parallel()

			for _, problem := range gitlabrender.Problems(t, path, map[string]any{}) {
				t.Error(problem)
			}
		})
	}
}

func TestGitLabRenderProblemsFindsWhatGitLabRefuses(t *testing.T) {
	t.Parallel()

	body := map[string]any{
		"stages":     []any{"build"},
		".hidden":    map[string]any{"image": map[string]any{"name": ""}},
		"empty name": map[string]any{"extends": ".hidden", "stage": "build", "script": []any{"true"}},
		"empty":      map[string]any{"image": "", "stage": "build", "script": []any{"true"}},
		"orphan":     map[string]any{"extends": ".missing", "stage": "build", "script": []any{"true"}},
		"needy":      map[string]any{"stage": "build", "needs": []any{"nobody", map[string]any{"job": "gone", "optional": true}}, "script": []any{"true"}},
		"dropped":    map[string]any{"stage": "build", "rules": []any{map[string]any{"if": `"true" == "false"`}}, "script": []any{"true"}},
		"stranded":   map[string]any{"stage": "build", "needs": []any{"dropped"}, "script": []any{"true"}},
		"also gone":  map[string]any{"stage": "build", "rules": []any{map[string]any{"if": `"true" == "false"`}}, "needs": []any{"dropped"}, "script": []any{"true"}},
		"by chance":  map[string]any{"stage": "build", "needs": []any{map[string]any{"job": "dropped", "optional": true}}, "script": []any{"true"}},
		"lost":       map[string]any{"stage": "deploy", "script": []any{"true"}},
		"idle":       map[string]any{"stage": "build"},
	}

	want := []string{
		`job "empty name": image name is empty`, `job "empty": image name is empty`,
		`job "idle": no script, run or trigger`, `job "lost": stage "deploy" is not in stages`,
		`job "needy": needs unknown job "nobody"`, `job "orphan": extends unknown ".missing"`,
		`job "stranded": needs job "dropped", which its rules drop`,
	}
	if got := gitlabrender.ConfigProblems(body); !slices.Equal(got, want) {
		t.Fatalf("problems:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestReleaseWithoutReleaseImagesNeedsNoBuilderJobImage(t *testing.T) {
	t.Parallel()

	for _, builder := range []string{"buildah", "kaniko"} {
		if problems := gitlabrender.Problems(t, "templates/release.yml", map[string]any{"image-builder": builder}); len(problems) != 0 {
			t.Errorf("%s:\n%s", builder, strings.Join(problems, "\n"))
		}
	}

	problems := gitlabrender.Problems(t, "templates/release.yml", map[string]any{"release-images": true})
	if !slices.ContainsFunc(problems, func(problem string) bool {
		return strings.Contains(problem, `job "Images: build amd64": image name is empty`)
	}) {
		t.Fatalf("an empty builder-job-image with release images was accepted:\n%s", strings.Join(problems, "\n"))
	}

	builderImage := "registry.invalid/team/buildah-job@sha256:" + strings.Repeat("7", 64)
	if problems := gitlabrender.Problems(t, "templates/release.yml", map[string]any{"release-images": true, "builder-job-image": builderImage}); len(problems) != 0 {
		t.Fatalf("with its builder job image:\n%s", strings.Join(problems, "\n"))
	}
}

func TestReleaseWithImagesRendersAValidConfiguration(t *testing.T) {
	t.Parallel()

	builderImage := "registry.invalid/team/builder-job@sha256:" + strings.Repeat("7", 64)
	export := []any{`forge-ci container build --mode local --platform "$EXPORT_PLATFORM" --output-dir "dist/linux-$EXPORT_ARCH"`}

	for _, builder := range []string{"buildah", "kaniko"} {
		for _, exports := range []bool{false, true} {
			for _, config := range []string{"", ".forge-ci/artifacts.yml", ".forge-ci/release-images.yml", "deploy/artifacts.yml"} {
				inputs := map[string]any{
					"release-images": true, "image-builder": builder, "builder-job-image": builderImage,
					"artifacts-config": config, "release-platforms": "linux/amd64",
					"job-user": "65532:65532", "runtime-image-node": builderImage, "export-files": exports, "export": []any{},
				}
				if exports {
					inputs["export"] = export
				}

				if builder == "buildah" {
					inputs["build-runner-tags-linux-amd64"] = []any{"privileged"}
				}

				if problems := gitlabrender.Problems(t, "templates/release.yml", inputs); len(problems) != 0 {
					t.Errorf("%s, export-files %t, artifacts-config %q:\n%s", builder, exports, config, strings.Join(problems, "\n"))
				}
			}
		}
	}

	for _, config := range []string{"/etc/artifacts.yml", "it's.yml", "-artifacts.yml"} {
		if problems := gitlabrender.Problems(t, "templates/release.yml", map[string]any{
			"release-images": true, "builder-job-image": builderImage, "artifacts-config": config,
		}); len(problems) == 0 {
			t.Errorf("artifacts-config %q was accepted", config)
		}
	}
}
