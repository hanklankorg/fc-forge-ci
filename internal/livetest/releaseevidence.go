// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const releaseEvidenceBudget int64 = 1 << 30

type releaseEvidenceAsset struct {
	Name   string `json:"name"`
	File   string `json:"file,omitempty"`
	Size   int    `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
}

// ReleaseEvidence retains public readback bytes, never API responses, URLs or
// signer workspaces. Its inventory is complete only after every listed asset
// has been saved. A receipt is observation evidence, not a parity verdict.
type ReleaseEvidence struct {
	root, prefix string
	target       Target
	secrets      []map[string]string
	remaining    int64
	receipt      struct {
		Provider       string                 `json:"provider"`
		Host           string                 `json:"host"`
		Repository     string                 `json:"repository"`
		Tag            string                 `json:"tag"`
		SourceRevision string                 `json:"source_revision,omitempty"`
		Complete       bool                   `json:"complete"`
		Assets         []releaseEvidenceAsset `json:"assets"`
	}
}

// NewReleaseEvidence uses the existing opt-in snapshot directory. The source
// revision, when known by the readback owner, is the request commit, not HEAD of
// the engine. Unknown revisions are deliberately omitted rather than inferred.
func NewReleaseEvidence(target Target, repo, tag, source string, names []string, secrets ...map[string]string) (*ReleaseEvidence, error) {
	e := &ReleaseEvidence{root: snapshotEvidenceRoot(), target: target, secrets: secrets, remaining: releaseEvidenceBudget}
	if e.root == "" {
		return e, nil
	}
	if err := jobLogEvidenceDestination(e.root); err != nil {
		return nil, err
	}
	if len(names) > 4096 {
		return nil, fmt.Errorf("release inventory exceeds evidence limit: %w", errReleaseEvidence)
	}
	e.receipt.Provider = string(target.Forge)
	e.receipt.Host = target.Host
	e.receipt.Repository = RepoSlug(target, repo)
	e.receipt.Tag = tag
	e.receipt.SourceRevision = source
	names = slices.Clone(names)
	slices.Sort(names)
	for i, name := range names {
		if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, "\\\x00\r\n") || (i > 0 && names[i-1] == name) {
			return nil, fmt.Errorf("unsafe or duplicate release evidence name: %w", errReleaseEvidence)
		}
		e.receipt.Assets = append(e.receipt.Assets, releaseEvidenceAsset{Name: name})
	}
	coordinates, err := json.Marshal(e.receipt)
	if err != nil {
		return nil, err
	}
	key := sha256.Sum256(coordinates)
	e.prefix = fmt.Sprintf("release-%x-", key[:12])
	return e, e.save()
}

// Retain is called immediately after the provider's bounded read. The aggregate
// budget is independent of advertised provider sizes. Refuse secrets rather
// than redacting binary/signature bytes and invalidating their public hashes.
func (e *ReleaseEvidence) Retain(name string, body []byte) error {
	if e.root == "" {
		return nil
	}
	index := slices.IndexFunc(e.receipt.Assets, func(a releaseEvidenceAsset) bool { return a.Name == name })
	if index < 0 {
		return fmt.Errorf("unlisted release evidence asset: %w", errReleaseEvidence)
	}
	if int64(len(body)) > 512<<20 || int64(len(body)) > e.remaining {
		return fmt.Errorf("release evidence exceeds byte budget: %w", errReleaseEvidence)
	}
	if redactJobLog(e.target, string(body), e.secrets...) != string(body) {
		return fmt.Errorf("release evidence contains a credential: %w", errReleaseEvidence)
	}
	a := &e.receipt.Assets[index]
	if a.File != "" {
		return fmt.Errorf("duplicate release evidence read: %w", errReleaseEvidence)
	}
	// Never use provider-controlled names as local paths.
	file := fmt.Sprintf("%sasset-%04d", e.prefix, index)
	if err := writeSnapshotEvidence(e.root, file, body); err != nil {
		return err
	}
	e.remaining -= int64(len(body))
	sum := sha256.Sum256(body)
	a.File, a.Size, a.SHA256 = file, len(body), hex.EncodeToString(sum[:])
	return e.save()
}

// Enabled lets owners avoid additional representative payload reads when
// retention has not been requested.
func (e *ReleaseEvidence) Enabled() bool { return e.root != "" }

// CaptureReadback saves the bounded public inventory before independent
// validation can fail. Verification reuses these exact bytes without another
// provider fetch. A complete capture is observation evidence, not acceptance.
// With retention disabled, preserve the caller's ordinary on-demand reader.
func (e *ReleaseEvidence) CaptureReadback(download func(string) ([]byte, error)) (func(string) ([]byte, error), error) {
	if !e.Enabled() {
		return download, nil
	}

	for _, asset := range e.receipt.Assets {
		body, err := download(asset.Name)
		if err != nil {
			return nil, err
		}

		if err := e.Retain(asset.Name, body); err != nil {
			return nil, err
		}
	}

	return e.readRetained, nil
}

func (e *ReleaseEvidence) readRetained(name string) ([]byte, error) {
	index := slices.IndexFunc(e.receipt.Assets, func(a releaseEvidenceAsset) bool { return a.Name == name && a.File != "" })
	if index < 0 {
		return nil, fmt.Errorf("release asset has no retained readback: %w", errReleaseEvidence)
	}

	root, err := os.OpenRoot(e.root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()

	return root.ReadFile(e.receipt.Assets[index].File)
}

func (e *ReleaseEvidence) retained(name string) bool {
	return slices.ContainsFunc(e.receipt.Assets, func(a releaseEvidenceAsset) bool { return a.Name == name && a.File != "" })
}

func (e *ReleaseEvidence) save() error {
	e.receipt.Complete = len(e.receipt.Assets) > 0
	for _, a := range e.receipt.Assets {
		e.receipt.Complete = e.receipt.Complete && a.File != ""
	}
	body, err := json.MarshalIndent(e.receipt, "", "  ")
	if err != nil {
		return err
	}
	if redactJobLog(e.target, string(body), e.secrets...) != string(body) {
		return fmt.Errorf("release coordinates contain a credential: %w", errReleaseEvidence)
	}
	return writeSnapshotEvidence(e.root, e.prefix+"inventory.json", body)
}
