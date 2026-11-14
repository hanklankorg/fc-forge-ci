// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	gitlabapi "gitlab.com/gitlab-org/api/client-go"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
)

// GitLabRecoveryCheckpoint names the policy-owned interruption gate.
const GitLabRecoveryCheckpoint = "Fixture: candidate checkpoint"

// GitLabRecoveryPauseVariable controls the disposable checkpoint job.
const GitLabRecoveryPauseVariable = "FIXTURE_PAUSE_AFTER_CANDIDATE"

const (
	recoveryPublisher = "Images: publish"
	recoveryArtifacts = "artifacts"
)

var errRecovery = errors.New("recovery contract violated")

// GitLabRecoveryObservation recognizes the current DAG's interruption boundary.
// A running consumer build is not a checkpoint, even if it wrote a registry tag.
func GitLabRecoveryObservation(jobs []*gitlabapi.Job) (int64, int64, error) {
	var publisher, checkpoint int64

	buildSucceeded := false

	for _, job := range jobs {
		if err := checkRecoveryJob(job); err != nil {
			return 0, 0, err
		}

		switch job.Name {
		case "Build: release files":
			buildSucceeded = job.Status == runSuccess
		case recoveryPublisher:
			if job.Status == runSuccess {
				publisher = job.ID
			}
		case GitLabRecoveryCheckpoint:
			if job.Status == runSuccess {
				return 0, 0, fmt.Errorf("checkpoint completed without controller cancellation: %w", errRecovery)
			}

			if job.Status == "running" {
				checkpoint = job.ID
			}
		}
	}

	if publisher == 0 || checkpoint == 0 || !buildSucceeded {
		return 0, 0, nil
	}

	return publisher, checkpoint, nil
}

func checkRecoveryJob(job *gitlabapi.Job) error {
	if job.Status == "failed" || job.Status == "canceled" {
		return fmt.Errorf("consumer failed before the interruption checkpoint: %s: %w", job.Name, errRecovery)
	}

	if (strings.HasPrefix(job.Name, "Sign:") || strings.HasPrefix(job.Name, "Publish:")) && job.Status != "created" && job.Status != "skipped" {
		return fmt.Errorf("downstream job %s advanced before checkpoint cancellation: %s: %w", job.Name, job.Status, errRecovery)
	}

	return nil
}

// GitLabRecoveryParent extends the captured engine parent, rather than retaining
// a second copy of its inputs, forwarding policy or child DAG. The wrapper is a
// component in the independently pinned test policy project. component.sha and
// component.reference must still identify the engine, not that wrapper project.
// extensionRef pins a separate, immutable commit containing only the test gate.
func GitLabRecoveryParent(parent []byte, engineSHA, policyProject, extensionRef string) ([]byte, error) {
	parent = bytes.ReplaceAll(parent, []byte("$[[ component.sha ]]"), []byte(engineSHA))
	parent = bytes.ReplaceAll(parent, []byte("$[[ component.reference ]]"), []byte(engineSHA))
	decoder := yaml.NewDecoder(bytes.NewReader(parent))

	var spec, jobs map[string]any
	if err := decoder.Decode(&spec); err != nil {
		return nil, err
	}

	if err := decoder.Decode(&jobs); err != nil {
		return nil, err
	}

	release, ok := jobs["release"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("captured parent has no release job: %w", errRecovery)
	}

	trigger, ok := release["trigger"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("captured parent has no child trigger: %w", errRecovery)
	}

	includes, ok := trigger["include"].([]any)
	// The pinned release jobs and the generated plan-driven jobs; GitLab
	// admits a third include, the checkpoint.
	if !ok || len(includes) != 2 {
		return nil, fmt.Errorf("expected the production child include and the generated jobs: %w", errRecovery)
	}

	trigger["include"] = append(includes, map[string]any{
		"project": policyProject, "ref": extensionRef, "file": "/recovery-checkpoint.yml",
	})

	var out bytes.Buffer

	encoder := yaml.NewEncoder(&out)
	if err := encoder.Encode(spec); err != nil {
		return nil, err
	}

	if err := encoder.Encode(jobs); err != nil {
		return nil, err
	}

	if err := encoder.Close(); err != nil {
		return nil, err
	}

	return out.Bytes(), nil
}

