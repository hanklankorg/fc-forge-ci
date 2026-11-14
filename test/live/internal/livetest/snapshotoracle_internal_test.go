// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// The evidence oracle is judged here on trees shaped as the snapshot
// publisher retains them; no run produced them, so this is no native
// evidence of SBOM generation.
func TestSnapshotEvidenceFindings(t *testing.T) {
	t.Parallel()

	const (
		scan     = `{"SchemaVersion":2,"ArtifactName":"registry.invalid/service/snapshot:1"}`
		document = `{"bomFormat":"CycloneDX","specVersion":"1.6","version":1,"components":[]}`
	)

	plan := snapshotimage.Plan{Images: []snapshotimage.Image{
		{Name: "service", Platforms: []string{"linux/amd64"}, Scan: true, SBOM: true},
		{Name: "worker", Platforms: []string{"linux/amd64"}, Scan: true},
	}}

	receipts := make([]snapshotimage.Receipt, 0, len(plan.Images))
	for i, image := range plan.Images {
		digest := "sha256:" + strings.Repeat(string(rune('a'+i)), 64)
		receipts = append(receipts, snapshotimage.Receipt{
			Name: image.Name, ImageRepository: "registry.invalid/service/snapshot", Tag: "1-" + image.Name, Digest: digest, Ref: "registry.invalid/service/snapshot@" + digest,
		})
	}

	record := func(receipt snapshotimage.Receipt) []byte {
		return fmt.Appendf(nil, `{"tag":%q,"digest":%q,"ref":%q}`, receipt.ImageRepository+":"+receipt.Tag, receipt.Digest, receipt.Ref)
	}

	// kept is what a run that kept the plan's policy retains: the SBOM of
	// service, which the plan requires, and none of worker, which it forbids.
	kept := func() map[string]map[string][]byte {
		return map[string]map[string][]byte{
			"service": {
				"service-dist/service.json":               record(receipts[0]),
				"service/linux-amd64/trivy.json":          []byte(scan),
				"service/linux-amd64/sbom.cyclonedx.json": []byte(document),
			},
			"worker": {
				"worker-dist/worker.json":       record(receipts[1]),
				"worker/linux-amd64/trivy.json": []byte(scan),
			},
		}
	}

	for _, tc := range []struct {
		name   string
		mutate func(evidence map[string]map[string][]byte)
		want   []string
	}{
		{name: "required present, forbidden absent with its scan", mutate: func(map[string]map[string][]byte) {}},
		{name: "derived scan reports", mutate: func(evidence map[string]map[string][]byte) {
			evidence["worker"]["worker/linux-amd64/trivy.sarif"] = []byte(`{}`)
			evidence["worker"]["worker/linux-amd64/gitlab.json"] = []byte(`{}`)
		}},
		{name: "required and missing", mutate: func(evidence map[string]map[string][]byte) {
			delete(evidence["service"], "service/linux-amd64/sbom.cyclonedx.json")
		}, want: []string{"image service (linux/amd64) retained no SBOM, which its plan requires"}},
		{name: "forbidden but present", mutate: func(evidence map[string]map[string][]byte) {
			evidence["worker"]["worker/linux-amd64/sbom.cyclonedx.json"] = []byte(document)
		}, want: []string{"image worker (linux/amd64) retained an SBOM its plan forbids"}},
		{name: "forbidden under another name", mutate: func(evidence map[string]map[string][]byte) {
			evidence["worker"]["worker/linux-amd64/inventory.cyclonedx.json"] = []byte(document)
		}, want: []string{"image worker retained unplanned evidence worker/linux-amd64/inventory.cyclonedx.json"}},
		{name: "forbidden absent without its scan", mutate: func(evidence map[string]map[string][]byte) {
			delete(evidence["worker"], "worker/linux-amd64/trivy.json")
		}, want: []string{"image worker (linux/amd64) retained no Trivy report"}},
		{name: "another image's SBOM", mutate: func(evidence map[string]map[string][]byte) {
			evidence["service"]["worker/linux-amd64/sbom.cyclonedx.json"] = evidence["service"]["service/linux-amd64/sbom.cyclonedx.json"]
			delete(evidence["service"], "service/linux-amd64/sbom.cyclonedx.json")
		}, want: []string{
			"image service (linux/amd64) retained no SBOM, which its plan requires",
			"image service retained unplanned evidence worker/linux-amd64/sbom.cyclonedx.json",
		}},
		{name: "unplanned platform", mutate: func(evidence map[string]map[string][]byte) {
			evidence["service"]["service/linux-arm64/sbom.cyclonedx.json"] = []byte(document)
		}, want: []string{"image service retained unplanned evidence service/linux-arm64/sbom.cyclonedx.json"}},
		{name: "bound to another digest", mutate: func(evidence map[string]map[string][]byte) {
			evidence["service"]["service-dist/service.json"] = record(receipts[1])
		}, want: []string{"the evidence of image service is not bound to its receipt: service-dist/service.json records {Tag:registry.invalid/service/snapshot:1-worker Digest:" + receipts[1].Digest + " Ref:" + receipts[1].Ref + "}"}},
		{name: "bound to a bare tag", mutate: func(evidence map[string]map[string][]byte) {
			evidence["service"]["service-dist/service.json"] = fmt.Appendf(nil, `{"tag":%q,"digest":%q,"ref":%q}`, receipts[0].Tag, receipts[0].Digest, receipts[0].Ref)
		}, want: []string{"the evidence of image service is not bound to its receipt: service-dist/service.json records {Tag:1-service Digest:" + receipts[0].Digest + " Ref:" + receipts[0].Ref + "}"}},
		{name: "unbound", mutate: func(evidence map[string]map[string][]byte) {
			delete(evidence["service"], "service-dist/service.json")
		}, want: []string{"the evidence of image service is not bound to its receipt: service-dist/service.json records {Tag: Digest: Ref:}"}},
		{name: "malformed JSON", mutate: func(evidence map[string]map[string][]byte) {
			evidence["service"]["service/linux-amd64/sbom.cyclonedx.json"] = []byte(document[:len(document)-1])
		}, want: []string{"image service (linux/amd64) retained an SBOM that is not a CycloneDX document"}},
		{name: "another format", mutate: func(evidence map[string]map[string][]byte) {
			evidence["service"]["service/linux-amd64/sbom.cyclonedx.json"] = []byte(`{"spdxVersion":"SPDX-2.3"}`)
		}, want: []string{"image service (linux/amd64) retained an SBOM that is not a CycloneDX document"}},
		{name: "missing evidence artifact", mutate: func(evidence map[string]map[string][]byte) {
			delete(evidence, "worker")
		}, want: []string{"image worker retained no evidence artifact"}},
		{name: "unexpected evidence artifact", mutate: func(evidence map[string]map[string][]byte) {
			evidence["extra"] = map[string][]byte{"extra/linux-amd64/sbom.cyclonedx.json": []byte(document)}
		}, want: []string{"evidence artifact of unplanned image extra"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			evidence := kept()
			tc.mutate(evidence)

			if got := SnapshotEvidenceFindings(plan, receipts, evidence); strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Fatalf("findings %q, want %q", got, tc.want)
			}
		})
	}

	// A receipt the collection did not return binds nothing.
	if got := SnapshotEvidenceFindings(plan, receipts[:1], kept()); len(got) != 1 || !strings.HasPrefix(got[0], "the evidence of image worker is not bound to its receipt") {
		t.Fatalf("findings %q, want the unbound worker alone", got)
	}
}

