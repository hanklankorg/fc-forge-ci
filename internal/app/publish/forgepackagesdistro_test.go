// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/app/publish"
	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// fakeDistroRegistry keeps one file for each package version at a place, as
// the registries do: a second upload of a version it holds is refused and
// changes nothing. The file's base name stands for the package version.
// rewrite makes it store other bytes than it was sent, as a registry that
// signs a package on upload does. unlisted is the number of reads that do
// not show what it holds yet, as while another run's indexes are rebuilt.
// As the real ones, it tells other content of a version apart from no
// version only for Alpine, which is read by name.
type fakeDistroRegistry struct {
	held     map[string]string
	uploads  []string
	rewrite  bool
	unlisted int
}

func fakeKey(pkg provider.DistroPackage) string {
	return string(pkg.Format) + " " + strings.Join(pkg.Place, "/") + " " + filepath.Base(pkg.Path)
}

func (f *fakeDistroRegistry) Upload(_ context.Context, _ provider.ForgeDistroRegistry, pkg provider.DistroPackage, body io.Reader) (bool, error) {
	f.uploads = append(f.uploads, fakeKey(pkg))

	content, err := io.ReadAll(body)
	if err != nil {
		return false, err
	}

	if _, exists := f.held[fakeKey(pkg)]; exists {
		return false, nil
	}

	if f.rewrite {
		content = append(content, "signed by the registry"...)
	}

	f.held[fakeKey(pkg)] = digestOf(content)

	return true, nil
}

func (f *fakeDistroRegistry) Listing(_ context.Context, _ provider.ForgeDistroRegistry, pkg provider.DistroPackage) (provider.DistroListing, error) {
	if f.unlisted > 0 {
		f.unlisted--

		return provider.DistroNotListed, nil
	}

	digest, exists := f.held[fakeKey(pkg)]

	switch {
	case exists && digest == pkg.SHA256:
		return provider.DistroListed, nil
	case exists && pkg.Format == provider.DistroPackageAlpine:
		return provider.DistroOtherContent, nil
	default:
		return provider.DistroNotListed, nil
	}
}

type distroResolver struct{ token string }

func (r distroResolver) ResolveForgeDistroRegistry() (provider.ForgeDistroRegistry, error) {
	return provider.ForgeDistroRegistry{URL: "https://forge.example/api/packages/owner", Token: r.token}, nil
}

func digestOf(content []byte) string {
	sum := sha256.Sum256(content)

	return hex.EncodeToString(sum[:])
}

// alpinePackage is the start of an apk: a gzip stream of a tar whose
// .PKGINFO states the package.
func alpinePackage(t *testing.T, architecture string) []byte {
	t.Helper()

	info := "pkgname = hello\npkgver = 1.2.3-r0\narch = " + architecture + "\n"

	var packed bytes.Buffer

	compressed := gzip.NewWriter(&packed)
	archive := tar.NewWriter(compressed)
	require.NoError(t, archive.WriteHeader(&tar.Header{Name: ".PKGINFO", Mode: 0o644, Size: int64(len(info))}))
	_, err := archive.Write([]byte(info))
	require.NoError(t, err)
	require.NoError(t, archive.Close())
	require.NoError(t, compressed.Close())

	return packed.Bytes()
}

func releaseFiles(t *testing.T, files map[string][]byte) []string {
	t.Helper()

	dist := filepath.Join(t.TempDir(), "dist")
	require.NoError(t, os.Mkdir(dist, 0o700))

	paths := make([]string, 0, len(files))

	for _, name := range []string{"hello_1.2.3_amd64.deb", "hello-1.2.3-1.x86_64.rpm", "hello-1.2.3-r0.apk", "hello.tar.gz", "hello_1.2.3_amd64.deb.sig"} {
		if content, named := files[name]; named {
			require.NoError(t, os.WriteFile(filepath.Join(dist, name), content, 0o600))
			paths = append(paths, filepath.Join(dist, name))
		}
	}

	return paths
}

