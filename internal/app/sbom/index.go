// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package sbom

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domainsbom "github.com/diggsweden/forge-ci/internal/domain/sbom"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// IndexBOMFile is one producer-owned record in the version-1 input manifest.
// File is relative to Root; URL is the public location the caller will publish.
type IndexBOMFile struct {
	Platform string `json:"platform"`
	ImageRef string `json:"image_ref"`
	File     string `json:"file"`
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
}

// IndexBOMInput keeps local input reads rooted separately from the output path.
type IndexBOMInput struct {
	IndexRef  string
	Root      string
	IndexFile string
	PartsFile string
	Output    string
}

// IndexBOMOutput supplies the ledger's checksum pin and the files the consumer
// must register as release evidence. No publication is performed here.
type IndexBOMOutput struct {
	Path          string
	SHA256        string
	EvidenceFiles []string
}

// ComposeIndex reads all inputs and validates their digest/subject bindings
// before writing an index BOM. Input documents are never rewritten.
func ComposeIndex(in IndexBOMInput) (*IndexBOMOutput, error) {
	if in.Output == cliio.StdSentinel {
		return nil, fmt.Errorf("index SBOM output requires a file; stdout (-) is not supported: %w", errs.ErrUsage)
	}

	if in.Root == "" || in.Output == "" || strings.ContainsAny(in.Root+in.Output, "\r\n") {
		return nil, fmt.Errorf("index SBOM root and output paths must be nonempty and single-line: %w", errs.ErrUsage)
	}

	root, err := pathsafe.OpenRoot(in.Root)
	if err != nil {
		return nil, err
	}

	defer func() { _ = root.Close() }()

	inputs, err := loadIndexBOMInputs(root, in.IndexFile, in.PartsFile)
	if err != nil {
		return nil, err
	}

	if err = refuseIndexInputOverwrite(in.Root, in.Output, inputs.files); err != nil {
		return nil, err
	}

	result, err := domainsbom.ComposeIndexBOM(in.IndexRef, inputs.index, inputs.parts)
	if err != nil {
		return nil, err
	}

	if err = cliio.WriteFile(in.Output, result, 0o644); err != nil {
		return nil, err
	}

	return &IndexBOMOutput{Path: in.Output, SHA256: fmt.Sprintf("%x", sha256.Sum256(result)), EvidenceFiles: indexEvidence(in.Root, inputs.files[2:])}, nil
}

// indexEvidence is the sorted, distinct paths of the platform SBOMs under root.
func indexEvidence(root string, names []string) []string {
	evidence := make([]string, 0, len(names))
	for _, name := range names {
		evidence = append(evidence, filepath.Join(root, name))
	}

	slices.Sort(evidence)

	return slices.Compact(evidence)
}

type indexBOMInputs struct {
	index []byte
	parts []domainsbom.IndexBOMPart
	files []string
}

func loadIndexBOMInputs(root *os.Root, indexFile, partsFile string) (indexBOMInputs, error) {
	var inputs indexBOMInputs

	index, err := readIndexInput(root, indexFile)
	if err != nil {
		return inputs, err
	}

	records, err := readIndexBOMRecords(root, partsFile)
	if err != nil {
		return inputs, err
	}

	inputs.index = index
	inputs.files = []string{indexFile, partsFile}
	inputs.parts = make([]domainsbom.IndexBOMPart, 0, len(records))

	for _, record := range records {
		raw, readErr := readIndexInput(root, record.File)
		if readErr != nil {
			return inputs, readErr
		}

		inputs.parts = append(inputs.parts, domainsbom.IndexBOMPart{Platform: record.Platform, ImageRef: record.ImageRef, URL: record.URL, SHA256: record.SHA256, Document: raw})
		inputs.files = append(inputs.files, record.File)
	}

	return inputs, nil
}

func readIndexBOMRecords(root *os.Root, name string) ([]IndexBOMFile, error) {
	body, err := readIndexInput(root, name)
	if err != nil {
		return nil, err
	}

	var manifest struct {
		Version   int            `json:"version"`
		Platforms []IndexBOMFile `json:"platforms"`
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()

	if err = decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode platform SBOM records: %w: %w", err, errs.ErrMalformedInput)
	}

	if decoder.Decode(new(any)) != io.EOF || manifest.Version != 1 {
		return nil, fmt.Errorf("platform SBOM records require one version-1 JSON document: %w", errs.ErrMalformedInput)
	}

	return manifest.Platforms, nil
}

func readIndexInput(root *os.Root, name string) ([]byte, error) {
	if !pathsafe.Relative(name) || strings.ContainsAny(name, "\r\n") {
		return nil, fmt.Errorf("index SBOM input must be relative to its root: %w", errs.ErrUsage)
	}

	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}

	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("index SBOM input must be a regular file: %w", errs.ErrValidation)
	}

	return root.ReadFile(name)
}

func refuseIndexInputOverwrite(root, output string, files []string) error {
	outputPath, err := filepath.Abs(output)
	if err != nil {
		return err
	}

	outputInfo, _ := os.Stat(output)

	for _, name := range files {
		path, err := filepath.Abs(filepath.Join(root, name))
		if err != nil {
			return err
		}

		info, _ := os.Stat(path)
		if path == outputPath || (info != nil && outputInfo != nil && os.SameFile(info, outputInfo)) {
			return fmt.Errorf("index SBOM output would overwrite an input: %w", errs.ErrUsage)
		}
	}

	return nil
}
