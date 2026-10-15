// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package aptsources_test

import (
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/aptsources"
)

// The one-line cases are whole lines, so that each also shows what is left
// alone: the type, the options in brackets, the suite and the components.
func TestHTTPS_OneLine(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		in      string
		want    string
		changed int
	}{
		{
			name:    "an official host",
			in:      "deb http://deb.debian.org/debian bookworm main\n",
			want:    "deb https://deb.debian.org/debian bookworm main\n",
			changed: 1,
		},
		{
			name:    "a source archive",
			in:      "deb-src http://security.debian.org/debian-security bookworm-security main\n",
			want:    "deb-src https://security.debian.org/debian-security bookworm-security main\n",
			changed: 1,
		},
		{
			name:    "options in brackets are kept, Signed-By among them",
			in:      "deb [arch=amd64 signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] http://deb.debian.org/debian bookworm main contrib\n",
			want:    "deb [arch=amd64 signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] https://deb.debian.org/debian bookworm main contrib\n",
			changed: 1,
		},
		{
			name:    "spaces inside the brackets and tabs between the fields",
			in:      "  deb\t[ arch=amd64 ]\thttp://archive.ubuntu.com/ubuntu/\tnoble main\n",
			want:    "  deb\t[ arch=amd64 ]\thttps://archive.ubuntu.com/ubuntu/\tnoble main\n",
			changed: 1,
		},
		{
			name:    "the host is compared without regard to case and kept as written",
			in:      "deb http://Deb.Debian.Org/debian bookworm main\n",
			want:    "deb https://Deb.Debian.Org/debian bookworm main\n",
			changed: 1,
		},
		{
			name:    "a trailing comment and a CRLF line end are kept",
			in:      "deb http://ports.ubuntu.com/ubuntu-ports noble main # arm64\r\n",
			want:    "deb https://ports.ubuntu.com/ubuntu-ports noble main # arm64\r\n",
			changed: 1,
		},
		{
			name:    "no newline at the end of the file",
			in:      "deb http://security.ubuntu.com/ubuntu noble-security main",
			want:    "deb https://security.ubuntu.com/ubuntu noble-security main",
			changed: 1,
		},
		{name: "already https", in: "deb https://deb.debian.org/debian bookworm main\n"},
		{name: "a private mirror may have no TLS", in: "deb http://mirror.example.internal/debian bookworm main\n"},
		{name: "a host that only starts like an official one", in: "deb http://deb.debian.org.example.com/debian bookworm main\n"},
		{name: "an explicit port is another service", in: "deb http://deb.debian.org:80/debian bookworm main\n"},
		{name: "user information in the authority", in: "deb http://mirror@deb.debian.org/debian bookworm main\n"},
		{name: "another scheme", in: "deb mirror+http://deb.debian.org/debian bookworm main\n"},
		{name: "an upper-case scheme is not apt's http method", in: "deb HTTP://deb.debian.org/debian bookworm main\n"},
		{name: "a commented-out source", in: "# deb http://deb.debian.org/debian bookworm main\n"},
		{name: "a URI in an option is not the archive", in: "deb [by-hash=http://deb.debian.org/x] file:/srv/mirror bookworm main\n"},
		{name: "an unterminated option block", in: "deb [arch=amd64 http://deb.debian.org/debian bookworm main\n"},
		{name: "a line that is not a source", in: "http://deb.debian.org/debian bookworm main\n"},
		{name: "a blank line", in: "\n"},
		{name: "an empty document", in: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			want := tc.want
			if tc.changed == 0 {
				want = tc.in
			}

			got, changed := aptsources.HTTPS(aptsources.OneLine, tc.in)
			if got != want || changed != tc.changed {
				t.Errorf("HTTPS(OneLine)\n got %q (%d changed)\nwant %q (%d changed)", got, changed, want, tc.changed)
			}
		})
	}
}

