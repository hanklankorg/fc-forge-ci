// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"
)

// What the Forgejo release scenarios read of their fixtures.
const (
	// ForgejoRecoveryCheckpointReached names the job whose success is the
	// interruption point: the unsigned hand-off is complete and the signing
	// workflow cannot start, because it needs the hold after this job.
	ForgejoRecoveryCheckpointReached = "Fixture: recovery checkpoint reached"
	// ForgejoRecoveryCheckpointHold names the job the signing workflow needs.
	ForgejoRecoveryCheckpointHold = "Fixture: recovery checkpoint hold"
	// ForgejoRecoveryHoldVariable is the repository variable the hold reads.
	// While the repository has none the hold lasts until the run is
	// cancelled; creating it as ForgejoRecoveryHoldReleased lets the next
	// release request, or a rerun of the hold, pass.
	ForgejoRecoveryHoldVariable = "FIXTURE_HOLD_BEFORE_SIGNING"
	// ForgejoRecoveryHoldReleased is the one value that ends the hold.
	ForgejoRecoveryHoldReleased = "released"
	// PreseededProvenance is the file a refusal fixture places in the unsigned
	// hand-off, and PreseededProvenanceRefusal the engine's refusal of it.
	PreseededProvenance        = "dist/slsa-provenance.intoto.json"
	PreseededProvenanceRefusal = "unsigned dist must not hold " + PreseededProvenance + ": signing writes it"

	forgejoRecoveryReachedJob = "fixture-recovery-reached"
	forgejoRecoveryHoldJob    = "fixture-recovery-hold"
	forgejoSigningWorkflow    = "/.forgejo/workflows/sign-and-publish-release.yml@"
	forgejoUploadDist         = "/.forgejo/actions/upload-dist@"
	forgejoHandoffArtifact    = "build-dist"
	forgejoFixtureHeader      = "# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government\n# SPDX-License-Identifier: CC0-1.0\n\n"

	fieldNeeds = "needs"
	fieldSteps = "steps"
	fieldUses  = "uses"
	fieldIf    = "if"
	fieldEnv   = "env"
	fieldRun   = "run"
)

var errForgejoReleaseFixture = errors.New("release fixture does not fit the consumer's release workflow")

// forgejoRecoveryHoldScript holds while the variable is unset and fails when
// nothing cancelled it, so an unobserved checkpoint never lets signing start.
// The run of the next request, or a rerun of the hold, reads the variable;
// the first attempt's shell cannot.
const forgejoRecoveryHoldScript = `set -eu
case "${FIXTURE_HOLD}" in
  ` + ForgejoRecoveryHoldReleased + `) exit 0 ;;
  '') ;;
  *) echo 'fixture: the hold variable must be unset or ` + ForgejoRecoveryHoldReleased + `' >&2; exit 1 ;;
esac
elapsed=0
while [ "${elapsed}" -lt 600 ]; do
  sleep 5
  elapsed=$((elapsed + 5))
done
echo 'fixture: the hold expired without a cancellation' >&2
exit 1
`

// WithPreseededForgejoProvenance adds the refusal fixture to the consumer's
// private index alone: the job that uploads the unsigned hand-off first
// writes an inert provenance statement into it. The consumer's own build is
// unchanged and runs first; the admitted checkout is never edited.
func WithPreseededForgejoProvenance(tb TB, source Consumer) Consumer {
	tb.Helper()

	return restageForgejoRelease(tb, source, "preseeded provenance", forgejoPreseededProvenanceWorkflow)
}

// WithForgejoRecoveryCheckpoint adds the interruption gate to the consumer's
// private index alone: two jobs of the consumer's own release workflow
// between its unsigned hand-off and the signing workflow. No job of the layer
// is changed, and the layer reads no fixture variable.
func WithForgejoRecoveryCheckpoint(tb TB, source Consumer) Consumer {
	tb.Helper()

	return restageForgejoRelease(tb, source, "recovery checkpoint", forgejoRecoveryCheckpointWorkflow)
}

// ForgejoPackagesFixtures is where the Go CLI demo keeps its example's
// GoReleaser release of distro packages: the release workflow, the
// declaration with a distro-packages block, the GoReleaser configuration
// and the locked mise tools the build installs.
const ForgejoPackagesFixtures = "test/live/forgejo-packages"

// ForgejoGPGFilesFixtures is where the Go service demo keeps its declaration
// with sign.method gpg: the Forgejo release signs its checksums and SBOMs
// with the release's GPG key, and its provenance, evidence and image with
// the Cosign key.
const ForgejoGPGFilesFixtures = "test/live/forgejo-gpg-files"

// GitLabGPGFilesFixtures is where the Go CLI demo keeps its declaration with
// sign.method gpg: the GitLab release signs its checksums and SBOMs with the
// release's GPG key, and its provenance and evidence with the Cosign key.
const GitLabGPGFilesFixtures = "test/live/gitlab-gpg-files"