func defaultPlaces() config.DistroPackages {
	return *(&config.DistroPackages{}).Resolved()
}

func threeFormats(t *testing.T) map[string][]byte {
	t.Helper()

	return map[string][]byte{
		"hello_1.2.3_amd64.deb":     []byte("debian package"),
		"hello-1.2.3-1.x86_64.rpm":  []byte("rpm package"),
		"hello-1.2.3-r0.apk":        alpinePackage(t, "x86_64"),
		"hello.tar.gz":              []byte("an archive, no package"),
		"hello_1.2.3_amd64.deb.sig": []byte("the release's signature of the package"),
	}
}

// Each package of the release goes to its format's registry at the declared
// place, as the bytes the release holds; no other release file is sent. A
// second run finds the same content and leaves the registry as it is.
func TestDistroPublishSendsEachPackageOnceAndARetrySucceeds(t *testing.T) {
	t.Parallel()

	files := threeFormats(t)
	paths := releaseFiles(t, files)
	registry := &fakeDistroRegistry{held: map[string]string{}}
	input := publish.ForgePackagesDistroInput{Files: paths, Places: defaultPlaces()}

	var out bytes.Buffer

	require.NoError(t, publish.ForgePackagesDistroPublish(t.Context(), registry, distroResolver{"token"}, &out, input))

	want := map[string]string{
		"debian stable/main hello_1.2.3_amd64.deb": digestOf(files["hello_1.2.3_amd64.deb"]),
		"rpm stable hello-1.2.3-1.x86_64.rpm":      digestOf(files["hello-1.2.3-1.x86_64.rpm"]),
		"alpine stable/main hello-1.2.3-r0.apk":    digestOf(files["hello-1.2.3-r0.apk"]),
	}
	require.Equal(t, want, registry.held)
	require.Equal(t, 3, strings.Count(out.String(), "Published "))

	out.Reset()

	require.NoError(t, publish.ForgePackagesDistroPublish(t.Context(), registry, distroResolver{"token"}, &out, input))
	require.Equal(t, want, registry.held)
	require.Equal(t, 3, strings.Count(out.String(), "Already published with this content"))
}

// The registry holds the package version with other content: the release
// fails, and the registry keeps what it had. Other content is said only
// where the registry shows it; a registry read by digest shows a version
// that is not listed, and the failure names both things that can be.
func TestDistroPublishRefusesAPublishedVersionWithOtherContent(t *testing.T) {
	t.Parallel()

	for name, say := range map[string]string{
		"hello-1.2.3-r0.apk":    "with other content than the release's file",
		"hello_1.2.3_amd64.deb": "either another run published it and the registry does not list it yet",
	} {
		content := []byte("debian package")
		key := "debian stable/main " + name

		if strings.HasSuffix(name, ".apk") {
			content, key = alpinePackage(t, "x86_64"), "alpine stable/main "+name
		}

		paths := releaseFiles(t, map[string][]byte{name: content})
		earlier := digestOf([]byte("another build of the same version"))
		registry := &fakeDistroRegistry{held: map[string]string{key: earlier}}

		err := publish.ForgePackagesDistroPublish(t.Context(), registry, distroResolver{"token"}, io.Discard,
			publish.ForgePackagesDistroInput{Files: paths, Places: defaultPlaces(), ListingWait: 20 * time.Millisecond})

		require.ErrorIs(t, err, errs.ErrValidation, name)
		require.ErrorContains(t, err, say, name)
		require.Equal(t, map[string]string{key: earlier}, registry.held, name)
	}
}

