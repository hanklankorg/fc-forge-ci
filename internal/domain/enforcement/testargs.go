// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package enforcement

import (
	"fmt"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// MavenTestArguments rejects the engine-argument spellings that skip, ignore
// or narrow a consumer's tests. Project code still owns its tests (its POM and
// .mvn/maven.config are out of reach); this keeps the engine's own argument
// surfaces from presenting a skipped, ignored or narrowed invocation as an
// enforced check.
//
// Maven's command-line parser takes a short or long option name after one
// dash or two (-fn, --fn, -fail-never, --fail-never), a value after "=" or
// as the next argument, and a -D property attached to the option name. It
// matches no abbreviations, but refusing them costs nothing should a later
// parser accept them.
func MavenTestArguments(args []string) error {
	for index := 0; index < len(args); index++ {
		arg := args[index]

		option, dashed := strings.CutPrefix(arg, "-")
		if !dashed {
			continue
		}

		dashes := 1
		if trimmed, ok := strings.CutPrefix(option, "-"); ok {
			option, dashes = trimmed, 2
		}

		name, attached, hasAttached := strings.Cut(option, "=")
		property := ""

		switch {
		case mavenOptionIs(name, dashes, "fn", "fail-never", len("fail-n")):
			// Maven exits 0 in fail-never mode however its tests end.
			return fmt.Errorf("consumer Maven option %q ignores build failures, failed tests included: %w", arg, errs.ErrUsage)
		case mavenOptionIs(name, dashes, "D", "define", len("def")):
			switch {
			case hasAttached:
				property = attached
			case index+1 < len(args):
				index++
				property = args[index]
			}
		case strings.HasPrefix(option, "D"):
			property = strings.TrimPrefix(option, "D")
		default:
			continue
		}

		if err := mavenTestProperty(property); err != nil {
			return err
		}
	}

	return nil
}

func mavenTestProperty(property string) error {
	name, value, _ := strings.Cut(property, "=")

	switch name {
	case "skipTests", "maven.test.skip", "maven.test.skip.exec", "maven.test.failure.ignore", "skipITs", "skipUTs":
		if value != "false" {
			return fmt.Errorf("consumer Maven option %q disables test execution or failure reporting: %w", name, errs.ErrUsage)
		}
	case "surefire.failIfNoSpecifiedTests", "it.failIfNoSpecifiedTests", "failIfNoTests":
		// The strict setting is Maven's default; only relaxing it narrows.
		if value != "true" {
			return fmt.Errorf("consumer Maven option %q narrows which tests run: %w", name, errs.ErrUsage)
		}
	case "test", "it.test", "groups", "excludedGroups",
		"surefire.includesFile", "surefire.excludesFile", "failsafe.includesFile", "failsafe.excludesFile":
		return fmt.Errorf("consumer Maven option %q narrows which tests run: %w", name, errs.ErrUsage)
	case "surefire.rerunFailingTestsCount", "failsafe.rerunFailingTestsCount":
		// A test that fails and then passes on a rerun is reported as flaky
		// and the build succeeds.
		if value != "" && value != "0" {
			return fmt.Errorf("consumer Maven option %q passes a failed test that passes on a rerun: %w", name, errs.ErrUsage)
		}
	}

	return nil
}

// mavenOptionIs reports whether name, given after the number of dashes, is
// the option's short or long spelling, or an abbreviation of its long one.
func mavenOptionIs(name string, dashes int, short, long string, minimum int) bool {
	return name == short || name == long || (dashes == 2 && abbreviates(name, long, minimum))
}

// abbreviates reports whether name is option, or a prefix of it at least
// minimum characters long: the shortest prefix no other Maven option shares.
func abbreviates(name, option string, minimum int) bool {
	return len(name) >= minimum && strings.HasPrefix(option, name)
}

// GradleTestArguments refuses task exclusions at the engine's test/build gate.
func GradleTestArguments(args []string) error {
	for _, arg := range args {
		if arg == "-x" || strings.HasPrefix(arg, "-x") || arg == "--exclude-task" || strings.HasPrefix(arg, "--exclude-task=") {
			return fmt.Errorf("consumer Gradle task exclusions cannot disable administrator-required tests: %w", errs.ErrUsage)
		}
	}

	return nil
}
