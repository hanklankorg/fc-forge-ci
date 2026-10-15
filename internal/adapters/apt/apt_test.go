// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package apt_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/apt"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
)

// TestInstall_UpdatesFirstThenInstalls pins the sequence and the flags.
// Installing without updating first fails on any runner whose package
// index predates the requested version, and --no-install-recommends is
// what keeps an unrelated dependency tree out of the build image.
func TestInstall_UpdatesFirstThenInstalls(t *testing.T) {
	t.Parallel()

	m := mockbinary.New(t)
	m.Add("apt-get", `printf '%s\n' "$*"`)

	var out bytes.Buffer

	a := newAdapter(t, m, image{})
	if err := a.Install(context.Background(), []string{"jq", "curl"}, &out); err != nil {
		t.Fatal(err)
	}

	want := "update\ninstall -y --no-install-recommends jq curl\n"
	if got := out.String(); got != want {
		t.Errorf("invocations =\n%q\nwant\n%q", got, want)
	}
}

// TestInstall_SetsNoninteractiveFrontend covers the variable that keeps a
// package with a debconf prompt from hanging the job until it times out.
func TestInstall_SetsNoninteractiveFrontend(t *testing.T) {
	t.Parallel()

	m := mockbinary.New(t)
	m.Add("apt-get", `printf '%s=%s\n' DEBIAN_FRONTEND "${DEBIAN_FRONTEND:-unset}"`)

	var out bytes.Buffer

	a := newAdapter(t, m, image{})
	if err := a.Install(context.Background(), []string{"jq"}, &out); err != nil {
		t.Fatal(err)
	}

	if got := out.String(); strings.Contains(got, "DEBIAN_FRONTEND=unset") {
		t.Errorf("DEBIAN_FRONTEND was not set for the subprocess: %q", got)
	}

	// Both invocations, not only the install.
	if got := strings.Count(out.String(), "DEBIAN_FRONTEND=noninteractive"); got != 2 {
		t.Errorf("set on %d of 2 invocations: %q", got, out.String())
	}
}

// TestInstall_UpdateFailureStopsBeforeInstalling covers the fail-fast:
// installing against a stale index would pull whatever version happens to
// be there, so a failed update must not be followed by an install.
func TestInstall_UpdateFailureStopsBeforeInstalling(t *testing.T) {
	t.Parallel()

	m := mockbinary.New(t)
	m.Add("apt-get", `
printf '%s\n' "$*"
case "$1" in
  update) exit 100 ;;
esac
`)

	var out bytes.Buffer

	a := newAdapter(t, m, image{})
	if err := a.Install(context.Background(), []string{"jq"}, &out); err == nil {
		t.Fatal("a failed update must be an error")
	}

	if strings.Contains(out.String(), "install") {
		t.Errorf("installed despite a failed update: %q", out.String())
	}
}

// image is the part of a job image the adapter reads: its apt sources and
// whether it has a CA bundle.
type image struct {
	sourceList string
	parts      map[string]string
	caBundle   string
}

