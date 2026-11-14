// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package distroregistry_test

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/adapters/distroregistry"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

const owner = "/api/packages/owner"

// registry stands for the three registries of one owner. It keeps the first
// upload to an upload path, answers 409 to the next, and serves the indexes
// and the Alpine file a package manager reads, with the digest of what it
// keeps.
type registry struct {
	t    *testing.T
	kept map[string][]byte
}

func (r *registry) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	assert.Equal(r.t, "token synthetic", req.Header.Get("Authorization"))
	assert.Empty(r.t, req.URL.RawQuery, "no parameter asks the registry to sign or change a package")

	path := strings.TrimPrefix(req.URL.Path, owner)

	if req.Method == http.MethodPut {
		body, err := io.ReadAll(req.Body)
		assert.NoError(r.t, err)

		if _, exists := r.kept[path]; exists {
			writer.WriteHeader(http.StatusConflict)

			return
		}

		r.kept[path] = body

		writer.WriteHeader(http.StatusCreated)

		return
	}

	assert.Equal(r.t, http.MethodGet, req.Method)

	digest := func(upload string) string {
		sum := sha256.Sum256(r.kept[upload])

		return hex.EncodeToString(sum[:])
	}

	switch path {
	case "/debian/dists/stable/Release":
		// Another component's index is not this place's.
		_, _ = fmt.Fprint(writer, "Suite: stable\nSHA256:\n 00 12 main/binary-amd64/Packages\n 00 12 main/binary-amd64/Packages.gz\n 00 12 other/binary-amd64/Packages\n")
	case "/debian/dists/stable/main/binary-amd64/Packages":
		_, _ = fmt.Fprintf(writer, "Package: hello\nFilename: pool/stable/main/hello_1.2.3_amd64.deb\nSHA256: %s\n", digest("/debian/pool/stable/main/upload"))
	case "/debian/dists/stable/other/binary-amd64/Packages":
		r.t.Errorf("the index of another component was read")
	case "/rpm/rocky/el9/repodata/repomd.xml":
		_, _ = fmt.Fprint(writer, `<repomd><data type="filelists"><location href="repodata/filelists.xml.gz"/></data><data type="primary"><location href="repodata/primary.xml.gz"/></data></repomd>`)
	case "/rpm/rocky/el9/repodata/primary.xml.gz":
		packed := gzip.NewWriter(writer)
		_, _ = fmt.Fprintf(packed, `<metadata><package type="rpm"><name>hello</name><checksum type="sha256" pkgid="YES">%s</checksum></package></metadata>`, digest("/rpm/rocky/el9/upload"))
		_ = packed.Close()
	case "/alpine/stable/main/x86_64/hello-1.2.3-r0.apk":
		if _, exists := r.kept["/alpine/stable/main"]; !exists {
			writer.WriteHeader(http.StatusNotFound)

			return
		}

		_, _ = writer.Write(r.kept["/alpine/stable/main"])
	default:
		writer.WriteHeader(http.StatusNotFound)
	}
}

func packageOf(format provider.DistroPackageFormat, content string, place ...string) provider.DistroPackage {
	sum := sha256.Sum256([]byte(content))

	return provider.DistroPackage{
		Format: format, Path: "dist/hello", Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:]), Place: place,
		Name: "hello", Version: "1.2.3-r0", Architecture: "x86_64",
	}
}

// For each format: the file goes once, unchanged, to the registry's upload
// path of the place; the registry is found to list it by the digest it
// serves; a second upload is the registry's refusal, not an error; and the
// same package version with other content is not listed.
func TestRegistryUploadsOnceAndReadsBackByDigest(t *testing.T) {
	t.Parallel()

	for upload, place := range map[string]provider.DistroPackage{
		"/debian/pool/stable/main/upload": packageOf(provider.DistroPackageDebian, "debian package", "stable", "main"),
		"/rpm/rocky/el9/upload":           packageOf(provider.DistroPackageRPM, "rpm package", "rocky/el9"),
		"/alpine/stable/main":             packageOf(provider.DistroPackageAlpine, "alpine package", "stable", "main"),
	} {
		t.Run(string(place.Format), func(t *testing.T) {
			t.Parallel()

			forge := &registry{t: t, kept: map[string][]byte{}}
			server := httptest.NewServer(forge)
			t.Cleanup(server.Close)

			target := provider.ForgeDistroRegistry{URL: server.URL + owner, Token: "synthetic"}
			client := distroregistry.Client{}
			content := map[provider.DistroPackageFormat]string{
				provider.DistroPackageDebian: "debian package", provider.DistroPackageRPM: "rpm package", provider.DistroPackageAlpine: "alpine package",
			}[place.Format]

			listing, err := client.Listing(t.Context(), target, place)
			require.NoError(t, err)
			require.Equal(t, provider.DistroNotListed, listing, "nothing is published yet")

			created, err := client.Upload(t.Context(), target, place, strings.NewReader(content))
			require.NoError(t, err)
			require.True(t, created)
			require.Equal(t, content, string(forge.kept[upload]))

			listing, err = client.Listing(t.Context(), target, place)
			require.NoError(t, err)
			require.Equal(t, provider.DistroListed, listing)

			other := packageOf(place.Format, "another build of the version", place.Place...)

			created, err = client.Upload(t.Context(), target, other, strings.NewReader("another build of the version"))
			require.NoError(t, err)
			require.False(t, created, "the registry refuses a version it holds")
			require.Equal(t, content, string(forge.kept[upload]), "and keeps what it had")

			// Alpine is read by name, so it shows the other content.
			unlike := provider.DistroNotListed
			if place.Format == provider.DistroPackageAlpine {
				unlike = provider.DistroOtherContent
			}

			listing, err = client.Listing(t.Context(), target, other)
			require.NoError(t, err)
			require.Equal(t, unlike, listing)
		})
	}
}

