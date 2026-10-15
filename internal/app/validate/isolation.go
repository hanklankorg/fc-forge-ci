// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/clicolor"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/validate"
	"gopkg.in/yaml.v3"
)

// IsolationInput drives Isolation.
type IsolationInput struct {
	// Workflow is the path to the workflow file to check (relative to FS when
	// FS is set, else an OS path).
	Workflow string
	// BuildJob is the artifact-producing job that must not see signing secrets.
	BuildJob string
	// SignJob is the cross-repo signing workflow call-site job. Empty keeps only
	// the generic release-isolation checks enabled.
	SignJob string
	// PrepareJob is the job that produces release identity outputs and may run
	// fail-fast secret presence checks before checkout.
	PrepareJob string
	// DistDigestOutput is the build job output that feeds the signer's dist-digest
	// input.
	DistDigestOutput string
	// SigningSecrets are the secret names forbidden in BuildJob.
	SigningSecrets []string
	// SinglePinSubject enables directory-wide pin consistency for uses refs that
	// contain this subject (for example, "example/provider-workflows"). Empty disables
	// the check.
	SinglePinSubject string
	// SinglePinInput names the input through which the subject's reusable
	// workflows receive their commit a second time. Every job in the workflow
	// directory that calls one of them must pass it as a literal commit, and
	// the value joins the pins that must agree. Empty checks uses: pins only.
	// It needs SinglePinSubject.
	SinglePinInput string
	// LiteralInputs names inputs of the subject's reusable workflows that a
	// caller must write out: every job in the workflow directory that calls
	// one of them and passes such an input must pass a literal, never an
	// expression. Each value is NAME or NAME=SHAPE, where SHAPE (sha256,
	// image-digest, or enum:VALUE/VALUE) also holds the literal to that form.
	// It needs SinglePinSubject.
	LiteralInputs []string
	// ContractRoot is the trusted checkout of SinglePinSubject at the pin
	// used by the caller. Its action/workflow declarations admit with: keys.
	// Empty keeps standalone isolation checks independent of a layer checkout.
	ContractRoot string
	// FS overrides filesystem access for tests. Nil -> the real OS filesystem.
	FS fs.FS
}

// Isolation asserts selected static release-isolation invariants: the
// build job references no signing secret, and every actions/checkout step sets
// persist-credentials: false. Violations are emitted as error annotations
// (GitHub: `::error file=…,line=…::`; elsewhere a plain `Error:` line) and the
// call returns a wrapped errs.ErrValidation. Sibling checks concern local header
// and lexical input-reference hints only, never runtime digest verification or
// signing order.
func Isolation(out io.Writer, annot output.Annotator, in IsolationInput) error { //nolint:varnamelen // idiomatic short names.
	literalInputs, err := subjectInputs(in)
	if err != nil {
		return err
	}

	data, err := readWorkflow(in)
	if err != nil {
		return err
	}

	violations, err := validate.CheckIsolation(data, validate.IsolationConfig{
		BuildJob:         in.BuildJob,
		SignJob:          in.SignJob,
		PrepareJob:       in.PrepareJob,
		DistDigestOutput: in.DistDigestOutput,
		SigningSecrets:   in.SigningSecrets,
	})
	if err != nil {
		return fmt.Errorf("check isolation %s: %w: %w", in.Workflow, err, errs.ErrMalformedInput)
	}

	violations = append(violations, singlePinViolations(in)...)
	violations = append(violations, literalInputViolations(in, literalInputs)...)
	violations = append(violations, declaredInputViolations(in)...)
	siblingViolations, siblingChecked := siblingContractViolations(in, data)
	violations = append(violations, siblingViolations...)

	if in.SignJob != "" {
		if siblingChecked {
			_, _ = fmt.Fprintln(out, "Local sibling header/input-reference hints checked (lexical only, not proof of input consumption); checkout is not verified against the workflow pin.")
		} else {
			_, _ = fmt.Fprintln(out, "Sibling signer static contract not checked: local workflow unavailable or malformed.")
		}

		_, _ = fmt.Fprintln(out, "Digest verification before signing is not checked; the owner of the pinned signing workflow must enforce the runtime verifier and ordering.")
	}

	for _, v := range violations {
		annot.ErrorAt(output.Annotation{File: in.Workflow, Line: v.Line}, "%s", v.Msg)
	}

	if len(violations) > 0 {
		return fmt.Errorf("isolation validation failed for %s: %w", in.Workflow, errs.ErrValidation)
	}

	if in.ContractRoot != "" {
		_, _ = fmt.Fprintf(out, "Action and workflow inputs checked against the supplied layer checkout %s\n", in.ContractRoot)
	}

	_, _ = fmt.Fprintf(out, "%s Static release-isolation checks passed in %s\n", clicolor.Check(out), in.Workflow)

	return nil
}