func TestHTTPS_Deb822(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		in      string
		want    string
		changed int
	}{
		{
			name:    "an official host",
			in:      "Types: deb\nURIs: http://deb.debian.org/debian\nSuites: bookworm\n",
			want:    "Types: deb\nURIs: https://deb.debian.org/debian\nSuites: bookworm\n",
			changed: 1,
		},
		{
			name:    "several URIs in one field, rewritten one by one",
			in:      "URIs: http://deb.debian.org/debian  http://mirror.example.internal/debian\thttp://security.debian.org/debian-security\n",
			want:    "URIs: https://deb.debian.org/debian  http://mirror.example.internal/debian\thttps://security.debian.org/debian-security\n",
			changed: 2,
		},
		{
			name:    "continuation lines belong to the field, a comment between them does not end it",
			in:      "URIs:\n http://archive.ubuntu.com/ubuntu/\n# the security archive\n\thttp://security.ubuntu.com/ubuntu/\nSuites: noble\n",
			want:    "URIs:\n https://archive.ubuntu.com/ubuntu/\n# the security archive\n\thttps://security.ubuntu.com/ubuntu/\nSuites: noble\n",
			changed: 2,
		},
		{
			name:    "the field name in another case",
			in:      "uris: http://deb.debian.org/debian\n",
			want:    "uris: https://deb.debian.org/debian\n",
			changed: 1,
		},
		{
			name:    "no space after the colon, CRLF line ends, no final newline",
			in:      "Types: deb\r\nURIs:http://ports.ubuntu.com/ubuntu-ports\r\nSuites: noble",
			want:    "Types: deb\r\nURIs:https://ports.ubuntu.com/ubuntu-ports\r\nSuites: noble",
			changed: 1,
		},
		{name: "already https", in: "URIs: https://deb.debian.org/debian\n"},
		{name: "a private mirror may have no TLS", in: "URIs: http://mirror.example.internal/debian\n"},
		{name: "an explicit port is another service", in: "URIs: http://deb.debian.org:8080/debian\n"},
		{name: "a comment that names an archive", in: "# http://deb.debian.org/debian\n"},
		{name: "a URI in another field", in: "Types: deb\nX-Repolib-Origin: http://deb.debian.org/debian\n"},
		{name: "the continuation of another field", in: "Suites: bookworm\n http://deb.debian.org/debian\n"},
		{name: "a continuation after a blank line belongs to no field", in: "URIs: file:/srv/mirror\n\n http://deb.debian.org/debian\n"},
		{name: "a field whose name only starts like URIs", in: "URIs-Extra: http://deb.debian.org/debian\n"},
		{name: "an empty document", in: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			want := tc.want
			if tc.changed == 0 {
				want = tc.in
			}

			got, changed := aptsources.HTTPS(aptsources.Deb822, tc.in)
			if got != want || changed != tc.changed {
				t.Errorf("HTTPS(Deb822)\n got %q (%d changed)\nwant %q (%d changed)", got, changed, want, tc.changed)
			}
		})
	}
}

// debianSources is the file the official Debian images ship, comments
// included; the comments name an http:// URI that is not a source.
const debianSources = `Types: deb
# http://snapshot.debian.org/archive/debian/20261005T000000Z
URIs: http://deb.debian.org/debian
Suites: bookworm bookworm-updates
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg

Types: deb
# http://snapshot.debian.org/archive/debian-security/20261005T000000Z
URIs: http://deb.debian.org/debian-security
Suites: bookworm-security
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg
`

