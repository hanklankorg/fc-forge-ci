// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package enforcement

import (
	"fmt"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// MavenTestArguments rejects alternate spellings of the consumer's test opt-out.
// Project code still owns its tests; this prevents the engine's own argument
// surfaces from presenting a skipped/ignored invocation as an enforced check.
func MavenTestArguments(args []string) error {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		property := ""
		switch {
		case arg == "-D" || arg == "--define":
			if index+1 < len(args) {
				index++
				property = args[index]
			}
		case strings.HasPrefix(arg, "-D"):
			property = strings.TrimPrefix(arg, "-D")
		case strings.HasPrefix(arg, "--define="):
			property = strings.TrimPrefix(arg, "--define=")
		}
		name, value, _ := strings.Cut(property, "=")
		switch name {
		case "skipTests", "maven.test.skip", "maven.test.failure.ignore", "skipITs", "skipUTs":
			if value != "false" {
				return fmt.Errorf("consumer Maven option %q disables test execution or failure reporting: %w", name, errs.ErrUsage)
			}
		}
	}
	return nil
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
