// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/diggsweden/forge-ci/internal/cliio"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/provenance"
)

// The `ledger add` use case used to live in
// cli/commands/container/ledgeradd.go, inside the command's Action. Like the
// rules in releaseimagesrules.go it is behaviour, not plumbing: the ordering of
// validation, capture, lock and write is the use case. Moved verbatim; the CLI
// file keeps only the flag definitions, flag reading and adapter construction.

var releaseTagFromImageTagRE = regexp.MustCompile(`^(v[0-9]+[.][0-9]+[.][0-9]+)(?:-|$)`) //nolint:gochecknoglobals // compiled regex table; read-only.

// AddLedgerEntryInput carries `ledger add`'s inputs: the ledger file, the
// capture-digest switch, and the entry's fields as the flags stated them. The
// error messages name the flags because they are the operator's vocabulary for
// these fields.
type AddLedgerEntryInput struct {
	LedgerPath          string
	CaptureDigest       bool // resolve the digest from the registry instead of Ref/Digest
	ReleaseTag          string
	Role                string
	ImageKind           string
	ImageName           string
	Ref                 string
	Digest              string
	SBOM                string
	SBOMSHA256          string
	ProvenanceJSON      string
	DefaultSBOM         bool
	FinalTagName        string
	FinalTag            string
	MovingTagName       string
	MovingTag           string
	Flavor              string
	DeriveCandidateTag  bool
	CandidateTagName    string
	CandidateTag        string
	BaseRef             string
	BaseInputID         string
	LegacyImageNameMode bool // neither a final tag nor a final tag name was given
}

// AddLedgerEntry validates one image entry and appends it to the ledger at
// in.LedgerPath, reporting the outcome on status. resolver is consulted only when
// in.CaptureDigest is set.
func AddLedgerEntry(ctx context.Context, resolver imageledger.DigestResolver, status io.Writer, in AddLedgerEntryInput) error { //nolint:cyclop // optional capture, entry preflight and locked update have separate failure boundaries.
	path := in.LedgerPath

	entry, releaseTag, err := ledgerAddEntryFromFlags(in)
	if err != nil {
		return err
	}

	// Everything that does not depend on the digest the registry is
	// about to return is checked first, so a bad SBOM path or an
	// out-of-scope tag fails here rather than after a round-trip whose
	// own error would be the first thing the operator sees.
	if in.CaptureDigest {
		if err := entry.ValidateBeforeCapture(releaseTag); err != nil {
			return err
		}
	}

	// Digest capture hits the registry — do it before taking the
	// lock so we never hold the ledger lock across a network call.
	if in.CaptureDigest {
		if err := captureEntryDigest(ctx, resolver, &entry); err != nil {
			return err
		}
	}

	if err := entry.Validate(releaseTag); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec,mnd // dist dir; 0755 is conventional.
		return fmt.Errorf("ledger: create dir for %s: %w", path, err)
	}

	// Serialise the read-modify-write under an exclusive file lock:
	// parallel `ledger add` against one ledger (e.g. several image
	// variants recorded concurrently) would otherwise clobber each
	// other and silently drop entries. Re-read inside the lock so
	// each writer sees the latest state.
	var added bool

	if err := cliio.WithLock(path, func() error {
		existing, err := readLedgerForAppend(path)
		if err != nil {
			return err
		}

		out, wasNew, err := imageledger.Append(existing, entry, releaseTag)
		if err != nil {
			return err
		}

		added = wasNew

		return cliio.WriteFile(path, out, 0o644) //nolint:gosec // ledger is public release metadata, not a secret.
	}); err != nil {
		return err
	}

	// Report the truthful outcome: Append is idempotent, so a
	// retried add of an already-recorded entry is a no-op, not an
	// append.
	verb := "appended"
	if !added {
		verb = "already recorded; no change to"
	}

	_, _ = fmt.Fprintf(status, "ledger: %s %s (%s) → %s\n", verb, entryDescriptor(entry.Role), entry.Digest, path)

	return nil
}

// readLedgerForAppend reads the ledger an entry is appended to. Only an absent
// ledger is an empty one: treating an unreadable ledger as empty rewrote it
// with the new entry alone, silently dropping every image recorded before.
// readLedgerForAppend returns the ledger add appends to. A ledger that does
// not exist yet is the empty ledger; one that exists is read as it is, so an
// empty or malformed file is refused by the parse instead of overwritten.
func readLedgerForAppend(path string) ([]byte, error) {
	existing, err := os.ReadFile(path) //nolint:gosec // operator-supplied ledger path.
	if errors.Is(err, fs.ErrNotExist) {
		return imageledger.Marshal(nil)
	}

	if err != nil {
		return nil, fmt.Errorf("ledger: read %s: %w", path, err)
	}

	return existing, nil
}

// entryDescriptor renders the ledger entry's noun for the add message. --role
// is optional, so an empty role collapses to a bare "entry" rather than
// leaving a doubled space.
func entryDescriptor(role string) string {
	if role == "" {
		return "entry"
	}

	return role + " entry"
}

