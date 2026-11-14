// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// CentralPublisher separates the one mutating submission from status reads.
type CentralPublisher interface {
	Upload(ctx context.Context, filename string, automatic bool) (string, error)
	State(ctx context.Context, id string) (string, error)
}

// CentralBundleInput describes already-exported, signed release bytes.
type CentralBundleInput struct {
	Directory, Version, PublicKey string
	Automatic                     bool
	WaitTimeout, PollInterval     time.Duration
}

// SubmitCentralBundle verifies before uploading and never retries an upload.
func SubmitCentralBundle(ctx context.Context, client CentralPublisher, out io.Writer, in CentralBundleInput) (result error) {
	set, err := VerifyMavenPublication(ctx, in.Directory, in.Version, in.PublicKey)
	if err != nil {
		return err
	}

	file, err := os.CreateTemp("", "forge-ci-central-*.zip")
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, os.Remove(file.Name())) }()

	if writeErr := writeCentralBundle(file, in.Directory, set.Files); writeErr != nil {
		_ = file.Close()

		return writeErr
	}

	if closeErr := file.Close(); closeErr != nil {
		return closeErr
	}

	id, err := client.Upload(ctx, file.Name(), in.Automatic)
	if err != nil {
		return fmt.Errorf("central submission did not confirm a deployment ID; inspect the Portal before retrying because the upload may have been accepted: %w", err)
	}

	_, _ = fmt.Fprintf(out, "Central deployment: %s (retain this ID; observation never resubmits)\n", id)

	return WaitCentralDeployment(ctx, client, out, id, in.Automatic, in.WaitTimeout, in.PollInterval)
}

func writeCentralBundle(out io.Writer, directory string, names []string) error {
	root, err := pathsafe.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	archive := zip.NewWriter(out)

	for _, name := range names {
		file, err := root.Open(name)
		if err != nil {
			_ = archive.Close()

			return err
		}

		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o644)

		member, err := archive.CreateHeader(header)
		if err == nil {
			_, err = io.Copy(member, file)
		}

		_ = file.Close()
		if err != nil {
			_ = archive.Close()

			return err
		}
	}

	return archive.Close()
}

// WaitCentralDeployment can resume observation by ID without resubmitting.
// Automatic publication succeeds only at PUBLISHED, never merely VALIDATED.
func WaitCentralDeployment(ctx context.Context, client CentralPublisher, out io.Writer, id string, automatic bool, timeout, interval time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}

	if interval <= 0 {
		interval = 10 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resume := "forge-ci publish maven-central wait --deployment-id " + id
	if automatic {
		resume += " --automatic"
	}

	for {
		done, err := observeCentralDeployment(ctx, client, out, id, automatic)
		if done {
			return err
		}

		if err != nil && !retryableCentralObservation(err) {
			return fmt.Errorf("cannot observe Central deployment %s: %w", id, err)
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()

			return fmt.Errorf("central deployment %s publication is not confirmed; resume with %s, do not re-upload: %w", id, resume, ctx.Err())
		case <-timer.C:
		}
	}
}

func retryableCentralObservation(err error) bool {
	return errors.Is(err, errs.ErrDependencyUnavailable) || errors.Is(err, errs.ErrRateLimited) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

func observeCentralDeployment(ctx context.Context, client CentralPublisher, out io.Writer, id string, automatic bool) (bool, error) {
	state, err := client.State(ctx, id)
	if err != nil {
		return false, err
	}

	_, _ = fmt.Fprintf(out, "Central deployment %s: %s\n", id, state)

	switch state {
	case "PUBLISHED":
		return true, nil
	case "VALIDATED":
		if !automatic {
			_, _ = fmt.Fprintln(out, "Validated only; manual release is still required in the Central Portal.")

			return true, nil
		}
	case "FAILED":
		return true, fmt.Errorf("central deployment %s failed; inspect its validation report: %w", id, errs.ErrValidation)
	case "PENDING", "VALIDATING", "PUBLISHING":
		// These are intermediate states, not proof of publication.
	default:
		return true, fmt.Errorf("unknown Central deployment state: %w", errs.ErrMalformedInput)
	}

	return false, nil
}