// subjectInputs refuses a flag that names inputs of the subject's workflows
// when no subject is given, and returns the parsed literal inputs.
func subjectInputs(in IsolationInput) ([]literalInput, error) {
	if in.ContractRoot != "" && in.SinglePinSubject == "" {
		return nil, fmt.Errorf("--contract-root requires --single-pin-subject: %w", errs.ErrUsage)
	}

	if in.SinglePinInput != "" && in.SinglePinSubject == "" {
		return nil, fmt.Errorf("--single-pin-input names an input of the --single-pin-subject workflows, and no subject is given: %w", errs.ErrUsage)
	}

	if len(in.LiteralInputs) > 0 && in.SinglePinSubject == "" {
		return nil, fmt.Errorf("--literal-input names inputs of the --single-pin-subject workflows, and no subject is given: %w", errs.ErrUsage)
	}

	return parseLiteralInputs(in.LiteralInputs)
}

//nolint:cyclop // Scans the two supported pin spellings and reports each independent consistency failure.
func singlePinViolations(in IsolationInput) []validate.IsolationViolation {
	if in.SinglePinSubject == "" {
		return nil
	}

	files, err := workflowDirYAMLFiles(in)
	if err != nil {
		return []validate.IsolationViolation{{Line: 1, Msg: err.Error()}}
	}

	shas := map[string]struct{}{}

	var violations []validate.IsolationViolation

	for _, body := range files {
		usesValues, err := yamlUsesValues([]byte(body))
		if err != nil {
			return []validate.IsolationViolation{{Line: 1, Msg: err.Error()}}
		}

		for _, uses := range usesValues {
			if sha := pinnedSubjectSHA(uses, in.SinglePinSubject); sha != "" {
				shas[sha] = struct{}{}
			}
		}

		inputPins, inputViolations := subjectCallInputPins([]byte(body), in.SinglePinSubject, in.SinglePinInput)
		violations = append(violations, inputViolations...)

		for _, sha := range inputPins {
			shas[sha] = struct{}{}
		}
	}

	if len(violations) > 0 {
		return violations
	}

	// A subject that matches nothing is not a workflow directory with one
	// consistent pin -- it is a check that ran over nothing and said so in the
	// same words. A stale or misspelled subject silently disabled this entirely.
	if len(shas) == 0 {
		return []validate.IsolationViolation{{
			Line: 1,
			Msg: fmt.Sprintf(
				"no %s pin found under workflow directory: the single-pin subject matches nothing, so pin consistency was not checked",
				in.SinglePinSubject),
		}}
	}

	if len(shas) == 1 {
		return nil
	}

	vals := make([]string, 0, len(shas))
	for sha := range shas {
		vals = append(vals, sha)
	}

	slices.Sort(vals)

	return []validate.IsolationViolation{{
		Line: 1,
		Msg:  fmt.Sprintf("mixed %s pins under workflow directory: %s", in.SinglePinSubject, strings.Join(vals, ", ")),
	}}
}