// WithForgejoFixtures stages every file of the consumer's directory fixtures
// over its root, in the private index alone: fixtures/<path> becomes <path>,
// as a person copies an alternative of an example into place. The admitted
// checkout is never edited, and the directory itself stays in the tree.
func WithForgejoFixtures(tb TB, source Consumer, fixtures string) Consumer {
	tb.Helper()

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	staged, err := source.withFixtures(ctx, fixtures)
	if err != nil {
		tb.Fatalf("livetest: stage the fixtures of %s: %v", fixtures, err)
	}

	return staged
}

// ForgejoTrialRoot writes the files of the consumer as it is staged, with
// its fixtures over its root, to the empty directory dir, for the host to
// read what the trial runs: the checkout holds the consumer without them.
func ForgejoTrialRoot(tb TB, staged Consumer, dir string) {
	tb.Helper()

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	if err := staged.export(ctx, dir); err != nil {
		tb.Fatalf("livetest: write the staged consumer: %v", err)
	}
}

// export writes the files of the consumer's private index to dir. Neither
// the index nor the checkout changes.
func (consumer Consumer) export(ctx context.Context, dir string) error {
	_, err := consumer.git.run(ctx, consumer.stage, "--work-tree="+dir, "checkout-index", "--all", "--prefix="+dir+string(filepath.Separator))

	return err
}

// withFixtures returns the consumer at a commit of its tree with the files
// of fixtures staged over its root.
func (consumer Consumer) withFixtures(ctx context.Context, fixtures string) (Consumer, error) {
	prefix := strings.Trim(fixtures, "/") + "/"

	listed, err := consumer.git.output(ctx, consumer.stage, nil, "ls-tree", "-r", "-z", consumer.Commit, "--", prefix)
	if err != nil {
		return Consumer{}, err
	}

	entries := strings.FieldsFunc(string(listed), func(r rune) bool { return r == 0 })
	if len(entries) == 0 {
		return Consumer{}, fmt.Errorf("the consumer holds no file under %s: %w", prefix, errForgejoReleaseFixture)
	}

	for _, entry := range entries {
		// `<mode> blob <id>\t<path>`: a link or a submodule is no file to copy.
		header, path, _ := strings.Cut(entry, "\t")

		fields := strings.Fields(header)
		if len(fields) != 3 || (fields[0] != "100644" && fields[0] != "100755") {
			return Consumer{}, fmt.Errorf("%s is not a regular file: %w", path, errForgejoReleaseFixture)
		}

		if _, err = consumer.git.run(ctx, consumer.stage, "update-index", "--add", "--cacheinfo", fields[0]+","+fields[2]+","+strings.TrimPrefix(path, prefix)); err != nil {
			return Consumer{}, err
		}
	}

	consumer.Commit, err = consumer.commit(ctx, consumerBaseMessage)

	return consumer, err
}

// restageForgejoRelease commits the consumer's release workflow as fixture
// renders it, in the private index alone.
func restageForgejoRelease(tb TB, source Consumer, what string, fixture func(string) (string, error)) Consumer {
	tb.Helper()

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	body, err := githubWorkflow(ctx, source, forgejoReleaseWorkflow)
	if err == nil {
		body, err = fixture(body)
	}

	if err == nil {
		err = source.stageFile(ctx, "100644", forgejoReleaseWorkflow, body)
	}

	if err != nil {
		tb.Fatalf("livetest: stage the %s fixture in %s: %v", what, forgejoReleaseWorkflow, err)
	}

	commit, err := source.commit(ctx, consumerBaseMessage)
	if err != nil {
		tb.Fatalf("livetest: commit the %s fixture: %v", what, err)
	}

	source.Commit = commit

	return source
}

