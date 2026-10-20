// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

// MaxBumpPatchBytes bounds a version-bump patch. A bump rewrites a handful of
// lines per project file; anything near this size is not a bump.
const MaxBumpPatchBytes = 1 << 20

// noNewlineMarker is the line git writes after a last line without a newline.
const noNewlineMarker = `\ No newline at end of file`

// BumpPatchFiles names the project files a version-bump patch may touch for a
// project type, by base name: the files FilePattern stages besides the
// changelog. A type that is absent has no isolated bump.
func BumpPatchFiles(pt projecttype.Type) ([]string, bool) {
	switch pt { //nolint:exhaustive // only the types with a verified isolated bump are listed.
	case projecttype.Maven:
		return []string{"pom.xml"}, true
	case projecttype.NPM:
		return []string{"package.json", "package-lock.json", "npm-shrinkwrap.json"}, true
	case projecttype.Cargo:
		return []string{"Cargo.toml", "Cargo.lock"}, true
	default:
		return nil, false
	}
}

// BumpPatchResult is what a verified bump patch changes.
type BumpPatchResult struct {
	Paths      []string // repository-relative project files, sorted
	OldVersion string   // the one version every changed line moved away from
}

var (
	bumpPatchHunk    = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)
	bumpPatchIndex   = regexp.MustCompile(`^index [0-9a-f]{7,64}\.\.[0-9a-f]{7,64} 100(?:644|755)$`)
	bumpPatchVersion = regexp.MustCompile(`^[0-9][0-9A-Za-z.+_-]*$`)

	bumpPatchOutputTimestamp = regexp.MustCompile(`^(\s*)<project\.build\.outputTimestamp>` +
		`([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:Z|[+-][0-9]{2}:[0-9]{2})|[0-9]{1,12})` +
		`</project\.build\.outputTimestamp>$`)
)

// VerifyBumpPatch accepts a `git diff` produced by a version bump only when
// every change is the substitution of one old version by newVersion, in the
// named project files and nowhere else. The patch comes from a job that ran
// project-controlled build tools, so anything else it carries is refused: other
// files, file creation, deletion, renames, mode or binary changes, unpaired
// lines, and lines that differ in more than the version.
func VerifyBumpPatch(patch []byte, files []string, newVersion string) (BumpPatchResult, error) {
	if err := checkBumpPatchText(patch, newVersion); err != nil {
		return BumpPatchResult{}, fmt.Errorf("version-bump patch refused: %w", err)
	}

	parser := bumpPatchParser{
		lines:      strings.Split(strings.TrimSuffix(string(patch), "\n"), "\n"),
		files:      files,
		newVersion: newVersion,
		seen:       map[string]bool{},
	}

	for parser.at < len(parser.lines) {
		if err := parser.file(); err != nil {
			return BumpPatchResult{}, fmt.Errorf("version-bump patch refused: %w", err)
		}
	}

	if parser.pairs == 0 {
		return BumpPatchResult{}, fmt.Errorf("version-bump patch refused: no line changes version: %w", errs.ErrValidation)
	}

	slices.Sort(parser.result.Paths)

	return parser.result, nil
}

func checkBumpPatchText(patch []byte, newVersion string) error {
	switch {
	case !bumpPatchVersion.MatchString(newVersion):
		return fmt.Errorf("new version %q is not a plain version: %w", newVersion, errs.ErrValidation)
	case len(patch) == 0:
		return fmt.Errorf("the patch is empty, so the bump changed nothing: %w", errs.ErrValidation)
	case len(patch) > MaxBumpPatchBytes:
		return fmt.Errorf("the patch is %d bytes, over the %d-byte limit: %w", len(patch), MaxBumpPatchBytes, errs.ErrValidation)
	case !utf8.Valid(patch):
		return fmt.Errorf("the patch is not UTF-8 text: %w", errs.ErrValidation)
	case patch[len(patch)-1] != '\n':
		return fmt.Errorf("the patch does not end with a newline: %w", errs.ErrValidation)
	}

	return nil
}

// bumpPatchParser walks a patch line by line; at is the next unread line.
type bumpPatchParser struct {
	lines      []string
	at         int
	files      []string
	newVersion string
	seen       map[string]bool
	pairs      int
	result     BumpPatchResult
}

