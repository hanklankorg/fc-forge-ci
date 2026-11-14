// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/gitlabrender"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// unpinnedImageInputs are the image inputs a job may run without a digest,
// each with its reason.
var unpinnedImageInputs = map[string]string{
	// A macOS runner image is a VM image the runner names, not an OCI
	// reference; there is no digest to pin.
	"publish-apple-appstore.yml macos-image": "a runner VM image name",
}

// A job's container receives the checkout, the CI variables and the job
// token, so every input that names a job's image admits only a digest pin,
// as GitLab checks it before any job runs: the image that runs is the one
// reviewed. runtime-image once took any text, though its description said
// "pinned by @sha256".
func TestImageInputsAdmitOnlyDigestPins(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(filepath.Join(reporoot.Path(t), "templates"))
	if err != nil {
		t.Fatal(err)
	}

	pinned := "registry.example/team/app@sha256:" + strings.Repeat("a", 64)
	seen := 0

	for _, entry := range entries {
		if entry.IsDir() || !isYAML(entry.Name()) {
			continue
		}

		path := "templates/" + entry.Name()
		spec := gitlabrender.InputSpec(t, path)

		for _, name := range jobImageInputs(t, path) {
			seen++

			if reason, excepted := unpinnedImageInputs[entry.Name()+" "+name]; excepted {
				t.Logf("%s: %s may be unpinned: %s", path, name, reason)

				continue
			}

			input, declared := spec[name]
			pattern, _ := input["regex"].(string)

			if !declared || pattern == "" {
				t.Errorf("%s: input %s names a job image and declares no regex; admit only a digest pin", path, name)

				continue
			}

			admits := regexp.MustCompile(pattern).MatchString
			if !admits(pinned) {
				t.Errorf("%s: input %s refuses the digest pin %s", path, name, pinned)
			}

			for _, unpinned := range []string{"registry.example/team/app:1.0", "registry.example/team/app", "registry.example/team/app@sha256:abc"} {
				if admits(unpinned) {
					t.Errorf("%s: input %s admits %s, which no digest pins", path, name, unpinned)
				}
			}
		}
	}

	if seen == 0 {
		t.Fatal("no component names a job image from an input; the guard has drifted")
	}
}

// jobImageInputs are the inputs a component's jobs take their image from.
func jobImageInputs(t *testing.T, path string) []string {
	t.Helper()

	_, document, found := strings.Cut(string(reporoot.ReadFile(t, path)), "\n---\n")
	if !found {
		t.Fatalf("%s has no spec", path)
	}

	var parsed any
	if err := yaml.Unmarshal([]byte(document), &parsed); err != nil {
		t.Fatalf("%s: %v", path, err)
	}

	var names []string

	var walk func(node any)

	walk = func(node any) {
		switch value := node.(type) {
		case map[string]any:
			for key, child := range value {
				if key == "image" {
					reference := child
					if image, ok := child.(map[string]any); ok {
						reference = image["name"]
					}

					if text, ok := reference.(string); ok {
						names = append(names, gitlabrender.InputReferences(text)...)
					}
				}

				walk(child)
			}
		case []any:
			for _, child := range value {
				walk(child)
			}
		}
	}

	walk(parsed)

	return names
}
