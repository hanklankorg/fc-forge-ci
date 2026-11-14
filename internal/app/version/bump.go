// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/diggsweden/forge-ci/internal/clicolor"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/domain/version"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// BumpInput drives Bump.
type BumpInput struct {
	Library     bool // Android libraries use Maven's version property, not application versionName/versionCode.
	ProjectType projecttype.Type
	Version     string
	WorkingDir  string // empty → cwd

	// GradleVersionFile is the path to the gradle properties file
	// (relative to WorkingDir). Empty → "gradle.properties".
	GradleVersionFile string
	// XcconfigFile is the .xcconfig path. Empty → "versions.xcconfig".
	XcconfigFile string

	// MavenCLIOpts is the value of $MAVEN_CLI_OPTS, split into argv.
	MavenCLIOpts []string
}

// MavenOps abstracts the maven adapter for dependency injection. The
// dir-aware RunInheritIn is used (rather than RunInherit) so the bump
// can run mvn in the project's working directory without mutating
// global cwd.
type MavenOps interface {
	RunInheritIn(ctx context.Context, dir string, w, stderr io.Writer, args ...string) error
}

// NPMOps abstracts the npm adapter.
type NPMOps interface {
	RunInherit(ctx context.Context, dir string, w, stderr io.Writer, args ...string) error
}

// CargoOps abstracts the cargo adapter.
type CargoOps interface {
	Available() bool
	RunInherit(ctx context.Context, dir string, w, stderr io.Writer, args ...string) error
}

// BumpOps bundles the three external-binary adapters Bump needs. Tests
// inject fakes; production wires the real adapters.
type BumpOps struct {
	Maven MavenOps
	NPM   NPMOps
	Cargo CargoOps
	// Git is used only to apply verified patches prepared in a key-free job.
	Git applyBumpOps
}

// bumpTarget keeps the engine-owned filename identical during preflight, writes
// and path reporting. Native Maven/npm target selection is deliberately absent.
type bumpTarget struct {
	BumpInput

	file string
}

func normalizeBumpTarget(in BumpInput) (bumpTarget, error) {
	kind, file, text := version.TextBumpTarget(in.ProjectType, in.Library, in.GradleVersionFile, in.XcconfigFile)
	in.ProjectType = kind

	switch {
	case text:
	case in.ProjectType == projecttype.Cargo:
		file = cargoManifestName
	case in.ProjectType == projecttype.Go, in.ProjectType == projecttype.Meta, in.ProjectType == projecttype.Maven, in.ProjectType == projecttype.NPM:
	default:
		return bumpTarget{}, fmt.Errorf("unknown project type: %s: %w", in.ProjectType, errs.ErrUsage)
	}

	// Check raw input before trimming can hide a forbidden line boundary. The
	// version reaches a file or a native tool's arguments for every project type.
	if strings.ContainsAny(in.Version, "\r\n\x00") {
		return bumpTarget{}, fmt.Errorf("version must not contain CR, LF or NUL: %w", errs.ErrUsage)
	}

	if !utf8.ValidString(in.Version) {
		return bumpTarget{}, fmt.Errorf("version must be valid UTF-8: %w", errs.ErrUsage)
	}

	if file != "" {
		if !pathsafe.Relative(file) || filepath.Clean(file) == "." || strings.ContainsRune(file, '\x00') {
			return bumpTarget{}, fmt.Errorf("version file must be project-relative and contain no NUL: %w", errs.ErrUsage)
		}

		file = filepath.Clean(file)
	}

	in.Version = strings.TrimSpace(in.Version)
	if in.Version == "" {
		return bumpTarget{}, fmt.Errorf("version is required: %w", errs.ErrUsage)
	}

	// npm takes the version as a bare argument, where a dash makes it an option.
	if strings.HasPrefix(in.Version, "-") {
		return bumpTarget{}, fmt.Errorf("version must not start with a dash: %q: %w", in.Version, errs.ErrUsage)
	}

	if parsed, ok := version.ParseSemver(in.Version); ok {
		in.Version = parsed.Version
	}

	return bumpTarget{BumpInput: in, file: file}, nil
}

