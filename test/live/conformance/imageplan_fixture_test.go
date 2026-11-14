// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package conformance_test

// These fixture and evidence checks run without a provider or the live tag.
import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/adapters/kaniko"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

//go:embed testdata/layer/resolved-image-plan.yml
var resolvedImagePlanFixture string

const resolvedImagePlanContainerfile = "FROM scratch AS unused\nCOPY missing-unused-file /unused\nFROM scratch AS selected\nCOPY marker.sh /marker.sh\n"

func imagePlanMarker(name, version string) string {
	return "#!/bin/sh\nprintf 'resolved image policy " + name + " " + version + "\\n'\n"
}

func imagePlanSHA256(body []byte) string {
	digest := sha256.Sum256(body)

	return hex.EncodeToString(digest[:])
}

// imagePlanStageJob reports whether job is the stage's job for the image on
// the lab's amd64 runners. release-images.yml names a matrix job
// "<stage>: <name> (<arch>)"; Forgejo may append the matrix values in
// parentheses.
func imagePlanStageJob(job, stage, name string) bool {
	suffix, found := strings.CutPrefix(job, stage+": "+name+" (amd64)")

	return found && (suffix == "" || strings.HasPrefix(suffix, " ("))
}

// imagePlanJobPair is the two jobs that carry an image's cache evidence since
// release-images.yml fetches registry inputs apart from the build: the fetch
// job reports a reuse, the build job an execution.
type imagePlanJobPair struct {
	fetch, build livetest.RunJob
}

// imagePlanJobs selects the image's one fetch and one build job. Zero or
// several jobs of either stage is ambiguous and refused.
func imagePlanJobs(jobs []livetest.RunJob, name string) (imagePlanJobPair, error) {
	var pair imagePlanJobPair

	for _, stage := range []struct {
		prefix string
		job    *livetest.RunJob
	}{{"Fetch registry inputs", &pair.fetch}, {"Build", &pair.build}} {
		matches := 0

		for _, job := range jobs {
			if imagePlanStageJob(job.Name, stage.prefix, name) {
				*stage.job = job
				matches++
			}
		}

		if matches != 1 {
			return imagePlanJobPair{}, fmt.Errorf("expected exactly one %q job for %s, got %d: %w", stage.prefix, name, matches, errs.ErrValidation)
		}
	}

	return pair, nil
}

// imagePlanCacheEvidence judges an image's fetch and build logs together. A
// hit is one reuse line naming ref in the fetch log and a build job that
// executed nothing; a miss is a fetch log without reuse and one kaniko
// execution in the build log. Digest equality alone cannot distinguish reuse
// from a reproducible kaniko build, so both paths need positive evidence, and
// a marker in the other stage's log is a contradiction.
func imagePlanCacheEvidence(fetchLog, buildLog, ref string, hit bool) (string, error) {
	const (
		reuse   = appcontainer.CacheReusePrefix
		execute = kaniko.ExecutionNotice
	)

	reused := strings.Count(fetchLog, reuse)
	built := strings.Count(buildLog, execute)
	misplaced := strings.Count(buildLog, reuse) + strings.Count(fetchLog, execute)

	switch {
	case misplaced != 0:
	case hit && reused == 1 && built == 0 && strings.Contains(fetchLog, reuse+ref+"\n"):
		return reuse + ref, nil
	case !hit && reused == 0 && built == 1 && strings.Contains(buildLog, kaniko.LayoutBuildNoticeSuffix+"\n"):
		return execute, nil
	}

	return "", fmt.Errorf("cache execution evidence disagrees: want hit=%t, fetch reuse markers=%d, build executor markers=%d, misplaced markers=%d: %w", hit, reused, built, misplaced, errs.ErrValidation)
}