// forgejoPreseededProvenanceWorkflow inserts the fixture step before the one
// upload-dist step that uploads the artifact the signing workflow downloads.
func forgejoPreseededProvenanceWorkflow(body string) (string, error) {
	workflow, jobs, err := forgejoReleaseJobs(body)
	if err != nil {
		return "", err
	}

	_, signer, err := forgejoSigningJob(jobs)
	if err != nil {
		return "", err
	}

	handoff := forgejoInput(signer, "artifact-name", forgejoHandoffArtifact)
	if signed := strings.TrimRight(forgejoInput(signer, "artifact-path", "dist/"), "/") + "/slsa-provenance.intoto.json"; signed != PreseededProvenance {
		return "", fmt.Errorf("the signer would write %s, not %s: %w", signed, PreseededProvenance, errForgejoReleaseFixture)
	}

	var (
		uploader map[string]any
		at       int
		uploads  int
	)

	for _, value := range jobs {
		job, _ := value.(map[string]any)
		steps, _ := job[fieldSteps].([]any)

		for index, entry := range steps {
			step, _ := entry.(map[string]any)
			if uses, _ := step[fieldUses].(string); strings.Contains(uses, forgejoUploadDist) && forgejoInput(step, "artifact-name", forgejoHandoffArtifact) == handoff {
				uploader, at = job, index
				uploads++
			}
		}
	}

	if uploads != 1 {
		return "", fmt.Errorf("%d upload-dist steps upload the signer's %s artifact, want one: %w", uploads, handoff, errForgejoReleaseFixture)
	}

	steps, _ := uploader[fieldSteps].([]any)
	upload, _ := steps[at].(map[string]any)

	uploader[fieldSteps] = slices.Insert(steps, at, any(map[string]any{
		fieldName: "Fixture: preseed provenance in the unsigned hand-off",
		fieldEnv:  map[string]any{"FIXTURE_DIST": strings.TrimRight(forgejoInput(upload, "path", "dist/"), "/")},
		fieldRun: `set -eu
test -d "${FIXTURE_DIST}"
test ! -L "${FIXTURE_DIST}"
printf '%s\n' '{"fixture":"untrusted-provenance"}' > "${FIXTURE_DIST}/slsa-provenance.intoto.json"
`,
	}))

	return encodeForgejoFixture(workflow)
}

// forgejoRecoveryCheckpointWorkflow adds the reached and hold jobs after
// everything the signing job needs and makes the signing job need the hold.
func forgejoRecoveryCheckpointWorkflow(body string) (string, error) {
	workflow, jobs, err := forgejoReleaseJobs(body)
	if err != nil {
		return "", err
	}

	_, signer, err := forgejoSigningJob(jobs)
	if err != nil {
		return "", err
	}

	for _, reserved := range []string{forgejoRecoveryReachedJob, forgejoRecoveryHoldJob} {
		if _, taken := jobs[reserved]; taken {
			return "", fmt.Errorf("the consumer already has a job %s: %w", reserved, errForgejoReleaseFixture)
		}
	}

	needs, err := forgejoNeeds(signer[fieldNeeds])
	if err != nil {
		return "", err
	}

	if condition, conditional := signer[fieldIf]; conditional {
		held, heldErr := forgejoHeldCondition(condition)
		if heldErr != nil {
			return "", heldErr
		}

		signer[fieldIf] = held
	}

	signer[fieldNeeds] = append(slices.Clone(needs), any(forgejoRecoveryHoldJob))

	const runner = "${{ fromJSON(vars.CI_RUNNER_LABEL) }}"

	jobs[forgejoRecoveryReachedJob] = map[string]any{
		fieldName: ForgejoRecoveryCheckpointReached, fieldNeeds: needs, "runs-on": runner, "timeout-minutes": 5,
		fieldSteps: []any{map[string]any{fieldRun: "printf 'fci-live-recovery-checkpoint-ready\\n'\n"}},
	}
	jobs[forgejoRecoveryHoldJob] = map[string]any{
		fieldName: ForgejoRecoveryCheckpointHold, fieldNeeds: []any{forgejoRecoveryReachedJob}, "runs-on": runner, "timeout-minutes": 15,
		fieldSteps: []any{map[string]any{
			fieldEnv: map[string]any{"FIXTURE_HOLD": "${{ vars." + ForgejoRecoveryHoldVariable + " }}"},
			fieldRun: forgejoRecoveryHoldScript,
		}},
	}

	return encodeForgejoFixture(workflow)
}

// forgejoWorkflowJobs reads a workflow and its jobs; the jobs are nil when
// it has none.
func forgejoWorkflowJobs(body string) (map[string]any, map[string]any, error) {
	var workflow map[string]any
	if err := yaml.Unmarshal([]byte(body), &workflow); err != nil {
		return nil, nil, err
	}

	jobs, _ := workflow["jobs"].(map[string]any)

	return workflow, jobs, nil
}

func forgejoReleaseJobs(body string) (map[string]any, map[string]any, error) {
	workflow, jobs, err := forgejoWorkflowJobs(body)
	if err == nil && jobs == nil {
		err = fmt.Errorf("the release workflow has no jobs: %w", errForgejoReleaseFixture)
	}

	return workflow, jobs, err
}

// forgejoSigningJob is the one job that calls the layer's signing workflow.
func forgejoSigningJob(jobs map[string]any) (string, map[string]any, error) {
	var (
		name   string
		signer map[string]any
	)

	for key, value := range jobs {
		job, _ := value.(map[string]any)
		if uses, _ := job[fieldUses].(string); !strings.Contains(uses, forgejoSigningWorkflow) {
			continue
		}

		if signer != nil {
			return "", nil, fmt.Errorf("jobs %s and %s both call the signing workflow: %w", name, key, errForgejoReleaseFixture)
		}

		name, signer = key, job
	}

	if signer == nil {
		return "", nil, fmt.Errorf("no job calls sign-and-publish-release.yml: %w", errForgejoReleaseFixture)
	}

	return name, signer, nil
}