// TestHTTPS_ChangesTheSchemeAndNothingElse holds whole documents to the
// claim the package makes: the only bytes that differ are the "s" of each
// rewritten scheme. A stanza's Signed-By, a line's options and a private
// mirror next to an official archive all come back as they went in.
func TestHTTPS_ChangesTheSchemeAndNothingElse(t *testing.T) {
	t.Parallel()

	const mixedList = `# Official archives.
deb [signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] http://deb.debian.org/debian bookworm main

deb http://mirror.example.internal/debian bookworm main
deb https://packages.example.com/apt stable main
deb-src http://security.debian.org/debian-security bookworm-security main
`

	for _, tc := range []struct {
		name    string
		format  aptsources.Format
		in      string
		changed int
	}{
		{name: "the Debian image's sources", format: aptsources.Deb822, in: debianSources, changed: 2},
		{name: "a mixed one-line file", format: aptsources.OneLine, in: mixedList, changed: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, changed := aptsources.HTTPS(tc.format, tc.in)
			if changed != tc.changed {
				t.Errorf("changed = %d, want %d", changed, tc.changed)
			}

			if len(got) != len(tc.in)+tc.changed {
				t.Errorf("the document grew by %d bytes, want one per rewritten scheme (%d)", len(got)-len(tc.in), tc.changed)
			}

			for _, official := range []string{"deb.debian.org", "security.debian.org"} {
				if strings.Contains(got, "URIs: http://"+official) || strings.Contains(got, "] http://"+official) || strings.Contains(got, "deb-src http://"+official) {
					t.Errorf("a source on %s is still fetched over HTTP:\n%s", official, got)
				}
			}

			// Putting the old scheme back gives the input, byte for byte.
			back := got
			for _, official := range []string{"deb.debian.org", "security.debian.org"} {
				back = strings.ReplaceAll(back, "https://"+official, "http://"+official)
			}

			if back != tc.in {
				t.Errorf("something other than a scheme changed:\n got %q\nwant %q", back, tc.in)
			}
		})
	}
}

// TestHTTPS_IsIdempotent covers a second pass over the result: nothing left
// to rewrite, nothing changed.
func TestHTTPS_IsIdempotent(t *testing.T) {
	t.Parallel()

	once, _ := aptsources.HTTPS(aptsources.Deb822, debianSources)

	twice, changed := aptsources.HTTPS(aptsources.Deb822, once)
	if twice != once || changed != 0 {
		t.Errorf("a second pass changed %d URIs:\n%s", changed, twice)
	}
}

// TestHTTPS_EveryNamedHost pins the set of hosts. Adding one is a claim that
// it serves the same archive over HTTPS, so it is made here as well as in the
// rule.
func TestHTTPS_EveryNamedHost(t *testing.T) {
	t.Parallel()

	for _, host := range []string{
		"deb.debian.org",
		"security.debian.org",
		"archive.ubuntu.com",
		"security.ubuntu.com",
		"ports.ubuntu.com",
	} {
		got, changed := aptsources.HTTPS(aptsources.OneLine, "deb http://"+host+"/x stable main\n")
		if want := "deb https://" + host + "/x stable main\n"; got != want || changed != 1 {
			t.Errorf("%s: got %q (%d changed), want %q", host, got, changed, want)
		}
	}

	for _, host := range []string{
		"ftp.debian.org", "snapshot.debian.org", "old-releases.ubuntu.com", "ppa.launchpad.net", "localhost", "",
	} {
		in := "deb http://" + host + "/x stable main\n"
		if got, changed := aptsources.HTTPS(aptsources.OneLine, in); got != in || changed != 0 {
			t.Errorf("%q is not in the set, and its source was rewritten: %q", host, got)
		}
	}
}

func TestFormatOf(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		format aptsources.Format
		ok     bool
	}{
		{name: "debian.sources", format: aptsources.Deb822, ok: true},
		{name: "docker.list", format: aptsources.OneLine, ok: true},
		{name: "ubuntu-24.04_main.sources", format: aptsources.Deb822, ok: true},
		{name: "docker.list.save"},
		{name: "debian.sources.dpkg-old"},
		{name: "README"},
		{name: ".list"},
		{name: ".hidden.sources"},
		{name: "with space.list"},
		{name: "tilde~.list"},
		{name: "sub/dir.list"},
		{name: ""},
	} {
		if format, ok := aptsources.FormatOf(tc.name); format != tc.format || ok != tc.ok {
			t.Errorf("FormatOf(%q) = %v, %v; want %v, %v", tc.name, format, ok, tc.format, tc.ok)
		}
	}
}