// Job names as Forgejo 16 served them for runs 524 and 525 of the
// 2026-11-08 R witness, matrix values appended.
const (
	imagePlanFetchSteadyJob = "Fetch registry inputs: steady (amd64) (map[arch:amd64 build-args: builder-material-id:6b7db0658136388289cfe8af4117b53cfe24fa14f21ba028239612e9d22b1c9a cache-tag:cache-arch-steady-amd64 candidate-tag:staging-ecffb014b163e1427ee4d41af2e714f65db78c067ee33f1713534c227e03bfdd-steady])"
	imagePlanBuildSteadyJob = "Build: steady (amd64) (map[arch:amd64 build-args: builder-material-id:6b7db0658136388289cfe8af4117b53cfe24fa14f21ba028239612e9d22b1c9a cache-tag:cache-arch-steady-amd64 candidate-tag:staging-ecffb014b163e1427ee4d41af2e714f65db78c067ee33f1713534c227e03bfdd-steady])"
)

func TestResolvedImagePlanStageJobNames(t *testing.T) {
	for _, tc := range []struct {
		job, stage string
		want       bool
	}{
		{"Build: steady (amd64)", "Build", true},
		{imagePlanBuildSteadyJob, "Build", true},
		{imagePlanFetchSteadyJob, "Fetch registry inputs", true},
		{imagePlanFetchSteadyJob, "Build", false},
		{imagePlanBuildSteadyJob, "Fetch registry inputs", false},
		{"Build: changing (amd64) (map[arch:amd64])", "Build", false},
		{"Build: steady (arm64)", "Build", false},
		{"Build steady (amd64)", "Build", false},
		{"Publish: steady (map[archs:amd64])", "Publish", false},
		{"Build: steady (amd64)-other", "Build", false},
	} {
		if imagePlanStageJob(tc.job, tc.stage, "steady") != tc.want {
			t.Errorf("job %q stage %q: want match=%t", tc.job, tc.stage, tc.want)
		}
	}
}

func TestResolvedImagePlanJobsRefuseAmbiguity(t *testing.T) {
	fetch := livetest.RunJob{ID: 7216, Name: imagePlanFetchSteadyJob}
	build := livetest.RunJob{ID: 7220, Name: imagePlanBuildSteadyJob}
	others := []livetest.RunJob{
		{ID: 7197, Name: "Check: plan"},
		{ID: 7217, Name: "Fetch registry inputs: changing (amd64) (map[arch:amd64])"},
		{ID: 7222, Name: "Publish: steady (map[archs:amd64 cache-tag-prefix:cache-arch-steady])"},
	}

	for _, tc := range []struct {
		name  string
		jobs  []livetest.RunJob
		valid bool
	}{
		{"run 525 pair", append([]livetest.RunJob{fetch, build}, others...), true},
		{"missing fetch job", append([]livetest.RunJob{build}, others...), false},
		{"missing build job", append([]livetest.RunJob{fetch}, others...), false},
		{"duplicate build job", []livetest.RunJob{fetch, build, {ID: 7221, Name: "Build: steady (amd64)"}}, false},
		{"pre-stage spelling", []livetest.RunJob{{ID: 1, Name: "Fetch steady (amd64)"}, {ID: 2, Name: "Build steady (amd64)"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pair, err := imagePlanJobs(tc.jobs, "steady")
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t: %v", tc.valid, err)
			}

			if tc.valid && (pair.fetch.ID != 7216 || pair.build.ID != 7220) {
				t.Fatalf("selected the wrong pair: %+v", pair)
			}
		})
	}
}

