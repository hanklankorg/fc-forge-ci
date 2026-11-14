// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestGradleExamplePRMatchesJavaToolchain(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)
	consumer := "test/live/fixtures/consumers/hello-gradle/consumer/"
	build, err := os.ReadFile(filepath.Join(root, consumer, "build.gradle.kts"))
	require.NoError(t, err)

	version := regexp.MustCompile(`JavaLanguageVersion\.of\((\d+)\)`).FindSubmatch(build)
	require.Len(t, version, 2, "Gradle demo must declare its Java toolchain")
	require.Equal(t, "25", string(version[1]), "the Gradle example defaults to Java 25, matching the prepared Java runtime")

	for _, file := range []string{
		"examples/gradle-app/github/pullrequest-workflow.yml",
		consumer + "test/live/github/workflows/pullrequest.yml",
	} {
		t.Run(file, func(t *testing.T) {
			t.Parallel()

			body, err := os.ReadFile(filepath.Join(root, file))
			require.NoError(t, err)

			var workflow struct {
				Jobs map[string]struct {
					Steps []struct {
						Uses string
						With map[string]string
					}
				}
			}
			require.NoError(t, yaml.Unmarshal(body, &workflow))

			setups := 0

			for _, step := range workflow.Jobs["test"].Steps {
				if strings.HasPrefix(step.Uses, "actions/setup-java@") {
					setups++

					require.Equal(t, string(version[1]), step.With["java-version"], "PR must install the Java toolchain its Gradle build requires")
				}
			}

			require.Equal(t, 1, setups, "native Gradle test job must select Java explicitly")
		})
	}
}
