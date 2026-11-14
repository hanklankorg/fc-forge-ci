// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"encoding/json"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

// Rebuild is one command of a Containerfile RUN instruction that builds a
// project, or fetches its dependencies for such a build.
type Rebuild struct {
	Line      int              // line the RUN instruction starts on, from 1
	Command   string           // the command as written, for example "./mvnw clean package"
	Ecosystem projecttype.Type // Maven, Gradle, NPM, Cargo or Go
	// FetchOnly is true for a command that fetches the dependencies of the
	// build and builds nothing itself, such as `npm ci`.
	FetchOnly bool
}

// buildTool is how one ecosystem's build is recognised on a command line.
// A phrase is the words that follow the tool's name. With anywhere set, a
// one-word phrase matches wherever it stands among the arguments, as a Maven
// phase or a Gradle task does; otherwise the phrase is the leading arguments
// that are not options.
type buildTool struct {
	ecosystem    projecttype.Type
	names        []string
	anywhere     bool
	builds       [][]string
	dependencies [][]string
}

// buildTools is the one table of build commands, one or more rows per
// project type forge-ci builds an artifact from.
//
//nolint:goconst // the rows are data: a tool's own words read best written out where they apply.
var buildTools = []buildTool{
	{
		ecosystem: projecttype.Maven, names: []string{"mvn", "mvnw"}, anywhere: true,
		builds:       [][]string{{"package"}, {"install"}, {"verify"}, {"deploy"}},
		dependencies: [][]string{{"dependency:go-offline"}, {"dependency:resolve"}},
	},
	{
		ecosystem: projecttype.Gradle, names: []string{"gradle", "gradlew"}, anywhere: true,
		builds: [][]string{
			{"build"}, {"assemble"}, {"bootJar"}, {"jar"}, {"shadowJar"}, {"installDist"}, {"distZip"}, {"distTar"},
		},
		dependencies: [][]string{{"dependencies"}},
	},
	{
		ecosystem: projecttype.NPM, names: []string{"npm"},
		builds:       [][]string{{"run", "build"}},
		dependencies: [][]string{{"ci"}, {"install"}},
	},
	{
		ecosystem: projecttype.NPM, names: []string{"pnpm", "yarn"},
		builds:       [][]string{{"run", "build"}, {"build"}},
		dependencies: [][]string{{"install"}},
	},
	{
		// install is left out for Cargo and Go: in a Containerfile it nearly
		// always installs a tool, not the project.
		ecosystem: projecttype.Cargo, names: []string{"cargo"},
		builds:       [][]string{{"build"}},
		dependencies: [][]string{{"fetch"}},
	},
	{
		ecosystem: projecttype.Go, names: []string{"go"},
		builds:       [][]string{{"build"}},
		dependencies: [][]string{{"mod", "download"}},
	},
}

var (
	// commandSeparator splits a shell script into its simple commands.
	commandSeparator = regexp.MustCompile(`&&|\|\||[;|&\n]`)
	// heredocStart finds the delimiter of a here-document: <<EOF, <<-EOF, <<"EOF".
	heredocStart = regexp.MustCompile(`<<-?\s*["']?([A-Za-z_][A-Za-z0-9_]*)["']?`)
)

// ContainerfileRebuilds returns the build commands the RUN instructions of a
// Containerfile execute, in file order. Comments and every other instruction
// are ignored. A command that only fetches dependencies, such as `npm ci`, is
// returned only when the file also runs a build of the same ecosystem: alone
// it says nothing about what the image holds.
func ContainerfileRebuilds(body string) []Rebuild {
	var (
		found  []Rebuild
		builds = map[projecttype.Type]bool{}
	)

	for _, run := range runInstructions(body) {
		for command := range strings.SplitSeq(commandSeparator.ReplaceAllString(run.script, "\n"), "\n") {
			ecosystem, isBuild, ok := classify(strings.Fields(command))
			if !ok {
				continue
			}

			builds[ecosystem] = builds[ecosystem] || isBuild
			found = append(found, Rebuild{
				Line: run.line, Command: strings.Join(strings.Fields(command), " "), Ecosystem: ecosystem, FetchOnly: !isBuild,
			})
		}
	}

	return slices.DeleteFunc(found, func(r Rebuild) bool { return !builds[r.Ecosystem] })
}

// runInstruction is the shell script of one RUN instruction.
type runInstruction struct {
	line   int
	script string
}