// The upload follows no redirect: the token and the package go to the
// registry alone.
func TestRegistryUploadFollowsNoRedirect(t *testing.T) {
	t.Parallel()

	var elsewhere atomic.Int64

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		elsewhere.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(other.Close)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", other.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)

	target := provider.ForgeDistroRegistry{URL: server.URL + owner, Token: "synthetic"}
	pkg := packageOf(provider.DistroPackageAlpine, "alpine package", "stable", "main")

	_, err := distroregistry.Client{}.Upload(t.Context(), target, pkg, strings.NewReader("alpine package"))
	require.Error(t, err)
	require.Zero(t, elsewhere.Load())
}

// A registry that keeps its files in object storage answers a read with a
// redirect. The read follows one, without the token, also when the address
// is the registry's own host, where a client would keep the header; it
// follows no second one, and none that is not HTTPS.
func TestRegistryReadFollowsOneRedirectWithoutTheToken(t *testing.T) {
	t.Parallel()

	var beyond atomic.Int64

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	redirect := func(to string) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			assert.Equal(t, "token synthetic", req.Header.Get("Authorization"))
			w.Header().Set("Location", to)
			w.WriteHeader(http.StatusSeeOther)
		}
	}

	mux.HandleFunc(owner+"/alpine/stable/main/x86_64/hello-1.2.3-r0.apk", redirect("/storage/hello.apk?signature=synthetic"))
	mux.HandleFunc("/storage/hello.apk", func(w http.ResponseWriter, req *http.Request) {
		assert.Empty(t, req.Header.Get("Authorization"), "the token stays with the registry")
		assert.Equal(t, "signature=synthetic", req.URL.RawQuery)

		_, _ = w.Write([]byte("alpine package"))
	})
	mux.HandleFunc(owner+"/alpine/twice/main/x86_64/hello-1.2.3-r0.apk", redirect("/storage/again"))
	mux.HandleFunc("/storage/again", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "/storage/beyond")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/storage/beyond", func(http.ResponseWriter, *http.Request) { beyond.Add(1) })
	mux.HandleFunc(owner+"/alpine/plain/main/x86_64/hello-1.2.3-r0.apk", redirect("http://storage.example/hello.apk"))

	target := provider.ForgeDistroRegistry{URL: server.URL + owner, Token: "synthetic"}

	listing, err := distroregistry.Client{}.Listing(t.Context(), target, packageOf(provider.DistroPackageAlpine, "alpine package", "stable", "main"))
	require.NoError(t, err)
	require.Equal(t, provider.DistroListed, listing)

	_, err = distroregistry.Client{}.Listing(t.Context(), target, packageOf(provider.DistroPackageAlpine, "alpine package", "twice", "main"))
	require.Error(t, err)
	require.Zero(t, beyond.Load())

	_, err = distroregistry.Client{}.Listing(t.Context(), target, packageOf(provider.DistroPackageAlpine, "alpine package", "plain", "main"))
	require.ErrorIs(t, err, errs.ErrInvalidConfig)
}