// The oracle on the plans of the shipped declarations, one per cell of
// snapshot route and image SBOM policy. Evidence shaped as a run that kept a
// plan's policy has no finding; the opposite of it, an SBOM where the
// declaration forbids one or none where it requires one, has the finding the
// policy names. No run produced these trees: which cell a native run keeps
// is for that run to show.
func TestSnapshotEvidenceFindingsOfTheDemoPolicies(t *testing.T) {
	t.Parallel()

	const (
		scan     = `{"SchemaVersion":2}`
		document = `{"bomFormat":"CycloneDX","specVersion":"1.6","version":1}`
		digest   = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)

	for name, cell := range map[string]struct {
		declaration string
		producers   int
		sbom        bool
	}{
		"artifact-first with":     {"hello-maven-service/consumer/" + DefaultArtifactsConfig, 1, true},
		"artifact-first without":  {"hello-maven-service/consumer/" + mavenServiceBuildSBOMDeclaration, 1, false},
		"container-first with":    {"hello-go-service/consumer/" + DefaultArtifactsConfig, 0, true},
		"container-first without": {"hello-cargo-container/consumer/" + GitLabDedicatedReleaseImagesConfig, 0, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, plan, err := serviceSnapshotDeclarationOverlay(string(reporoot.ReadFile(t, "test/live/fixtures/consumers/"+cell.declaration)))
			if err != nil {
				t.Fatal(err)
			}

			if len(plan.Images) != 1 || len(plan.Artifacts) != cell.producers || plan.Images[0].SBOM != cell.sbom {
				t.Fatalf("%s plans %d producers and image SBOM %t, want %d and %t", cell.declaration, len(plan.Artifacts), plan.Images[0].SBOM, cell.producers, cell.sbom)
			}

			image := plan.Images[0]
			receipts := []snapshotimage.Receipt{{Name: image.Name, ImageRepository: image.ImageRepository, Tag: image.Tag, Digest: digest, Ref: image.ImageRepository + "@" + digest}}
			sbom := image.Name + "/linux-amd64/sbom.cyclonedx.json"

			evidence := map[string]map[string][]byte{image.Name: {
				image.Name + "-dist/" + image.Name + ".json": fmt.Appendf(nil, `{"tag":%q,"digest":%q,"ref":%q}`, image.ImageRepository+":"+image.Tag, receipts[0].Digest, receipts[0].Ref),
				image.Name + "/linux-amd64/trivy.json":       []byte(scan),
			}}
			if cell.sbom {
				evidence[image.Name][sbom] = []byte(document)
			}

			if got := SnapshotEvidenceFindings(plan, receipts, evidence); len(got) != 0 {
				t.Fatalf("a run that kept the policy has findings %q", got)
			}

			want := "image " + image.Name + " (linux/amd64) retained an SBOM its plan forbids"
			if cell.sbom {
				delete(evidence[image.Name], sbom)

				want = "image " + image.Name + " (linux/amd64) retained no SBOM, which its plan requires"
			} else {
				evidence[image.Name][sbom] = []byte(document)
			}

			if got := SnapshotEvidenceFindings(plan, receipts, evidence); len(got) != 1 || got[0] != want {
				t.Fatalf("findings %q, want %q", got, want)
			}
		})
	}
}