func imagePlanReview(tree map[string][]byte, repository string) (map[string]imageledger.Entry, error) {
	var entries []imageledger.Entry
	if err := json.Unmarshal(tree["release-images.json"], &entries); err != nil {
		return nil, err
	}

	if len(entries) != 2 {
		return nil, fmt.Errorf("expected exactly two base entries, got %d: %w", len(entries), errs.ErrValidation)
	}

	result := make(map[string]imageledger.Entry, 2)

	for _, entry := range entries {
		name := entry.Flavor
		if (name != "steady" && name != "changing") || result[name].Ref != "" {
			return nil, fmt.Errorf("unexpected or duplicate base flavor %q: %w", name, errs.ErrValidation)
		}

		if err := entry.ValidateBeforeCapture("v0.0.1"); err != nil {
			return nil, err
		}

		if entry.ImageKind != imageledger.ImageKindBase || entry.Role != "base" || entry.Ref != repository+"@"+entry.Digest || entry.Provenance["review_fixture"] != "resolved-image-plan" {
			return nil, fmt.Errorf("unexpected base lineage for %s: %w", name, errs.ErrValidation)
		}

		var scan struct {
			SchemaVersion int    `json:"SchemaVersion"`
			ArtifactName  string `json:"ArtifactName"`
		}
		if err := json.Unmarshal(tree["trivy-results-"+name+"-amd64.json"], &scan); err != nil || scan.SchemaVersion != 2 || scan.ArtifactName == "" {
			return nil, fmt.Errorf("missing or invalid amd64 scan for %s: %w", name, errs.ErrValidation)
		}

		inventory := tree["inventory-"+name+"-amd64.cyclonedx.json"]

		var bom struct {
			Format string `json:"bomFormat"`
		}
		if err := json.Unmarshal(inventory, &bom); err != nil || bom.Format != "CycloneDX" {
			return nil, fmt.Errorf("missing or invalid amd64 inventory for %s: %w", name, errs.ErrValidation)
		}

		delivered := tree["base-sbom-"+name+".cyclonedx.json"]
		if len(delivered) == 0 || string(delivered) != string(inventory) || entry.SBOM != "premade-sboms/base-sbom-"+name+".cyclonedx.json" || entry.SBOMSHA256 != imagePlanSHA256(delivered) {
			return nil, fmt.Errorf("ledger does not pin the delivered amd64 premade inventory for %s: %w", name, errs.ErrValidation)
		}

		result[name] = entry
	}

	return result, nil
}

func TestResolvedImagePlanCacheEvidence(t *testing.T) {
	// Log lines as the Forgejo jobs of runs 524 (cold) and 525 (partial)
	// printed them for steady, timestamps dropped.
	const (
		ref         = "registry.invalid:8443/forgeci/fci-consumer-trial-base@sha256:812f2ba2f9fed2544f6201a0e32f783847d68886ef0bf6f5f8fc247c779eaf99"
		layout      = "kaniko: localhost/steady:ecffb014b163e1427ee4d41af2e714f65db78c067ee33f1713534c227e03bfdd-amd64"
		fetchCommon = "Logged in to registry.invalid:8443 as forgeci → /tmp/registry-input-auth.f0fodh\n"
		buildCommon = "Downloaded resolved-base-review-cold-inputs-steady-amd64 (2 files, 187 bytes) → /workspace/forgeci/fci-consumer-trial/registry-inputs\n" +
			"Restored 0 blobs from blob store resolved-base-review-cold-input-blob → /workspace/forgeci/fci-consumer-trial/registry-inputs\n"
		buildTail = "Blob store resolved-base-review-cold-blob: 0 blobs, 0 bytes uploaded, 0 bytes already in the run\n" +
			"Uploaded resolved-base-review-cold-layout-steady-amd64 (8 files, 2985 bytes)\n"
	)

	reuse := appcontainer.CacheReusePrefix + ref + "\n"
	execute := layout + kaniko.LayoutBuildNoticeSuffix + "\n" + kaniko.ExecutionNotice + "\n" +
		"INFO[0000] Taking snapshot of files...\n" +
		"Image architecture prepared: arch=amd64 digest=sha256:812f2ba2f9fed2544f6201a0e32f783847d68886ef0bf6f5f8fc247c779eaf99\n"
	coldFetch := fetchCommon + "Blob store resolved-base-review-cold-input-blob: 0 blobs, 0 bytes uploaded, 0 bytes already in the run\n"
	coldBuild := buildCommon + execute + buildTail
	partialFetch := fetchCommon + reuse
	partialBuild := buildCommon + buildTail

	for _, tc := range []struct {
		name, fetch, build string
		hit, valid         bool
	}{
		{"run 525 steady reused", partialFetch, partialBuild, true, true},
		{"run 524 steady built", coldFetch, coldBuild, false, true},
		{"reuse reported by the build job", coldFetch, buildCommon + reuse + buildTail, true, false},
		{"execution reported by the fetch job", coldFetch + kaniko.ExecutionNotice + "\n", partialBuild, false, false},
		{"both reuse and execution", partialFetch, coldBuild, true, false},
		{"both on a miss", partialFetch, coldBuild, false, false},
		{"fresh identical build on a hit", coldFetch, coldBuild, true, false},
		{"unexpected reuse on a miss", partialFetch, partialBuild, false, false},
		{"digest alone", fetchCommon + ref + "\n", partialBuild, true, false},
		{"wrong digest", fetchCommon + strings.ReplaceAll(reuse, "812f", "0000"), partialBuild, true, false},
		{"duplicate reuse", partialFetch + reuse, partialBuild, true, false},
		{"execution without its layout line", coldFetch, buildCommon + kaniko.ExecutionNotice + "\n", false, false},
		{"empty miss", "", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := imagePlanCacheEvidence(tc.fetch, tc.build, ref, tc.hit); (err == nil) != tc.valid {
				t.Fatalf("valid=%t: %v", tc.valid, err)
			}
		})
	}
}

