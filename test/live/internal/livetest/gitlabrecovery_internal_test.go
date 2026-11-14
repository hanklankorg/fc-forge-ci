// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"os/exec"
	"strings"
	"testing"

	gitlabapi "gitlab.com/gitlab-org/api/client-go"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestGitLabRecoveryObservationAfterPolicyPublication(t *testing.T) {
	// The old oracle rejected this reachable state because the file build had
	// succeeded. The policy DAG regression below proves that success is required.
	jobs := []*gitlabapi.Job{
		{ID: 11, Name: "Build: release files", Status: "success"},
		{ID: 12, Name: "Images: publish", Status: "success"},
		{ID: 13, Name: GitLabRecoveryCheckpoint, Status: "running"},
		{ID: 14, Name: "Sign: files and images", Status: "created"},
		{ID: 15, Name: "Publish: images", Status: "created"},
	}

	publisher, checkpoint, err := GitLabRecoveryObservation(jobs)
	if err != nil || publisher != 12 || checkpoint != 13 {
		t.Fatalf("current policy boundary not observed: %d, %d, %v", publisher, checkpoint, err)
	}

	jobs[0].Status, jobs[1].Status, jobs[2].Status = "running", "created", "created"
	if _, checkpoint, err := GitLabRecoveryObservation(jobs); err != nil || checkpoint != 0 {
		t.Fatalf("historical consumer-build boundary admitted: %d, %v", checkpoint, err)
	}

	jobs[0].Status, jobs[1].Status, jobs[2].Status = "success", "success", "running"

	jobs[3].Status = "pending"
	if _, _, err := GitLabRecoveryObservation(jobs); err == nil {
		t.Fatal("signer scheduled before cancellation was accepted")
	}

	jobs[3].Status, jobs[2].Status = "created", "success"
	if _, _, err := GitLabRecoveryObservation(jobs); err == nil {
		t.Fatal("completed checkpoint was accepted as cancellation coverage")
	}
}

func TestGitLabRecoveryPolicyDAG(t *testing.T) {
	// Exercise the real parent and child, not a hand-maintained fixture DAG.
	engine := strings.Repeat("a", 40)
	extensionRef := strings.Repeat("b", 40)

	body, err := GitLabRecoveryParent(reporoot.ReadFile(t, "templates/release.yml"), engine, "owned/policy", extensionRef)
	if err != nil {
		t.Fatal(err)
	}

	parent := recoveryJobs(t, body)

	trigger := parent["release"].(map[string]any)["trigger"].(map[string]any)
	if trigger["strategy"] != "mirror" {
		t.Fatal("wrapper lost parent lock lifetime")
	}

	includes := trigger["include"].([]any)
	if len(includes) != 3 {
		t.Fatalf("child needs production DAG, the generated jobs, then test extension, got %v", includes)
	}

	production := includes[0].(map[string]any)
	if production["file"] != "/templates/release-jobs.yml" || production["ref"] != engine || production["project"] != "$[[ inputs.forge-ci-repository ]]" {
		t.Fatalf("wrapper changed production child source: %v", production)
	}

	inputs := production["inputs"].(map[string]any)
	if inputs["engine-sha"] != engine || inputs["engine-reference"] != engine {
		t.Fatal("engine pin drifted to the policy wrapper's commit")
	}

	if generated := includes[1].(map[string]any); generated["artifact"] != "_release-plan/release-families.yml" || generated["job"] != "Release: plan" {
		t.Fatalf("wrapper changed the generated jobs' source: %v", generated)
	}

	ext := includes[2].(map[string]any)
	if ext["project"] != "owned/policy" || ext["ref"] != extensionRef || ext["file"] != "/recovery-checkpoint.yml" {
		t.Fatalf("checkpoint is not independently pinned: %v", ext)
	}

	child := recoveryJobs(t, reporoot.ReadFile(t, "templates/release-jobs.yml"))

	extension, err := GitLabRecoveryExtension("registry.example/job@sha256:" + strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}

	var gate map[string]any
	if err := yaml.Unmarshal(extension, &gate); err != nil {
		t.Fatal(err)
	}
	// GitLab deep-merges mappings; needs arrays replace the inherited array.
	for name, override := range gate {
		job, exists := child[name].(map[string]any)
		if !exists {
			job = map[string]any{}
		}

		maps.Copy(job, override.(map[string]any))

		child[name] = job
	}
	// This is precisely why the old build-running/finalTag oracle is impossible:
	// candidate publication is downstream of successful file build.
	if !recoveryDepends(t, child, "Images: publish", "Build: release files") ||
		!recoveryDepends(t, child, GitLabRecoveryCheckpoint, "Images: publish") {
		t.Fatal("checkpoint must follow completed file build and policy publication")
	}

	for _, name := range []string{"Sign: files and images", "Publish: images", "Publish: release", "Publish: tidy up"} {
		if !recoveryDepends(t, child, name, GitLabRecoveryCheckpoint) {
			t.Fatalf("%s can escape the running checkpoint", name)
		}
	}

	signer := child["Sign: files and images"].(map[string]any)
	if signer["script"].([]any)[0] != "rc_sign_release '$[[ inputs.release-images ]]'" {
		t.Fatal("extension replaced production signing")
	}

	needs := signer["needs"].([]any)
	if len(needs) != 2 || needs[0].(map[string]any)["job"] != "Images: publish" || needs[0].(map[string]any)["artifacts"] != true || needs[1].(map[string]any)["artifacts"] != false {
		t.Fatal("signer lost its original artifact source")
	}

	checkpoint := child[GitLabRecoveryCheckpoint].(map[string]any)
	if _, exists := checkpoint["environment"]; exists {
		t.Fatal("checkpoint must not select a credential environment")
	}
}

