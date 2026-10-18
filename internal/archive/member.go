// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package archive

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// ReadTarGzipMember reads one exact regular member without extracting any files.
// The complete stream is bounded and scanned so duplicate manifests cannot make
// the policy reader and the package manager interpret different package bytes.
// A nested target requires every member to share its canonical top-level root,
// because package managers strip that root while extracting the archive.
func ReadTarGzipMember(filename, name string, limit int64) ([]byte, error) {
	file, err := openInspectionArchive(filename, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	gz, err := gzip.NewReader(io.LimitReader(file, maxTarballBodySize+1))
	if err != nil {
		return nil, fmt.Errorf("read gzip member: %w: %w", err, errs.ErrMalformedInput)
	}
	defer func() { _ = gz.Close() }()

	stream := &io.LimitedReader{R: gz, N: maxTarballStreamSize + 1}

	found, err := scanTarMember(stream, name, limit)
	if err != nil {
		return nil, err
	}

	if _, drainErr := io.Copy(io.Discard, stream); drainErr != nil || stream.N <= 0 {
		return nil, fmt.Errorf("archive has invalid or excessive trailing data: %w", errs.ErrMalformedInput)
	}

	return found, nil
}

func openInspectionArchive(filename string, limit int64) (*os.File, error) {
	info, err := inspectArchive(filename)
	if err != nil {
		return nil, err
	}

	if !info.Mode().IsRegular() || info.Size() > maxTarballBodySize || limit <= 0 {
		return nil, fmt.Errorf("archive or member limit is invalid: %w", errs.ErrValidation)
	}

	return os.Open(filename) // #nosec G304 -- caller-selected archive, checked by inspectArchive; no extraction.
}

func scanTarMember(stream *io.LimitedReader, name string, limit int64) ([]byte, error) {
	reader := tar.NewReader(stream)

	var found []byte

	for count := 0; ; count++ {
		header, readErr := reader.Next()
		if errors.Is(readErr, io.EOF) {
			break
		}

		if readErr != nil {
			return nil, fmt.Errorf("read tar member: %w: %w", readErr, errs.ErrMalformedInput)
		}

		if count >= maxTarballEntries || stream.N <= 0 {
			return nil, fmt.Errorf("archive exceeds inspection limits: %w", errs.ErrValidation)
		}

		var memberErr error

		found, memberErr = inspectTarMember(reader, header, name, limit, found)
		if memberErr != nil {
			return nil, memberErr
		}
	}

	if found == nil {
		return nil, fmt.Errorf("archive is missing %s: %w", name, errs.ErrMalformedInput)
	}

	return found, nil
}

func inspectTarMember(reader io.Reader, header *tar.Header, name string, limit int64, found []byte) ([]byte, error) {
	if !canonicalMember(header, name) {
		return nil, fmt.Errorf("archive contains a noncanonical path or special file: %w", errs.ErrValidation)
	}

	if header.Name != name {
		return found, nil
	}

	if found != nil || header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size > limit {
		return nil, fmt.Errorf("archive member %q must be one nonempty bounded regular file: %w", name, errs.ErrValidation)
	}

	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w: %w", name, err, errs.ErrMalformedInput)
	}

	return body, nil
}

func canonicalMember(header *tar.Header, target string) bool {
	if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir {
		return false
	}

	member := header.Name
	if !fs.ValidPath(strings.TrimSuffix(member, "/")) || strings.Contains(member, "\\") {
		return false
	}

	if root, _, nested := strings.Cut(target, "/"); nested {
		return member == root || strings.HasPrefix(member, root+"/")
	}

	return true
}
