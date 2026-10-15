// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
)

func TestCaptureEntryDigest_PinsTheRefToADigestOrFails(t *testing.T) {
	t.Parallel()

	const dig = "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	// Staging flow: resolve from the candidate tag, rewrite Ref + Digest.
	e := imageledger.Entry{
		CandidateTag: "codeberg.org/itiquette/repo:staging-v1.2.3",
		FinalTag:     "codeberg.org/itiquette/repo:v1.2.3",
	}
	if err := captureEntryDigest(context.Background(), stubResolver{e.CandidateTag: dig}, &e); err != nil {
		t.Fatal(err)
	}

	if e.Digest != dig {
		t.Errorf("Digest = %q", e.Digest)
	}

	if e.Ref != "codeberg.org/itiquette/repo@"+dig {
		t.Errorf("Ref = %q (should be candidate's registry path + @digest)", e.Ref)
	}

	// Direct push: no candidate → resolve from the final tag.
	e2 := imageledger.Entry{FinalTag: "codeberg.org/itiquette/repo:v1.2.3"}
	if err := captureEntryDigest(context.Background(), stubResolver{e2.FinalTag: dig}, &e2); err != nil {
		t.Fatal(err)
	}

	if e2.Ref != "codeberg.org/itiquette/repo@"+dig {
		t.Errorf("direct-push Ref = %q", e2.Ref)
	}

	// No tag to resolve → usage error.
	if err := captureEntryDigest(context.Background(), stubResolver{}, &imageledger.Entry{}); !errors.Is(err, errs.ErrUsage) {
		t.Errorf("no tag should be a usage error, got %v", err)
	}
}