func recoveryJobs(t *testing.T, body []byte) map[string]any {
	t.Helper()

	d := yaml.NewDecoder(bytes.NewReader(body))

	var spec, jobs map[string]any
	if err := d.Decode(&spec); err != nil {
		t.Fatal(err)
	}

	if err := d.Decode(&jobs); err != nil {
		t.Fatal(err)
	}

	return jobs
}

func recoveryDepends(t *testing.T, jobs map[string]any, from, on string) bool {
	t.Helper()

	// An optional need names a generated family job this plan does not run.
	job, planned := jobs[from].(map[string]any)
	if !planned {
		return false
	}

	needs, _ := job["needs"].([]any)
	for _, need := range needs {
		name, ok := need.(string)
		if !ok {
			name = need.(map[string]any)["job"].(string)
		}

		if name == on || recoveryDepends(t, jobs, name, on) {
			return true
		}
	}

	return false
}

func TestGitLabRecoveryCheckpointFailClosed(t *testing.T) {
	body, err := GitLabRecoveryExtension("example.invalid/job")
	if err != nil {
		t.Fatal(err)
	}

	var jobs map[string]any
	if err := yaml.Unmarshal(body, &jobs); err != nil {
		t.Fatal(err)
	}

	script := jobs[GitLabRecoveryCheckpoint].(map[string]any)["script"].([]any)[0].(string)

	for _, value := range []string{"true", "false", "", "garbage"} {
		t.Run("pause="+value, func(t *testing.T) {
			// Let the sleep expire immediately: a controller that misses its
			// cancellation window must fail, never claim recovery coverage.
			cmd := exec.CommandContext(t.Context(), "bash", "-c", "sleep() { :; };\n"+script)
			cmd.Env = []string{"PATH=/usr/bin:/bin", GitLabRecoveryPauseVariable + "=" + value}

			out, err := cmd.CombinedOutput()
			if (err == nil) != (value == "false") {
				t.Fatalf("checkpoint status: %v: %s", err, out)
			}

			if value == "true" && !bytes.Contains(out, []byte("fci-live-candidate-checkpoint-ready")) {
				t.Fatal("missing controller observation marker")
			}
		})
	}
}

