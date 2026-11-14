// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package platform

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// DependencyInput admits one dependency from a forge's serialized needs object.
// Job is a local ID; Forgejo may qualify it with reusable-workflow namespaces.
type DependencyInput struct {
	NeedsJSON          string
	Job                string
	Results            []string
	Output             string
	AllowMissingOutput bool
}

// Dependency resolves exactly one job, checks its result and returns either a
// canonical JSON object or a single-line string output. Wrapper success without
// the requested output is never treated as evidence of producer completion.
func Dependency(in DependencyInput) (string, error) {
	if in.Job == "" || strings.ContainsAny(in.Job, ".\r\n\t ") || len(in.NeedsJSON) > 1<<20 {
		return "", fmt.Errorf("dependency requires a local job ID and bounded needs JSON: %w", errs.ErrUsage)
	}

	if in.AllowMissingOutput && in.Output == "" {
		return "", fmt.Errorf("allow-missing-output requires output: %w", errs.ErrUsage)
	}

	results, err := dependencyResults(in.Results)
	if err != nil {
		return "", err
	}

	job, err := selectDependency(in.NeedsJSON, in.Job)
	if err != nil {
		return "", err
	}

	var result string
	if err := json.Unmarshal(job["result"], &result); err != nil {
		return "", fmt.Errorf("dependency %q lacks a string result: %w", in.Job, errs.ErrValidation)
	}

	if !slices.Contains(results, result) {
		return "", fmt.Errorf("dependency %q result %q is not admitted: %w", in.Job, result, errs.ErrValidation)
	}

	if raw := job["outputs"]; !absentJSON(raw) {
		if _, err := dependencyObject(raw); err != nil {
			return "", err
		}
	}

	if in.Output == "" {
		body, err := json.Marshal(job)

		return string(body), err
	}

	return dependencyOutput(job, in)
}

// dependencyResults is the admitted result set: success unless the caller
// names others, each one a result the forges report.
func dependencyResults(results []string) ([]string, error) {
	if len(results) == 0 {
		return []string{"success"}, nil
	}

	for _, result := range results {
		switch result {
		case "success", "failure", "cancelled", "skipped":
		default:
			return nil, fmt.Errorf("unsupported dependency result %q: %w", result, errs.ErrUsage)
		}
	}

	return results, nil
}

// selectDependency returns the one needs entry for the job, matched by its
// local ID or as the last segment of a namespaced one.
func selectDependency(needsJSON, id string) (map[string]json.RawMessage, error) {
	needs, err := dependencyObject([]byte(needsJSON))
	if err != nil {
		return nil, err
	}

	var selected json.RawMessage

	count := 0

	for key, value := range needs {
		if key == id || strings.HasSuffix(key, "."+id) {
			selected = value
			count++
		}
	}

	if count != 1 {
		return nil, fmt.Errorf("expected exactly one dependency %q, found %d: %w", id, count, errs.ErrValidation)
	}

	return dependencyObject(selected)
}

// dependencyOutput is the job's requested output: a single-line string, or
// empty when the caller allows it to be missing.
func dependencyOutput(job map[string]json.RawMessage, in DependencyInput) (string, error) {
	outputs, err := dependencyObject(job["outputs"])
	if err != nil {
		if in.AllowMissingOutput && absentJSON(job["outputs"]) {
			return "", nil
		}

		return "", err
	}

	raw := outputs[in.Output]
	if absentJSON(raw) {
		if in.AllowMissingOutput {
			return "", nil
		}

		return "", fmt.Errorf("dependency %q is missing output %q: %w", in.Job, in.Output, errs.ErrMissingInput)
	}

	var value string
	if err := json.Unmarshal(raw, &value); err != nil || strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("dependency output %q must be a single-line string: %w", in.Output, errs.ErrValidation)
	}

	if value == "" && !in.AllowMissingOutput {
		return "", fmt.Errorf("dependency %q has empty output %q: %w", in.Job, in.Output, errs.ErrMissingInput)
	}

	return value, nil
}

// absentJSON reports a value the forge left out or serialized as null.
func absentJSON(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

// dependencyObject rejects duplicate coordinates/fields rather than accepting
// encoding/json's last-value-wins behavior at a workflow admission boundary.
func dependencyObject(body []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))

	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, fmt.Errorf("dependency data must be a JSON object: %w", errs.ErrValidation)
	}

	values := map[string]json.RawMessage{}

	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, err
		}

		name, ok := key.(string)
		if !ok {
			return nil, fmt.Errorf("dependency field must be a string: %w", errs.ErrValidation)
		}

		if _, exists := values[name]; exists {
			return nil, fmt.Errorf("duplicate dependency field %q: %w", name, errs.ErrValidation)
		}

		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}

		values[name] = value
	}

	if _, err := decoder.Token(); err != nil {
		return nil, err
	}

	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("dependency data has trailing content: %w", errs.ErrValidation)
	}

	return values, nil
}