// The files oracle is judged on a bundle shaped as the snapshot's files job
// keeps it; no run produced it. A bundle that keeps its plan has no finding,
// and each difference is named by its own.
func TestSnapshotFilesFindings(t *testing.T) {
	t.Parallel()

	in, err := serviceSnapshotStandInInput()
	if err != nil {
		t.Fatal(err)
	}

	const declaration = "artifacts: [{name: app, project-type: go}]\n"

	plan, err := planSnapshotDeclaration(declaration, in)
	if err != nil || len(plan.Files) != 1 {
		t.Fatalf("%+v, %v; want one planned file output", plan.Files, err)
	}

	frozen, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}

	run := SnapshotRun{Declaration: declaration, Branch: in.SourceBranch, Head: in.SourceSHA, Plan: frozen}
	if expected := RequireSnapshotFilesPlan(t, run); !reflect.DeepEqual(expected, plan) {
		t.Fatalf("the oracle expects %+v of the frozen plan %+v", expected, plan)
	}

	binary := []byte("built")

	bundle := func(change func(receipt *snapshotimage.FilesReceipt, tree map[string][]byte)) map[string]map[string][]byte {
		receipt := snapshotimage.FilesReceipt{
			Version: snapshotimage.Version, SourceSHA: plan.SourceSHA, SourceBranch: plan.SourceBranch, SnapshotVersion: plan.ArtifactVersion(),
			Name: "app", ProjectType: plan.Files[0].ProjectType, Signing: snapshotimage.FilesSigning,
			Files: []snapshotimage.DependencyFile{{Path: "app_linux_amd64", SHA256: fmt.Sprintf("%x", sha256.Sum256(binary)), Size: int64(len(binary)), Mode: "0755"}},
		}
		tree := map[string][]byte{"app_linux_amd64": binary}

		change(&receipt, tree)

		body, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}

		tree[snapshotimage.FilesManifest] = body

		return map[string]map[string][]byte{"app": tree}
	}

	if findings := SnapshotFilesFindings(plan, bundle(func(*snapshotimage.FilesReceipt, map[string][]byte) {})); len(findings) != 0 {
		t.Fatalf("a bundle that keeps its plan: %v", findings)
	}

	if findings := SnapshotFilesFindings(plan, map[string]map[string][]byte{"other": {}}); len(findings) != 2 ||
		!strings.Contains(findings[0], "app kept no bundle") || !strings.Contains(findings[1], "unplanned file output other") {
		t.Fatalf("a run that kept another output's bundle: %v", findings)
	}

	for name, test := range map[string]struct {
		change func(receipt *snapshotimage.FilesReceipt, tree map[string][]byte)
		want   string
	}{
		"signed": {func(receipt *snapshotimage.FilesReceipt, _ map[string][]byte) { receipt.Signing = "COSIGN" }, "Signing:COSIGN"},
		"other commit": {func(receipt *snapshotimage.FilesReceipt, _ map[string][]byte) {
			receipt.SourceSHA = strings.Repeat("b", 40)
		}, "SourceSHA:" + strings.Repeat("b", 40)},
		"no file":       {func(receipt *snapshotimage.FilesReceipt, _ map[string][]byte) { receipt.Files = nil }, "lists no file"},
		"file missing":  {func(_ *snapshotimage.FilesReceipt, tree map[string][]byte) { delete(tree, "app_linux_amd64") }, "which its bundle does not hold"},
		"file changed":  {func(_ *snapshotimage.FilesReceipt, tree map[string][]byte) { tree["app_linux_amd64"] = []byte("other") }, "its receipt lists 5 bytes and sha256"},
		"file unlisted": {func(_ *snapshotimage.FilesReceipt, tree map[string][]byte) { tree["extra"] = binary }, "holds extra, which its receipt does not list"},
	} {
		if findings := SnapshotFilesFindings(plan, bundle(test.change)); len(findings) == 0 || !strings.Contains(strings.Join(findings, "\n"), test.want) {
			t.Errorf("%s: findings %v, want one naming %q", name, findings, test.want)
		}
	}
}

