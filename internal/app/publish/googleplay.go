// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"context"
	"fmt"
	"io"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domainpublish "github.com/diggsweden/forge-ci/internal/domain/publish"
)

// GooglePlayCheckCredentialsInput drives GooglePlayCheckCredentials.
type GooglePlayCheckCredentialsInput struct {
	// ServiceAccountJSON is supplied by the caller, typically from
	// GOOGLE_PLAY_SERVICE_ACCOUNT_JSON in the CLI.
	ServiceAccountJSON string
}

// GooglePlayCheckCredentials checks service-account JSON and RSA private-key
// shape before upload. It does not authenticate the account or establish that
// Google accepts the credential.
func GooglePlayCheckCredentials(_ context.Context, w io.Writer, in GooglePlayCheckCredentialsInput) error { //nolint:varnamelen // idiomatic short name (testing/http/io conventions).
	if in.ServiceAccountJSON == "" {
		return fmt.Errorf("google Play service account JSON is required: %w", errs.ErrMissingInput)
	}

	if err := domainpublish.ValidateGooglePlayServiceAccount([]byte(in.ServiceAccountJSON)); err != nil {
		return fmt.Errorf("service account: %w: %w", err, errs.ErrValidation)
	}

	_, _ = fmt.Fprintln(w, "Service account secret is configured")

	return nil
}