func TestLedgerAddEntryFromFlags_ShapesForgejoReleaseImageRecord(t *testing.T) {
	t.Parallel()

	const dig = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	entry, releaseTag, err := ledgerAddEntryFromFlags(AddLedgerEntryInput{
		ReleaseTag:       "v1.2.3",
		Role:             "flavor",
		ImageKind:        string(imageledger.ImageKindRelease),
		Flavor:           "rust",
		ImageName:        "docker://codeberg.org/itiquette/nanolinter:ignored",
		Digest:           dig,
		FinalTagName:     "v1.2.3-rust",
		CandidateTagName: "staging-v1.2.3-rust",
		MovingTagName:    "rust",
		BaseRef:          "codeberg.org/itiquette/nanolinter-base@" + dig,
		BaseInputID:      "base-input",
		DefaultSBOM:      true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if releaseTag != "v1.2.3" {
		t.Fatalf("releaseTag = %q", releaseTag)
	}

	want := imageledger.Entry{
		Role:         "flavor",
		ImageKind:    imageledger.ImageKindRelease,
		Flavor:       "rust",
		Ref:          "codeberg.org/itiquette/nanolinter@" + dig,
		Digest:       dig,
		SBOM:         "dist/image-sbom-rust.cyclonedx.json",
		FinalTag:     "codeberg.org/itiquette/nanolinter:v1.2.3-rust",
		MovingTag:    "codeberg.org/itiquette/nanolinter:rust",
		CandidateTag: "codeberg.org/itiquette/nanolinter:staging-v1.2.3-rust",
		BaseRef:      "codeberg.org/itiquette/nanolinter-base@" + dig,
		BaseInputID:  "base-input",
	}
	if !reflect.DeepEqual(entry, want) {
		t.Fatalf("entry = %#v\nwant  %#v", entry, want)
	}
}

func TestLedgerAddEntryFromFlags_DirectPushDoesNotInventCandidate(t *testing.T) {
	t.Parallel()

	const dig = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	entry, releaseTag, err := ledgerAddEntryFromFlags(AddLedgerEntryInput{
		Role:         "distroless",
		ImageName:    "codeberg.org/itiquette/gommitlint",
		Digest:       dig,
		FinalTagName: "v1.2.3",
		SBOM:         "dist/image-sbom.cyclonedx.json",
	})
	if err != nil {
		t.Fatal(err)
	}

	if releaseTag != "v1.2.3" {
		t.Fatalf("releaseTag = %q", releaseTag)
	}

	if entry.CandidateTag != "" {
		t.Fatalf("CandidateTag = %q, want empty", entry.CandidateTag)
	}

	if entry.Ref != "codeberg.org/itiquette/gommitlint@"+dig {
		t.Fatalf("Ref = %q", entry.Ref)
	}
}

func TestLedgerAddEntryFromFlags_CanDeriveCandidateWhenRequested(t *testing.T) {
	t.Parallel()

	const dig = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	entry, _, err := ledgerAddEntryFromFlags(AddLedgerEntryInput{
		ReleaseTag:         "v1.2.3",
		ImageName:          "codeberg.org/itiquette/nanolinter",
		Digest:             dig,
		FinalTagName:       "v1.2.3-alpine",
		DeriveCandidateTag: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if entry.CandidateTag != "codeberg.org/itiquette/nanolinter:staging-v1.2.3-alpine" {
		t.Fatalf("CandidateTag = %q", entry.CandidateTag)
	}
}

func TestLedgerAddEntryFromFlags_KeepsLegacyImageNameMode(t *testing.T) {
	t.Parallel()

	const dig = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	entry, releaseTag, err := ledgerAddEntryFromFlags(AddLedgerEntryInput{
		ReleaseTag:          "v1.2.3",
		ImageName:           "codeberg.org/owner/repo",
		Digest:              dig,
		LegacyImageNameMode: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if releaseTag != "v1.2.3" {
		t.Fatalf("releaseTag = %q", releaseTag)
	}

	if entry.FinalTag != "codeberg.org/owner/repo:v1.2.3" {
		t.Fatalf("FinalTag = %q", entry.FinalTag)
	}

	if entry.CandidateTag != "codeberg.org/owner/repo:staging-v1.2.3" {
		t.Fatalf("CandidateTag = %q", entry.CandidateTag)
	}
}

func TestLedgerAddEntryFromFlags_RecordsSBOMPinAndProvenanceExtras(t *testing.T) {
	t.Parallel()

	const dig = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	pin := strings.Repeat("a", 64)

	entry, _, err := ledgerAddEntryFromFlags(AddLedgerEntryInput{
		Role:           "distroless",
		ImageName:      "codeberg.org/itiquette/gommitlint",
		Digest:         dig,
		FinalTagName:   "v1.2.3",
		SBOM:           "dist/image-sbom.cyclonedx.json",
		SBOMSHA256:     pin,
		ProvenanceJSON: `{"base_input_set":"abc123","build_group":"core"}`,
	})
	if err != nil {
		t.Fatal(err)
	}

	if entry.SBOMSHA256 != pin {
		t.Errorf("SBOMSHA256 = %q, want %q", entry.SBOMSHA256, pin)
	}

	wantExtras := map[string]any{"base_input_set": "abc123", "build_group": "core"}
	if !reflect.DeepEqual(entry.Provenance, wantExtras) {
		t.Errorf("Provenance = %#v, want %#v", entry.Provenance, wantExtras)
	}

	// The domain accepts the shaped entry, so `ledger add` records it.
	if _, _, err := imageledger.Append([]byte("[]"), entry, "v1.2.3"); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// A malformed pin is refused by the domain BEFORE any write: the
	// validation runs inside Append, ahead of the ledger update.
	bad := entry
	bad.SBOMSHA256 = "not-hex"

	if _, _, err := imageledger.Append([]byte("[]"), bad, "v1.2.3"); !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("bad pin err = %v, want ErrValidation", err)
	}

	// A pin without an SBOM path to verify is likewise refused.
	orphan := entry
	orphan.SBOM = ""

	if _, _, err := imageledger.Append([]byte("[]"), orphan, "v1.2.3"); !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("pin without sbom err = %v, want ErrValidation", err)
	}
}

func TestLedgerAddEntryFromFlags_RejectsMalformedProvenanceJSON(t *testing.T) {
	t.Parallel()

	const dig = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	for name, raw := range map[string]string{
		"invalid":   `{not json`,
		"array":     `[1,2]`,
		"string":    `"x"`,
		"null":      `null`,
		"truncated": `{"a":`,
	} {
		_, _, err := ledgerAddEntryFromFlags(AddLedgerEntryInput{
			Role:           "distroless",
			ImageName:      "codeberg.org/itiquette/gommitlint",
			Digest:         dig,
			FinalTagName:   "v1.2.3",
			ProvenanceJSON: raw,
		})
		if !errors.Is(err, errs.ErrMalformedInput) {
			t.Errorf("%s: err = %v, want ErrMalformedInput", name, err)
		}
	}
}

func TestLedgerAddEntryFromFlags_RejectsAmbiguousCandidateInputs(t *testing.T) {
	t.Parallel()

	_, _, err := ledgerAddEntryFromFlags(AddLedgerEntryInput{
		ReleaseTag:         "v1.2.3",
		ImageName:          "codeberg.org/owner/repo",
		FinalTagName:       "v1.2.3",
		DeriveCandidateTag: true,
		CandidateTagName:   "staging-v1.2.3",
	})
	if !errors.Is(err, errs.ErrUsage) {
		t.Fatalf("err = %v, want usage", err)
	}
}