// GitLabRecoveryExtension is child-only test policy. It does not replace the
// signer script, role, environment or artifact source. No production template
// reads the fixture variable. A retry receives its new project-variable value;
// the original running shell deliberately cannot notice that change.
func GitLabRecoveryExtension(image string) ([]byte, error) {
	extension, err := encodeYAML(map[string]any{
		GitLabRecoveryCheckpoint: map[string]any{
			"stage": "sign", "image": map[string]any{fieldName: image, "entrypoint": []string{""}},
			"variables": map[string]string{"GIT_STRATEGY": "empty", "GIT_SUBMODULE_STRATEGY": "none"},
			"needs":     []any{map[string]any{lendingJob: recoveryPublisher, recoveryArtifacts: false}},
			"script": []string{`set -eu
case "${FIXTURE_PAUSE_AFTER_CANDIDATE:?}" in
  true)
    echo fci-live-candidate-checkpoint-ready
    sleep 1200
    echo 'ERROR: controller did not cancel the candidate checkpoint' >&2
    exit 1
    ;;
  false) echo fci-live-candidate-checkpoint-cleared ;;
  *) exit 1 ;;
esac`},
		},
		"Sign: files and images": map[string]any{
			"needs": []any{
				map[string]any{lendingJob: recoveryPublisher, recoveryArtifacts: true},
				map[string]any{lendingJob: GitLabRecoveryCheckpoint, recoveryArtifacts: false},
			},
		},
	})

	return []byte(extension), err
}

// GitLabRecoveryCandidate admits exactly the single-image recovery case. Both
// tags belong to the source repository; the signer later rewrites the final
// destination to the separate publication repository. Never infer identity from
// whichever mutable tag happens to exist in the registry. The fixture seeds
// latest and v0.0.0; only an explicitly declared latest may move on release.
func GitLabRecoveryCandidate(ledger []byte, repository, finalTag string) (imageledger.Entry, error) {
	entries, err := imageledger.Parse(ledger)
	if err != nil {
		return imageledger.Entry{}, err
	}

	if len(entries) != 1 {
		return imageledger.Entry{}, fmt.Errorf("recovery requires exactly one candidate, got %d: %w", len(entries), errRecovery)
	}

	entry := entries[0]
	if err := entry.Validate(finalTag); err != nil {
		return imageledger.Entry{}, err
	}

	if entry.ImageKind != imageledger.ImageKindRelease || entry.CandidateTag != repository+":staging-"+finalTag ||
		entry.FinalTag != repository+":"+finalTag || entry.Ref != repository+"@"+entry.Digest ||
		(entry.MovingTag != "" && entry.MovingTag != repository+":latest") {
		return imageledger.Entry{}, fmt.Errorf("recovery receipt does not bind the exact candidate: %+v: %w", entry, errRecovery)
	}

	return entry, nil
}

// GitLabRecoveryTagState distinguishes an absent tag from a present digest.
// Registry read errors are not states and must be handled separately.
type GitLabRecoveryTagState struct {
	Digest string
	Found  bool
}

// GitLabRecoveryPublicationDigests names the exact post-retry tag states for
// an admitted candidate, seeded prior version and observed initial latest.
func GitLabRecoveryPublicationDigests(candidate imageledger.Entry, finalTag, previousDigest string, initialLatest GitLabRecoveryTagState) map[string]GitLabRecoveryTagState {
	latest := initialLatest
	if candidate.MovingTag != "" {
		latest = GitLabRecoveryTagState{Digest: candidate.Digest, Found: true}
	}

	return map[string]GitLabRecoveryTagState{
		finalTag: {Digest: candidate.Digest, Found: true},
		"latest": latest, "v0.0.0": {Digest: previousDigest, Found: true},
	}
}

// CheckGitLabRecoveryTag compares an actual lookup with its exact expected state.
func CheckGitLabRecoveryTag(tag string, observed, expected GitLabRecoveryTagState, readErr error) error {
	if readErr != nil {
		return fmt.Errorf("read recovery publication tag %s: %w", tag, readErr)
	}

	if observed != expected {
		return fmt.Errorf("recovered publication tag %s has state %+v, want %+v: %w", tag, observed, expected, errRecovery)
	}

	return nil
}