// Bump rewrites the version-of-record for a project per its
// project-type, optionally invoking maven/npm/cargo to refresh
// dependent files.
func Bump(ctx context.Context, ops BumpOps, out, stderr io.Writer, annot output.Annotator, in BumpInput) error {
	target, err := normalizeBumpTarget(in)
	if err != nil {
		return err
	}

	in = target.BumpInput

	dir := in.WorkingDir
	if dir == "" {
		dir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("getwd: %w", err)
		}
	}

	_, _ = fmt.Fprintf(out, "Bumping version to %s for %s project in %s\n", in.Version, in.ProjectType, dir)

	switch in.ProjectType {
	case projecttype.Maven:
		return bumpMaven(ctx, ops.Maven, dir, in.MavenCLIOpts, in.Version, out, stderr)
	case projecttype.NPM:
		return bumpNPM(ctx, ops.NPM, dir, in.Version, out, stderr)
	case projecttype.Gradle:
		return withVersionRoot(dir, target.file, func(root *os.Root, relative, display string) error {
			return bumpGradleJVM(root, relative, display, in.Version, out, annot)
		})
	case projecttype.GradleAndroid:
		return withVersionRoot(dir, target.file, func(root *os.Root, relative, display string) error {
			return bumpGradleAndroid(root, relative, display, in.Version, out, annot)
		})
	case projecttype.XcodeIOS:
		return withVersionRoot(dir, target.file, func(root *os.Root, relative, display string) error {
			return bumpXcodeIOS(root, relative, display, in.Version, out)
		})
	case projecttype.Go:
		_, _ = fmt.Fprintln(out, "Go project type - no version file to update")
		_, _ = fmt.Fprintf(out, "%s Version %s will be supplied by the release tag/build ldflags\n", clicolor.Check(out), in.Version)

		return nil
	case projecttype.Cargo:
		return withVersionRoot(dir, target.file, func(root *os.Root, relative, display string) error {
			return bumpCargo(ctx, ops.Cargo, root, relative, display, dir, in.Version, out, stderr, annot)
		})
	case projecttype.Meta:
		_, _ = fmt.Fprintln(out, "Meta project type - no version file to update")
		_, _ = fmt.Fprintf(out, "%s Version %s recorded for changelog generation only\n", clicolor.Check(out), in.Version)

		return nil
	default:
		return fmt.Errorf("unknown project type: %s: %w", in.ProjectType, errs.ErrUsage)
	}
}

func withVersionRoot(dir, relative string, run func(*os.Root, string, string) error) error {
	if !pathsafe.Relative(relative) {
		return fmt.Errorf("version file must be relative to the project directory: %q: %w", relative, errs.ErrUsage)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open project directory %s: %w: %w", dir, err, errs.ErrMissingInput)
	}
	defer func() { _ = root.Close() }()

	return run(root, filepath.Clean(relative), filepath.Join(dir, relative))
}

func bumpMaven(ctx context.Context, ops MavenOps, dir string, cliOpts []string, ver string, out, stderr io.Writer) error {
	if ops == nil {
		return fmt.Errorf("maven adapter not provided"+": %w", errs.ErrUsage)
	}

	_, _ = fmt.Fprintf(out, "Updating Maven version to %s\n", ver)

	args := make([]string, 0, len(cliOpts)+5)
	args = append(args, cliOpts...)

	args = append(args,
		"versions:set",
		"-DnewVersion="+ver,
		"-DgenerateBackupPoms=false",
		"-DprocessAllModules=true",
		"-DskipTests",
	)
	// Adapter already labels its error with the binary + subcommand;
	// wrapping again here would double-prefix.
	if err := ops.RunInheritIn(ctx, dir, out, stderr, args...); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out, "%s Maven version updated (including all sub-modules)\n", clicolor.Check(out))

	return nil
}

func bumpNPM(ctx context.Context, ops NPMOps, dir, ver string, out, stderr io.Writer) error {
	if ops == nil {
		return fmt.Errorf("npm adapter not provided"+": %w", errs.ErrUsage)
	}

	_, _ = fmt.Fprintf(out, "Updating NPM version to %s\n", ver)

	if err := ops.RunInherit(ctx, dir, out, stderr,
		"version", ver, "--no-git-tag-version", "--allow-same-version"); err != nil {
		return fmt.Errorf("npm version: %w", err)
	}

	_, _ = fmt.Fprintf(out, "%s NPM version updated\n", clicolor.Check(out))

	return nil
}