func TestResolvedImagePlanReviewRequiresDeliveredEvidence(t *testing.T) {
	for _, broken := range []string{"", "scan", "inventory", "pin", "delivery", "duplicate", "lineage"} {
		t.Run(broken, func(t *testing.T) {
			tree := make(map[string][]byte)
			entries := make([]imageledger.Entry, 0, 2)

			for _, name := range []string{"steady", "changing"} {
				id := strings.Repeat("a", 64)
				ref := "registry.example/base"
				bom := []byte(`{"bomFormat":"CycloneDX"}`)
				entries = append(entries, imageledger.Entry{
					Role: "base", ImageKind: imageledger.ImageKindBase, Flavor: name,
					Digest: "sha256:" + id, Ref: ref + "@sha256:" + id, BaseInputID: id,
					FinalTag: ref + ":" + id + "-" + name, CandidateTag: ref + ":staging-" + id + "-" + name,
					Provenance: map[string]any{"review_fixture": "resolved-image-plan"},
					SBOM:       "premade-sboms/base-sbom-" + name + ".cyclonedx.json", SBOMSHA256: imagePlanSHA256(bom),
				})
				tree["base-sbom-"+name+".cyclonedx.json"] = bom
				tree["inventory-"+name+"-amd64.cyclonedx.json"] = bom
				tree["trivy-results-"+name+"-amd64.json"] = []byte(`{"SchemaVersion":2,"ArtifactName":"layout"}`)
			}

			switch broken {
			case "scan":
				delete(tree, "trivy-results-changing-amd64.json")
			case "inventory":
				tree["inventory-changing-amd64.cyclonedx.json"] = []byte(`{}`)
			case "pin":
				entries[1].SBOMSHA256 = strings.Repeat("b", 64)
			case "delivery":
				delete(tree, "base-sbom-changing.cyclonedx.json")
			case "duplicate":
				entries[1] = entries[0]
			case "lineage":
				entries[1].Provenance = nil
			}

			body, err := json.Marshal(entries)
			if err != nil {
				t.Fatal(err)
			}

			tree["release-images.json"] = body
			if _, err := imagePlanReview(tree, "registry.example/base"); (err == nil) != (broken == "") {
				t.Fatalf("review for %q: %v", broken, err)
			}
		})
	}
}

