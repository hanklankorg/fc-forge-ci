// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package httpretry

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// maxRedirects is net/http's own limit, kept because a custom policy replaces it.
const maxRedirects = 10

// SameAuthority returns a copy of client that refuses a redirect leaving the
// scheme and host of the request it started from, or naming userinfo. forge
// names the service in the refusal. The caller's client is not modified, and a
// redirect policy it already has is asked first.
//
// net/http replays a redirected request, body included, wherever it is sent.
// It drops Authorization only when the host changes, so a move to plain HTTP
// or to another port keeps it; it never drops a custom credential header; and
// a transport that adds the credential itself adds it to every hop. Followed,
// such a redirect delivers the request, and often the token, to another
// place, and that place's answer is read as the forge's.
func SameAuthority(client *http.Client, forge string) *http.Client {
	guarded := *client
	previous := client.CheckRedirect
	guarded.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("%s redirect limit reached: %w", forge, errs.ErrDependencyUnavailable)
		}

		if previous != nil {
			if err := previous(next, via); err != nil {
				return err
			}
		}

		first := via[0].URL
		if next.URL.User != nil || !strings.EqualFold(next.URL.Scheme, first.Scheme) || !strings.EqualFold(next.URL.Host, first.Host) {
			// Scheme and host only: the rest of a redirect is the other
			// server's text. They are what shows a server URL given as http.
			return fmt.Errorf("%s redirect changes authority, from %s://%s to %s://%s: %w",
				forge, first.Scheme, first.Host, next.URL.Scheme, next.URL.Host, errs.ErrValidation)
		}

		return nil
	}

	return &guarded
}