func TestGitLabRecoveryCandidateIdentity(t *testing.T) {
	const repository = "registry.example/owned/consumer"

	digest := "sha256:" + strings.Repeat("a", 64)
	entry := imageledger.Entry{ImageKind: imageledger.ImageKindRelease, Role: "distroless", Ref: repository + "@" + digest, Digest: digest,
		CandidateTag: repository + ":staging-v0.3.0", FinalTag: repository + ":v0.3.0"}

	for _, mutation := range []string{"valid", "old-final-tag-as-candidate", "wrong-repository", "wrong-ref-digest", "wrong-version", "multiple-images", "unrelated-moving-tag", "previous-version-moving-tag"} {
		t.Run(mutation, func(t *testing.T) {
			candidate := entry

			switch mutation {
			case "old-final-tag-as-candidate":
				candidate.CandidateTag = candidate.FinalTag
			case "wrong-repository":
				candidate.Ref = "registry.example/other/consumer@" + digest
			case "wrong-ref-digest":
				candidate.Ref = repository + "@sha256:" + strings.Repeat("b", 64)
			case "wrong-version":
				candidate.CandidateTag = repository + ":staging-v0.4.0"
			case "unrelated-moving-tag":
				candidate.MovingTag = repository + ":stable"
			case "previous-version-moving-tag":
				candidate.MovingTag = repository + ":v0.0.0"
			}

			entries := []imageledger.Entry{candidate}
			if mutation == "multiple-images" {
				entries = append(entries, candidate)
			}

			ledger, err := json.Marshal(entries)
			if err != nil {
				t.Fatal(err)
			}

			got, err := GitLabRecoveryCandidate(ledger, repository, "v0.3.0")
			if (err == nil) != (mutation == "valid") {
				t.Fatalf("candidate admission: %+v, %v", got, err)
			}

			if err == nil && got.Digest != digest {
				t.Fatal("receipt changed immutable digest")
			}
		})
	}
}

func TestGitLabRecoveryPublicationFollowsReceiptDestinations(t *testing.T) {
	const (
		repository = "registry.example/owned/consumer"
		finalTag   = "v0.3.0"
	)
	// The terminal native run's distinct candidate and observed latest digests.
	// Seed the previous image explicitly: no content equivalence or arbitrary
	// post-retry digest is accepted as a substitute for the original candidate.
	const (
		candidateDigest = "sha256:c8238d06fc33526877e93b1608b9ddc234df498d761a381155161a6b19417da2"
		previousDigest  = "sha256:a8544d4845c8b007f8ad36ed2f52265eb2b3b3ec53a4b8c389249788d062e5ac"
	)

	plan, err := pipeline.PlanReleaseImages([]pipeline.PlannedContainer{{
		Name: "hello-cargo-container", ContainerFile: "Containerfile", Context: ".", Target: "runtime",
		Platforms: "linux/amd64,linux/arm64",
	}}, pipeline.ReleaseImagesInput{ImageRepository: repository, Repository: "owned/consumer", ReleaseTag: finalTag, ReleasePlatforms: "linux/amd64"})
	if err != nil {
		t.Fatal(err)
	}

	image := plan.Images[0]
	if image.MovingTagName != "" {
		t.Fatal("ordinary release planner unexpectedly declares a moving destination")
	}

	for _, moving := range []string{"", repository + ":latest"} {
		t.Run("moving="+moving, func(t *testing.T) {
			entry := imageledger.Entry{ImageKind: imageledger.ImageKindRelease, Role: image.Name,
				Ref: repository + "@" + candidateDigest, Digest: candidateDigest,
				CandidateTag: repository + ":" + image.CandidateTag, FinalTag: repository + ":" + image.FinalTagName, MovingTag: moving}

			body, err := json.Marshal([]imageledger.Entry{entry})
			if err != nil {
				t.Fatal(err)
			}

			candidate, err := GitLabRecoveryCandidate(body, repository, finalTag)
			if err != nil {
				t.Fatal(err)
			}

			latest := previousDigest
			if moving != "" {
				latest = candidateDigest
			}

			want := map[string]GitLabRecoveryTagState{finalTag: {Digest: candidateDigest, Found: true}, "latest": {Digest: latest, Found: true}, "v0.0.0": {Digest: previousDigest, Found: true}}
			if got := GitLabRecoveryPublicationDigests(candidate, finalTag, previousDigest, GitLabRecoveryTagState{Digest: previousDigest, Found: true}); !maps.Equal(got, want) {
				t.Fatalf("publication oracle = %v, want exact ledger destinations %v", got, want)
			}
		})
	}
}

