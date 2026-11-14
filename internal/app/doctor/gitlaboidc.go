// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package doctor

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// Without a detected provider, infer only an unambiguous GitLab-only layout.
func useGitLabOIDCCheck(root string, forge provider.ForgeAPI) bool {
	_, gitLabErr := os.Stat(filepath.Join(root, ".gitlab-ci.yml"))
	hasActions := false

	for _, rel := range actionsWorkflowDirRels() {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			hasActions = true
		}
	}

	return provider.UsesGitLabCIConfig(forge, gitLabErr == nil, hasActions)
}

func gitLabOIDCUnknown(message string) Check {
	return Check{
		Name: checkWorkflowIDToken, Severity: SeverityWarn, Message: message,
		Remediation: "inspect the merged GitLab CI configuration and the signing job's id_tokens; GitLab supplies the token at runtime, so do not commit a token value",
	}
}

// This checks local declarations only, not GitLab's merged pipeline or job execution.
func checkGitLabOIDC(root string) Check {
	body, err := os.ReadFile(filepath.Join(root, ".gitlab-ci.yml")) //nolint:gosec // operator-selected repository root.
	if err != nil {
		return gitLabOIDCUnknown("could not read .gitlab-ci.yml; a custom CI configuration path or external policy cannot be checked locally")
	}

	rootNode, valid := gitLabPipelineRoot(body)
	if !valid {
		return Check{
			Name: checkWorkflowIDToken, Severity: SeverityFail,
			Message:     "could not parse .gitlab-ci.yml as a pipeline or a spec header followed by a pipeline",
			Remediation: "correct .gitlab-ci.yml using GitLab CI Lint, then rerun doctor",
		}
	}

	if yamlChild(rootNode, "include") != nil || gitLabNeedsEvaluation(rootNode) {
		return gitLabOIDCUnknown("GitLab includes, YAML inheritance or input expressions prevent a complete local OIDC check; no external configuration was fetched")
	}

	if yamlChild(yamlChild(rootNode, "default"), "id_tokens") != nil {
		return gitLabOIDCUnknown("GitLab default id_tokens require per-job inheritance evaluation, which this local check does not perform")
	}

	found, unresolved := gitLabLocalTokens(rootNode)
	if unresolved {
		return gitLabOIDCUnknown("GitLab job inheritance, child pipelines or custom token variables require checking the effective signing job")
	}

	if found {
		return Check{
			Name: checkWorkflowIDToken, Severity: SeverityOK,
			Message: "a local GitLab job declares id_tokens.SIGSTORE_ID_TOKEN with an audience; job execution, audience suitability and token use were not verified",
		}
	}

	return Check{
		Name: checkWorkflowIDToken, Severity: SeverityFail,
		Message:     "sign.method=sigstore but no local GitLab job declares id_tokens.SIGSTORE_ID_TOKEN with an audience",
		Remediation: "declare id_tokens: { SIGSTORE_ID_TOKEN: { aud: sigstore } } on the signing job, using the audience your signing service requires; GitLab creates the token at runtime, so do not commit a token value",
	}
}

func gitLabLocalTokens(root *yaml.Node) (bool, bool) {
	found, unresolved := false, false

	for index := 0; index+1 < len(root.Content); index += 2 {
		declared, unknown := gitLabJobOIDC(root.Content[index].Value, root.Content[index+1])
		found = found || declared
		unresolved = unresolved || unknown
	}

	return found, unresolved
}

// Returns whether a concrete job declares the token, and whether its token
// configuration needs GitLab's pipeline evaluation or script interpretation.
func gitLabJobOIDC(name string, job *yaml.Node) (bool, bool) {
	if strings.HasPrefix(name, ".") || job.Kind != yaml.MappingNode {
		return false, false
	}

	if yamlChild(job, "extends") != nil || yamlChild(job, "trigger") != nil {
		return false, true
	}

	if yamlChild(job, "script") == nil && yamlChild(job, "run") == nil {
		return false, false
	}

	tokens := yamlChild(job, "id_tokens")
	token := yamlChild(tokens, "SIGSTORE_ID_TOKEN")

	return gitLabTokenHasAudience(token), tokens != nil && token == nil
}

func gitLabTokenHasAudience(token *yaml.Node) bool {
	audience := yamlChild(token, "aud")
	if audience == nil {
		return false
	}

	if audience.Kind == yaml.ScalarNode {
		return audience.Tag == "!!str" && strings.TrimSpace(audience.Value) != ""
	}

	if audience.Kind != yaml.SequenceNode || len(audience.Content) == 0 {
		return false
	}

	for _, value := range audience.Content {
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || strings.TrimSpace(value.Value) == "" {
			return false
		}
	}

	return true
}

// GitLab accepts a pipeline mapping, optionally preceded by a separate spec
// header. Decode the whole supported structure so later jobs or malformed
// trailing documents cannot silently disappear from the check.
func gitLabPipelineRoot(body []byte) (*yaml.Node, bool) {
	decoder := yaml.NewDecoder(bytes.NewReader(body))

	var first yaml.Node
	if err := decoder.Decode(&first); err != nil || !gitLabDocumentIsMapping(&first) {
		return nil, false
	}

	var pipeline yaml.Node
	switch err := decoder.Decode(&pipeline); {
	case errors.Is(err, io.EOF):
		return first.Content[0], yamlChild(first.Content[0], "spec") == nil
	case err != nil || !gitLabDocumentIsMapping(&pipeline):
		return nil, false
	}

	header := first.Content[0]
	if len(header.Content) != 2 || header.Content[0].Value != "spec" || header.Content[1].Kind != yaml.MappingNode {
		return nil, false
	}

	var trailing yaml.Node

	return pipeline.Content[0], errors.Is(decoder.Decode(&trailing), io.EOF)
}

func gitLabDocumentIsMapping(document *yaml.Node) bool {
	return len(document.Content) == 1 && document.Content[0].Kind == yaml.MappingNode
}

func gitLabNeedsEvaluation(node *yaml.Node) bool {
	if node.Kind == yaml.AliasNode || node.Tag == "!!merge" {
		return true
	}

	if node.Kind == yaml.ScalarNode && strings.Contains(node.Value, "$[[") {
		return true
	}

	return slices.ContainsFunc(node.Content, gitLabNeedsEvaluation)
}