// An npm file output keeps its one packed tarball: a second file and a file
// that is no tarball are each a finding, though the receipt lists and hashes
// them truly.
func TestSnapshotFilesFindingsOfAnNPMPackage(t *testing.T) {
	t.Parallel()

	in, err := serviceSnapshotStandInInput()
	if err != nil {
		t.Fatal(err)
	}

	plan, err := planSnapshotDeclaration("artifacts: [{name: pkg, project-type: npm}]\n", in)
	if err != nil || len(plan.Files) != 1 {
		t.Fatalf("%+v, %v; want one planned file output", plan.Files, err)
	}

	packed := []byte("packed")

	bundle := func(names ...string) map[string]map[string][]byte {
		receipt := snapshotimage.FilesReceipt{
			Version: snapshotimage.Version, SourceSHA: plan.SourceSHA, SourceBranch: plan.SourceBranch, SnapshotVersion: plan.ArtifactVersion(),
			Name: "pkg", ProjectType: plan.Files[0].ProjectType, Signing: snapshotimage.FilesSigning,
		}
		tree := map[string][]byte{}

		for _, name := range names {
			receipt.Files = append(receipt.Files, snapshotimage.DependencyFile{Path: name, SHA256: fmt.Sprintf("%x", sha256.Sum256(packed)), Size: int64(len(packed)), Mode: "0644"})
			tree[name] = packed
		}

		body, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}

		tree[snapshotimage.FilesManifest] = body

		return map[string]map[string][]byte{"pkg": tree}
	}

	if findings := SnapshotFilesFindings(plan, bundle("pkg-1.0.0.tgz")); len(findings) != 0 {
		t.Fatalf("a bundle of one tarball: %v", findings)
	}

	for name, files := range map[string][]string{"two tarballs": {"pkg-1.0.0.tgz", "other-1.0.0.tgz"}, "no tarball": {"bom.json"}} {
		if findings := SnapshotFilesFindings(plan, bundle(files...)); len(findings) != 1 || !strings.Contains(findings[0], "want its one packed tarball") {
			t.Errorf("%s: findings %v, want the one naming the tarball", name, findings)
		}
	}
}