func ledgerAddEntryFromFlags(flags AddLedgerEntryInput) (imageledger.Entry, string, error) {
	imageName, err := ledgerAddImageName(flags.ImageName)
	if err != nil {
		return imageledger.Entry{}, "", err
	}

	flags, finalTag, finalTagName, err := ledgerAddFinalTag(flags, imageName)
	if err != nil {
		return imageledger.Entry{}, "", err
	}

	movingTag, err := ledgerAddMovingTag(flags, imageName)
	if err != nil {
		return imageledger.Entry{}, "", err
	}

	candidateTag, err := ledgerAddCandidateTag(flags, imageName, finalTagName)
	if err != nil {
		return imageledger.Entry{}, "", err
	}

	releaseTag, err := ledgerAddReleaseTag(flags.ReleaseTag, finalTagName)
	if err != nil {
		return imageledger.Entry{}, "", err
	}

	sbom, provenanceExtras, err := ledgerAddEvidence(flags)
	if err != nil {
		return imageledger.Entry{}, "", err
	}

	ref := flags.Ref
	if ref == "" && imageName != "" && flags.Digest != "" {
		ref = imageName + "@" + flags.Digest
	}

	return imageledger.Entry{
		Role:         flags.Role,
		ImageKind:    imageledger.ImageKind(flags.ImageKind),
		Flavor:       flags.Flavor,
		Ref:          ref,
		Digest:       flags.Digest,
		SBOM:         sbom,
		SBOMSHA256:   flags.SBOMSHA256,
		Provenance:   provenanceExtras,
		FinalTag:     finalTag,
		MovingTag:    movingTag,
		CandidateTag: candidateTag,
		BaseRef:      flags.BaseRef,
		BaseInputID:  flags.BaseInputID,
	}, releaseTag, nil
}

// ledgerAddEvidence resolves the entry's evidence fields: the SBOM path
// (--sbom / --default-sbom) and the declared provenance extras
// (--provenance-json). The extras are parsed before any ledger write, so
// an invalid or non-object JSON document (malformed input) never reaches
// the file.
func ledgerAddEvidence(flags AddLedgerEntryInput) (string, map[string]any, error) {
	sbom, err := ledgerAddSBOM(flags)
	if err != nil {
		return "", nil, err
	}

	extras, err := provenance.ParseExternalParametersJSON(flags.ProvenanceJSON)
	if err != nil {
		return "", nil, fmt.Errorf("ledger add: --provenance-json: %w", err)
	}

	return sbom, extras, nil
}

// ledgerAddFinalTag resolves the final tag and its tag name from
// --final-tag / --final-tag-name plus the legacy image-name derivation.
// It returns the (possibly legacy-updated) flags for the later
// candidate-tag resolution.
func ledgerAddFinalTag(flags AddLedgerEntryInput, imageName string) (AddLedgerEntryInput, string, string, error) {
	finalTag := flags.FinalTag

	finalTagName := flags.FinalTagName
	if finalTag != "" && finalTagName != "" {
		return flags, "", "", fmt.Errorf("ledger add: --final-tag and --final-tag-name are mutually exclusive: %w", errs.ErrUsage)
	}

	flags, finalTag = ledgerAddApplyLegacyTags(flags, imageName, finalTag)

	if finalTag == "" && finalTagName != "" {
		if imageName == "" {
			return flags, "", "", fmt.Errorf("ledger add: --final-tag-name requires --image-name: %w", errs.ErrUsage)
		}

		finalTag = ledgerTagRef(imageName, finalTagName)
	}

	if finalTagName == "" {
		finalTagName = ledgerTagName(finalTag)
	}

	return flags, finalTag, finalTagName, nil
}

// ledgerAddApplyLegacyTags applies the legacy image-name mode: derive the
// final tag (and, when no candidate flag is in play, the candidate tag)
// from the image name and release tag.
func ledgerAddApplyLegacyTags(flags AddLedgerEntryInput, imageName, finalTag string) (AddLedgerEntryInput, string) {
	if !flags.LegacyImageNameMode || imageName == "" || flags.ReleaseTag == "" {
		return flags, finalTag
	}

	dFinal, dCandidate := imageledger.DeriveTags(imageName, flags.ReleaseTag)
	finalTag = cmp.Or(finalTag, dFinal)

	if flags.CandidateTag == "" && flags.CandidateTagName == "" && !flags.DeriveCandidateTag {
		flags.CandidateTag = dCandidate
	}

	return flags, finalTag
}

// ledgerAddMovingTag resolves the moving tag from --moving-tag /
// --moving-tag-name, enforcing their mutual exclusion.
func ledgerAddMovingTag(flags AddLedgerEntryInput, imageName string) (string, error) {
	movingTag := flags.MovingTag
	if movingTag != "" && flags.MovingTagName != "" {
		return "", fmt.Errorf("ledger add: --moving-tag and --moving-tag-name are mutually exclusive: %w", errs.ErrUsage)
	}

	if movingTag == "" && flags.MovingTagName != "" {
		if imageName == "" {
			return "", fmt.Errorf("ledger add: --moving-tag-name requires --image-name: %w", errs.ErrUsage)
		}

		movingTag = ledgerTagRef(imageName, flags.MovingTagName)
	}

	return movingTag, nil
}

