// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate

import (
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/validate"
)

// The shapes a literal input can be held to. An enum shape lists the values
// it accepts after its prefix, separated by '/': enum:none/public. The
// separator needs no quoting in a shell and is not the one a repeated flag
// splits its values on.
const (
	literalShapeSHA256      = "sha256"
	literalShapeImageDigest = "image-digest"
	literalShapeEnumPrefix  = "enum:"
)

// literalInput is one --literal-input value: the name of an input of the
// subject's reusable workflows and, optionally, the shape its value must have.
type literalInput struct {
	name  string
	shape string
	// allowed holds the values of an enum shape, and is nil for another.
	allowed []string
}

// parseLiteralShape reads SHAPE. An enum lists at least one value, none of
// them empty or holding whitespace, and none twice.
func parseLiteralShape(shape string) ([]string, bool) {
	if shape == "" || shape == literalShapeSHA256 || shape == literalShapeImageDigest {
		return nil, true
	}

	list, isEnum := strings.CutPrefix(shape, literalShapeEnumPrefix)
	if !isEnum {
		return nil, false
	}

	allowed := strings.Split(list, "/")
	for index, value := range allowed {
		if value == "" || strings.ContainsAny(value, " \t\r\n") || slices.Contains(allowed[:index], value) {
			return nil, false
		}
	}

	return allowed, true
}

// parseLiteralInputs reads NAME or NAME=SHAPE values. A name given twice, an
// empty name and an unknown shape are usage errors: a misspelled shape must
// not pass as "any literal".
func parseLiteralInputs(values []string) ([]literalInput, error) {
	inputs := make([]literalInput, 0, len(values))

	for _, value := range values {
		name, shape, _ := strings.Cut(value, "=")

		if name == "" || strings.ContainsAny(name, " \t\r\n") {
			return nil, fmt.Errorf("--literal-input %q names no input: want NAME or NAME=SHAPE: %w", value, errs.ErrUsage)
		}

		if shape == "" && strings.Contains(value, "=") {
			return nil, fmt.Errorf("--literal-input %q names no shape after '=': %w", value, errs.ErrUsage)
		}

		allowed, known := parseLiteralShape(shape)
		if !known {
			return nil, fmt.Errorf("--literal-input %q: shape must be %s, %s or %sVALUE/VALUE: %w",
				value, literalShapeSHA256, literalShapeImageDigest, literalShapeEnumPrefix, errs.ErrUsage)
		}

		if slices.ContainsFunc(inputs, func(seen literalInput) bool { return seen.name == name }) {
			return nil, fmt.Errorf("--literal-input names %s twice: %w", name, errs.ErrUsage)
		}

		inputs = append(inputs, literalInput{name: name, shape: shape, allowed: allowed})
	}

	return inputs, nil
}

// refusal says why value is not an acceptable literal for the input, or ""
// when it is one. An expression is refused whatever it would evaluate to: the
// point of a literal is that the value is in the reviewed file.
func (l literalInput) refusal(value string, scalar bool) string {
	switch {
	case !scalar:
		return "is not a scalar"
	case strings.Contains(value, "${{"):
		return "is an expression"
	case strings.TrimSpace(value) == "":
		return "is empty"
	default:
		return l.shapeRefusal(value)
	}
}

// shapeRefusal says why a written-out value does not have the input's shape,
// or "" when it has, or when the input has none.
func (l literalInput) shapeRefusal(value string) string {
	switch {
	case l.shape == literalShapeSHA256 && !domaincontainer.ValidSHA256Hex(value):
		return "is not a SHA-256 digest of 64 lowercase hex characters"
	case l.shape == literalShapeImageDigest && !domaincontainer.ValidDigestPinnedRef(value):
		return "is not an image reference pinned by digest (registry/path@sha256:<64 hex>)"
	case l.allowed != nil && !slices.Contains(l.allowed, value):
		return "is not one of " + strings.Join(l.allowed, ", ")
	default:
		return ""
	}
}

// literalInputViolations reports every job in the workflow directory that
// calls a reusable workflow of the subject and passes one of the named inputs
// as anything but a literal of its shape.
//
// A job that does not pass the input is not reported: whether the called
// workflow needs it is that workflow's contract, which is not in the caller's
// repository to read, and a required input left out fails there.
func literalInputViolations(in IsolationInput, inputs []literalInput) []validate.IsolationViolation {
	if len(inputs) == 0 {
		return nil
	}

	files, err := workflowDirYAMLFiles(in)
	if err != nil {
		return []validate.IsolationViolation{{Line: 1, Msg: err.Error()}}
	}

	var violations []validate.IsolationViolation

	for _, body := range files {
		violations = append(violations, literalInputViolationsIn([]byte(body), in.SinglePinSubject, inputs)...)
	}

	return violations
}

func literalInputViolationsIn(body []byte, subject string, inputs []literalInput) []validate.IsolationViolation {
	var doc struct {
		Jobs map[string]struct {
			Uses yaml.Node            `yaml:"uses"`
			With map[string]yaml.Node `yaml:"with"`
		} `yaml:"jobs"`
	}

	if err := yaml.Unmarshal(body, &doc); err != nil {
		return []validate.IsolationViolation{{Line: 1, Msg: fmt.Sprintf("parse workflow YAML: %v", err)}}
	}

	names := make([]string, 0, len(doc.Jobs))
	for name := range doc.Jobs {
		names = append(names, name)
	}

	slices.Sort(names)

	var violations []validate.IsolationViolation

	for _, name := range names {
		job := doc.Jobs[name]

		uses, ok := resolvedScalar(&job.Uses)
		if !ok || !referencesSubject(uses, subject) {
			continue
		}

		for _, input := range inputs {
			node, passed := job.With[input.name]
			if !passed {
				continue
			}

			value, scalar := resolvedScalar(&node)
			if why := input.refusal(value, scalar); why != "" {
				violations = append(violations, validate.IsolationViolation{
					Line: node.Line,
					Msg: fmt.Sprintf("job %q passes %s to %s as a value that %s: write the value out in this file, so that it changes only by a reviewed commit",
						name, input.name, subject, why),
				})
			}
		}
	}

	return violations
}
