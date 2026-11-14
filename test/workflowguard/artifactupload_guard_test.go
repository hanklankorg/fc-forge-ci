// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestUploadArtifactPathsAreNarrow scans every repository workflow and rejects
// an upload-artifact path that is not one of the narrow shapes they use.
func TestUploadArtifactPathsAreNarrow(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(reporoot.Path(t), ".github", "workflows")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	var (
		violations []string
		audited    int
	)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yml") {
			continue
		}

		body, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoErrorf(t, readErr, "read %s", entry.Name())

		audited++

		violations = append(violations, auditUploadArtifactPaths(t, entry.Name(), body)...)
	}

	// Without this, an empty or wrongly filtered directory reports no
	// violations and looks identical to a clean one.
	require.GreaterOrEqual(t, audited, 20, "audited %d workflows; the walk, not the tree, is what was measured", audited)

	require.Emptyf(t, violations, "upload-artifact path violations:\n  %s", strings.Join(violations, "\n  "))
}

// TestUploadPathViolation pins the allowed shapes and the ways out of them.
func TestUploadPathViolation(t *testing.T) {
	t.Parallel()

	const workingDirectory = "${{ inputs['working-directory'] }}"

	confined := map[string]bool{workingDirectory: true}

	for _, scoped := range []string{
		"dist/",
		"dist/**",
		".ci-results/*.json",
		"*-sbom.cyclonedx.json",
		"**/dist",
		"!dist/**",
		"!**/*.tmp",
		"${{ runner.temp }}/image-layout/",
		"${{ runner.temp }}/forge-ci-canonical-${{ github.run_id }}-${{ github.run_attempt }}/",
		workingDirectory + "/**/bom.json",
		"!" + workingDirectory + "/**/buildSrc/**",
		workingDirectory + "/${{ inputs['build-module'] }}/build/outputs/aar/*.aar",
	} {
		require.Emptyf(t, uploadPathViolation(scoped, confined), "scoped path %q was rejected", scoped)
	}

	for _, broad := range []string{
		".", "./", "./**/*", "**", "**/*", "*", "!**/*", "!!dist/**",
		"dist/..", "../secrets", "./dist/../release/**",
		"/tmp/output", "/var/**", `C:\runner\work`, "C:/runner/work", "~/cache",
		"$GITHUB_WORKSPACE", "$GITHUB_WORKSPACE/dist/**",
		"dist/[[:alnum:]]*.jar", "dist/{debug,release}/*.apk", "dist/@(debug|release)/*.apk",
		"dist/${{ inputs['build-module'] }}/*.jar",
		"${{ runner.temp }}", "${{ runner.temp }}/**",
		workingDirectory + "/**", workingDirectory + "/${{ inputs['build-module'] }}/**",
		workingDirectory + "/../secrets/**",
	} {
		require.Containsf(t, uploadPathViolation(broad, confined), "is over-broad", "unsafe path %q was accepted", broad)
	}

	for _, unproven := range []string{
		"${{ inputs['other-directory'] }}/dist/**",
		"${{ github.workspace }}/dist/**",
		"${{ inputs.artifact-path }}/x",
	} {
		require.Containsf(t, uploadPathViolation(unproven, confined), "nothing in this workflow proves", "unproven root %q was accepted", unproven)
	}
}

// TestUploadArtifactDynamicRootsNeedProof: a caller-supplied root earns the
// right to root an upload only from a step that refuses both escapes itself.
func TestUploadArtifactDynamicRootsNeedProof(t *testing.T) {
	t.Parallel()

	const upload = `      - name: Upload
        uses: actions/upload-artifact@v7
        with:
          path: |
            ${{ inputs['working-directory'] }}/dist/**
            /tmp/digests/*
`

	guard := func(run string) string {
		return `      - name: Guard working-directory
        env:
          WORKING_DIRECTORY: ${{ inputs['working-directory'] }}
        run: |
` + run
	}

	for _, testCase := range []struct {
		name string
		body string
		want int
	}{
		{"unproven caller root", upload, 2},
		{"root proved by the workflow", guard("          echo \"must be workspace-relative\"\n          echo \"must not contain parent-directory steps\"\n") + upload, 1},
		{"a guard that only claims to check proves nothing", guard("          echo \"checking working-directory\"\n") + upload, 2},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			violations := auditUploadArtifactPaths(t, "fixture.yml", []byte("jobs:\n  build:\n    steps:\n"+testCase.body))
			require.Len(t, violations, testCase.want, violations)
		})
	}
}