// line returns line n, or an empty string past the end.
func (p *bumpPatchParser) line(n int) string {
	if n < len(p.lines) {
		return p.lines[n]
	}

	return ""
}

// file reads one file's section: its headers, then at least one hunk.
func (p *bumpPatchParser) file() error {
	file, err := p.fileHeader()
	if err != nil {
		return err
	}

	hunks := 0

	for p.at < len(p.lines) && !strings.HasPrefix(p.line(p.at), "diff --git ") {
		if err := p.hunk(file); err != nil {
			return err
		}

		hunks++
	}

	if hunks == 0 {
		return fmt.Errorf("%s has no content change: %w", file, errs.ErrValidation)
	}

	return nil
}

// fileHeader reads a file section's headers and returns the project file.
func (p *bumpPatchParser) fileHeader() (string, error) {
	header, found := strings.CutPrefix(p.line(p.at), "diff --git ")
	if !found {
		return "", fmt.Errorf("line %d is %q, want a diff --git header: %w", p.at+1, p.line(p.at), errs.ErrValidation)
	}

	file, err := bumpPatchFile(header, p.files)
	if err != nil {
		return "", err
	}

	if p.seen[file] {
		return "", fmt.Errorf("%s appears twice: %w", file, errs.ErrValidation)
	}

	p.seen[file] = true
	p.result.Paths = append(p.result.Paths, file)
	p.at++

	if bumpPatchIndex.MatchString(p.line(p.at)) {
		p.at++
	}

	if p.line(p.at) != "--- a/"+file || p.line(p.at+1) != "+++ b/"+file {
		return "", fmt.Errorf("%s: %q where only a text content change is allowed (no new, deleted, renamed, binary or mode-changed file): %w",
			file, p.line(p.at), errs.ErrValidation)
	}

	p.at += 2

	return file, nil
}

// hunk reads one hunk and holds it to its header's line counts.
func (p *bumpPatchParser) hunk(file string) error {
	match := bumpPatchHunk.FindStringSubmatch(p.line(p.at))
	if match == nil {
		return fmt.Errorf("%s: line %d is %q, want a hunk header: %w", file, p.at+1, p.line(p.at), errs.ErrValidation)
	}

	oldCount, newCount := hunkCount(match[2]), hunkCount(match[4])
	p.at++

	for oldCount > 0 || newCount > 0 {
		lines, err := p.hunkStep(file)
		if err != nil {
			return err
		}

		oldCount -= lines
		newCount -= lines
	}

	if oldCount != 0 || newCount != 0 {
		return fmt.Errorf("%s: a hunk's lines do not match its header: %w", file, errs.ErrValidation)
	}

	return nil
}

// hunkStep reads one context line or one change run and returns how many lines
// it covers on each side; a bump changes lines in place, so both sides match.
func (p *bumpPatchParser) hunkStep(file string) (int, error) {
	switch current := p.line(p.at); {
	case p.at >= len(p.lines):
		return 0, fmt.Errorf("%s: a hunk ends before its line counts: %w", file, errs.ErrValidation)
	case strings.HasPrefix(current, " "):
		p.at++

		if p.line(p.at) == noNewlineMarker {
			p.at++
		}

		return 1, nil
	case strings.HasPrefix(current, "-"):
		return p.change(file)
	default:
		return 0, fmt.Errorf("%s: line %d is %q, which a bump does not produce: %w", file, p.at+1, current, errs.ErrValidation)
	}
}

// change reads a run of removed lines and the equal run of added lines that
// replaces them, pairing them in order, and returns the run's length.
func (p *bumpPatchParser) change(file string) (int, error) {
	removed := p.run(p.at, "-")
	added := p.run(p.at+removed, "+")

	if added != removed {
		return 0, fmt.Errorf("%s: %d removed lines meet %d added lines; a bump changes each line in place: %w",
			file, removed, added, errs.ErrValidation)
	}

	for offset := range removed {
		before, after := p.line(p.at + offset)[1:], p.line(p.at + removed + offset)[1:]
		if outputTimestampChange(before, after) {
			continue
		}

		old, err := bumpPatchSubstitution(before, after, p.newVersion)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", file, err)
		}

		if p.result.OldVersion != "" && old != p.result.OldVersion {
			return 0, fmt.Errorf("%s: lines move from both %q and %q: %w", file, p.result.OldVersion, old, errs.ErrValidation)
		}

		p.result.OldVersion = old
		p.pairs++
	}

	p.at += removed + added

	return removed, nil
}