// readVersionFile reads path, emitting a format-aware error
// annotation to stderr when missing (::error:: on GitHub, a plain Error:
// line elsewhere incl. Forgejo) and returning a wrapped error either way.
// The label names the project's version file in the diagnostic.
//
// The read goes through the project's os.Root, which has three distinct
// failure modes an operator has to be able to tell apart:
//
//   - the file is absent — their path is wrong (EX_NOINPUT);
//   - the file is unreadable — a permission problem (EX_NOPERM);
//   - the path resolved outside the project directory, most commonly a
//     symlink pointing out of the checkout. os.Root refuses that, and it is a
//     refusal rather than a missing file.
//
// All three used to be reported as "Gradle version file not found" with
// EX_NOINPUT. For the escape that described a file the operator can see with
// `ls`, and buried the one detail worth surfacing: something in the tree
// points out of it.
func readVersionFile(root *os.Root, relative, display, label string, annot output.Annotator) ([]byte, error) {
	body, err := root.ReadFile(relative)
	if err == nil {
		return body, nil
	}

	// Keep the underlying os error wrapped in every branch so debug output
	// still shows the real cause.
	switch {
	case errors.Is(err, fs.ErrNotExist):
		annot.Errorf("%s not found: %s", label, display)

		return nil, fmt.Errorf("read %s: %w: %w", display, err, errs.ErrMissingInput)

	case errors.Is(err, fs.ErrPermission):
		annot.Errorf("%s is not readable: %s", label, display)

		return nil, fmt.Errorf("read %s: %w: %w", display, err, errs.ErrPermissionDenied)
	}

	annot.Errorf("%s resolves outside the project directory: %s", label, display)

	return nil, fmt.Errorf("read %s: %w: %w", display, err, errs.ErrValidation)
}

func bumpGradleJVM(root *os.Root, relative, display, ver string, writer io.Writer, annot output.Annotator) error {
	body, err := readVersionFile(root, relative, display, "Gradle version file", annot)
	if err != nil {
		return err
	}

	bump, err := version.TextBump(projecttype.Gradle, string(body), true, ver)
	if err != nil {
		return err
	}

	if bump.Property == version.UpdatePropertyUpdated {
		_, _ = fmt.Fprintf(writer, "Updated version to %s\n", ver)
	} else {
		_, _ = fmt.Fprintf(writer, "Added version=%s\n", ver)
	}

	if err := root.WriteFile(relative, []byte(bump.Body), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", display, err)
	}

	_, _ = fmt.Fprintf(writer, "%s Gradle JVM version updated\n", clicolor.Check(writer))
	_, _ = fmt.Fprint(writer, bump.Body)

	return nil
}

func bumpGradleAndroid(root *os.Root, relative, display, ver string, writer io.Writer, annot output.Annotator) error {
	body, err := readVersionFile(root, relative, display, "Gradle version file", annot)
	if err != nil {
		return err
	}

	bump, err := version.TextBump(projecttype.GradleAndroid, string(body), true, ver)
	if err != nil {
		return err
	}

	if bump.Property == version.UpdatePropertyUpdated {
		_, _ = fmt.Fprintf(writer, "Updated versionName to %s\n", ver)
	} else {
		_, _ = fmt.Fprintf(writer, "Added versionName=%s\n", ver)
	}

	if !bump.Writes {
		return nil
	}

	if bump.VersionCode.Added {
		_, _ = fmt.Fprintln(writer, "Added versionCode=1")
	} else {
		_, _ = fmt.Fprintf(writer, "Incremented versionCode: %d → %d\n", bump.VersionCode.Old, bump.VersionCode.New)
	}

	if err := root.WriteFile(relative, []byte(bump.Body), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", display, err)
	}

	_, _ = fmt.Fprintf(writer, "%s Gradle Android version updated\n", clicolor.Check(writer))
	_, _ = fmt.Fprint(writer, bump.Body)

	return nil
}

