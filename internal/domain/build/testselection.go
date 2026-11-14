// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// GoTestSelection is an administrator-owned native selection, not extra argv.
// Race detection and uncached execution cannot be disabled by the selection.
// Gate classifies the whole single invocation; mixed policy/application suites
// must be classified as policy rather than waiving their policy assertions.
type GoTestSelection struct {
	Packages []string `json:"packages"`
	Run      string   `json:"run,omitempty"`
	Gate     TestGate `json:"gate"`
}

// CargoTestSelection lists individual native executables. Grouped selectors are
// intentionally unsupported: an early failed harness must not hide a later one.
type CargoTestSelection struct {
	Runs []CargoTestTarget `json:"runs"`
}

// CargoTestTarget identifies one native executable and its enforcement class.
type CargoTestTarget struct {
	Kind    string   `json:"kind"`
	Package string   `json:"package"`
	Name    string   `json:"name"`
	Gate    TestGate `json:"gate"`
	// Ignored explicitly selects libtest's ignored tests instead of its defaults.
	Ignored bool `json:"ignored,omitempty"`
}

// TestGate is owned by the administrator's selection, not the product name.
// Policy checks always block; only application assertions can be report-only.
type TestGate string

// Native test gates distinguish waivable application assertions from policy.
const (
	ApplicationTestGate TestGate = "application"
	PolicyTestGate      TestGate = "policy"
)

func decodeTestSelection(body string, target any) error {
	if err := uniqueSelectionMembers(json.NewDecoder(strings.NewReader(body))); err != nil {
		return fmt.Errorf("test selection: %s: %w", err.Error(), errs.ErrUsage)
	}

	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("test selection: %s: %w", err.Error(), errs.ErrUsage)
	}

	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("test selection requires one JSON object: %w", errs.ErrUsage)
	}

	return nil
}

var (
	errInvalidSelectionMember = errors.New("duplicate or invalid member")
	errNullSelectionValue     = errors.New("null is not a typed selection value")
)

// Selection JSON must not silently overwrite a gate or executable identity.
func uniqueSelectionMembers(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}

	switch token {
	case json.Delim('{'):
		var seen []string

		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}

			name, ok := key.(string)
			if !ok {
				return fmt.Errorf("%w %q", errInvalidSelectionMember, key)
			}

			for _, previous := range seen {
				// Match encoding/json's Unicode folding, including long s.
				if strings.EqualFold(previous, name) {
					return fmt.Errorf("%w %q", errInvalidSelectionMember, key)
				}
			}

			seen = append(seen, name)

			if err := uniqueSelectionMembers(decoder); err != nil {
				return err
			}
		}

		_, err = decoder.Token()
	case json.Delim('['):
		for decoder.More() {
			if err := uniqueSelectionMembers(decoder); err != nil {
				return err
			}
		}

		_, err = decoder.Token()
	case nil:
		return errNullSelectionValue
	}

	return err
}

// ParseGoTestSelection accepts only workspace-relative package patterns and an
// optional native test-name regexp. Empty/missing selections are not opt-outs.
func ParseGoTestSelection(body string) (GoTestSelection, error) {
	var selection GoTestSelection
	if err := decodeTestSelection(body, &selection); err != nil {
		return selection, err
	}

	if len(selection.Packages) == 0 {
		return selection, fmt.Errorf("test selection requires packages: %w", errs.ErrUsage)
	}

	if selection.Gate != ApplicationTestGate && selection.Gate != PolicyTestGate {
		return selection, fmt.Errorf("test selection requires application or policy gate: %w", errs.ErrUsage)
	}

	for _, pkg := range selection.Packages {
		if pkg != "." && !strings.HasPrefix(pkg, "./") || strings.ContainsAny(pkg, "\t\r\n \\@") || strings.Contains("/"+pkg+"/", "/../") || path.Clean(pkg) == ".." {
			return selection, fmt.Errorf("invalid workspace test package %q: %w", pkg, errs.ErrUsage)
		}
	}

	if _, err := regexp.Compile(selection.Run); err != nil {
		return selection, fmt.Errorf("invalid test regexp: %s: %w", err.Error(), errs.ErrUsage)
	}

	return selection, nil
}

var cargoTestName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]*$`)

// ParseCargoTestSelection deliberately has no all-targets, command or extra-args
// escape hatch. Every entry names one bin or integration-test executable and
// explicitly declares its enforcement class.
func ParseCargoTestSelection(body string) (CargoTestSelection, error) {
	var selection CargoTestSelection
	if err := decodeTestSelection(body, &selection); err != nil {
		return selection, err
	}

	if len(selection.Runs) == 0 {
		return selection, fmt.Errorf("test selection requires runs: %w", errs.ErrUsage)
	}

	seen := map[string]bool{}

	for _, run := range selection.Runs {
		valid := (run.Kind == "package-bin" || run.Kind == "package-test") &&
			cargoTestName.MatchString(run.Package) && cargoTestName.MatchString(run.Name) &&
			(run.Gate == ApplicationTestGate || run.Gate == PolicyTestGate)
		if !valid {
			return selection, fmt.Errorf("invalid Cargo test target %+v: %w", run, errs.ErrUsage)
		}

		key := run.Package + "/" + run.Kind + "/" + run.Name
		if seen[key] {
			return selection, fmt.Errorf("duplicate Cargo test executable %q: %w", key, errs.ErrUsage)
		}

		seen[key] = true
	}

	return selection, nil
}

// Args is fixed native argv, never interpreted by a shell.
func (target CargoTestTarget) Args() []string {
	args := []string{"test", "--locked"}

	args = append(args, "--package", target.Package)
	switch target.Kind {
	case "package-bin":
		args = append(args, "--bin", target.Name)
	default:
		args = append(args, "--test", target.Name)
	}

	if target.Ignored {
		args = append(args, "--", "--ignored")
	}

	return args
}
