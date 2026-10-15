// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package imageledger_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
)

func TestBaseImportPreservesIdentityAndCopiesOnlyUnsignedDigest(t *testing.T) {
	t.Parallel()

	entry := baseEntry()
	source := "codeberg.org/itiquette/nanolinter-base"
	entry.BaseRef = "registry.example.test/trusted/parent@" + otherDigest
	entry.Provenance = map[string]any{"build_inputs": "original"}
	before := entry
	registry := &fakeRegistry{digests: map[string]string{entry.Ref: goodDigest, entry.CandidateTag: otherDigest}}

	result, err := imageledger.ImportBaseImages(t.Context(), registry, []imageledger.Entry{entry}, source, importDestination)
	if err != nil || len(result) != 1 {
		t.Fatalf("base import: %v", err)
	}

	want := entry
	want.Ref = importDestination + "@" + goodDigest
	want.FinalTag = importDestination + ":" + entry.BaseInputID + "-go"

	want.CandidateTag = importDestination + ":staging-" + entry.BaseInputID + "-go"
	if !reflect.DeepEqual(result[0], want) || !reflect.DeepEqual(entry, before) {
		t.Fatal("base import changed source identity, SBOM or provenance")
	}

	if len(registry.copies) != 1 || registry.copies[0] != entry.Ref+"->"+want.CandidateTag {
		t.Fatalf("base import copied a mutable source or final tag: %v", registry.copies)
	}

	if _, exists := registry.digests[want.FinalTag]; exists {
		t.Fatal("unsigned base import published a final tag")
	}

	if _, err := imageledger.ImportBaseImages(t.Context(), registry, []imageledger.Entry{entry}, source, importDestination); err != nil || len(registry.copies) != 1 {
		t.Fatalf("matching candidate was not reused: %v", err)
	}

	if _, err := imageledger.PlanReleaseImport([]imageledger.Entry{entry}, "v1.2.3", source, importDestination); err == nil {
		t.Fatal("base support weakened release-only import")
	}
}

func TestBaseImportRejectsTheWholeBatchBeforeEffects(t *testing.T) {
	t.Parallel()

	for name, change := range map[string]func(*imageledger.Entry){
		"release entry":         func(entry *imageledger.Entry) { entry.ImageKind = imageledger.ImageKindRelease },
		"foreign ref":           func(entry *imageledger.Entry) { entry.Ref = "registry.example.test/foreign/base@" + goodDigest },
		"digest mismatch":       func(entry *imageledger.Entry) { entry.Digest = otherDigest },
		"missing identity":      func(entry *imageledger.Entry) { entry.BaseInputID = "" },
		"moving tag":            func(entry *imageledger.Entry) { entry.MovingTag = "codeberg.org/itiquette/nanolinter-base:latest" },
		"wrong final tag":       func(entry *imageledger.Entry) { entry.FinalTag = "codeberg.org/itiquette/nanolinter-base:unrelated" },
		"duplicate destination": func(entry *imageledger.Entry) {},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			entries := []imageledger.Entry{baseEntry(), baseEntry()}
			change(&entries[1])

			registry := &fakeRegistry{digests: map[string]string{entries[0].Ref: goodDigest}}
			if result, err := imageledger.ImportBaseImages(t.Context(), registry, entries, "codeberg.org/itiquette/nanolinter-base", importDestination); !errors.Is(err, errs.ErrValidation) || result != nil || len(registry.copies) != 0 {
				t.Fatalf("invalid base import performed effects: %v, %v", err, registry.copies)
			}
		})
	}
}

func TestBaseImportRejectsConflictingAndIncorrectCopiedBytes(t *testing.T) {
	t.Parallel()

	entry := baseEntry()
	for _, existing := range []bool{false, true} {
		registry := &fakeRegistry{digests: map[string]string{entry.Ref: goodDigest}}
		if existing {
			registry.digests[importDestination+":staging-"+entry.BaseInputID+"-go"] = otherDigest
		} else {
			registry.badCopy = otherDigest
		}

		if result, err := imageledger.ImportBaseImages(t.Context(), registry, []imageledger.Entry{entry}, "codeberg.org/itiquette/nanolinter-base", importDestination); err == nil || result != nil {
			t.Fatal("conflicting bytes produced an admitted base ledger")
		}

		if existing && len(registry.copies) != 0 {
			t.Fatal("base import overwrote an existing candidate")
		}
	}
}