// forgejoInput is a job's or a step's with: input, or fallback when it has none.
func forgejoInput(owner map[string]any, input, fallback string) string {
	inputs, _ := owner["with"].(map[string]any)
	if value, ok := inputs[input].(string); ok && value != "" {
		return value
	}

	return fallback
}

// forgejoNeeds reads a needs: value, one job or a list, as a list.
func forgejoNeeds(value any) ([]any, error) {
	switch needs := value.(type) {
	case string:
		return []any{needs}, nil
	case []any:
		if len(needs) != 0 {
			return needs, nil
		}
	}

	return nil, fmt.Errorf("the signing job needs no job, so nothing hands over to it: %w", errForgejoReleaseFixture)
}

// ForgejoRecoveryHoldDependants names every job of the release workflow
// body that needs the recovery hold, directly or through another job, by
// the name the run lists it under: its name:, or its key when it has none.
// A rerun of the hold runs these again with it. The forge lists each job's
// needs by key and the job itself by name, so the graph is read from the
// workflow the run was started with.
func ForgejoRecoveryHoldDependants(body string) ([]string, error) {
	_, jobs, err := forgejoReleaseJobs(body)
	if err != nil {
		return nil, err
	}

	if _, held := jobs[forgejoRecoveryHoldJob]; !held {
		return nil, fmt.Errorf("the release workflow has no job %s: %w", forgejoRecoveryHoldJob, errForgejoReleaseFixture)
	}

	needs := make(map[string][]string, len(jobs))

	for key, value := range jobs {
		job, _ := value.(map[string]any)

		switch listed := job[fieldNeeds].(type) {
		case string:
			needs[key] = []string{listed}
		case []any:
			for _, need := range listed {
				text, _ := need.(string)
				needs[key] = append(needs[key], text)
			}
		}
	}

	keys := dependantJobs(needs, forgejoRecoveryHoldJob)
	names := make([]string, 0, len(keys))

	for _, key := range keys {
		job, _ := jobs[key].(map[string]any)

		name, _ := job[fieldName].(string)
		if name == "" {
			name = key
		}

		names = append(names, name)
	}

	return names, nil
}

// dependantJobs is every job, by key and sorted, that needs root directly
// or through another job.
func dependantJobs(needs map[string][]string, root string) []string {
	found := map[string]bool{}
	pending := []string{root}

	for len(pending) != 0 {
		needed := pending[0]
		pending = pending[1:]

		for job, listed := range needs {
			if !found[job] && slices.Contains(listed, needed) {
				found[job] = true
				pending = append(pending, job)
			}
		}
	}

	return slices.Sorted(maps.Keys(found))
}

// forgejoHeldCondition adds the hold to a signing job's own condition. A
// condition with always() would otherwise start signing after a hold that
// failed or was cancelled.
func forgejoHeldCondition(condition any) (string, error) {
	text, ok := condition.(string)
	if !ok {
		return "", fmt.Errorf("the signing job's condition is not an expression: %w", errForgejoReleaseFixture)
	}

	expression := strings.TrimSpace(text)
	if inner, wrapped := strings.CutPrefix(expression, "${{"); wrapped {
		inner, closed := strings.CutSuffix(inner, "}}")
		if !closed {
			return "", fmt.Errorf("the signing job's condition %q is not one expression: %w", text, errForgejoReleaseFixture)
		}

		expression = strings.TrimSpace(inner)
	}

	return "${{ (" + expression + ") && needs." + forgejoRecoveryHoldJob + ".result == 'success' }}", nil
}

func encodeForgejoFixture(workflow map[string]any) (string, error) {
	encoded, err := encodeYAML(workflow)

	return forgejoFixtureHeader + encoded, err
}

// NextPatchVersion is the version an interrupted release of version is
// recovered with on Forgejo: the same major and minor, the next patch. A
// version with a prerelease or build part has no next patch here.
func NextPatchVersion(version string) (string, error) {
	parts := strings.Split(version, ".")
	if len(parts) != 3 || !semver.IsValid("v"+version) || semver.Prerelease("v"+version) != "" || semver.Build("v"+version) != "" {
		return "", fmt.Errorf("%q is not a release version X.Y.Z: %w", version, errForgejoReleaseFixture)
	}

	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return "", fmt.Errorf("%q is not a release version X.Y.Z: %w", version, errForgejoReleaseFixture)
	}

	return parts[0] + "." + parts[1] + "." + strconv.Itoa(patch+1), nil
}
