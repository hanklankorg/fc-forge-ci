// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package imageledger_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
)

const importSource = "codeberg.org/itiquette/gommitlint"
const importDestination = "registry.example.test/publication/gommitlint"

func TestImportReleaseImagesUsesDigestAndPreservesBuildIdentity(t *testing.T) {
	t.Parallel()

	entry := candidateEntry()
	entry.MovingTag = importSource + ":stable"
	entry.BaseRef = "registry.example.test/build/base@" + goodDigest
	entry.BaseInputID = sixtyfour
	entry.Provenance = map[string]any{"builderInput": "original-source"}
	before := entry
	registry := &fakeRegistry{digests: map[string]string{entry.Ref: goodDigest, entry.CandidateTag: otherDigest}}

	imported, err := imageledger.ImportReleaseImages(t.Context(), registry, []imageledger.Entry{entry}, "v1.2.3", importSource, importDestination)
	if err != nil || len(imported) != 1 {
		t.Fatalf("import failed: %v", err)
	}

	want := entry
	want.Ref = importDestination + "@" + goodDigest
	want.FinalTag = importDestination + ":v1.2.3"
	want.CandidateTag = importDestination + ":staging-v1.2.3"

	want.MovingTag = importDestination + ":stable"
	if !reflect.DeepEqual(imported[0], want) || !reflect.DeepEqual(entry, before) {
		t.Fatal("import changed build metadata or its source ledger")
	}

	if len(registry.copies) != 1 || registry.copies[0] != entry.Ref+"->"+want.CandidateTag {
		t.Fatalf("copy was not digest-pinned: %v", registry.copies)
	}

	if _, exists := registry.digests[want.FinalTag]; exists {
		t.Fatal("import published the final tag before signing")
	}

	if _, err := imageledger.ImportReleaseImages(t.Context(), registry, []imageledger.Entry{entry}, "v1.2.3", importSource, importDestination); err != nil || len(registry.copies) != 1 {
		t.Fatalf("a verified matching candidate was not reused: %v", err)
	}
}

func TestImportReleaseImagesRefusesScopeAndConflictsBeforeCopy(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func([]imageledger.Entry){
		"foreign source":          func(entries []imageledger.Entry) { entries[1].Ref = "registry.example.test/other/image@" + goodDigest },
		"conflicting destination": func(entries []imageledger.Entry) { entries[1].FinalTag = entries[0].FinalTag },
		"wrong release":           func(entries []imageledger.Entry) { entries[1].FinalTag = importSource + ":v9.0.0" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			entries := []imageledger.Entry{validEntry(), validEntry()}
			entries[1].FinalTag += "-alpine"
			mutate(entries)

			registry := &fakeRegistry{digests: map[string]string{entries[0].Ref: goodDigest}}
			if _, err := imageledger.ImportReleaseImages(t.Context(), registry, entries, "v1.2.3", importSource, importDestination); !errors.Is(err, errs.ErrValidation) || len(registry.copies) != 0 {
				t.Fatalf("invalid import caused registry writes: %v, %v", err, registry.copies)
			}
		})
	}

	for _, destination := range []string{importSource, "registry.example.test/pub:tag", "registry.example.test/pub/../escape", "https://registry.example.test/pub"} {
		if _, err := imageledger.PlanReleaseImport([]imageledger.Entry{validEntry()}, "v1.2.3", importSource, destination); err == nil {
			t.Fatalf("unsafe destination was accepted: %s", destination)
		}
	}
}

func TestImportReleaseImagesRefusesWrongExistingOrCopiedBytes(t *testing.T) {
	t.Parallel()

	for _, existing := range []bool{false, true} {
		registry := &fakeRegistry{digests: map[string]string{validEntry().Ref: goodDigest}}
		if existing {
			registry.digests[importDestination+":staging-v1.2.3"] = otherDigest
		} else {
			registry.badCopy = "sha256:" + strings.Repeat("b", 64)
		}

		if result, err := imageledger.ImportReleaseImages(t.Context(), registry, []imageledger.Entry{validEntry()}, "v1.2.3", importSource, importDestination); err == nil || result != nil {
			t.Fatal("wrong bytes produced a publication ledger")
		}

		if existing && len(registry.copies) != 0 {
			t.Fatal("a conflicting destination candidate was overwritten")
		}
	}
}