// runInstructions reads the RUN instructions of a Containerfile: it reads
// the JSON (exec) form and takes the lines of `RUN <<EOF` as the script. Any
// other here-document is data, a file the instruction writes, and is skipped.
func runInstructions(body string) []runInstruction {
	var runs []runInstruction

	for _, ins := range instructions(body) {
		if ins.keyword != instructionRun {
			continue
		}

		// Options such as --mount=type=cache stand before the command.
		script := execFormScript(strings.Join(withoutOptions(ins.words), " "))
		if strings.HasPrefix(script, "<<") {
			script = ins.heredoc
		}

		runs = append(runs, runInstruction{line: ins.line, script: script})
	}

	return runs
}

// The Containerfile instructions the checks read.
const (
	instructionFrom = "FROM"
	instructionRun  = "RUN"
	instructionCopy = "COPY"
	instructionAdd  = "ADD"
)

// instruction is one instruction of a Containerfile.
type instruction struct {
	line    int      // line it starts on, from 1
	keyword string   // upper case: FROM, RUN, COPY
	words   []string // what follows the keyword
	heredoc string   // the lines of its here-document, if it has one
}

// instructions reads a Containerfile: it joins continuation lines, drops
// comment lines and takes the lines of a here-document out of the file, so
// none of them is read as an instruction.
func instructions(body string) []instruction {
	var (
		found []instruction
		lines = strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	)

	for index := 0; index < len(lines); index++ {
		var (
			start  int
			joined strings.Builder
		)

		for ; index < len(lines); index++ {
			line := strings.TrimSpace(lines[index])
			if strings.HasPrefix(line, "#") {
				continue // a comment line, also inside a continued instruction
			}

			if start == 0 {
				start = index + 1
			}

			joined.WriteString(strings.TrimSuffix(line, `\`) + " ")

			if !strings.HasSuffix(line, `\`) {
				break
			}
		}

		words := strings.Fields(joined.String())
		if len(words) == 0 {
			continue
		}

		ins := instruction{line: start, keyword: strings.ToUpper(words[0]), words: words[1:]}

		// Only these instructions take a here-document.
		if ins.keyword == instructionRun || ins.keyword == instructionCopy || ins.keyword == instructionAdd {
			if delimiter := heredocStart.FindStringSubmatch(execFormScript(strings.Join(ins.words, " "))); delimiter != nil {
				ins.heredoc, index = readHeredoc(lines, index+1, delimiter[1])
			}
		}

		found = append(found, ins)
	}

	return found
}

// readHeredoc reads the lines of a here-document from index up to its
// delimiter, without the comment lines. It returns them with the index of
// the delimiter line, or the number of lines when the document is not closed.
func readHeredoc(lines []string, index int, delimiter string) (string, int) {
	var document strings.Builder

	for ; index < len(lines) && strings.TrimSpace(lines[index]) != delimiter; index++ {
		if line := strings.TrimSpace(lines[index]); !strings.HasPrefix(line, "#") {
			document.WriteString(strings.TrimSuffix(line, `\`) + "\n")
		}
	}

	return document.String(), index
}

// execFormScript returns the script of a RUN instruction written in the JSON
// (exec) form: the argument of `sh -c`, or the words themselves. A shell-form
// instruction is returned unchanged.
func execFormScript(command string) string {
	var argv []string
	if json.Unmarshal([]byte(command), &argv) != nil {
		return command
	}

	if len(argv) == 3 && argv[1] == "-c" {
		return argv[2]
	}

	return strings.Join(argv, " ")
}

// classify names the ecosystem a simple command builds, and whether it is a
// build or only fetches dependencies; the last result is false when the
// command is neither. Leading VAR=value assignments are skipped, and the tool
// is matched by its file name, so ./mvnw is mvnw.
func classify(words []string) (projecttype.Type, bool, bool) {
	for index := range words {
		words[index] = strings.Trim(words[index], `"'()`)
	}

	for len(words) > 0 && strings.Contains(words[0], "=") {
		words = words[1:]
	}

	if len(words) < 2 {
		return "", false, false
	}

	name, args := path.Base(words[0]), words[1:]

	for _, tool := range buildTools {
		if !slices.Contains(tool.names, name) {
			continue
		}

		if tool.matches(tool.builds, args) {
			return tool.ecosystem, true, true
		}

		if tool.matches(tool.dependencies, args) {
			return tool.ecosystem, false, true
		}
	}

	return "", false, false
}

// matches reports whether args carry one of the phrases.
func (t buildTool) matches(phrases [][]string, args []string) bool {
	var plain []string

	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}

		// A Gradle task may carry its project path, :app:build.
		if strings.HasPrefix(arg, ":") {
			arg = arg[strings.LastIndex(arg, ":")+1:]
		}

		plain = append(plain, arg)
	}

	for _, phrase := range phrases {
		if t.anywhere && slices.Contains(plain, phrase[0]) {
			return true
		}

		if !t.anywhere && len(plain) >= len(phrase) && slices.Equal(plain[:len(phrase)], phrase) {
			return true
		}
	}

	return false
}
