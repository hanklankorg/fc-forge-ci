// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	domainci "github.com/diggsweden/forge-ci/internal/domain/ci"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// maxPublicKeyBytes bounds the staged file. A trust anchor is a few hundred
// bytes; anything near this is not one.
const (
	maxPublicKeyBytes = 1 << 20

	// defaultDigestOutputKey is the output key the staged digest is published
	// under when the caller names none.
	defaultDigestOutputKey = "sha256"
)

// StagePublicKeyInput drives StagePublicKey.
type StagePublicKeyInput struct {
	Source      string // reviewed public key, read without following a symlink
	DigestFile  string // sha256sum-format sidecar recording Source's digest
	Destination string // workspace-relative path to write
	Workspace   string // workspace root; empty means the working directory
	OutputKey   string // output key for the staged digest; empty means sha256
}

// StagePublicKey copies a reviewed public key into the workspace and publishes
// its digest, for a later step that verifies signatures against it.
//
// The copy is confined to the workspace: every path component is resolved
// inside an os.Root, so a symlinked parent or destination cannot place the key
// outside it, and the destination is replaced atomically. The bytes that are
// hashed are the bytes that are written, and the file on disk is re-read and
// compared with the sidecar digest before it becomes visible, which also
// catches a short write.
//
// It does not authenticate the key. The sidecar detects drift within a
// reviewed checkout; which key is trusted is the caller's policy, as it is for
// `validate signing-key`.
func StagePublicKey(ctx context.Context, sink domainci.OutputSink, out io.Writer, in StagePublicKeyInput) error {
	if in.Source == "" || in.DigestFile == "" || in.Destination == "" {
		return fmt.Errorf("stage-public-key: source, digest-file and destination are required: %w", errs.ErrUsage)
	}

	destination, err := workspaceRelative(in.Destination)
	if err != nil {
		return err
	}

	want, err := recordedDigest(in.DigestFile)
	if err != nil {
		return err
	}

	workspace := in.Workspace
	if workspace == "" {
		workspace = "."
	}

	root, err := os.OpenRoot(workspace)
	if err != nil {
		return fmt.Errorf("stage-public-key: open workspace: %w", err)
	}
	defer func() { _ = root.Close() }()

	digest, err := stageInto(root, in.Source, destination, want)
	if err != nil {
		return err
	}

	return publishStaged(ctx, sink, out, in.OutputKey, destination, digest)
}

// publishStaged reports the staged key to the workflow and to the operator.
func publishStaged(ctx context.Context, sink domainci.OutputSink, out io.Writer, key, destination, digest string) error {
	if key == "" {
		key = defaultDigestOutputKey
	}

	if sink != nil {
		if err := sink.Set(ctx, key, digest); err != nil {
			return fmt.Errorf("stage-public-key: publish staged digest: %w", err)
		}
	}

	if out != nil {
		_, _ = fmt.Fprintf(out, "Public key staged at %s (%s)\n", destination, digest)
	}

	return nil
}

// stageInto writes source to destination under root and returns its digest.
func stageInto(root *os.Root, source, destination, want string) (string, error) {
	if parent := path.Dir(destination); parent != "." {
		if err := root.MkdirAll(parent, 0o755); err != nil {
			return "", fmt.Errorf("stage-public-key: create destination directory: %w", err)
		}
	}

	key, err := readPublicKey(source)
	if err != nil {
		return "", err
	}

	digest := sha256.Sum256(key)
	if got := hex.EncodeToString(digest[:]); got != want {
		return "", fmt.Errorf("stage-public-key: public key does not match its recorded digest\n  recorded: %s\n  actual:   %s: %w", want, got, errs.ErrValidation)
	}

	// A temporary neighbour, then a rename: a reader of the destination sees
	// either the previous file or the whole new one, never a partial write.
	temporary := destination + ".staging"
	if err := root.WriteFile(temporary, key, 0o644); err != nil {
		return "", fmt.Errorf("stage-public-key: write staged key: %w", err)
	}

	staged, readErr := root.ReadFile(temporary)
	if readErr != nil || sha256.Sum256(staged) != digest {
		_ = root.Remove(temporary)

		return "", fmt.Errorf("stage-public-key: staged key does not match the key that was read: %w", errs.ErrValidation)
	}

	if renameErr := root.Rename(temporary, destination); renameErr != nil {
		_ = root.Remove(temporary)

		return "", fmt.Errorf("stage-public-key: install staged key: %w", renameErr)
	}

	return want, nil
}

// readPublicKey reads source without following a symlink at the leaf.
func readPublicKey(source string) ([]byte, error) {
	file, err := os.OpenFile(source, os.O_RDONLY|syscallNoFollow, 0) //nolint:gosec // caller-pinned trust anchor path.
	if err != nil {
		return nil, fmt.Errorf("stage-public-key: open public key: %w", err)
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("stage-public-key: public key is not a regular file: %s: %w", source, errs.ErrValidation)
	}

	key, err := io.ReadAll(io.LimitReader(file, maxPublicKeyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("stage-public-key: read public key: %w", err)
	}

	if len(key) == 0 || len(key) > maxPublicKeyBytes {
		return nil, fmt.Errorf("stage-public-key: public key is empty or larger than %d bytes: %w", maxPublicKeyBytes, errs.ErrValidation)
	}

	return key, nil
}

// recordedDigest reads the one sha256sum-format row of a companion digest file.
func recordedDigest(path string) (string, error) {
	body, err := os.ReadFile(path) //nolint:gosec // caller-pinned trust anchor sidecar.
	if err != nil {
		return "", fmt.Errorf("stage-public-key: read digest file: %w", err)
	}

	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	fields := strings.Fields(lines[0])

	if len(lines) != 1 || len(fields) == 0 || !domaincontainer.ValidSHA256Hex(fields[0]) {
		return "", fmt.Errorf("stage-public-key: digest file must hold one sha256sum row: %s: %w", path, errs.ErrValidation)
	}

	return fields[0], nil
}

// workspaceRelative refuses a destination that names anything but a path
// inside the workspace. os.Root would refuse an escape anyway; this reports
// the caller's mistake in the caller's terms, before anything is opened.
func workspaceRelative(destination string) (string, error) {
	cleaned := path.Clean(destination)

	switch {
	case strings.ContainsAny(destination, "\n\r"),
		path.IsAbs(destination),
		cleaned == "." || cleaned == "..",
		strings.HasPrefix(cleaned, "../"):
		return "", fmt.Errorf("stage-public-key: destination must be a workspace-relative path: %q: %w", destination, errs.ErrUsage)
	}

	return cleaned, nil
}
