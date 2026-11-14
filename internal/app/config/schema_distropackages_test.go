// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	domainconfig "github.com/diggsweden/forge-ci/internal/domain/config"
)

// TestSchemaAndParserAgreeOnDistroPackages holds the published schema and
// the parser to one verdict on the distro-packages block, both ways.
func TestSchemaAndParserAgreeOnDistroPackages(t *testing.T) {
	t.Parallel()

	schema := loadSchema(t)

	for body, valid := range map[string]bool{
		"distro-packages: {}\n":                                                   true,
		"distro-packages:\n  rpm: {group: rocky/el9}\n":                           true,
		"distro-packages:\n  alpine: {branch: v3.22}\n":                           true,
		"distro-packages:\n":                                                      false,
		"distro-packages: ~\n":                                                    false,
		"distro-packages:\n  debian:\n":                                           false,
		"distro-packages:\n  debian: {distribution: ''}\n":                        false,
		"distro-packages:\n  debian: {distribution: 1}\n":                         false,
		"distro-packages:\n  alpine: {branch: " + strings.Repeat("a", 64) + "}\n": true,
		"distro-packages:\n  alpine: {branch: " + strings.Repeat("a", 65) + "}\n": false,
		"distro-packages:\n  rpm: {group: " + strings.Repeat("a", 64) + "/" + strings.Repeat("b", 63) + "}\n": true,
		"distro-packages:\n  rpm: {group: " + strings.Repeat("a", 64) + "/" + strings.Repeat("b", 64) + "}\n": false,
		"distro-packages: []\n":                                   false,
		"distro-packages:\n  publish-to: [forge-packages]\n":      false,
		"distro-packages:\n  debian: {distribution: a/b}\n":       false,
		"distro-packages:\n  rpm: {group: el9/}\n":                false,
		"distro-packages:\n  alpine: {repository: ..}\n":          false,
		"distro-packages:\n  debian: {distribution: \"st\\n\"}\n": false,
		"distro-packages:\n  apt: {}\n":                           false,
	} {
		document := []byte("artifacts:\n  - name: cli\n    project-type: go\n" + body)

		cfg, parseErr := domainconfig.Parse(document)
		if parseErr == nil {
			parseErr = domainconfig.Validate(cfg)
		}

		schemaErr := schema.Validate(yamlToInstance(t, document))

		require.Equal(t, valid, parseErr == nil, "parser on %q: %v", body, parseErr)
		require.Equal(t, valid, schemaErr == nil, "schema on %q: %v", body, schemaErr)
	}
}

// The example an adopter copies is valid for the schema and the parser, and
// names the default places.
func TestDistroPackagesExampleIsValidAndNamesTheDefaults(t *testing.T) {
	t.Parallel()

	document, err := os.ReadFile(filepath.Join(repoRoot(t), "examples", "go-cli", "artifacts-forgejo-packages.yml"))
	require.NoError(t, err)
	require.NoError(t, loadSchema(t).Validate(yamlToInstance(t, document)))

	cfg, err := domainconfig.Parse(document)
	require.NoError(t, err)
	require.NoError(t, domainconfig.Validate(cfg))

	defaults := &domainconfig.DistroPackages{}
	require.Equal(t, defaults.Resolved(), cfg.DistroPackages)
}
