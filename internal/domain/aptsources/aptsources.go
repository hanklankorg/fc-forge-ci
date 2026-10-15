// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package aptsources holds the rule for fetching apt package sources over
// HTTPS: which archive hosts are known to serve it, and how a sources
// document is rewritten to use it.
//
// The Debian and Ubuntu images used as job images name their archives as
// plain http:// URIs, which a runner whose egress allows only TLS cannot
// reach. The rule changes the scheme of those URIs and nothing else. What a
// package is trusted for is unchanged: apt verifies the archive's signature
// either way, and no option of a source is added, dropped or altered.
package aptsources

import (
	"path"
	"strings"
)

// Format is one of the two formats sources.list(5) defines.
type Format int

const (
	// OneLine is the format of sources.list and of *.list files: one source
	// a line, as "deb [options] uri suite components".
	OneLine Format = iota + 1
	// Deb822 is the format of *.sources files: stanzas of "Field: value"
	// lines, the archive in the URIs field.
	Deb822
)

const (
	plainScheme = "http://"
	tlsScheme   = "https://"

	oneLineExtension = ".list"
	deb822Extension  = ".sources"

	urisField = "URIs"
)

// servesHTTPS reports whether host is an official archive known to serve over
// HTTPS what it serves over HTTP. The set is closed on purpose: any other
// host, a private mirror for one, may have no TLS at all, and its sources are
// left as they are. Each host was checked to return the same InRelease file
// over both schemes (2026-10-06).
func servesHTTPS(host string) bool {
	switch strings.ToLower(host) {
	case "deb.debian.org",
		"security.debian.org",
		"archive.ubuntu.com",
		"security.ubuntu.com",
		"ports.ubuntu.com":
		return true
	default:
		return false
	}
}

// FormatOf returns the format apt reads a file of a sources directory in, by
// its name. apt reads a file there only when its name is made of letters,
// digits, underscores, hyphens and periods, does not start with a period and
// ends in .list or .sources; for any other name the second result is false.
func FormatOf(fileName string) (Format, bool) {
	if strings.HasPrefix(fileName, ".") {
		return 0, false
	}

	for _, char := range fileName {
		if !isFileNameChar(char) {
			return 0, false
		}
	}

	switch path.Ext(fileName) {
	case oneLineExtension:
		return OneLine, true
	case deb822Extension:
		return Deb822, true
	default:
		return 0, false
	}
}

func isFileNameChar(char rune) bool {
	switch {
	case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9':
		return true
	default:
		return char == '_' || char == '-' || char == '.'
	}
}

// HTTPS returns document with the scheme of every http:// archive URI on a
// host that serves HTTPS changed to https://, and the number of URIs changed.
// Every other byte is returned as it was: comments, blank lines, options,
// Signed-By, suites, components, and the URIs of every other host, scheme or
// port.
func HTTPS(format Format, document string) (string, int) {
	var (
		out     strings.Builder
		inURIs  bool
		changed int
	)

	for line := range strings.SplitAfterSeq(document, "\n") {
		var count int

		switch format {
		case OneLine:
			line, count = oneLineHTTPS(line)
		case Deb822:
			line, count, inURIs = deb822HTTPS(line, inURIs)
		}

		out.WriteString(line)

		changed += count
	}

	return out.String(), changed
}

// oneLineHTTPS rewrites the URI of a "deb [options] uri suite components"
// line. A comment, a blank line and anything else that is not a source is
// returned unchanged.
func oneLineHTTPS(line string) (string, int) {
	typeStart := skipSpace(line, 0)
	typeEnd := wordEnd(line, typeStart)

	if sourceType := line[typeStart:typeEnd]; sourceType != "deb" && sourceType != "deb-src" {
		return line, 0
	}

	uriStart := skipSpace(line, typeEnd)

	if strings.HasPrefix(line[uriStart:], "[") {
		optionsEnd := strings.IndexByte(line[uriStart:], ']')
		if optionsEnd < 0 {
			return line, 0
		}

		uriStart = skipSpace(line, uriStart+optionsEnd+1)
	}

	uriEnd := wordEnd(line, uriStart)

	uri, ok := httpsURI(line[uriStart:uriEnd])
	if !ok {
		return line, 0
	}

	return line[:uriStart] + uri + line[uriEnd:], 1
}

// deb822HTTPS rewrites the URIs of one line of a deb822 document. inURIs says
// whether the line before it left the URIs field open, which a continuation
// line (one that starts with a space or a tab) carries on; the last result
// says the same of this line.
func deb822HTTPS(line string, inURIs bool) (string, int, bool) {
	switch {
	case strings.HasPrefix(line, "#"):
		return line, 0, inURIs
	case strings.TrimRight(line, " \t\r\n") == "":
		return line, 0, false
	case line[0] == ' ' || line[0] == '\t':
		if !inURIs {
			return line, 0, false
		}

		rewritten, changed := wordsHTTPS(line)

		return rewritten, changed, true
	}

	name, value, isField := strings.Cut(line, ":")
	if !isField || !strings.EqualFold(name, urisField) {
		return line, 0, false
	}

	rewritten, changed := wordsHTTPS(value)

	return name + ":" + rewritten, changed, true
}

// wordsHTTPS rewrites every whitespace-separated URI of text.
func wordsHTTPS(text string) (string, int) {
	var (
		out     strings.Builder
		changed int
	)

	for pos := 0; pos < len(text); {
		start := skipSpace(text, pos)
		end := wordEnd(text, start)

		uri, ok := httpsURI(text[start:end])
		if ok {
			changed++
		}

		out.WriteString(text[pos:start])
		out.WriteString(uri)

		pos = end
	}

	return out.String(), changed
}

// httpsURI changes the scheme of an http:// URI whose authority is exactly a
// host that serves HTTPS. An authority with a port or credentials is not
// such a host, and its URI is returned unchanged.
func httpsURI(uri string) (string, bool) {
	rest, isPlain := strings.CutPrefix(uri, plainScheme)
	if !isPlain {
		return uri, false
	}

	authority, _, _ := strings.Cut(rest, "/")
	if !servesHTTPS(authority) {
		return uri, false
	}

	return tlsScheme + rest, true
}

func isSpace(char byte) bool {
	return char == ' ' || char == '\t' || char == '\r' || char == '\n'
}

// skipSpace returns the index of the first byte at or after pos that is not
// whitespace.
func skipSpace(text string, pos int) int {
	for pos < len(text) && isSpace(text[pos]) {
		pos++
	}

	return pos
}

// wordEnd returns the index of the first whitespace byte at or after pos.
func wordEnd(text string, pos int) int {
	for pos < len(text) && !isSpace(text[pos]) {
		pos++
	}

	return pos
}
