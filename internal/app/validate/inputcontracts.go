// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/validate"
)

type isolationCall struct {
	Uses string               `yaml:"uses"`
	With map[string]yaml.Node `yaml:"with"`
}

// Read only the checkout supplied by the caller. A consumer file with the
// same name is not the pinned layer's declaration and must not admit inputs.
func declaredInputViolations(in IsolationInput) []validate.IsolationViolation {
	if in.ContractRoot == "" {
		return nil
	}

	files, err := workflowDirYAMLFiles(in)
	if err != nil {
		return []validate.IsolationViolation{{Line: 1, Msg: err.Error()}}
	}

	var violations []validate.IsolationViolation

	for _, body := range files {
		var document struct {
			Jobs map[string]struct {
				isolationCall `yaml:",inline"`
				Steps         []isolationCall `yaml:"steps"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal([]byte(body), &document); err != nil {
			violations = append(violations, validate.IsolationViolation{Line: 1, Msg: fmt.Sprintf("parse caller inputs: %v", err)})

			continue
		}

		for _, name := range slices.Sorted(maps.Keys(document.Jobs)) {
			job := document.Jobs[name]
			calls := append([]isolationCall{job.isolationCall}, job.Steps...)

			for _, call := range calls {
				violations = append(violations, callInputViolations(in, name, call)...)
			}
		}
	}

	return violations
}

func callInputViolations(in IsolationInput, job string, call isolationCall) []validate.IsolationViolation {
	end := subjectEnd(call.Uses, in.SinglePinSubject)
	if end < 0 {
		return nil
	}

	relative, pin, _ := strings.Cut(strings.TrimPrefix(call.Uses[end:], "/"), "@")
	if !isSHA1(pin) || !validLayerContractPath(relative) {
		return []validate.IsolationViolation{{Line: 1, Msg: fmt.Sprintf("job %q has no supported pinned layer declaration for %s", job, call.Uses)}}
	}

	workflow := strings.Contains(relative, "/workflows/")
	if !workflow {
		relative = path.Join(relative, "action.yml")
	}

	inputs, err := contractInputNames(in, relative, workflow)
	if err != nil {
		return []validate.IsolationViolation{{Line: 1, Msg: fmt.Sprintf("job %q: %v", job, err)}}
	}

	var violations []validate.IsolationViolation

	for _, name := range slices.Sorted(maps.Keys(call.With)) {
		if _, declared := inputs[name]; !declared {
			violations = append(violations, validate.IsolationViolation{
				Line: call.With[name].Line,
				Msg:  fmt.Sprintf("job %q passes undeclared input %q to %s", job, name, call.Uses),
			})
		}
	}

	return violations
}

func validLayerContractPath(relative string) bool {
	parts := strings.Split(relative, "/")

	return len(parts) == 3 && path.Clean(relative) == relative && !strings.ContainsAny(relative, "\\\x00") &&
		slices.Contains([]string{".forgejo", ".github", ".gitea"}, parts[0]) &&
		slices.Contains([]string{"actions", "workflows"}, parts[1]) && parts[2] != ""
}

func contractInputNames(in IsolationInput, relative string, workflow bool) (map[string]any, error) {
	name := path.Join(in.ContractRoot, relative)
	read := os.ReadFile

	if in.FS != nil {
		read = func(name string) ([]byte, error) { return fs.ReadFile(in.FS, name) }
	}

	body, err := read(filepath.FromSlash(name))
	if err != nil {
		return nil, fmt.Errorf("read pinned declaration %s: %w", name, err)
	}

	var declaration struct {
		Inputs map[string]any       `yaml:"inputs"`
		On     map[string]yaml.Node `yaml:"on"`
	}
	if err := yaml.Unmarshal(body, &declaration); err != nil {
		return nil, fmt.Errorf("parse pinned declaration %s: %w", name, err)
	}

	if !workflow {
		return declaration.Inputs, nil
	}

	call, found := declaration.On["workflow_call"]
	if !found {
		return nil, fmt.Errorf("pinned declaration %s has no workflow_call: %w", name, errs.ErrMalformedInput)
	}

	var contract struct {
		Inputs map[string]any `yaml:"inputs"`
	}
	if err := call.Decode(&contract); err != nil {
		return nil, fmt.Errorf("parse workflow_call in %s: %w", name, err)
	}

	return contract.Inputs, nil
}