// newAdapter returns an adapter whose apt-get is the stub and whose sources
// and CA bundle are the image's, under the test's own directory. Nothing of
// the machine the test runs on is read.
func newAdapter(t *testing.T, m *mockbinary.Mock, img image) *apt.Adapter {
	t.Helper()

	root := t.TempDir()
	adapter := &apt.Adapter{
		Bin:         m.Path("apt-get"),
		SourceList:  filepath.Join(root, "etc", "sources.list"),
		SourceParts: filepath.Join(root, "etc", "sources.list.d"),
		CABundle:    filepath.Join(root, "ssl", "ca-certificates.crt"),
	}

	write := func(path, content string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if img.sourceList != "" {
		write(adapter.SourceList, img.sourceList)
	}

	for name, content := range img.parts {
		write(filepath.Join(adapter.SourceParts, name), content)
	}

	if img.caBundle != "" {
		write(adapter.CABundle, img.caBundle)
	}

	return adapter
}

const (
	caBundle = "-----BEGIN CERTIFICATE-----\n"

	debianSources = `Types: deb
# http://snapshot.debian.org/archive/debian/20261005T000000Z
URIs: http://deb.debian.org/debian
Suites: bookworm bookworm-updates
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg
`
	debianSourcesHTTPS = `Types: deb
# http://snapshot.debian.org/archive/debian/20261005T000000Z
URIs: https://deb.debian.org/debian
Suites: bookworm bookworm-updates
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg
`
	mirrorList = "deb [signed-by=/etc/apt/keyrings/mirror.gpg] http://mirror.example.internal/debian bookworm main\n"
)

// sourcesStub is an apt-get that prints the sources it was pointed at: the
// two options, then every file under the directory they name, as apt would
// read them while it runs.
const sourcesStub = `
list='' parts=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) case "$2" in
          Dir::Etc::SourceList=*) list=${2#*=} ;;
          Dir::Etc::SourceParts=*) parts=${2#*=} ;;
        esac
        shift 2 ;;
    *) break ;;
  esac
done
printf 'command=%s\n' "$1"
[ -z "$list" ] || { printf 'list:\n'; cat "$list"; }
[ -z "$parts" ] || for file in "$parts"/*; do printf 'part %s:\n' "${file##*/}"; cat "$file"; done
`

// sourceOptions returns the arguments of an invocation that precede its
// command, which is where the adapter puts the options that select sources.
func sourceOptions(t *testing.T, args []string, command string) []string {
	t.Helper()

	index := slices.Index(args, command)
	if index < 0 {
		t.Fatalf("no %q in %q", command, args)
	}

	return args[:index]
}

// TestInstall_FetchesOfficialSourcesOverHTTPS is the fix: with a CA bundle in
// the image, update and install both read a private copy of the sources in
// which the official archive is https://. A runner that allows only TLS out
// could not reach the http:// one, and an install that read other sources
// than its update would look packages up in indexes it did not fetch.
func TestInstall_FetchesOfficialSourcesOverHTTPS(t *testing.T) {
	t.Parallel()

	m := mockbinary.New(t)
	m.Add("apt-get", sourcesStub)

	var out bytes.Buffer

	adapter := newAdapter(t, m, image{
		sourceList: mirrorList,
		parts:      map[string]string{"debian.sources": debianSources, "notes.txt": "http://deb.debian.org/ignored\n"},
		caBundle:   caBundle,
	})
	if err := adapter.Install(context.Background(), []string{"jq"}, &out); err != nil {
		t.Fatal(err)
	}

	calls := m.Invocations("apt-get")
	if len(calls) != 2 {
		t.Fatalf("apt-get ran %d times, want 2", len(calls))
	}

	update := sourceOptions(t, calls[0].Args, "update")
	install := sourceOptions(t, calls[1].Args, "install")

	if len(update) != 4 || update[0] != "-o" || update[2] != "-o" ||
		!strings.HasPrefix(update[1], "Dir::Etc::SourceList=/") || !strings.HasPrefix(update[3], "Dir::Etc::SourceParts=/") {
		t.Fatalf("update was not pointed at a private sources list and directory: %q", update)
	}

	if !slices.Equal(update, install) {
		t.Errorf("update and install read different sources:\n%q\n%q", update, install)
	}

	if got, want := calls[1].Args[len(install):], []string{"install", "-y", "--no-install-recommends", "jq"}; !slices.Equal(got, want) {
		t.Errorf("install ran as %q, want %q", got, want)
	}

	// What each command read: the mirror's line as it was, the official
	// archive over HTTPS, and nothing from the file apt would not read.
	wantRead := "list:\n" + mirrorList + "part debian.sources:\n" + debianSourcesHTTPS

	for _, command := range []string{"update", "install"} {
		if want := "command=" + command + "\n" + wantRead; !strings.Contains(out.String(), want) {
			t.Errorf("%s did not read the rewritten sources:\n%s\nwant\n%s", command, out.String(), want)
		}
	}

	if !strings.Contains(out.String(), "apt: fetching 1 official package source(s) over HTTPS") {
		t.Errorf("the log does not say that sources are fetched over HTTPS:\n%s", out.String())
	}

	// The system's files are not the ones rewritten, and the copy is gone.
	if got, err := os.ReadFile(filepath.Join(adapter.SourceParts, "debian.sources")); err != nil || string(got) != debianSources {
		t.Errorf("the image's own sources changed: %q, %v", got, err)
	}

	private := strings.TrimPrefix(update[3], "Dir::Etc::SourceParts=")
	if _, err := os.Stat(filepath.Dir(private)); !os.IsNotExist(err) {
		t.Errorf("the private sources directory outlived the install: %v", err)
	}
}

// TestInstall_NoTrustOptionIsEverPassed holds the boundary of the change:
// the transport changes and nothing that decides what a package is trusted
// for does. The only options are the two that name the sources.
func TestInstall_NoTrustOptionIsEverPassed(t *testing.T) {
	t.Parallel()

	m := mockbinary.New(t)
	m.Add("apt-get", ":")

	adapter := newAdapter(t, m, image{parts: map[string]string{"debian.sources": debianSources}, caBundle: caBundle})
	if err := adapter.Install(context.Background(), []string{"jq"}, nil); err != nil {
		t.Fatal(err)
	}

	for _, call := range m.Invocations("apt-get") {
		for index, arg := range call.Args {
			lower := strings.ToLower(arg)
			if strings.Contains(lower, "allow") || strings.Contains(lower, "verify") || strings.Contains(lower, "trusted") || strings.Contains(lower, "insecure") {
				t.Errorf("apt-get was given %q", arg)
			}

			if arg == "-o" && !strings.HasPrefix(call.Args[index+1], "Dir::Etc::Source") {
				t.Errorf("apt-get was given the option %q", call.Args[index+1])
			}
		}
	}
}

// TestInstall_KeepsTheConfiguredSources covers every image in which the
// adapter must run apt-get exactly as it did before the rewrite existed.
func TestInstall_KeepsTheConfiguredSources(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		image   image
		wantLog string
	}{
		{
			name:    "no CA bundle: HTTPS could not verify the archive",
			image:   image{parts: map[string]string{"debian.sources": debianSources}},
			wantLog: "no CA certificates at ",
		},
		{
			name:  "only a private mirror: it may have no TLS",
			image: image{sourceList: mirrorList, caBundle: caBundle},
		},
		{
			name:  "already HTTPS",
			image: image{parts: map[string]string{"debian.sources": debianSourcesHTTPS}, caBundle: caBundle},
		},
		{
			name:  "no sources at all",
			image: image{caBundle: caBundle},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := mockbinary.New(t)
			m.Add("apt-get", ":")

			var out bytes.Buffer

			if err := newAdapter(t, m, tc.image).Install(context.Background(), []string{"jq"}, &out); err != nil {
				t.Fatal(err)
			}

			calls := m.Invocations("apt-get")
			if len(calls) != 2 {
				t.Fatalf("apt-get ran %d times, want 2", len(calls))
			}

			if want := []string{"update"}; !slices.Equal(calls[0].Args, want) {
				t.Errorf("update ran as %q, want %q", calls[0].Args, want)
			}

			if want := []string{"install", "-y", "--no-install-recommends", "jq"}; !slices.Equal(calls[1].Args, want) {
				t.Errorf("install ran as %q, want %q", calls[1].Args, want)
			}

			// The fallback is said once, and only when there was something
			// to fetch over HTTPS.
			if tc.wantLog == "" && out.Len() != 0 {
				t.Errorf("unexpected output: %q", out.String())
			}

			if got := strings.Count(out.String(), "\n"); tc.wantLog != "" && (got != 1 || !strings.Contains(out.String(), tc.wantLog) || !strings.Contains(out.String(), "stay on HTTP")) {
				t.Errorf("want one line containing %q, got %q", tc.wantLog, out.String())
			}
		})
	}
}

