// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"regexp"
	"strconv"
	"strings"
)

// ShortImageName is an image a Containerfile names without a registry host,
// such as eclipse-temurin:21. Which registry such a name is pulled from is
// decided by the registries.conf of the machine that builds, so the same
// Containerfile builds on one runner and fails, or pulls another image, on
// the next.
type ShortImageName struct {
	Line  int    // line the instruction starts on, from 1
	Image string // the name as written, for example "eclipse-temurin@sha256:…"
	// Resolved is the name with its build arguments replaced by their
	// literal defaults. It is Image when the name has none.
	Resolved string
	// Unjudged is true when the name holds a build argument that has no
	// literal default in the file: the caller of the build decides the
	// image, so the file alone does not say whether it names a registry.
	Unjudged bool
}

// DockerHubName returns the full name of the Docker Hub image a short name
// usually means: docker.io/library/<name> for an official image and
// docker.io/<name> for any other.
func (n ShortImageName) DockerHubName() string {
	if strings.Contains(n.Resolved, "/") {
		return "docker.io/" + n.Resolved
	}

	return "docker.io/library/" + n.Resolved
}

// buildArgument finds $NAME, ${NAME} and ${NAME:-default} in an image name.
var buildArgument = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// ContainerfileShortImageNames returns, in file order, the images a
// Containerfile names without a registry host: in FROM, in COPY --from= and
// in the from= of a RUN --mount. The name scratch, an earlier build stage
// and a stage index are no images and are left out. A build argument is
// replaced by its literal default (ARG BASE=…, or ${BASE:-…}); a name whose
// argument has none is returned as unjudged.
func ContainerfileShortImageNames(body string) []ShortImageName {
	var (
		found     []ShortImageName
		stages    = map[string]bool{}
		stage     int
		arguments = map[string]string{}
	)

	for _, ins := range instructions(body) {
		var images []string

		switch ins.keyword {
		case "ARG":
			for _, word := range ins.words {
				name, value, hasDefault := strings.Cut(word, "=")
				if !hasDefault {
					continue // a bare ARG keeps the default it had, if any
				}

				if resolved, ok := resolveArguments(strings.Trim(value, `"'`), arguments); ok {
					arguments[name] = resolved
				} else {
					delete(arguments, name)
				}
			}

			continue
		case instructionFrom:
			if words := withoutOptions(ins.words); len(words) > 0 {
				images = []string{words[0]}
			}
		case instructionCopy, instructionRun:
			images = fromOptions(ins.words)
		default:
			continue
		}

		for _, image := range images {
			if short, ok := shortImageName(ins.line, image, stages, arguments); ok {
				found = append(found, short)
			}
		}

		// A stage is known by its index and, with AS, by its name, to the
		// instructions after its own FROM.
		if ins.keyword == instructionFrom {
			stages[strconv.Itoa(stage)] = true
			stage++

			if words := withoutOptions(ins.words); len(words) == 3 && strings.EqualFold(words[1], "AS") {
				stages[strings.ToLower(words[2])] = true
			}
		}
	}

	return found
}

// shortImageName judges one name. The last result is false for a name that
// is no image, or that names its registry.
func shortImageName(line int, image string, stages map[string]bool, arguments map[string]string) (ShortImageName, bool) {
	image = strings.Trim(image, `"'`)

	resolved, ok := resolveArguments(image, arguments)
	if !ok {
		return ShortImageName{Line: line, Image: image, Resolved: image, Unjudged: true}, true
	}

	if resolved == "" || resolved == "scratch" || stages[strings.ToLower(resolved)] {
		return ShortImageName{}, false
	}

	// The registry is the first part of a name with more than one part, and
	// it is a host: it has a dot or a port, or it is localhost.
	host, _, hasPath := strings.Cut(resolved, "/")
	if hasPath && (strings.ContainsAny(host, ".:") || host == "localhost") {
		return ShortImageName{}, false
	}

	return ShortImageName{Line: line, Image: image, Resolved: resolved}, true
}

// resolveArguments replaces the build arguments of a value by their literal
// defaults. It reports false when one has none.
func resolveArguments(value string, arguments map[string]string) (string, bool) {
	known := true

	resolved := buildArgument.ReplaceAllStringFunc(value, func(match string) string {
		groups := buildArgument.FindStringSubmatch(match)
		name := groups[1] + groups[3]

		if literal, ok := arguments[name]; ok {
			return literal
		}

		// ${NAME:-default} carries its own default.
		if strings.Contains(match, ":-") && !strings.Contains(groups[2], "$") {
			return groups[2]
		}

		known = false

		return match
	})

	return resolved, known
}

// withoutOptions drops the leading --options of an instruction.
func withoutOptions(words []string) []string {
	for len(words) > 0 && strings.HasPrefix(words[0], "--") {
		words = words[1:]
	}

	return words
}

// fromOptions returns the values of --from= and of the from= of a --mount
// among the leading options of a COPY or RUN instruction.
func fromOptions(words []string) []string {
	var values []string

	for _, word := range words {
		if !strings.HasPrefix(word, "--") {
			break
		}

		if value, ok := strings.CutPrefix(word, "--from="); ok {
			values = append(values, value)

			continue
		}

		if mount, ok := strings.CutPrefix(word, "--mount="); ok {
			for field := range strings.SplitSeq(mount, ",") {
				if value, ok := strings.CutPrefix(field, "from="); ok {
					values = append(values, value)
				}
			}
		}
	}

	return values
}
