// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
)

type filePayload struct {
	Body string
	Mode fs.FileMode
}

const snapshotArtifactBranch = "feature/artifact-source"
const snapshotArtifactConfig = `artifacts:
  - name: canary
    project-type: go
    sboms: none
    config:
      binary-name: canary
      platforms: linux/amd64
containers:
  - name: canary
    from: [canary]
    container-file: Containerfile
    context: .
    platforms: linux/amd64
    enable-scan: false
    enable-slsa: false
`
const snapshotArtifactContainerfile = "FROM scratch\nCOPY --chmod=0755 dist/linux-amd64/canary-linux-amd64 /canary\nENTRYPOINT [\"/canary\"]\n"

func snapshotCanaryFiles(selected bool) map[string]filePayload {
	marker := "policy-source"
	if selected {
		marker = "selected-source"
	}

	return map[string]filePayload{
		".forge-ci/artifacts.yml": {Body: snapshotArtifactConfig, Mode: 0644},
		"Containerfile":           {Body: snapshotArtifactContainerfile, Mode: 0644},
		"go.mod":                  {Body: "module example.invalid/snapshot-canary\n\ngo 1.27.3\n", Mode: 0644},
		"main.go":                 {Body: "package main\nimport \"fmt\"\nvar commit = \"unknown\"\nvar version = \"unknown\"\nvar date = \"unknown\"\nfunc main() { fmt.Println(\"" + marker + "|\"+commit) }\n", Mode: 0644},
	}
}

func checkSnapshotArtifactEvidence(planBytes, resultBytes, inventoryBytes []byte, source, imageRepository string) (snapshotimage.Receipt, snapshotimage.DependencyFile, error) {
	if len(source) != 40 && len(source) != 64 {
		return snapshotimage.Receipt{}, snapshotimage.DependencyFile{}, errors.New("source must be a full commit")
	}

	var (
		plan      snapshotimage.Plan
		receipts  []snapshotimage.Receipt
		inventory snapshotimage.DependencyInventory
	)

	if err := json.Unmarshal(planBytes, &plan); err != nil {
		return snapshotimage.Receipt{}, snapshotimage.DependencyFile{}, err
	}

	if err := plan.Validate(); err != nil {
		return snapshotimage.Receipt{}, snapshotimage.DependencyFile{}, err
	}

	if err := json.Unmarshal(resultBytes, &receipts); err != nil {
		return snapshotimage.Receipt{}, snapshotimage.DependencyFile{}, err
	}

	if err := json.Unmarshal(inventoryBytes, &inventory); err != nil {
		return snapshotimage.Receipt{}, snapshotimage.DependencyFile{}, err
	}

	fail := func() (snapshotimage.Receipt, snapshotimage.DependencyFile, error) {
		return snapshotimage.Receipt{}, snapshotimage.DependencyFile{}, errors.New("artifact-first snapshot source/plan/receipt/inventory mismatch")
	}
	branchHash := fmt.Sprintf("%x", sha256.Sum256([]byte(snapshotArtifactBranch)))
	suffix := "-feature-artifact-source-" + branchHash[:16] + "-" + source[:7] + "-snapshot"

	if !plan.Enabled || plan.SourceSHA != source || plan.SourceBranch != snapshotArtifactBranch || !strings.HasSuffix(plan.Tag, suffix) || len(plan.Artifacts) != 1 || len(plan.Builds) != 1 || len(plan.Images) != 1 || plan.Builds[0].Arch != "amd64" || len(plan.Builds[0].RequiredArtifacts) != 1 || plan.Images[0].Scan || plan.Images[0].SBOM || len(receipts) != 1 {
		return fail()
	}

	r := receipts[0]
	if r.SourceSHA != source || r.SourceBranch != snapshotArtifactBranch || r.Tag != plan.Tag || r.ImageRepository != imageRepository || r.Ref != imageRepository+"@"+r.Digest || len(r.Platforms) != 1 || r.Platforms[0] != "linux/amd64" {
		return fail()
	}

	if inventory.SourceSHA != source || inventory.SourceBranch != snapshotArtifactBranch || len(inventory.Dependencies) != 1 || len(inventory.Dependencies[0].Files) != 1 || inventory.Dependencies[0].Platform != "linux/amd64" {
		return fail()
	}

	file := inventory.Dependencies[0].Files[0]
	if file.Mode != "0755" || file.Size <= 0 || len(file.SHA256) != 64 || file.Path != "dist/linux-amd64/canary-linux-amd64" {
		return fail()
	}

	return r, file, nil
}

