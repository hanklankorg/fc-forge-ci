// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejo_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

const (
	listMavenPackage = "GET /api/v1/packages/owner?limit=50&page=1&q=org.example%3Ademo&type=maven"
	linkMavenPackage = "POST /api/v1/packages/owner/maven/org.example:demo/-/link/repo"
)

func mavenPackage(linkedTo string) string {
	repository := "null"
	if linkedTo != "" {
		repository = `{"full_name":"` + linkedTo + `"}`
	}

	return `[{"type":"maven","name":"org.example:demo-parent","version":"1.0.0","repository":{"full_name":"owner/other"}},` +
		`{"type":"maven","name":"org.example:demo","version":"1.0.0","repository":` + repository + `}]`
}

// A package is linked to the run's repository only when no repository has
// it: one this repository has is left alone, and one another repository has
// is never taken from it.
func TestLinkForgePackage_LinksOnlyAPackageNoRepositoryHas(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, linkedTo string
		wantRequests   []string
		wantError      string
	}{
		{"linked to no repository", "", []string{listMavenPackage + " | token tok", linkMavenPackage + " | token tok"}, ""},
		{"linked to this repository", "Owner/Repo", []string{listMavenPackage + " | token tok"}, ""},
		{"linked to another repository", "owner/other", []string{listMavenPackage + " | token tok"}, "it is linked to owner/other, which keeps it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			api := &wireAPI{t: t, replies: map[string]wireReply{
				listMavenPackage: {status: http.StatusOK, body: mavenPackage(tc.linkedTo)},
				linkMavenPackage: {status: http.StatusCreated},
			}}

			err := api.provider().LinkForgePackage(context.Background(), "maven", "org.example:demo")
			if tc.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantError)
			}

			require.Equal(t, tc.wantRequests, api.trace)
		})
	}
}

// The listing is followed to the page that has the package, and a package
// Forgejo does not list is not linked.
func TestLinkForgePackage_FindsThePackageOnALaterPageOrLinksNothing(t *testing.T) {
	t.Parallel()

	const (
		pageOne = "GET /api/v1/packages/owner?limit=50&page=1&q=%40owner%2Fpkg&type=npm"
		pageTwo = "GET /api/v1/packages/owner?limit=50&page=2&q=%40owner%2Fpkg&type=npm"
		link    = "POST /api/v1/packages/owner/npm/@owner%2Fpkg/-/link/repo"
		other   = `[{"type":"npm","name":"@owner/pkg-extra","version":"1.0.0","repository":null}]`
	)

	api := &wireAPI{t: t, replies: map[string]wireReply{
		pageOne: {status: http.StatusOK, body: other, next: "2"},
		pageTwo: {status: http.StatusOK, body: `[{"type":"npm","name":"@owner/pkg","version":"1.0.0","repository":null}]`},
		link:    {status: http.StatusCreated},
	}}

	require.NoError(t, api.provider().LinkForgePackage(context.Background(), "npm", "@owner/pkg"))
	require.Equal(t, []string{pageOne + " | token tok", pageTwo + " | token tok", link + " | token tok"}, api.trace)

	absent := &wireAPI{t: t, replies: map[string]wireReply{pageOne: {status: http.StatusOK, body: other}}}

	err := absent.provider().LinkForgePackage(context.Background(), "npm", "@owner/pkg")
	require.ErrorIs(t, err, errs.ErrMissingInput)
	require.ErrorIs(t, err, provider.ErrForgePackageNotListed)
	require.EqualError(t, err, "it is not listed among the npm packages of owner: missing input")
	require.Equal(t, []string{pageOne + " | token tok"}, absent.trace)
}

// A Forgejo without the route and a token Forgejo refuses each say so in
// one plain sentence, without the request and its status.
func TestLinkForgePackage_SaysPlainlyWhyForgejoRefused(t *testing.T) {
	t.Parallel()

	for status, want := range map[int]struct {
		message string
		class   error
	}{
		http.StatusNotFound:            {"this Forgejo has no API that links a package to a repository: unsupported on this platform", errs.ErrUnsupported},
		http.StatusForbidden:           {"the token may publish the package but Forgejo refuses it the link to owner/repo: permission denied", errs.ErrPermissionDenied},
		http.StatusUnauthorized:        {"the token may publish the package but Forgejo refuses it the link to owner/repo: permission denied", errs.ErrPermissionDenied},
		http.StatusInternalServerError: {"", errs.ErrDependencyUnavailable},
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()

			api := &wireAPI{t: t, failOn: linkMavenPackage, failStatus: status, replies: map[string]wireReply{
				listMavenPackage: {status: http.StatusOK, body: mavenPackage("")},
			}}

			err := api.provider().LinkForgePackage(context.Background(), "maven", "org.example:demo")
			require.ErrorIs(t, err, want.class)

			if want.message != "" {
				require.EqualError(t, err, want.message)
			}
		})
	}
}
