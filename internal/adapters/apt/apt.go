// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package apt wraps apt-get for Debian-family CI runner bootstrap tasks.
package apt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/diggsweden/forge-ci/internal/domain/aptsources"
	"github.com/diggsweden/forge-ci/internal/safeexec"
)

// Where apt keeps its sources, and where the ca-certificates package of
// Debian and Ubuntu writes the bundle apt's HTTPS transport trusts.
const (
	defaultSourceList  = "/etc/apt/sources.list"
	defaultSourceParts = "/etc/apt/sources.list.d"
	defaultCABundle    = "/etc/ssl/certs/ca-certificates.crt"
)

// The names apt-get is pointed at inside the private sources directory.
const (
	sourceListName  = "sources.list"
	sourcePartsName = "sources.list.d"
)

// Adapter runs apt-get.
//
// It fetches over HTTPS when it can. The Debian and Ubuntu images name their
// archives as http:// URIs, which a runner whose egress allows only TLS cannot
// reach. Install therefore runs apt-get against a private copy of the
// configured sources in which the official archive hosts are https:// (the
// rule is aptsources.HTTPS), and leaves the system's files alone. Without a
// CA bundle HTTPS cannot verify a server, so the configured sources are used
// as they are. Either way a package is trusted for its archive signature, as
// before: no apt option that decides trust is set.
//
// SourceList, SourceParts and CABundle name the default locations when empty.
// A job image that moves its sources elsewhere (Dir::Etc in apt.conf) is not
// followed there.
type Adapter struct {
	Bin         string
	SourceList  string
	SourceParts string
	CABundle    string
}

// New returns an Adapter using apt-get from PATH.
func New() *Adapter { return &Adapter{} }

// Install refreshes package indexes and installs packages without recommends.
func (a *Adapter) Install(ctx context.Context, packages []string, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}

	env := append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")

	sources, cleanup := a.httpsSources(out)
	defer cleanup()

	if err := a.run(ctx, out, env, "update", sources); err != nil {
		return err
	}

	return a.run(ctx, out, env, "install", sources, append([]string{"-y", "--no-install-recommends"}, packages...)...)
}

// run runs one apt-get command. The source options come first, so that both
// commands of an install read the same sources.
func (a *Adapter) run(ctx context.Context, out io.Writer, env []string, command string, sources []string, args ...string) error {
	argv := append(append(append([]string{}, sources...), command), args...)

	cmd := safeexec.Command(ctx, a.bin(), argv...)
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.Env = env

	if err := cmd.Run(); err != nil {
		return safeexec.WrapError(err, a.bin(), command)
	}

	return nil
}

// httpsSources returns the apt-get options that select a private HTTPS copy
// of the configured sources, and the function that removes the copy. The
// options are empty when the configured sources are to be used as they are:
// nothing in them can be rewritten, the image has no CA bundle, or the copy
// could not be made. The last two are said on out, because they are why a
// runner with TLS-only egress then fails to reach its archive.
func (a *Adapter) httpsSources(out io.Writer) ([]string, func()) {
	keep := func() {}

	sources, err := a.readSources()
	if err != nil {
		_, _ = fmt.Fprintf(out, "apt: keeping the configured package sources, HTTP ones included: %v\n", err)

		return nil, keep
	}

	if sources.changed == 0 {
		return nil, keep
	}

	if !hasContent(a.caBundle()) {
		_, _ = fmt.Fprintf(out, "apt: no CA certificates at %s, so %d official package source(s) stay on HTTP; install ca-certificates in the job image to fetch them over HTTPS\n", a.caBundle(), sources.changed)

		return nil, keep
	}

	dir, err := sources.write()
	if err != nil {
		_, _ = fmt.Fprintf(out, "apt: keeping the configured package sources, HTTP ones included: %v\n", err)

		return nil, keep
	}

	_, _ = fmt.Fprintf(out, "apt: fetching %d official package source(s) over HTTPS instead of HTTP\n", sources.changed)

	return []string{
		"-o", "Dir::Etc::SourceList=" + filepath.Join(dir, sourceListName),
		"-o", "Dir::Etc::SourceParts=" + filepath.Join(dir, sourcePartsName),
	}, func() { _ = os.RemoveAll(dir) }
}

// rewrittenSources is every sources file apt would read, rewritten for HTTPS.
type rewrittenSources struct {
	// documents holds each file under its path in the private directory.
	documents map[string]string
	// changed is the number of URIs the rewrite changed, in all files.
	changed int
}

// readSources reads the configured sources: sources.list, and the files
// of the sources directory that apt reads by their names.
func (a *Adapter) readSources() (rewrittenSources, error) {
	// apt reads a missing sources.list as an empty one; the copy always has
	// the file, so that the option names something that exists.
	sources := rewrittenSources{documents: map[string]string{sourceListName: ""}}

	if err := sources.add(sourceListName, aptsources.OneLine, a.sourceList()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return rewrittenSources{}, err
	}

	entries, err := os.ReadDir(a.sourceParts())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return rewrittenSources{}, fmt.Errorf("read %s: %w", a.sourceParts(), err)
	}

	for _, entry := range entries {
		format, ok := aptsources.FormatOf(entry.Name())
		if !ok || entry.IsDir() {
			continue
		}

		if err := sources.add(filepath.Join(sourcePartsName, entry.Name()), format, filepath.Join(a.sourceParts(), entry.Name())); err != nil {
			return rewrittenSources{}, err
		}
	}

	return sources, nil
}

func (s *rewrittenSources) add(name string, format aptsources.Format, source string) error {
	content, err := os.ReadFile(source) //nolint:gosec // G304: apt's own sources files, or the locations a test names.
	if err != nil {
		return fmt.Errorf("read %s: %w", source, err)
	}

	document, changed := aptsources.HTTPS(format, string(content))
	s.documents[name] = document
	s.changed += changed

	return nil
}

// write writes the documents into a new directory only this user can read or
// write, and returns it: what apt-get is pointed at must not be another
// user's to change in a shared temporary directory.
func (s *rewrittenSources) write() (string, error) {
	dir, err := os.MkdirTemp("", "forge-ci-apt-sources-")
	if err != nil {
		return "", fmt.Errorf("create the private sources directory: %w", err)
	}

	if err := s.writeInto(dir); err != nil {
		_ = os.RemoveAll(dir)

		return "", err
	}

	return dir, nil
}

func (s *rewrittenSources) writeInto(dir string) error {
	if err := os.Mkdir(filepath.Join(dir, sourcePartsName), 0o700); err != nil {
		return fmt.Errorf("create the private sources directory: %w", err)
	}

	for name, document := range s.documents {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(document), 0o600); err != nil {
			return fmt.Errorf("write the private sources: %w", err)
		}
	}

	return nil
}

// hasContent reports whether path is a file with something in it. An empty
// bundle is what a half-installed ca-certificates leaves, and verifies no
// server.
func hasContent(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

func (a *Adapter) bin() string {
	if a.Bin != "" {
		return a.Bin
	}

	return "apt-get"
}

func (a *Adapter) sourceList() string {
	if a.SourceList != "" {
		return a.SourceList
	}

	return defaultSourceList
}

func (a *Adapter) sourceParts() string {
	if a.SourceParts != "" {
		return a.SourceParts
	}

	return defaultSourceParts
}

func (a *Adapter) caBundle() string {
	if a.CABundle != "" {
		return a.CABundle
	}

	return defaultCABundle
}
