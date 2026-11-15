// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/archive"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/sbom"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

const completionStep = "Complete the SBOM bundle with the image SBOMs"

// signJobSteps is the sign job of sign-and-publish-release.yml, in order.
func signJobSteps(t *testing.T) []step {
	t.Helper()

	var document struct {
		Jobs map[string]struct {
			Steps []step `yaml:"steps"`
		} `yaml:"jobs"`
	}
	loadYAML(t, ".forgejo/workflows/sign-and-publish-release.yml", &document)

	return document.Jobs["sign"].Steps
}

// The SBOM bundle is made final before anything that covers it exists: after
// the hand-off is verified against its digest and admitted as unsigned, and
// before the provenance statement and every signature. No later step of the
// job changes the bundle, and the step that completes it is given no key and
// no token.
func TestSBOMBundleIsCompletedBeforeProvenanceAndSignatures(t *testing.T) {
	t.Parallel()

	steps := signJobSteps(t)
	position := func(name string) int {
		index := slices.IndexFunc(steps, func(candidate step) bool { return candidate.Name == name })
		if index == -1 {
			t.Fatalf("the sign job has no step %q", name)
		}

		return index
	}

	completion := position(completionStep)

	for _, earlier := range []string{"Verify dist integrity", "Verify unsigned release file hand-off"} {
		if position(earlier) >= completion {
			t.Errorf("%q must run before the bundle is completed", earlier)
		}
	}

	for _, later := range []string{
		"GPG-sign distro packages", "Generate SLSA provenance", "Cosign sign-blob checksums and release files",
		"GPG-sign checksums and release files", "Sign and attest container images",
		"Cosign sign-blob optional release evidence", "Write release file manifest", "Upload signed dist artifact",
	} {
		if position(later) <= completion {
			t.Errorf("%q must run after the bundle is completed: it covers the bundle or what lists it", later)
		}
	}

	if position("Generate SLSA provenance") >= position("Cosign sign-blob checksums and release files") ||
		position("Generate SLSA provenance") >= position("GPG-sign checksums and release files") {
		t.Error("the provenance statement is written before the release files are signed")
	}

	for index, candidate := range steps {
		changesBundle := strings.Contains(candidate.Run, "--release-images-ledger") || strings.Contains(candidate.Run, "--image-sbom") ||
			strings.Contains(candidate.Run, "sbom-bundle") || strings.Contains(candidate.Run, "files declare")
		if changesBundle && index != completion {
			t.Errorf("step %q changes the SBOM bundle; only %q may", candidate.Name, completionStep)
		}
	}

	env := steps[completion].Env
	if len(env) != 1 || env["RELEASE_IMAGES_PATH"] != "${{ inputs['release-images-path'] }}" {
		t.Errorf("the completion step's env = %q, want the ledger path alone: it holds no key and no token", env)
	}
}

// writeCompletionHandoff writes an unsigned hand-off with one archive and one
// build SBOM in its bundle, and the pinned index SBOM, platform inventories
// and ledger of one release image, into the sandbox's dist.
func writeCompletionHandoff(t *testing.T, box *sandbox) map[string]string {
	t.Helper()

	const repository = "registry.invalid/owner/project"

	want := map[string]string{"app.cdx.json": `{"bomFormat":"CycloneDX"}`}
	parts := make([]sbom.IndexBOMPart, 0, 2)
	children := make([]any, 0, 2)

	for index, arch := range []string{"amd64", "arm64"} {
		digest := "sha256:" + strings.Repeat(strconv.Itoa(index+1), 64)
		name := "app-1.2.3-aaaaaaa-analyzed-container-linux-" + arch + "-sbom.cyclonedx.json"
		body := `{"bomFormat":"CycloneDX","specVersion":"1.6","components":[{"type":"library","name":"` + arch + `"}]}`
		want[name] = body

		parts = append(parts, sbom.IndexBOMPart{Platform: "linux/" + arch, ImageRef: repository + "@" + digest, Name: name, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(body))), Document: []byte(body)})
		children = append(children, map[string]any{"digest": digest, "platform": map[string]string{"os": "linux", "architecture": arch}})
	}

	index, err := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": children})
	if err != nil {
		t.Fatal(err)
	}

	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(index))
	entry := imageledger.Entry{Role: "app", Ref: repository + "@" + digest, Digest: digest, FinalTag: repository + ":v1.2.3", SBOM: "dist/app-1.2.3-aaaaaaa-analyzed-container-index-sbom.cyclonedx.json"}

	bom, err := sbom.ComposeIndexBOM(entry.Ref, index, parts)
	if err != nil {
		t.Fatal(err)
	}

	want["app-1.2.3-aaaaaaa-analyzed-container-index-sbom.cyclonedx.json"] = string(bom)
	entry.SBOMSHA256 = fmt.Sprintf("%x", sha256.Sum256(bom))

	ledger, err := imageledger.Marshal([]imageledger.Entry{entry})
	if err != nil {
		t.Fatal(err)
	}

	var bundle bytes.Buffer

	writer := archive.NewTarGzipWriter(&bundle)
	if err := writer.AddBytes("app.cdx.json", []byte(want["app.cdx.json"])); err != nil {
		t.Fatal(err)
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	for name, body := range want {
		if name != "app.cdx.json" {
			box.write("dist/"+name, body, 0o600)
		}
	}

	box.write("dist/app.tar.gz", "archive\n", 0o600)
	box.write("dist/project-1.2.3-sboms.tar.gz", bundle.String(), 0o600)
	box.write("dist/release-images.json", string(ledger), 0o600)
	box.write("dist/checksums.sha256", fmt.Sprintf("%x  app.tar.gz\n%x  project-1.2.3-sboms.tar.gz\n", sha256.Sum256([]byte("archive\n")), sha256.Sum256(bundle.Bytes())), 0o600)
	box.write("dist/release-files.json", `{"version":1,`+
		`"assets":[{"path":"dist/app.tar.gz","name":"app.tar.gz","source":"producer"},`+
		`{"path":"dist/project-1.2.3-sboms.tar.gz","name":"project-1.2.3-sboms.tar.gz","source":"sbom_bundle"},`+
		`{"path":"dist/checksums.sha256","name":"checksums.sha256","source":"producer"}],`+
		`"checksums":[{"path":"dist/checksums.sha256","name":"checksums.sha256","source":"producer"}],`+
		`"sboms":[{"path":"dist/project-1.2.3-sboms.tar.gz","name":"project-1.2.3-sboms.tar.gz","source":"sbom_bundle"}],`+
		`"evidence":[],"provenance":[]}`, 0o600)

	return want
}