func bumpXcodeIOS(root *os.Root, relative, display, ver string, writer io.Writer) error {
	body, err := readVersionFile(root, relative, display, "Xcode version file", output.Annotator{})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	bump, bumpErr := version.TextBump(projecttype.XcodeIOS, string(body), err == nil, ver)
	if bumpErr != nil {
		return bumpErr
	}

	if bump.Created {
		// Bash creates the file with MARKETING_VERSION when missing.
		_, _ = fmt.Fprintf(writer, "Creating %s\n", display)

		if werr := writeNewPublicVersionFile(root, relative, []byte(bump.Body)); werr != nil {
			return fmt.Errorf("create %s: %w", display, werr)
		}

		_, _ = fmt.Fprintf(writer, "%s Created %s with MARKETING_VERSION = %s\n", clicolor.Check(writer), display, ver)

		return nil
	}

	if bump.Property == version.UpdatePropertyUpdated {
		_, _ = fmt.Fprintf(writer, "Updated MARKETING_VERSION to %s\n", ver)
	} else {
		_, _ = fmt.Fprintf(writer, "Added MARKETING_VERSION = %s\n", ver)
	}

	if err := root.WriteFile(relative, []byte(bump.Body), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", display, err)
	}

	_, _ = fmt.Fprintf(writer, "%s Xcode version updated\n", clicolor.Check(writer))
	_, _ = fmt.Fprint(writer, bump.Body)

	return nil
}

func bumpCargo(ctx context.Context, ops CargoOps, root *os.Root, relative, display, dir, ver string, writer, stderr io.Writer, annot output.Annotator) error {
	body, err := readVersionFile(root, relative, display, cargoManifestName, annot)
	if err != nil {
		return err
	}

	out, sec, err := version.UpdateCargoVersion(string(body), ver)
	if err != nil {
		annot.Errorf("%s", err.Error())

		return err
	}

	// An existing Cargo.lock is committed with the manifest, so the bump needs
	// cargo to refresh it. Without cargo it is refused before anything is
	// written.
	_, lockErr := root.Stat("Cargo.lock")
	locked := lockErr == nil

	if locked && (ops == nil || !ops.Available()) {
		return fmt.Errorf("refresh Cargo.lock: cargo not found in $PATH: %w", errs.ErrPrerequisite)
	}

	if err := root.WriteFile(relative, []byte(out), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", display, err)
	}

	switch sec {
	case version.CargoSectionWorkspacePackage:
		_, _ = fmt.Fprintf(writer, "Updated [workspace.package].version to %s\n", ver)
	case version.CargoSectionPackage:
		_, _ = fmt.Fprintf(writer, "Updated [package].version to %s\n", ver)
	case version.CargoSectionNone:
		// Unreachable in practice — BumpCargoVersion above returns an
		// error before we get here when no version line was found.
	}

	if locked {
		if err := refreshCargoLock(ctx, ops, dir, writer, stderr); err != nil {
			return err
		}
	}

	_, _ = fmt.Fprintf(writer, "%s Rust version updated\n", clicolor.Check(writer))

	return nil
}

// refreshCargoLock re-resolves an existing Cargo.lock after a version bump.
// The lockfile is committed with the manifest, so one it cannot refresh is
// an error: a stale lock would be released, or refused a job later. A crates
// index cargo says it could not reach ends as the dependency being
// unavailable; every other failure is cargo's own.
func refreshCargoLock(ctx context.Context, ops CargoOps, dir string, out, stderr io.Writer) error {
	if err := ops.RunInherit(ctx, dir, out, stderr, "update", "--workspace", "--offline"); err == nil {
		return nil
	}

	// Try without --offline.
	var said bytes.Buffer

	heard := io.Writer(&said)
	if stderr != nil {
		heard = io.MultiWriter(stderr, &said)
	}

	err := ops.RunInherit(ctx, dir, out, heard, "update", "--workspace")
	if err == nil {
		return nil
	}

	if class := errs.ToolFailure(errs.CargoReport(said.String())); errs.Retriable(class) {
		//nolint:errorlint // flattened: the tool's failure class must not outrank the retriable one.
		return fmt.Errorf("refresh Cargo.lock: %v: %w", err, class)
	}

	return fmt.Errorf("refresh Cargo.lock: %w", err)
}
