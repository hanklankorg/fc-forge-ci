// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package baseimages

import (
	"fmt"
	"strings"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/provenance"
)

// AdmitBaseImageLedger admits the ledger a base-image signer is asked to sign,
// before any registry or signing authority is acquired: it parses, holds at
// least one entry, every entry is a base image that passes the ledger's own
// rules, and every ref and tag stays inside the expected base-image repository.
func AdmitBaseImageLedger(ledger []byte, expectedRepository string) ([]imageledger.Entry, error) {
	if expectedRepository == "" {
		return nil, fmt.Errorf("base images: expected repository is required: %w", errs.ErrMissingInput)
	}

	entries, err := imageledger.Parse(ledger)
	if err != nil {
		return nil, err
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("base images: ledger must contain at least one entry: %w", errs.ErrValidation)
	}

	for idx, entry := range entries {
		if entry.ImageKind != imageledger.ImageKindBase {
			return nil, fmt.Errorf("base images: ledger entry %d must have image_kind %q, got %q: %w", idx, imageledger.ImageKindBase, entry.ImageKind, errs.ErrValidation)
		}
	}

	// Base entries are not release-scoped, so no release tag takes part.
	if err := imageledger.ValidateAll(entries, ""); err != nil {
		return nil, err
	}

	if err := imageledger.ValidateEntryRepositories(entries, expectedRepository); err != nil {
		return nil, err
	}

	return entries, nil
}

// RequireSourceOwnedRepository holds the repository a base-image verb writes
// to, with signatures or with final tags, to the source it speaks for. The
// repository is on the registry the verb logs in to, host and port exactly.
// On the forge's own registry its path is also below the owner namespace of
// the source repository, compared without case; the name below the owner is
// the caller's choice.
//
// A statement signed here says the images were built from the source
// repository. Without the owner rule the caller of a shared signing workflow
// could name any path on the forge's registry, where the forge's owners are
// the namespaces, and have it attested as that source's build. A repository
// the caller declares external, on another registry such as GHCR, has no
// forge owner to compare with: it is the one the caller's reviewed workflow
// declared, every ledger entry must already equal it, and that registry's own
// token decides what can be written there.
func RequireSourceOwnedRepository(repository, registryHost, sourceRepository string, external bool) error {
	slash := strings.LastIndex(sourceRepository, "/")
	if slash <= 0 || slash == len(sourceRepository)-1 {
		return fmt.Errorf("base images: the source repository must be <owner>/<name> to name the owner of the base-image repository, got %q: %w", sourceRepository, errs.ErrUsage)
	}

	owner := sourceRepository[:slash]

	host, path, found := strings.Cut(repository, "/")
	if !found || registryHost == "" || host != registryHost {
		return fmt.Errorf("base images: base-image repository %q must be on the registry this command logs in to, %q: %w", repository, registryHost, errs.ErrValidation)
	}

	if external {
		return nil
	}

	name, below := strings.CutPrefix(strings.ToLower(path), strings.ToLower(owner)+"/")
	if !below || name == "" {
		return fmt.Errorf("base images: base-image repository %q must be below the source repository's owner, %s/%s/: %w", repository, registryHost, strings.ToLower(owner), errs.ErrValidation)
	}

	return nil
}

// BaseImageProvenanceInput drives BaseImageProvenance. The source coordinates
// are the caller's: the repository the bases were built from and the commit
// that build was pinned to, which need not be the commit the signer runs on.
type BaseImageProvenanceInput struct {
	Entries       []imageledger.Entry // admitted by AdmitBaseImageLedger
	RepositoryURL string              // <server>/<owner>/<repo>
	Ref           string              // ref the signing run was started for
	SourceSHA     string              // pinned source commit
	Workflow      string              // calling workflow, matched by the base verifiers
	BuilderID     string
	InvocationID  string
	StartedOn     string // RFC3339 UTC
}

// BaseImageProvenance builds the run-level in-toto statement for a base-image
// signing run: generic release provenance whose subjects are the ledger's own
// image digests. The signer enriches its predicate per image before attesting.
//
// The workflow is required here although the generic profile accepts none:
// base-image verification matches externalParameters.workflow, so a statement
// without one could be signed but never verified.
func BaseImageProvenance(in BaseImageProvenanceInput) ([]byte, error) {
	if in.Workflow == "" {
		return nil, fmt.Errorf("base images: workflow is required: %w", errs.ErrUsage)
	}

	subjects := make([]provenance.Subject, 0, len(in.Entries))

	for _, entry := range in.Entries {
		name, _, _ := strings.Cut(entry.Ref, "@")
		subjects = append(subjects, provenance.Subject{Name: name, SHA256: strings.TrimPrefix(entry.Digest, "sha256:")})
	}

	return apprelease.GenerateProvenance(apprelease.ProvenanceInput{
		Subjects:      subjects,
		RepositoryURL: in.RepositoryURL,
		Ref:           in.Ref,
		SHA:           in.SourceSHA,
		BuilderID:     in.BuilderID,
		InvocationID:  in.InvocationID,
		StartedOn:     in.StartedOn,
		Profile:       apprelease.ProvenanceProfileGeneric,
		Workflow:      in.Workflow,
	})
}