// subjectCallInputPins returns the commit each job passes as input to a
// reusable workflow of subject, and a violation for every such job that does
// not pass a literal commit.
//
// The name is the caller's, not a guess from the subject. Deriving it from the
// subject's repository name ("<repo>-helpers-sha") skipped this half of the
// check whenever the serving repository was not named like the input: for
// every fork that renamed the repository, and for a lab snapshot. A job that
// calls the subject without the input is refused for the same reason: an
// absent or computed value is a second pin nothing compared.
func subjectCallInputPins(body []byte, subject, input string) ([]string, []validate.IsolationViolation) {
	if input == "" {
		return nil, nil
	}

	var doc struct {
		Jobs map[string]struct {
			Uses yaml.Node            `yaml:"uses"`
			With map[string]yaml.Node `yaml:"with"`
		} `yaml:"jobs"`
	}

	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, []validate.IsolationViolation{{Line: 1, Msg: fmt.Sprintf("parse workflow YAML: %v", err)}}
	}

	names := make([]string, 0, len(doc.Jobs))
	for name := range doc.Jobs {
		names = append(names, name)
	}

	slices.Sort(names)

	var (
		pins       []string
		violations []validate.IsolationViolation
	)

	for _, name := range names {
		job := doc.Jobs[name]

		uses, ok := resolvedScalar(&job.Uses)
		if !ok || !referencesSubject(uses, subject) {
			continue
		}

		value := job.With[input]
		if sha, ok := resolvedScalar(&value); ok && isSHA1(sha) {
			pins = append(pins, sha)

			continue
		}

		violations = append(violations, validate.IsolationViolation{
			Line: job.Uses.Line,
			Msg: fmt.Sprintf("job %q calls %s without a literal commit in its %s input: pass the commit of its uses: pin, written out",
				name, subject, input),
		})
	}

	return pins, violations
}

func siblingContractViolations(in IsolationInput, workflowYAML []byte) ([]validate.IsolationViolation, bool) {
	if in.SignJob == "" {
		return nil, false
	}

	uses := signJobUses(workflowYAML, in.SignJob)
	if uses == "" {
		return nil, false
	}

	siblingPath := siblingWorkflowPath(in.Workflow, uses)
	if siblingPath == "" {
		return nil, false
	}

	body, ok, err := readOptionalIsolationFile(in, siblingPath)
	if err != nil {
		return []validate.IsolationViolation{{Line: 1, Msg: err.Error()}}, false
	}

	if !ok {
		return nil, false
	}

	violations := missingContractSecrets(string(body), in.SigningSecrets)

	scalars, err := yamlScalarValues(body)
	if err != nil {
		return append(violations, validate.IsolationViolation{Line: 1, Msg: fmt.Sprintf("parse sibling signer workflow %s: %v", siblingPath, err)}), false
	}

	if !signerMentionsDistDigest(scalars) {
		violations = append(violations, validate.IsolationViolation{Line: 1, Msg: "sibling signer workflow has no literal dist-digest input reference (lexical hint only; consumption is not checked)"})
	}

	return violations, true
}

// missingContractSecrets reports enforced signing secrets absent from the
// sibling signer's secrets-contract header (when such a header is declared).
func missingContractSecrets(body string, signingSecrets []string) []validate.IsolationViolation {
	declared := secretsContractHeader(body)
	if len(declared) == 0 {
		return nil
	}

	var violations []validate.IsolationViolation

	for _, secret := range signingSecrets {
		if secret == "" {
			continue
		}

		if _, ok := declared[secret]; !ok {
			violations = append(violations, validate.IsolationViolation{
				Line: 1,
				Msg:  fmt.Sprintf("gate enforces %s but sibling signer secrets-contract header does not list it", secret),
			})
		}
	}

	return violations
}

// signerMentionsDistDigest is only a lexical hint. A name or description can
// satisfy it; neither this nor verifier-looking text establishes execution.
func signerMentionsDistDigest(scalars []string) bool {
	for _, text := range scalars {
		if strings.Contains(text, "inputs.dist-digest") ||
			strings.Contains(text, "inputs['dist-digest']") ||
			strings.Contains(text, `inputs["dist-digest"]`) {
			return true
		}
	}

	return false
}

