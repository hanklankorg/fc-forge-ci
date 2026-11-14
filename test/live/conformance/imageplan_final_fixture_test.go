// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package conformance_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
)

// Review delivered bytes independently of the production BOM composer. The live
// caller also compares the retained index hash with the registry-served digest
// and reads the child's marker through the constrained registry SDK.
func imagePlanFinalReview(tree map[string][]byte, repository, urlPrefix string) (imageledger.Entry, string, error) {
	fail := func(reason string) (imageledger.Entry, string, error) {
		return imageledger.Entry{}, "", fmt.Errorf("final image evidence: %s: %w", reason, errs.ErrValidation)
	}

	var entries []imageledger.Entry
	if err := json.Unmarshal(tree["release-images.json"], &entries); err != nil || len(entries) != 1 {
		return fail("expected one delivered release ledger entry")
	}

	entry := entries[0]
	if err := entry.ValidateBeforeCapture("v0.0.1"); err != nil {
		return fail("invalid release ledger")
	}

	if entry.Role != "distroless" || entry.ImageKind != "release" || entry.Flavor != "" || entry.BaseRef != "" || entry.BaseInputID != "" || entry.Ref != repository+"@"+entry.Digest || entry.FinalTag != repository+":v0.0.1" || entry.CandidateTag != repository+":staging-v0.0.1" {
		return fail("release identity or tag binding differs")
	}

	indexBytes := tree["index-final.json"]

	var index struct {
		SchemaVersion int `json:"schemaVersion"`
		Manifests     []struct {
			Digest   string `json:"digest"`
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
				Variant      string `json:"variant"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(indexBytes, &index); err != nil || index.SchemaVersion != 2 || len(index.Manifests) != 1 || entry.Digest != "sha256:"+imagePlanSHA256(indexBytes) {
		return fail("retained index bytes or selected child set differs")
	}

	child := index.Manifests[0]
	if child.Platform.OS != "linux" || child.Platform.Architecture != "amd64" || child.Platform.Variant != "" || !container.ValidDigest(child.Digest) {
		return fail("index child is not native linux/amd64 with a sha256 digest")
	}

	var scan struct {
		SchemaVersion int    `json:"SchemaVersion"`
		ArtifactName  string `json:"ArtifactName"`
	}
	if err := json.Unmarshal(tree["trivy-results-final-amd64.json"], &scan); err != nil || scan.SchemaVersion != 2 || scan.ArtifactName == "" {
		return fail("missing delivered native scan")
	}

	inventory := tree["sbom-distroless-amd64.json"]

	var header struct {
		Format string `json:"bomFormat"`
		Spec   string `json:"specVersion"`
	}
	if err := json.Unmarshal(inventory, &header); err != nil || header.Format != "CycloneDX" || header.Spec == "" {
		return fail("missing delivered native CycloneDX inventory")
	}

	bomBytes := tree["image-sbom-distroless.cyclonedx.json"]
	if entry.SBOM != "dist/image-sbom-distroless.cyclonedx.json" || entry.SBOMSHA256 != imagePlanSHA256(bomBytes) {
		return fail("ledger does not pin the delivered index BOM")
	}

	childRef := repository + "@" + child.Digest
	expected := map[string]any{
		"bomFormat": "CycloneDX", "specVersion": "1.6", "version": float64(1),
		"metadata": map[string]any{"component": map[string]any{"type": "container", "name": repository, "bom-ref": entry.Ref}},
		"components": []any{map[string]any{
			"type": "container", "name": childRef, "bom-ref": childRef + "#linux/amd64",
			"properties": []any{map[string]any{"name": "oci:platform", "value": "linux/amd64"}},
			"externalReferences": []any{map[string]any{"type": "bom", "url": urlPrefix + "/sbom-distroless-amd64.json",
				"hashes": []any{map[string]any{"alg": "SHA-256", "content": imagePlanSHA256(inventory)}}}},
		}},
	}

	var actual map[string]any
	if err := json.Unmarshal(bomBytes, &actual); err != nil || !reflect.DeepEqual(expected, actual) {
		return fail("index BOM platform, digest, URL or inventory hash binding differs")
	}

	return entry, child.Digest, nil
}

func TestResolvedFinalImageReviewRejectsContradictoryDelivery(t *testing.T) {
	const repository = "registry.example/base"

	const prefix = "https://forge.example/forgeci/fci-example/releases/download/v0.0.1"

	for _, broken := range []string{"", "ledger", "duplicate", "role", "tag", "pin", "base-sbom-path", "index", "platform", "child", "scan", "inventory", "missing-bom", "bom-platform", "bom-digest", "bom-url", "bom-hash"} {
		t.Run(broken, func(t *testing.T) {
			child := "sha256:" + strings.Repeat("a", 64)
			index := []byte(`{"schemaVersion":2,"manifests":[{"digest":"` + child + `","platform":{"os":"linux","architecture":"amd64"}}]}`)
			inventory := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6","components":[]}`)
			childRef := repository + "@" + child
			ref := repository + "@sha256:" + imagePlanSHA256(index)
			bom := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6","version":1,"metadata":{"component":{"type":"container","name":"` + repository + `","bom-ref":"` + ref + `"}},"components":[{"type":"container","name":"` + childRef + `","bom-ref":"` + childRef + `#linux/amd64","properties":[{"name":"oci:platform","value":"linux/amd64"}],"externalReferences":[{"type":"bom","url":"` + prefix + `/sbom-distroless-amd64.json","hashes":[{"alg":"SHA-256","content":"` + imagePlanSHA256(inventory) + `"}]}]}]}`)
			entry := imageledger.Entry{Role: "distroless", ImageKind: "release", Ref: ref, Digest: "sha256:" + imagePlanSHA256(index), FinalTag: repository + ":v0.0.1", CandidateTag: repository + ":staging-v0.0.1", SBOM: "dist/image-sbom-distroless.cyclonedx.json", SBOMSHA256: imagePlanSHA256(bom)}
			tree := map[string][]byte{"index-final.json": index, "sbom-distroless-amd64.json": inventory, "image-sbom-distroless.cyclonedx.json": bom, "trivy-results-final-amd64.json": []byte(`{"SchemaVersion":2,"ArtifactName":"staged-layout"}`)}

			switch broken {
			case "role":
				entry.Role = "alpine"
			case "tag":
				entry.CandidateTag = repository + ":staging-other"
			case "pin":
				entry.SBOMSHA256 = strings.Repeat("b", 64)
			case "base-sbom-path":
				entry.SBOM = "premade-sboms/image-sbom-distroless.cyclonedx.json"
			case "index":
				tree["index-final.json"] = append(index, '\n')
			case "platform":
				tree["index-final.json"] = []byte(strings.ReplaceAll(string(index), "amd64", "arm64"))
				entry.Digest = "sha256:" + imagePlanSHA256(tree["index-final.json"])
				entry.Ref = repository + "@" + entry.Digest
			case "child":
				tree["index-final.json"] = []byte(strings.ReplaceAll(string(index), child, "sha256:"+strings.Repeat("b", 64)))
				entry.Digest = "sha256:" + imagePlanSHA256(tree["index-final.json"])
				entry.Ref = repository + "@" + entry.Digest
			case "scan":
				delete(tree, "trivy-results-final-amd64.json")
			case "inventory":
				tree["sbom-distroless-amd64.json"] = append(inventory, '\n')
			case "missing-bom":
				delete(tree, "image-sbom-distroless.cyclonedx.json")
			case "bom-platform":
				tree["image-sbom-distroless.cyclonedx.json"] = []byte(strings.ReplaceAll(string(bom), "linux/amd64", "linux/arm64"))
			case "bom-digest":
				tree["image-sbom-distroless.cyclonedx.json"] = []byte(strings.ReplaceAll(string(bom), child, "sha256:"+strings.Repeat("b", 64)))
			case "bom-url":
				tree["image-sbom-distroless.cyclonedx.json"] = []byte(strings.ReplaceAll(string(bom), prefix, "https://other.example/release"))
			case "bom-hash":
				tree["image-sbom-distroless.cyclonedx.json"] = []byte(strings.ReplaceAll(string(bom), imagePlanSHA256(inventory), strings.Repeat("b", 64)))
			}

			if strings.HasPrefix(broken, "bom-") {
				entry.SBOMSHA256 = imagePlanSHA256(tree["image-sbom-distroless.cyclonedx.json"])
			}

			entries := []imageledger.Entry{entry}
			if broken == "duplicate" {
				entries = append(entries, entry)
			}

			if broken == "ledger" {
				entries = nil
			}

			body, err := json.Marshal(entries)
			if err != nil {
				t.Fatal(err)
			}

			tree["release-images.json"] = body

			_, _, err = imagePlanFinalReview(tree, repository, prefix)
			if (err == nil) != (broken == "") {
				t.Fatalf("delivery %q: %v", broken, err)
			}
		})
	}
}
