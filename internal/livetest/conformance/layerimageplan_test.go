// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"os"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
)

// TestForgejoLayer_ResolvedImagePlan uses two tiny bases and a final-release
// companion across three sequential runs in one public SHA-256 consumer.
// The publisher alone populates cache slots; a content change invalidates one
// slot before the warm repeat.
func TestForgejoLayer_ResolvedImagePlan(t *testing.T) {
	livetest.RegisterLiveScenario(t)
	jobImage := os.Getenv("FORGE_CI_LIVE_IMAGE_PLAN_JOB_IMAGE")
	if jobImage == "" {
		t.Skip("set FORGE_CI_LIVE_IMAGE_PLAN_JOB_IMAGE to the lab kaniko job image for this focused scenario")
	}

	target := livetest.Accept(t, provider.ForgeForgejo)
	if livetest.RunnerArch(t, target) != "amd64" {
		t.Skip("this focused image-filesystem canary currently verifies linux/amd64")
	}

	registry, err := livetest.RegistryHost(target)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(jobImage, registry+"/") || !strings.Contains(jobImage, "@sha256:") {
		t.Fatal("the focused scenario requires a digest-pinned job image on the admitted lab registry")
	}

	// The admitted job image names this exact scratch layer coordinate.
	scenario := newLayerScenario(t, livetest.ConsumerTrial, false, "sha256")
	repo := scenario.repo
	livetest.SweepPackagesAfter(t, target, repo)
	livetest.SetRepoVariable(t, target, repo, "CI_IMAGE_BUILDER", "kaniko")
	livetest.SetRepoVariable(t, target, repo, "CI_IMAGE_BUILDER_JOB_IMAGE", jobImage)
	livetest.SetRepoVariable(t, target, repo, "RELEASE_REGISTRY_USER", "forgeci")
	livetest.SetRepoSecret(t, target, repo, "RELEASE_REGISTRY_TOKEN", target.Token)
	baseRepository := registry + "/" + strings.ToLower(livetest.RepoSlug(target, repo)) + "-base"

	slots := make(map[string]string, 2)

	for _, name := range []string{"steady", "changing"} {
		if _, found := livetest.ImageDigest(t, target, repo+"-base", "cache-arch-"+name+"-amd64"); found {
			t.Fatal("cold phase requires absent scratch cache slots")
		}
	}

	var (
		previous map[string]imageledger.Entry
		afterID  int64
	)

	for _, phase := range []string{"cold", "partial", "warm"} {
		files := map[string]string{"canary-phase": phase + "\n"}
		if phase == "cold" {
			files[".forgejo/workflows/resolved-image-plan.yml"] = scenario.render(t, resolvedImagePlanFixture,
				"RENDER_BASE_REPOSITORY", baseRepository)
			for _, name := range []string{"steady", "changing"} {
				files[name+"/Containerfile"] = resolvedImagePlanContainerfile
				files[name+"/marker.sh"] = imagePlanMarker(name, "v1")
			}
		}

		if phase == "partial" {
			files["changing/marker.sh"] = imagePlanMarker("changing", "v2")
		}

		livetest.CommitFiles(t, target, repo, livetest.FileCommit{
			Branch: layerMain, Message: "test: resolved image cache " + phase, Files: files,
		})
		run := livetest.WaitForConsumerRunAfter(t, target, repo, "resolved-image-plan.yml", "push", afterID)
		jobs := scenario.requireStatus(t, run, jobSuccess)
		afterID = run.ID
		artifactName := "resolved-base-review-" + phase

		var tree map[string][]byte

		for _, artifact := range livetest.RunArtifacts(t, target, repo, run.ID) {
			if artifact.Name == artifactName {
				if tree != nil {
					t.Fatalf("run %d has duplicate review artifacts", run.ID)
				}

				tree, err = livetest.ZIPTree(livetest.ArtifactZIP(t, target, repo, artifact.ID), maxReleaseFile)
				if err != nil {
					t.Fatal(err)
				}
			}
		}

		entries, err := imagePlanReview(tree, baseRepository)
		if err != nil {
			t.Fatalf("%s run %d artifact %s: %v", phase, run.ID, artifactName, err)
		}

		for _, name := range []string{"steady", "changing"} {
			entry := entries[name]
			hit := phase == "warm" || (phase == "partial" && name == "steady")
			matchingJobs := 0

			for _, job := range jobs {
				if !imagePlanBuildJob(job.Name, name) {
					continue
				}

				matchingJobs++

				evidence, err := imagePlanCacheEvidence(livetest.JobLog(t, target, repo, job), baseRepository+"@"+slots[name], hit)
				if err != nil {
					t.Fatalf("%s run %d job %d %q: %v", phase, run.ID, job.ID, job.Name, err)
				}

				t.Logf("cache execution verified: phase=%s run=%d job=%d image=%s hit=%t evidence=%s", phase, run.ID, job.ID, name, hit, evidence)
			}

			if matchingJobs != 1 {
				t.Fatalf("%s run %d: expected exactly one build job for %s, got %d: %+v", phase, run.ID, name, matchingJobs, jobs)
			}

			candidate := "staging-" + entry.BaseInputID + "-" + name
			served, found := livetest.ImageDigest(t, target, repo+"-base", candidate)

			if !found || served != entry.Digest {
				t.Fatal("the namespaced ledger does not identify the registry-served candidate")
			}

			version := "v1"
			if phase != "cold" && name == "changing" {
				version = "v2"
			}

			hashes := livetest.ImageFileSHA256(t, target, repo+"-base", entry.Digest, []string{"/marker.sh"})
			wantMarker := imagePlanSHA256([]byte(imagePlanMarker(name, version)))

			if hashes["marker.sh"] != wantMarker {
				t.Fatal("the actual registry image does not contain the selected target's canary bytes")
			}

			slot, found := livetest.ImageDigest(t, target, repo+"-base", "cache-arch-"+name+"-amd64")
			if !found {
				t.Fatal("the gated publisher did not populate its architecture cache slot")
			}

			if phase != "cold" {
				unchanged := entry.BaseInputID == previous[name].BaseInputID && entry.Digest == previous[name].Digest && slot == slots[name]
				if hit != unchanged || (!hit && (entry.BaseInputID == previous[name].BaseInputID || slot == slots[name])) {
					t.Fatalf("%s %s: content/slot transition disagrees with hit=%t", phase, name, hit)
				}
			}

			slots[name] = slot
			t.Logf("resolved image plan verified: phase=%s run=%d image=%s cache-child=%s@%s marker-sha256=%s sbom-sha256=%s artifact=%s", phase, run.ID, entry.Ref, baseRepository, slot, wantMarker, entry.SBOMSHA256, artifactName)
		}

		previous = entries

		finalArtifact := "resolved-final-review-" + artifactName

		var finalTree map[string][]byte

		for _, artifact := range livetest.RunArtifacts(t, target, repo, run.ID) {
			if artifact.Name != finalArtifact {
				continue
			}

			if finalTree != nil {
				t.Fatalf("run %d has duplicate final review artifacts", run.ID)
			}

			finalTree, err = livetest.ZIPTree(livetest.ArtifactZIP(t, target, repo, artifact.ID), maxReleaseFile)
			if err != nil {
				t.Fatal(err)
			}
		}

		urlPrefix := strings.TrimRight(target.BaseURL(), "/") + "/" + livetest.RepoSlug(target, repo) + "/releases/download/v0.0.1"

		final, child, err := imagePlanFinalReview(finalTree, baseRepository, urlPrefix)
		if err != nil {
			t.Fatalf("%s run %d artifact %s: %v", phase, run.ID, finalArtifact, err)
		}

		finalBuilds := 0

		for _, job := range jobs {
			if !imagePlanBuildJob(job.Name, "final") {
				continue
			}

			finalBuilds++

			if _, err := imagePlanCacheEvidence(livetest.JobLog(t, target, repo, job), "", false); err != nil {
				t.Fatalf("final run %d build job %d: %v", run.ID, job.ID, err)
			}
		}

		if finalBuilds != 1 {
			t.Fatalf("run %d: expected exactly one executed final build, got %d", run.ID, finalBuilds)
		}

		served, found := livetest.ImageDigest(t, target, repo+"-base", "staging-v0.0.1")
		if !found || served != final.Digest {
			t.Fatal("delivered final index bytes do not identify the registry-served candidate")
		}
		// Read the exact child bound by both retained index and BOM, not a
		// mutable tag. No container or product binary is executed.
		hashes := livetest.ImageFileSHA256(t, target, repo+"-base", child, []string{"/marker.sh"})
		if hashes["marker.sh"] != imagePlanSHA256([]byte(imagePlanMarker("steady", "v1"))) {
			t.Fatal("final index child does not contain the selected target's marker")
		}

		t.Logf("final release evidence verified: phase=%s run=%d index=%s child=%s inventory-sha256=%s index-bom-sha256=%s url-prefix=%s artifact=%s", phase, run.ID, final.Ref, child, imagePlanSHA256(finalTree["sbom-distroless-amd64.json"]), final.SBOMSHA256, urlPrefix, finalArtifact)
	}
}