func TestSnapshotArtifactEvidenceRefusesWrongSourceOrNonExecutableContext(t *testing.T) {
	cfg, err := config.Parse([]byte(snapshotArtifactConfig))
	if err != nil {
		t.Fatal(err)
	}

	if err = config.Derive(cfg); err != nil {
		t.Fatal(err)
	}

	source := strings.Repeat("a", 40)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(snapshotArtifactBranch)))
	tag := "0.0.0-feature-artifact-source-" + hash[:16] + "-" + source[:7] + "-snapshot"

	for _, broken := range []string{"", "mode", "inventory-source", "receipt-source", "receipt-tag", "disabled"} {
		t.Run(broken, func(t *testing.T) {
			plan, err := snapshotimage.New(pipeline.NewConfigPlan(cfg), snapshotimage.Input{SourceSHA: source, SourceBranch: snapshotArtifactBranch, Tag: tag, ImageRepository: "registry.example/smoke/canary", SourceDateEpoch: "1", Publish: true})
			if err != nil {
				t.Fatal(err)
			}

			receipt := snapshotimage.Receipt{Version: 1, SourceSHA: source, SourceBranch: snapshotArtifactBranch, Name: "canary", ImageRepository: "registry.example/smoke/canary", Tag: tag, Digest: "sha256:" + strings.Repeat("b", 64), Ref: "registry.example/smoke/canary@sha256:" + strings.Repeat("b", 64), Platforms: []string{"linux/amd64"}}
			inv := snapshotimage.DependencyInventory{Version: 1, SourceSHA: source, SourceBranch: snapshotArtifactBranch, Dependencies: []snapshotimage.DependencyRecord{{Platform: "linux/amd64", Files: []snapshotimage.DependencyFile{{Path: "dist/linux-amd64/canary-linux-amd64", SHA256: strings.Repeat("c", 64), Size: 10, Mode: "0755"}}}}}

			switch broken {
			case "mode":
				inv.Dependencies[0].Files[0].Mode = "0644"
			case "inventory-source":
				inv.SourceSHA = strings.Repeat("d", 40)
			case "receipt-source":
				receipt.SourceSHA = strings.Repeat("d", 40)
			case "receipt-tag":
				receipt.Tag = "latest"
			case "disabled":
				plan.Enabled = false
			}

			pb, _ := json.Marshal(plan)
			rb, _ := json.Marshal([]snapshotimage.Receipt{receipt})
			ib, _ := json.Marshal(inv)

			_, _, err = checkSnapshotArtifactEvidence(pb, rb, ib, source, receipt.ImageRepository)
			if (err == nil) != (broken == "") {
				t.Fatalf("broken=%q err=%v", broken, err)
			}
		})
	}
}

func TestSnapshotArtifactFixtureSeparatesSelectedSource(t *testing.T) {
	if snapshotCanaryFiles(false)["main.go"] == snapshotCanaryFiles(true)["main.go"] {
		t.Fatal("source branch must differ from policy branch")
	}

	if strings.Contains(snapshotArtifactConfig, "arm64") || !strings.Contains(snapshotArtifactContainerfile, "COPY --chmod=0755") {
		t.Fatal("fixture must remain a single native executable handoff")
	}

	cfg, err := config.Parse([]byte(snapshotArtifactConfig))
	if err != nil {
		t.Fatal(err)
	}

	if err = config.Validate(cfg); err != nil {
		t.Fatal(err)
	}

	if err = config.Derive(cfg); err != nil {
		t.Fatal(err)
	}

	if len(cfg.Artifacts) != 1 || len(cfg.Containers) != 1 || cfg.Containers[0].EnableScan == nil || *cfg.Containers[0].EnableScan || cfg.Containers[0].EnableAnalyzedContainerSBOM {
		t.Fatal("fixture must declare exactly one Go producer with scan/SBOM explicitly disabled")
	}
}