// TestInstall_AnEmptyCABundleIsNoBundle covers the file a half-configured
// ca-certificates leaves behind: it exists and verifies no server.
func TestInstall_AnEmptyCABundleIsNoBundle(t *testing.T) {
	t.Parallel()

	m := mockbinary.New(t)
	m.Add("apt-get", ":")

	adapter := newAdapter(t, m, image{parts: map[string]string{"debian.sources": debianSources}, caBundle: caBundle})
	if err := os.Truncate(adapter.CABundle, 0); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer

	if err := adapter.Install(context.Background(), []string{"jq"}, &out); err != nil {
		t.Fatal(err)
	}

	if got := m.Invocations("apt-get")[0].Args; !slices.Equal(got, []string{"update"}) {
		t.Errorf("update ran as %q against an empty CA bundle", got)
	}

	if !strings.Contains(out.String(), "no CA certificates at ") {
		t.Errorf("the fallback is not explained: %q", out.String())
	}
}

// TestInstall_UnreadableSourcesKeepTheConfiguredOnes covers a sources file
// the adapter cannot copy. apt-get is left to read and report on its own
// configuration, and the log says why HTTP sources were not rewritten.
func TestInstall_UnreadableSourcesKeepTheConfiguredOnes(t *testing.T) {
	t.Parallel()

	m := mockbinary.New(t)
	m.Add("apt-get", ":")

	adapter := newAdapter(t, m, image{parts: map[string]string{"debian.sources": debianSources}, caBundle: caBundle})

	// A directory with a sources name: os.ReadFile fails on it for any user.
	if err := os.Mkdir(adapter.SourceList, 0o700); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer

	if err := adapter.Install(context.Background(), []string{"jq"}, &out); err != nil {
		t.Fatal(err)
	}

	if got := m.Invocations("apt-get")[0].Args; !slices.Equal(got, []string{"update"}) {
		t.Errorf("update ran as %q", got)
	}

	if !strings.Contains(out.String(), "keeping the configured package sources") {
		t.Errorf("the fallback is not explained: %q", out.String())
	}
}
