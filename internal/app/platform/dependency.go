// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package platform

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	results := in.Results
	if len(results) == 0 {
		results = []string{"success"}
	}
	for _, result := range results {
		switch result {
		case "success", "failure", "cancelled", "skipped":
		default:
			return "", fmt.Errorf("unsupported dependency result %q: %w", result, errs.ErrUsage)
		}
	}
	needs, err := dependencyObject([]byte(in.NeedsJSON))
	if err != nil {
		return "", err
	}
	var selected json.RawMessage
	count := 0
	for key, value := range needs {
		if key == in.Job || strings.HasSuffix(key, "."+in.Job) {
			selected = value
			count++
		}
	}
	if count != 1 {
		return "", fmt.Errorf("expected exactly one dependency %q, found %d: %w", in.Job, count, errs.ErrValidation)
	}
	job, err := dependencyObject(selected)
	if err != nil {
		return "", err
	}
	var result string
	if err := json.Unmarshal(job["result"], &result); err != nil {
		return "", fmt.Errorf("dependency %q lacks a string result: %w", in.Job, errs.ErrValidation)
	}
	admitted := false
	for _, allowed := range results {
		if result == allowed {
			admitted = true
		}
	}
	if !admitted {
		return "", fmt.Errorf("dependency %q result %q is not admitted: %w", in.Job, result, errs.ErrValidation)
	}
	if raw := job["outputs"]; len(raw) != 0 && string(raw) != "null" {
		if _, err := dependencyObject(raw); err != nil {
			return "", err
		}
	}
	if in.Output == "" {
		body, err := json.Marshal(job)
		return string(body), err
	}
	outputs, err := dependencyObject(job["outputs"])
	if err != nil {
		if in.AllowMissingOutput && (len(job["outputs"]) == 0 || string(job["outputs"]) == "null") {
			return "", nil
		}
		return "", err
	}
	raw, exists := outputs[in.Output]
	if !exists || string(raw) == "null" {
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
