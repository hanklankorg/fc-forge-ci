// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGitLabConsumerPreservesChecksAndChangesOnlyTrialCoordinates(t *testing.T) {
	t.Parallel()

	pipeline := `# Consumer licence header.
workflow:
  rules:
    - if: '$CI_PIPELINE_SOURCE == "merge_request_event"'
include:
  - component: $CI_SERVER_FQDN/team/engine/check@` + strings.Repeat("a", 40) + `
    inputs:
      engine-project: team/engine
      job-image: $CI_JOB_IMAGE
      commands:
        - 'nanolinter verify --require secrets,sast'
        - 'go test -race ./...'
`
	layer := Layer{ServerURL: "https://gitlab.k3s.forgelab", Repository: "forgeci/fci-engine", Commit: strings.Repeat("b", 40)}
	image := "registry.gitlab.k3s.forgelab/forgeci/fci-engine@sha256:" + strings.Repeat("c", 64)

	rendered, err := renderGitLabConsumer([]byte(pipeline), layer, image)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(rendered, "# Consumer licence header.") {
		t.Fatal("rendering lost the consumer's licence header")
	}

	var parsed struct {
		Include []struct {
			Component string `yaml:"component"`
			Inputs    struct {
				EngineProject string   `yaml:"engine-project"`
				JobImage      string   `yaml:"job-image"`
				Commands      []string `yaml:"commands"`
			} `yaml:"inputs"`
		} `yaml:"include"`
	}
	if err := yaml.Unmarshal([]byte(rendered), &parsed); err != nil {
		t.Fatal(err)
	}

	entry := parsed.Include[0]
	if entry.Component != "gitlab.k3s.forgelab/forgeci/fci-engine/check@"+layer.Commit ||
		entry.Inputs.EngineProject != layer.Repository || entry.Inputs.JobImage != image {
		t.Fatal("the trial did not bind all three coordinates")
	}

	commands := entry.Inputs.Commands
	if len(commands) != 2 || commands[0] != "nanolinter verify --require secrets,sast" || commands[1] != "go test -race ./..." {
		t.Fatal("the trial changed the consumer's check commands")
	}

	for _, bad := range []string{
		pipeline + "\n---\nunexpected: job\n",
		strings.ReplaceAll(pipeline, strings.Repeat("a", 40), "main"),
		strings.ReplaceAll(pipeline, "engine-project: team/engine", "engine-project: other/engine"),
		strings.ReplaceAll(pipeline, "job-image: $CI_JOB_IMAGE", "job-image: unreviewed:latest"),
	} {
		if _, err := renderGitLabConsumer([]byte(bad), layer, image); err == nil {
			t.Fatal("a mutable or inconsistent trial input was accepted")
		}
	}
}