// outputTimestampChange reports whether a line pair moves the reproducible
// build timestamp, project.build.outputTimestamp, from one timestamp to
// another: `mvn versions:set` updates it with the version, as the bump on
// GitHub commits it. The value must be an ISO-8601 time or epoch seconds, and
// nothing else on the line may change. It is never a version change itself.
func outputTimestampChange(removed, added string) bool {
	before, after := bumpPatchOutputTimestamp.FindStringSubmatch(removed), bumpPatchOutputTimestamp.FindStringSubmatch(added)

	return before != nil && after != nil && before[1] == after[1] && before[2] != after[2]
}

// run counts the lines from start that begin with marker.
func (p *bumpPatchParser) run(start int, marker string) int {
	count := 0
	for start+count < len(p.lines) && strings.HasPrefix(p.lines[start+count], marker) {
		count++
	}

	return count
}

// bumpPatchFile reads `a/<path> b/<path>` and returns the path when it is a
// plain repository-relative project file the bump may touch.
func bumpPatchFile(header string, files []string) (string, error) {
	if strings.HasPrefix(header, `"`) {
		return "", fmt.Errorf("quoted path %s: a project file path is plain text: %w", header, errs.ErrValidation)
	}

	rest, found := strings.CutPrefix(header, "a/")
	if !found || len(rest)%2 == 0 {
		return "", fmt.Errorf("header %q is not a/<path> b/<path>: %w", header, errs.ErrValidation)
	}

	half := (len(rest) - len(" b/")) / 2
	file := rest[:half]

	if rest[half:] != " b/"+file {
		return "", fmt.Errorf("header %q names two different paths: %w", header, errs.ErrValidation)
	}

	if !IsPlainRepositoryPath(file) {
		return "", fmt.Errorf("path %q is not a plain repository-relative path: %w", file, errs.ErrValidation)
	}

	if !slices.Contains(files, path.Base(file)) {
		return "", fmt.Errorf("%s is not a project file the bump may change (%s): %w", file, strings.Join(files, ", "), errs.ErrValidation)
	}

	return file, nil
}

// IsPlainRepositoryPath reports whether file is a clean repository-relative
// path that git reads literally: no parent step, no .git component, no
// control character, backslash, pathspec magic or glob character.
func IsPlainRepositoryPath(file string) bool {
	return file != "" && path.Clean(file) == file && !path.IsAbs(file) && file != ".." && !strings.HasPrefix(file, "../") &&
		!strings.HasPrefix(file, "-") && !strings.HasPrefix(file, ":") &&
		!strings.ContainsFunc(file, func(r rune) bool { return unicode.IsControl(r) || strings.ContainsRune(`\*?[`, r) }) &&
		!slices.Contains(strings.Split(file, "/"), ".git")
}

// bumpPatchSubstitution returns the old version when added is removed with
// that version, and only it, replaced by newVersion.
func bumpPatchSubstitution(removed, added, newVersion string) (string, error) {
	if strings.Count(added, newVersion) != 1 {
		return "", fmt.Errorf("changed line %q does not hold the new version %s exactly once: %w", added, newVersion, errs.ErrValidation)
	}

	at := strings.Index(added, newVersion)
	prefix, suffix := added[:at], added[at+len(newVersion):]

	if len(removed) <= len(prefix)+len(suffix) || !strings.HasPrefix(removed, prefix) || !strings.HasSuffix(removed, suffix) {
		return "", fmt.Errorf("line %q becomes %q, which is more than a version change: %w", removed, added, errs.ErrValidation)
	}

	old := removed[len(prefix) : len(removed)-len(suffix)]
	if old == newVersion || !bumpPatchVersion.MatchString(old) {
		return "", fmt.Errorf("line %q becomes %q, which is more than a version change: %w", removed, added, errs.ErrValidation)
	}

	return old, nil
}

// hunkCount reads a hunk header's line count, which git leaves out when it
// is one. An unreadable count fails the hunk's own count check.
func hunkCount(written string) int {
	if written == "" {
		return 1
	}

	count, err := strconv.Atoi(written)
	if err != nil {
		return -1
	}

	return count
}