// Before any request: a registry that is not HTTPS, a place or a package
// name that is more than one path step, and a missing token.
func TestRegistryRefusesAnAddressItWouldNotSendATokenTo(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	t.Cleanup(server.Close)

	local := provider.ForgeDistroRegistry{URL: server.URL + owner, Token: "synthetic"}
	debian := packageOf(provider.DistroPackageDebian, "debian package", "stable", "main")
	escaping := packageOf(provider.DistroPackageDebian, "debian package", "..", "main")
	query := packageOf(provider.DistroPackageRPM, "rpm package", "el9?sign=true")
	named := packageOf(provider.DistroPackageAlpine, "alpine package", "stable", "main")
	named.Architecture = "../x86_64"

	for name, tc := range map[string]struct {
		registry provider.ForgeDistroRegistry
		pkg      provider.DistroPackage
		want     error
	}{
		"plain http":      {provider.ForgeDistroRegistry{URL: "http://forge.example" + owner, Token: "synthetic"}, debian, errs.ErrInvalidConfig},
		"credentials":     {provider.ForgeDistroRegistry{URL: "https://user:secret@forge.example" + owner, Token: "synthetic"}, debian, errs.ErrInvalidConfig},
		"no token":        {provider.ForgeDistroRegistry{URL: server.URL + owner}, debian, errs.ErrPermissionDenied},
		"escaping place":  {local, escaping, errs.ErrInvalidConfig},
		"query in place":  {local, query, errs.ErrInvalidConfig},
		"escaping apk id": {local, named, errs.ErrInvalidConfig},
	} {
		_, uploadErr := distroregistry.Client{}.Upload(t.Context(), tc.registry, tc.pkg, bytes.NewReader(nil))
		_, holdsErr := distroregistry.Client{}.Listing(t.Context(), tc.registry, tc.pkg)

		// The Alpine upload path holds no package name: its read does.
		if name != "escaping apk id" {
			require.ErrorIs(t, uploadErr, tc.want, name)
		}

		require.ErrorIs(t, holdsErr, tc.want, name)
	}

	require.LessOrEqual(t, requests.Load(), int64(1), "only the Alpine upload, whose path is valid, reaches the server")
}

func TestRegistryClassifiesARefusedToken(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	t.Cleanup(server.Close)

	target := provider.ForgeDistroRegistry{URL: server.URL + owner, Token: "synthetic"}
	pkg := packageOf(provider.DistroPackageRPM, "rpm package", "stable")

	_, err := distroregistry.Client{}.Upload(t.Context(), target, pkg, strings.NewReader("rpm package"))
	require.ErrorIs(t, err, errs.ErrPermissionDenied)

	_, err = distroregistry.Client{}.Listing(t.Context(), target, pkg)
	require.ErrorIs(t, err, errs.ErrPermissionDenied)
}

// A registry that could not be reached is unavailable; one whose certificate
// does not verify is a failure the next attempt meets again.
func TestRegistryTellsAnUnreachedHostFromARefusedCertificate(t *testing.T) {
	t.Parallel()

	untrusted := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(untrusted.Close)

	gone := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	gone.Close()

	pkg := packageOf(provider.DistroPackageRPM, "rpm package", "stable")

	_, err := distroregistry.Client{}.Upload(t.Context(), provider.ForgeDistroRegistry{URL: untrusted.URL + owner, Token: "synthetic"}, pkg, strings.NewReader("rpm package"))
	require.ErrorIs(t, err, errs.ErrValidation)
	require.NotErrorIs(t, err, errs.ErrDependencyUnavailable)

	_, err = distroregistry.Client{}.Upload(t.Context(), provider.ForgeDistroRegistry{URL: gone.URL + owner, Token: "synthetic"}, pkg, strings.NewReader("rpm package"))
	require.ErrorIs(t, err, errs.ErrDependencyUnavailable)
}

// An HTTPS registry's read is not followed to plain HTTP, loopback or not.
func TestRegistryReadFollowsNoPlainRedirectFromHTTPS(t *testing.T) {
	t.Parallel()

	var plain atomic.Int64

	storage := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { plain.Add(1) }))
	t.Cleanup(storage.Close)

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Location", storage.URL+"/hello.apk")
		writer.WriteHeader(http.StatusSeeOther)
	}))
	t.Cleanup(server.Close)

	target := provider.ForgeDistroRegistry{URL: server.URL + owner, Token: "synthetic"}
	pkg := packageOf(provider.DistroPackageAlpine, "alpine package", "stable", "main")

	_, err := distroregistry.Client{HTTP: server.Client()}.Listing(t.Context(), target, pkg)
	require.ErrorIs(t, err, errs.ErrInvalidConfig)
	require.Zero(t, plain.Load())
}

// The address is checked without a request, so a release is refused for it
// before it is signed.
func TestCheckRegistryRefusesPlainHTTPOutsideLoopback(t *testing.T) {
	t.Parallel()

	require.ErrorIs(t, distroregistry.CheckRegistry(provider.ForgeDistroRegistry{URL: "http://forge.example" + owner}), errs.ErrInvalidConfig)
	require.NoError(t, distroregistry.CheckRegistry(provider.ForgeDistroRegistry{URL: "https://forge.example" + owner}))
	require.NoError(t, distroregistry.CheckRegistry(provider.ForgeDistroRegistry{URL: "http://127.0.0.1:3000" + owner}))
}