// Another run published the same file and the registry does not list it
// yet: the refused upload waits for the listing and succeeds.
func TestDistroPublishAwaitsAnotherRunsListing(t *testing.T) {
	t.Parallel()

	content := []byte("debian package")
	paths := releaseFiles(t, map[string][]byte{"hello_1.2.3_amd64.deb": content})
	registry := &fakeDistroRegistry{held: map[string]string{"debian stable/main hello_1.2.3_amd64.deb": digestOf(content)}, unlisted: 2}

	var out bytes.Buffer

	require.NoError(t, publish.ForgePackagesDistroPublish(t.Context(), registry, distroResolver{"token"}, &out,
		publish.ForgePackagesDistroInput{Files: paths, Places: defaultPlaces(), ListingWait: time.Second}))
	require.Contains(t, out.String(), "Already published with this content")
	require.Zero(t, registry.unlisted)
}

// A registry that accepts the package and stores other bytes has not
// published the release's file.
func TestDistroPublishRefusesARegistryThatRewritesThePackage(t *testing.T) {
	t.Parallel()

	paths := releaseFiles(t, map[string][]byte{"hello-1.2.3-1.x86_64.rpm": []byte("rpm package")})
	registry := &fakeDistroRegistry{held: map[string]string{}, rewrite: true}

	err := publish.ForgePackagesDistroPublish(t.Context(), registry, distroResolver{"token"}, io.Discard,
		publish.ForgePackagesDistroInput{Files: paths, Places: defaultPlaces()})

	require.ErrorIs(t, err, errs.ErrValidation)
	require.ErrorContains(t, err, "does not serve the release's file")
}

// Nothing is sent without a token, for a release without a package, or
// while one of the packages cannot be published: the packages go together.
// Admission, which the signing job runs before the release is public, finds
// the same without a registry.
func TestDistroPublishSendsNothingItCannotFinish(t *testing.T) {
	t.Parallel()

	files := threeFormats(t)

	for name, tc := range map[string]struct {
		token  string
		change func()
		want   error
	}{
		"no token":    {token: "", change: func() {}, want: errs.ErrPermissionDenied},
		"no package":  {token: "token", change: func() { files = map[string][]byte{"hello.tar.gz": []byte("archive")} }, want: errs.ErrMissingInput},
		"noarch apk":  {token: "token", change: func() { files["hello-1.2.3-r0.apk"] = alpinePackage(t, "noarch") }, want: errs.ErrUnsupported},
		"android apk": {token: "token", change: func() { files["hello-1.2.3-r0.apk"] = []byte("PK\x03\x04 a zip") }, want: errs.ErrMalformedInput},
	} {
		files = threeFormats(t)

		tc.change()

		paths := releaseFiles(t, files)
		registry := &fakeDistroRegistry{held: map[string]string{}}

		err := publish.ForgePackagesDistroPublish(t.Context(), registry, distroResolver{tc.token}, io.Discard,
			publish.ForgePackagesDistroInput{Files: paths, Places: defaultPlaces()})

		require.ErrorIs(t, err, tc.want, name)
		require.Empty(t, registry.uploads, name)

		if tc.token != "" {
			_, err = publish.ForgePackagesDistroAdmit(publish.ForgePackagesDistroInput{Files: paths, Places: defaultPlaces()})
			require.ErrorIs(t, err, tc.want, name)
		}
	}

	paths := releaseFiles(t, threeFormats(t))
	admitted, err := publish.ForgePackagesDistroAdmit(publish.ForgePackagesDistroInput{Files: paths, Places: defaultPlaces()})
	require.NoError(t, err)
	require.Equal(t, 3, admitted)
}

// The wait for another run's listing ends when the run is cancelled.
func TestDistroPublishWaitHonoursCancellation(t *testing.T) {
	t.Parallel()

	content := []byte("debian package")
	paths := releaseFiles(t, map[string][]byte{"hello_1.2.3_amd64.deb": content})
	registry := &fakeDistroRegistry{held: map[string]string{"debian stable/main hello_1.2.3_amd64.deb": digestOf(content)}, unlisted: 1 << 20}

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	started := time.Now()
	err := publish.ForgePackagesDistroPublish(ctx, registry, distroResolver{"token"}, io.Discard,
		publish.ForgePackagesDistroInput{Files: paths, Places: defaultPlaces(), ListingWait: time.Hour})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), time.Minute)
}