// A published snapshot manifest is an index of its planned platforms and no
// other child: an unplanned architecture, an attestation manifest, a missing
// platform and a manifest that is no index are each refused.
func TestSnapshotManifestPlatforms(t *testing.T) {
	t.Parallel()

	const (
		index  = "application/vnd.oci.image.index.v1+json"
		image  = "application/vnd.oci.image.manifest.v1+json"
		digest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	)

	child := func(platform string) string {
		system, architecture, _ := strings.Cut(platform, "/")

		return fmt.Sprintf(`{"mediaType":%q,"size":1,"digest":%q,"platform":{"os":%q,"architecture":%q}}`, image, digest, system, architecture)
	}

	manifest := func(children ...string) []byte {
		return fmt.Appendf(nil, `{"schemaVersion":2,"mediaType":%q,"manifests":[%s]}`, index, strings.Join(children, ","))
	}

	amd64, arm64 := child("linux/amd64"), child("linux/arm64")

	for name, held := range map[string]error{
		"one planned child": snapshotManifestPlatforms(index, manifest(amd64), []string{"linux/amd64"}),
		"both planned":      snapshotManifestPlatforms(index, manifest(arm64, amd64), []string{"linux/amd64", "linux/arm64"}),
	} {
		if held != nil {
			t.Errorf("%s: %v", name, held)
		}
	}

	for name, refused := range map[string]error{
		"unplanned architecture": snapshotManifestPlatforms(index, manifest(amd64, arm64), []string{"linux/amd64"}),
		"attestation manifest":   snapshotManifestPlatforms(index, manifest(amd64, child("unknown/unknown")), []string{"linux/amd64"}),
		"missing platform":       snapshotManifestPlatforms(index, manifest(amd64), []string{"linux/amd64", "linux/arm64"}),
		"child without platform": snapshotManifestPlatforms(index, manifest(fmt.Sprintf(`{"mediaType":%q,"size":1,"digest":%q}`, image, digest)), []string{"linux/amd64"}),
		"single image":           snapshotManifestPlatforms(image, []byte(`{}`), []string{"linux/amd64"}),
		"neither":                snapshotManifestPlatforms("text/plain", nil, []string{"linux/amd64"}),
	} {
		if refused == nil {
			t.Errorf("%s was admitted", name)
		}
	}
}