// ledgerAddValidateCandidateFlags enforces the mutual exclusions between
// --candidate-tag, --candidate-tag-name, and --derive-candidate-tag.
func ledgerAddValidateCandidateFlags(flags AddLedgerEntryInput) error {
	if flags.CandidateTag != "" && flags.CandidateTagName != "" {
		return fmt.Errorf("ledger add: --candidate-tag and --candidate-tag-name are mutually exclusive: %w", errs.ErrUsage)
	}

	if flags.DeriveCandidateTag && (flags.CandidateTag != "" || flags.CandidateTagName != "") {
		return fmt.Errorf("ledger add: --derive-candidate-tag is mutually exclusive with explicit candidate tag flags: %w", errs.ErrUsage)
	}

	return nil
}

// ledgerAddCandidateTag resolves the candidate tag from --candidate-tag /
// --candidate-tag-name / --derive-candidate-tag.
func ledgerAddCandidateTag(flags AddLedgerEntryInput, imageName, finalTagName string) (string, error) {
	if err := ledgerAddValidateCandidateFlags(flags); err != nil {
		return "", err
	}

	candidateTag := flags.CandidateTag
	if candidateTag == "" && flags.CandidateTagName != "" {
		if imageName == "" {
			return "", fmt.Errorf("ledger add: --candidate-tag-name requires --image-name: %w", errs.ErrUsage)
		}

		candidateTag = ledgerTagRef(imageName, flags.CandidateTagName)
	}

	if candidateTag == "" && flags.DeriveCandidateTag {
		if imageName == "" {
			return "", fmt.Errorf("ledger add: --derive-candidate-tag requires --image-name: %w", errs.ErrUsage)
		}

		if finalTagName == "" {
			return "", fmt.Errorf("ledger add: --derive-candidate-tag requires a final tag name: %w", errs.ErrUsage)
		}

		candidateTag = ledgerTagRef(imageName, imageledger.StagingTagPrefix+finalTagName)
	}

	return candidateTag, nil
}

// ledgerAddSBOM resolves the SBOM path: --sbom wins, else --default-sbom
// derives the conventional dist path from the flavor or role.
func ledgerAddSBOM(flags AddLedgerEntryInput) (string, error) {
	sbom := flags.SBOM
	if sbom == "" && flags.DefaultSBOM {
		suffix := cmp.Or(flags.Flavor, flags.Role)
		if suffix == "" {
			return "", fmt.Errorf("ledger add: --default-sbom requires --flavor or --role: %w", errs.ErrUsage)
		}

		sbom = "dist/image-sbom-" + suffix + ".cyclonedx.json"
	}

	return sbom, nil
}

func ledgerAddImageName(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}

	imageName := strings.TrimPrefix(raw, "docker://")

	imageName = domaincontainer.StripTagOrDigest(imageName)
	if imageName == "" {
		return "", fmt.Errorf("ledger add: --image-name is empty after normalisation: %w", errs.ErrUsage)
	}

	return imageName, nil
}

func ledgerAddReleaseTag(explicit, finalTagName string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}

	matches := releaseTagFromImageTagRE.FindStringSubmatch(finalTagName)
	if len(matches) == 2 {
		return matches[1], nil
	}

	return "", fmt.Errorf("ledger add: --tag is required when final tag name is not scoped to vMAJOR.MINOR.PATCH: %s: %w", finalTagName, errs.ErrUsage)
}

func ledgerTagName(ref string) string {
	if colon := strings.LastIndex(ref, ":"); colon >= 0 {
		return ref[colon+1:]
	}

	return ""
}

func ledgerTagRef(imageName, tagName string) string {
	return imageName + ":" + tagName
}

// captureEntryDigest resolves the entry's image digest from the registry
// — the authoritative single source (no hand-typed digest, no
// transcription gap) — and rewrites Digest + the digest-pinned Ref from
// it. It reads whichever tag the consumer just pushed: the candidate tag
// (staging flow) when set, else the final tag. Matches the design the
// migration plan recommends (§8) over a consumer-supplied digest.
func captureEntryDigest(ctx context.Context, resolver imageledger.DigestResolver, entry *imageledger.Entry) error {
	src := entry.DigestSource()
	if src == "" {
		return fmt.Errorf("ledger add --capture-digest needs a candidate or final tag to resolve: %w", errs.ErrUsage)
	}

	if !domaincontainer.ValidTaggedRef(src) {
		return fmt.Errorf("ledger add: capture source must be a qualified tagged reference: %w", errs.ErrValidation)
	}

	digest, err := resolver.ResolveDigest(ctx, src)
	if err != nil {
		return fmt.Errorf("ledger add: capture digest from %s: %w", src, err)
	}

	if !domaincontainer.ValidDigest(digest) {
		return fmt.Errorf("ledger add: resolver returned a noncanonical digest: %w", errs.ErrMalformedInput)
	}

	entry.PinDigest(digest)

	return nil
}
