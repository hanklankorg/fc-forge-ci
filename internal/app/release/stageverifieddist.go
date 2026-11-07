// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

var distDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// ReadDistDigestFile accepts the producer's exact lowercase digest recording,
// never stdin, links or special files. Trailing newlines match shell substitution.
func ReadDistDigestFile(path string) (string, error) {
	file, err := openReleaseFile(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()

	body, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return "", err
	}

	digest := strings.TrimRight(string(body), "\n")
	if len(body) > 4096 || !distDigestPattern.MatchString(digest) {
		return "", fmt.Errorf("handoff digest file must contain a lowercase SHA-256: %w", errs.ErrValidation)
	}

	return digest, nil
}

// StageVerifiedDist hashes the actual privately staged bytes before installing
// them into a new destination. Source mutation can never bypass the digest by
// occurring between a separate validation and copy. Existing targets are refused.
func StageVerifiedDist(ctx context.Context, source, destination, expected, manifestRoot string) error {
	if expected == "" || source == "" || destination == "" {
		return fmt.Errorf("source, destination and expected digest required: %w", errs.ErrUsage)
	}

	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		return fmt.Errorf("handoff destination must not exist yet: %w", errs.ErrValidation)
	}

	stage, err := pathsafe.NewArtifactStaging(destination)
	if err != nil {
		return err
	}

	installed := false

	defer func() {
		_ = stage.Close()

		if !installed {
			_ = os.Remove(destination)
		}
	}()

	if err := gatherExportTrees(ctx, GatherExportsInput{Sources: []string{source}, Destination: destination, Enabled: true}, stage.Root()); err != nil {
		return err
	}

	if err := domainrelease.WalkSafe(stage.Root().FS()); err != nil {
		return err
	}

	if manifestRoot == "" {
		manifestRoot = source
	}

	actual, err := domainrelease.DistDigest(stage.Root().FS(), manifestRoot)
	if err != nil {
		return err
	}

	if actual != expected {
		return fmt.Errorf("staged dist digest mismatch across job boundary: got %s, want %s: %w", actual, expected, errs.ErrValidation)
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if err := stage.Install(); err != nil {
		return err
	}

	installed = true

	return nil
}