func workflowDirYAMLFiles(in IsolationInput) ([]string, error) {
	dir := workflowDir(in.Workflow)
	if in.FS != nil {
		entries, err := fs.ReadDir(in.FS, dir)
		if err != nil {
			return nil, fmt.Errorf("read workflow directory %s: %w", dir, err)
		}

		return readYAMLFilesFS(in.FS, dir, entries)
	}

	entries, err := os.ReadDir(filepath.FromSlash(dir))
	if err != nil {
		return nil, fmt.Errorf("read workflow directory %s: %w", dir, err)
	}

	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !isYAMLFile(entry.Name()) {
			continue
		}

		data, err := os.ReadFile(filepath.Join(filepath.FromSlash(dir), entry.Name())) //nolint:gosec // workflow directory under caller-provided repo path.
		if err != nil {
			return nil, fmt.Errorf("read workflow file %s: %w", entry.Name(), err)
		}

		files = append(files, string(data))
	}

	return files, nil
}

func readYAMLFilesFS(fsys fs.FS, dir string, entries []fs.DirEntry) ([]string, error) {
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !isYAMLFile(entry.Name()) {
			continue
		}

		data, err := fs.ReadFile(fsys, path.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read workflow file %s: %w", entry.Name(), err)
		}

		files = append(files, string(data))
	}

	return files, nil
}

func readOptionalIsolationFile(in IsolationInput, name string) ([]byte, bool, error) {
	if in.FS != nil {
		data, err := fs.ReadFile(in.FS, name)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, false, nil
			}

			return nil, false, fmt.Errorf("read sibling signer workflow %s: %w", name, err)
		}

		return data, true, nil
	}

	data, err := os.ReadFile(filepath.FromSlash(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}

		return nil, false, fmt.Errorf("read sibling signer workflow %s: %w", name, err)
	}

	return data, true, nil
}

func signJobUses(workflowYAML []byte, signJob string) string {
	var doc struct {
		Jobs map[string]yaml.Node `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(workflowYAML, &doc); err != nil {
		return ""
	}

	job, ok := doc.Jobs[signJob]
	if !ok || job.Kind != yaml.MappingNode {
		return ""
	}

	for i := 0; i+1 < len(job.Content); i += 2 {
		if job.Content[i].Value == "uses" && job.Content[i+1].Kind == yaml.ScalarNode {
			return job.Content[i+1].Value
		}
	}

	return ""
}

func siblingWorkflowPath(workflow, uses string) string {
	refIdx := strings.LastIndex(uses, "@")
	if refIdx < 0 {
		return ""
	}

	pathPart := uses[:refIdx]
	if schemeIdx := strings.Index(pathPart, "://"); schemeIdx >= 0 {
		afterScheme := pathPart[schemeIdx+3:]

		slashIdx := strings.Index(afterScheme, "/")
		if slashIdx < 0 {
			return ""
		}

		pathPart = afterScheme[slashIdx+1:]
	}

	parts := strings.Split(pathPart, "/")
	if len(parts) < 3 {
		return ""
	}

	repo := parts[1]
	subpath := path.Join(parts[2:]...)
	repoRoot := path.Dir(path.Dir(workflowDir(workflow)))

	return path.Join(path.Dir(repoRoot), repo, subpath)
}

func workflowDir(workflow string) string {
	cleaned := filepath.ToSlash(strings.TrimPrefix(workflow, "./"))

	return path.Dir(cleaned)
}

func isYAMLFile(name string) bool {
	return strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")
}

func secretsContractHeader(body string) map[string]struct{} {
	for line := range strings.SplitSeq(body, "\n") {
		contract, ok := strings.CutPrefix(line, "# workflow-call-secrets-contract:")
		if !ok {
			continue
		}

		declared := map[string]struct{}{}
		for _, field := range strings.Fields(contract) {
			declared[field] = struct{}{}
		}

		return declared
	}

	return nil
}

func readWorkflow(in IsolationInput) ([]byte, error) {
	if in.FS != nil {
		data, err := fs.ReadFile(in.FS, in.Workflow)
		if err != nil {
			return nil, workflowReadError(in.Workflow, err)
		}

		return data, nil
	}

	data, err := os.ReadFile(in.Workflow)
	if err != nil {
		return nil, workflowReadError(in.Workflow, err)
	}

	return data, nil
}