func auditUploadArtifactPaths(t *testing.T, fileName string, body []byte) []string {
	t.Helper()

	var doc uploadWorkflow
	require.NoErrorf(t, yaml.Unmarshal(body, &doc), "parse %s", fileName)

	confined := confinedUploadRoots(doc)

	var violations []string

	for jobName, job := range doc.Jobs {
		for stepIdx, step := range job.Steps {
			action, _, _ := strings.Cut(step.Uses, "@")
			if !strings.EqualFold(action, "actions/upload-artifact") {
				continue
			}

			for line := range strings.SplitSeq(step.With.Path, "\n") {
				path := strings.TrimSpace(line)

				// Fixed outputs produced in these jobs' temporary filesystems.
				// Do not generalize this exception to arbitrary absolute trees.
				if path == "" || (path == "/tmp/digests/*" && (fileName == "publish-container.yml" || fileName == "self-runtime-container.yml")) {
					continue
				}

				if violation := uploadPathViolation(path, confined); violation != "" {
					violations = append(violations, fmt.Sprintf("%s: job %s, step %d (%s): path=%q %s",
						fileName, jobName, stepIdx, step.Name, path, violation))
				}
			}
		}
	}

	return violations
}

// uploadExpression is one `${{ … }}` expression inside an upload path.
var uploadExpression = regexp.MustCompile(`\$\{\{[^}]*\}\}`)

// uploadPathViolation says why an upload path may reach beyond a narrow
// output, or "" for an allowed shape: a workspace-relative path, negated at
// most once, or one rooted at `${{ runner.temp }}` or at a caller value the
// workflow proves stays inside the workspace. Below such a root an expression
// stands for one unknown segment. Every path needs a segment that is more than
// a glob, and none may hold `.` or `..` steps, character classes, braces,
// extglobs, variables, absolute or home paths.
func uploadPathViolation(path string, confined map[string]bool) string {
	rest := strings.TrimPrefix(path, "!")

	if strings.HasPrefix(rest, "${{") {
		root, below, found := strings.Cut(rest, "}}/")
		if !found {
			return "is over-broad"
		}

		if root += "}}"; root != "${{ runner.temp }}" && !confined[root] {
			return "is rooted at " + root + ", which nothing in this workflow proves stays inside the workspace"
		}

		rest = uploadExpression.ReplaceAllString(below, "*")
	}

	if rest == "" || strings.HasPrefix(rest, "/") || strings.ContainsAny(rest, `$~\:![]{}()`) {
		return "is over-broad"
	}

	static := false

	for segment := range strings.SplitSeq(strings.TrimSuffix(rest, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "is over-broad"
		}

		static = static || strings.Trim(segment, "*?") != ""
	}

	if !static {
		return "is over-broad"
	}

	return ""
}

// confinedUploadRoots returns the expressions this workflow proves confined.
// A dynamic upload root is a caller-supplied value: accepting it because it has
// a static suffix only proves the leaf, never the root it hangs off, so the
// workflow has to refuse an escaping value itself before anything consumes it.
func confinedUploadRoots(doc uploadWorkflow) map[string]bool {
	confined := map[string]bool{}

	for _, job := range doc.Jobs {
		for _, step := range job.Steps {
			// A guard step earns its input the right to root an upload by
			// refusing both ways out of the workspace. A step that only says it
			// checks something proves nothing.
			if !strings.Contains(step.Run, "must be workspace-relative") ||
				!strings.Contains(step.Run, "must not contain parent-directory steps") {
				continue
			}

			for _, value := range step.Env {
				confined[strings.TrimSpace(value)] = true
			}
		}
	}

	return confined
}

type uploadWorkflow struct {
	Jobs map[string]uploadJob `yaml:"jobs"`
}

type uploadJob struct {
	Steps []uploadStep `yaml:"steps"`
}

type uploadStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
	With struct {
		Path string `yaml:"path"`
	} `yaml:"with"`
}