func TestResolvedImagePlanPolicyPhases(t *testing.T) {
	var fixture struct {
		Jobs map[string]struct {
			Steps []probeStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(resolvedImagePlanFixture), &fixture); err != nil {
		t.Fatal(err)
	}

	var script string

	for _, step := range fixture.Jobs["policy"].Steps {
		if step.ID == "policy" {
			script = step.Run
		}
	}

	if script == "" {
		t.Fatal("missing fixture policy")
	}

	dir := t.TempDir()
	for _, name := range []string{"steady", "changing"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(dir, name, "Containerfile"), []byte(resolvedImagePlanContainerfile), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var previous pipeline.ReleaseImagesPlan

	for _, phase := range []string{"cold", "partial", "warm"} {
		for _, name := range []string{"steady", "changing"} {
			version := "v1"
			if phase != "cold" && name == "changing" {
				version = "v2"
			}

			if err := os.WriteFile(filepath.Join(dir, name, "marker.sh"), []byte(imagePlanMarker(name, version)), 0o600); err != nil {
				t.Fatal(err)
			}
		}

		if err := os.WriteFile(filepath.Join(dir, "canary-phase"), []byte(phase+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		output := filepath.Join(dir, "output-"+phase)
		// Inert collaborator supplies only the opaque native builder identity;
		// execute the actual fixture policy and admit it with the real engine.
		cmd := exec.CommandContext(t.Context(), testBash(t), "--noprofile", "--norc", "-c", "forge-ci() { test \"$*\" = 'container builder-identity' || return 1; printf '%064d\\n' 1; }; "+script)
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + testenv.GNUPath(t, "sha256sum") + ":/usr/bin:/bin", "HOME=" + dir, "LC_ALL=C", "BASE_IMAGE_REPOSITORY=registry.example/base", "FORGEJO_OUTPUT=" + output}

		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s policy: %v: %s", phase, err, out)
		}

		body, err := os.ReadFile(output)
		if err != nil {
			t.Fatal(err)
		}

		lines := strings.Split(strings.TrimSpace(string(body)), "\n")
		if len(lines) != 3 || lines[1] != "artifact=resolved-base-review-"+phase {
			t.Fatalf("unexpected phase output: %s", body)
		}

		plan, err := pipeline.ReadImageBuildPlan(strings.TrimPrefix(lines[0], "image-plan="), "registry.example/base", "v0.0.1")
		if err != nil || len(plan.Builds) != 2 || len(plan.Images) != 2 {
			t.Fatalf("%s plan: %+v: %v", phase, plan, err)
		}

		for idx, image := range plan.Images {
			build := plan.Builds[idx]
			if !image.Scan || image.Revision != image.BaseInputID || build.Revision != image.Revision || build.CacheTag != image.CacheTagPrefix+"-amd64" || build.RequiredLabels != "org.example.base-input-id="+image.BaseInputID || build.BuilderMaterialID == "" {
				t.Fatalf("unbound cache plan: %+v %+v", image, build)
			}

			if phase != "cold" {
				changed := previous.Images[idx].Revision != image.Revision
				if changed != (phase == "partial" && image.Name == "changing") || image.CacheTagPrefix != previous.Images[idx].CacheTagPrefix {
					t.Fatalf("%s changed the wrong content/slot: %+v", phase, image)
				}
			}
		}

		previous = plan

		final, err := pipeline.ReadImageBuildPlan(strings.TrimPrefix(lines[2], "final-plan="), "registry.example/base", "v0.0.1")
		if err != nil || len(final.Builds) != 1 || len(final.Images) != 1 {
			t.Fatalf("%s final plan: %+v: %v", phase, final, err)
		}

		image, build := final.Images[0], final.Builds[0]
		if path := imageledger.DefaultSBOMPath(imageledger.ImageKind(image.ImageKind), image.Flavor, image.Role); path != "dist/image-sbom-distroless.cyclonedx.json" {
			t.Fatalf("final fixture disagrees with production default SBOM path: %s", path)
		}

		if image.Role != "distroless" || image.ImageKind != "release" || !image.Scan || image.Archs != "amd64" || build.Platform != "linux/amd64" || build.Context != "steady" || build.Target != "selected" || build.CacheTag != "" || image.CacheTagPrefix != "" || image.CandidateTag != "staging-v0.0.1" || image.FinalTagName != "v0.0.1" {
			t.Fatalf("unexpected final release scope: %+v %+v", image, build)
		}
	}
}