func TestGitLabRecoveryPublicationObservedStates(t *testing.T) {
	const (
		candidateDigest = "sha256:c8238d06fc33526877e93b1608b9ddc234df498d761a381155161a6b19417da2"
		previousDigest  = "sha256:a8544d4845c8b007f8ad36ed2f52265eb2b3b3ec53a4b8c389249788d062e5ac"
	)

	present := GitLabRecoveryTagState{Digest: previousDigest, Found: true}
	distinct := GitLabRecoveryTagState{Digest: "sha256:" + strings.Repeat("d", 64), Found: true}
	promoted := GitLabRecoveryTagState{Digest: candidateDigest, Found: true}
	absent := GitLabRecoveryTagState{}

	readFailure := errors.New("registry read uncertain")
	for _, tc := range []struct {
		name, tag       string
		initial, actual GitLabRecoveryTagState
		declared        bool
		readErr         error
		wantErr         bool
	}{
		{name: "undeclared present preserved", tag: "latest", initial: present, actual: present},
		{name: "undeclared distinct latest preserved", tag: "latest", initial: distinct, actual: distinct},
		{name: "undeclared absent preserved", tag: "latest", initial: absent, actual: absent},
		{name: "declared latest updated", tag: "latest", initial: present, actual: promoted, declared: true},
		{name: "declared latest created", tag: "latest", initial: absent, actual: promoted, declared: true},
		{name: "versioned final preserved", tag: "v0.3.0", initial: present, actual: promoted},
		{name: "prior version preserved", tag: "v0.0.0", initial: present, actual: present},
		{name: "wrong versioned digest", tag: "v0.3.0", initial: present, actual: present, wantErr: true},
		{name: "wrong declared latest digest", tag: "latest", initial: present, actual: present, declared: true, wantErr: true},
		{name: "undeclared latest advanced", tag: "latest", initial: present, actual: promoted, wantErr: true},
		{name: "unexpected latest creation", tag: "latest", initial: absent, actual: promoted, wantErr: true},
		{name: "missing final", tag: "v0.3.0", initial: present, actual: absent, wantErr: true},
		{name: "missing declared latest", tag: "latest", initial: absent, actual: absent, declared: true, wantErr: true},
		{name: "missing previously present latest", tag: "latest", initial: present, actual: absent, wantErr: true},
		{name: "missing prior version", tag: "v0.0.0", initial: present, actual: absent, wantErr: true},
		{name: "prior version changed", tag: "v0.0.0", initial: present, actual: promoted, wantErr: true},
		{name: "uncertain absence", tag: "latest", initial: absent, actual: absent, readErr: readFailure, wantErr: true},
		{name: "uncertain matching digest", tag: "v0.3.0", initial: present, actual: promoted, readErr: readFailure, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := imageledger.Entry{Digest: candidateDigest}
			if tc.declared {
				candidate.MovingTag = "registry.example/owned/consumer:latest"
			}

			expected := GitLabRecoveryPublicationDigests(candidate, "v0.3.0", previousDigest, tc.initial)
			if tc.tag == "latest" && !tc.declared && expected[tc.tag] != tc.initial {
				t.Errorf("undeclared latest expectation = %+v, want initial state %+v", expected[tc.tag], tc.initial)
			}

			err := CheckGitLabRecoveryTag(tc.tag, tc.actual, expected[tc.tag], tc.readErr)
			if (err != nil) != tc.wantErr {
				t.Fatalf("observed publication comparison = %v, want error %t", err, tc.wantErr)
			}

			if tc.readErr != nil && !errors.Is(err, tc.readErr) {
				t.Fatal("registry uncertainty was not preserved")
			}
		})
	}
}
