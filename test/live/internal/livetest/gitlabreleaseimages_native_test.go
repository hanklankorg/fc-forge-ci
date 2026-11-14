// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package livetest

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

func TestGitLabReleaseEvidenceNativeImageJourney(t *testing.T) {
	for _, goMetadata := range []bool{false, true} {
		name := "declared"
		if goMetadata {
			name = "goreleaser"
		}

		t.Run(name, func(t *testing.T) {
			fixture := newNativeReleaseJourney(t, Binary(t), HostTool(t, "cosign"), goMetadata, true)

			got, err := fixture.checkWithPublisher(t, fixture.producer, fixture.publisher, fixture.signer, fixture.published)
			if err != nil || got.Verified != 8 {
				t.Fatalf("native build/image-publisher/signer/published chain: %+v, %v", got, err)
			}
		})
	}
}

func TestGitLabReleaseEvidenceRejectsImageSubstitutions(t *testing.T) {
	fixture := newNativeReleaseJourney(t, Binary(t), HostTool(t, "cosign"), true, true)
	for _, name := range []string{"missing publisher", "changed build subject", "changed publisher subject", "missing ledger", "missing index", "changed index", "missing platform", "changed platform", "missing pin", "wrong pin", "unplanned image", "unplanned ordinary evidence", "removed build declaration", "fabricated publisher source", "signer-only evidence", "changed signer ledger", "changed signer evidence", "published image substitution"} {
		t.Run(name, func(t *testing.T) {
			build, publisher, signer, published := maps.Clone(fixture.producer), maps.Clone(fixture.publisher), maps.Clone(fixture.signer), maps.Clone(fixture.published)

			switch name {
			case "missing publisher":
				publisher = nil
			case "changed build subject":
				build["dist/release-notes.md"] = []byte("different build notes")
			case "changed publisher subject":
				publisher["dist/release-notes.md"] = []byte("publisher rewrote build notes")
			case "missing ledger":
				delete(publisher, "dist/release-images.json")
			case "missing index":
				delete(publisher, "dist/image-sbom-app.cyclonedx.json")
			case "changed index":
				publisher["dist/image-sbom-app.cyclonedx.json"] = append(publisher["dist/image-sbom-app.cyclonedx.json"], '\n')
			case "missing platform":
				delete(publisher, "dist/sbom-app-amd64.json")
			case "changed platform":
				publisher["dist/sbom-app-amd64.json"] = []byte(`{"bomFormat":"CycloneDX","components":[{"name":"other"}]}`)
			case "missing pin", "wrong pin":
				var ledger []imageledger.Entry
				if err := json.Unmarshal(publisher["dist/release-images.json"], &ledger); err != nil {
					t.Fatal(err)
				}

				ledger[0].SBOMSHA256 = ""
				if name == "wrong pin" {
					ledger[0].SBOMSHA256 = strings.Repeat("f", 64)
				}

				body, err := json.Marshal(ledger)
				if err != nil {
					t.Fatal(err)
				}

				publisher["dist/release-images.json"] = body
			case "unplanned image":
				addNativeDeclaredEvidence(t, publisher, "image-sbom-extra.cyclonedx.json")
			case "unplanned ordinary evidence":
				addNativeDeclaredEvidence(t, publisher, "unexpected.txt")
			case "removed build declaration":
				mutateNativeManifest(t, publisher, func(m *apprelease.FileManifest) { m.Evidence = nil })
			case "fabricated publisher source":
				mutateNativeManifest(t, publisher, func(m *apprelease.FileManifest) { m.Assets[0].Source = "fabricated" })
			case "signer-only evidence":
				addNativeDeclaredEvidence(t, signer, "unexpected.txt")
			case "changed signer ledger":
				signer["dist/release-images.json"] = []byte("[]")
			case "changed signer evidence":
				signer["dist/sbom-app-amd64.json"] = []byte("substituted signer bytes")
			case "published image substitution":
				published["sbom-app-amd64.json"] = []byte("substituted published inventory")
			}

			_, err := fixture.checkWithPublisher(t, build, publisher, signer, published)
			if err == nil {
				t.Fatal("accepted substituted image release evidence")
			}

			var boundary string

			switch name {
			case "changed build subject", "changed publisher subject", "changed signer ledger", "changed signer evidence":
				boundary = "handoff changed producer subject"
			case "unplanned ordinary evidence":
				boundary = "publisher manifest disagrees with native extension"
			case "wrong pin":
				boundary = "does not match sbom_sha256"
			}

			if boundary != "" && !strings.Contains(err.Error(), boundary) {
				t.Fatalf("mutation rejected at the wrong boundary: want %q, got %v", boundary, err)
			}
		})
	}
}

func addNativeDeclaredEvidence(t *testing.T, tree map[string][]byte, name string) {
	t.Helper()

	tree["dist/"+name] = []byte("unplanned evidence")
	mutateNativeManifest(t, tree, func(m *apprelease.FileManifest) {
		entry := apprelease.FileEntry{Path: "dist/" + name, Name: name, Source: "evidence"}
		m.Assets = append(m.Assets, entry)
		m.Evidence = append(m.Evidence, entry)
	})
}

func TestGitLabReleaseEvidenceRetainsReadbackBeforeNativeRejection(t *testing.T) {
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", t.TempDir())
	fixture := newNativeReleaseJourney(t, Binary(t), HostTool(t, "cosign"), true, true)
	names := slices.Collect(maps.Keys(fixture.published))

	retained, err := NewReleaseEvidence(Target{Forge: provider.ForgeGitLab, Host: "example.invalid", Owner: "org"}, "app", "v0.94.0", "request", names)
	if err != nil {
		t.Fatal(err)
	}

	fetches := map[string]int{}

	readback, err := retained.CaptureReadback(func(name string) ([]byte, error) {
		fetches[name]++

		return fixture.published[name], nil
	})
	if err != nil {
		t.Fatal(err)
	}

	signer := maps.Clone(fixture.signer)
	addNativeDeclaredEvidence(t, signer, "signer-only.txt")

	_, err = VerifyGitLabReleaseEvidence(t.Context(), fixture.binary,
		nativeEvidenceZIP(t, fixture.producer, "_release-build/"), nativeEvidenceZIP(t, fixture.publisher, "_release-images/"), nativeEvidenceZIP(t, signer, "_release-sign/"), names,
		readback, func(_, _ []byte) error {
			t.Fatal("invalid declaration reached signature verification")

			return nil
		}, func(_, _ []byte) error {
			t.Fatal("invalid declaration reached signature verification")

			return nil
		})
	if err == nil || !retained.receipt.Complete {
		t.Fatalf("native rejection must leave complete observation evidence, not acceptance: %v", err)
	}

	for _, name := range names {
		if fetches[name] != 1 || !retained.retained(name) {
			t.Fatalf("asset %s was not retained exactly once", name)
		}

		if _, err := readback(name); err != nil {
			t.Fatalf("retained readback lost after rejection: %v", err)
		}
	}
}