// The completion step runs here, its own shell against the engine built from
// this tree. With a ledger the image SBOMs it pins join the build's SBOM in
// the one bundle and the checksums file lists that bundle; without a ledger
// the hand-off is left as it is; an inventory that is not the one the index
// SBOM records, or a signature that already covers the bundle, stops the job
// with the hand-off unchanged.
func TestSBOMBundleCompletionStepThroughTheEngine(t *testing.T) {
	t.Parallel()

	engine := forgeCIEngine(t)
	lib := string(reporoot.ReadFile(t, ".engine/bin/lib-forge-ci.sh"))

	for _, scenario := range []string{"image SBOMs join the bundle", "no ledger", "changed inventory", "bundle already signed"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()

			box := newSandbox(t, "sign-and-publish-release")
			box.write(".engine/bin/lib-forge-ci.sh", lib, 0o600)
			want := writeCompletionHandoff(t, box)

			switch scenario {
			case "no ledger":
				if err := os.Remove(filepath.Join(box.root, "dist/release-images.json")); err != nil {
					t.Fatal(err)
				}
			case "changed inventory":
				box.write("dist/app-1.2.3-aaaaaaa-analyzed-container-linux-arm64-sbom.cyclonedx.json", `{"bomFormat":"CycloneDX","specVersion":"1.6"}`, 0o600)
			case "bundle already signed":
				box.write("dist/project-1.2.3-sboms.tar.gz.sig", "early\n", 0o600)
			}

			read := func(name string) string {
				body, err := os.ReadFile(filepath.Join(box.root, "dist", name))
				if err != nil {
					t.Fatal(err)
				}

				return string(body)
			}
			handedOver := map[string]string{}

			for _, name := range []string{"project-1.2.3-sboms.tar.gz", "checksums.sha256", "release-files.json"} {
				handedOver[name] = read(name)
			}

			got := box.run(workflowStep(t, "sign-and-publish-release.yml", "sign", completionStep).Run,
				"FORGE_CI_BIN="+engine, "HELPERS_DIR="+box.root, "CONSUMER_WORKSPACE="+box.root,
				"DIST_DIR=dist", "RELEASE_FILES_MANIFEST=dist/release-files.json", "RELEASE_IMAGES_PATH=",
				"FORGEJO_REPO=owner/project", "RELEASE_TAG=v1.2.3")

			if scenario != "image SBOMs join the bundle" {
				if (got.status != 0) != (scenario != "no ledger") {
					t.Fatalf("status %d\nstdout %s\nstderr %s", got.status, got.stdout, got.stderr)
				}

				for name, body := range handedOver {
					if read(name) != body {
						t.Errorf("%s changed although the bundle was not completed", name)
					}
				}

				return
			}

			if got.status != 0 {
				t.Fatalf("status %d\nstdout %s\nstderr %s", got.status, got.stdout, got.stderr)
			}

			members, err := archive.ReadTarGzipFiles(filepath.Join(box.root, "dist/project-1.2.3-sboms.tar.gz"), 1<<20)
			if err != nil {
				t.Fatal(err)
			}

			bundled := map[string]string{}
			for _, member := range members {
				bundled[member.Name] = string(member.Body)
			}

			if fmt.Sprint(bundled) != fmt.Sprint(want) {
				t.Errorf("bundle holds %v, want the build SBOM and the image SBOMs %v", bundled, want)
			}

			line := fmt.Sprintf("%x  project-1.2.3-sboms.tar.gz\n", sha256.Sum256([]byte(read("project-1.2.3-sboms.tar.gz"))))
			if checksums := read("checksums.sha256"); !strings.Contains(checksums, line) || strings.Count(checksums, "project-1.2.3-sboms.tar.gz") != 1 {
				t.Errorf("checksums.sha256 does not list the completed bundle once:\n%s", checksums)
			}

			if manifest := read("release-files.json"); strings.Contains(manifest, "-analyzed-container-") {
				t.Errorf("an image SBOM became a release file of its own:\n%s", manifest)
			}
		})
	}
}
