// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

// The changelog configuration of the consumer that LAYER-4 releases:
// prepare-release.yml renders CHANGELOG.md and the release commit's body with
// git-chglog, from the two configurations its inputs name by default. They
// are the consumer's files, so they are committed with it and kept here as
// text, beside nothing that would read them as this repository's own.
const (
	chglogKeepConfig = `style: ""
template: CHANGELOG-keepachangelog.tpl
info:
  title: CHANGELOG
  repository_url: https://acceptance.invalid/layer-release-canary
options:
  tag_filter_pattern: '^v'
  sort: semver
  commits:
    sort_by: Scope
  commit_groups:
    group_by: Type
    sort_by: Title
    title_order: [Added, Changed, Fixed, Security]
    title_maps:
      feat: Added
      fix: Fixed
      test: Changed
      chore: Changed
  header:
    pattern: "^(\\w*)(?:\\(([\\w$\\.\\-\\*\\s]*)\\))?!?\\:\\s(.*)$"
    pattern_maps: [Type, Scope, Subject]
  notes:
    keywords: [BREAKING CHANGE]
`

	chglogKeepTemplate = `# Changelog

All notable changes to this acceptance fixture will be documented in this file.
{{- if .Unreleased.CommitGroups }}

## [Unreleased]
{{- range .Unreleased.CommitGroups }}

### {{ .Title }}
{{- range .Commits }}
- {{ .Subject }}
{{- end }}
{{- end }}
{{- end }}
{{- range .Versions }}

## [{{ .Tag.Name | trimPrefix "v" }}] - {{ datetime "2006-01-02" .Tag.Date }}
{{- range .CommitGroups }}

### {{ .Title }}
{{- range .Commits }}
- {{ .Subject }}
{{- end }}
{{- end }}
{{- end }}
`

	chglogMinimalConfig = `style: ""
template: CHANGELOG-minimal.tpl
info:
  title: CHANGELOG
  repository_url: https://acceptance.invalid/layer-release-canary
options:
  tag_filter_pattern: '^v'
  sort: semver
  commits:
    sort_by: Scope
  commit_groups:
    group_by: Type
    sort_by: Title
    title_maps:
      feat: Features
      fix: Bug Fixes
      test: Testing
      chore: Other
  header:
    pattern: "^(\\w*)(?:\\(([\\w$\\.\\-\\*\\s]*)\\))?!?\\:\\s(.*)$"
    pattern_maps: [Type, Scope, Subject]
  notes:
    keywords: [BREAKING CHANGE]
`

	chglogMinimalTemplate = `{{ range .Versions -}}

## {{ .Tag.Name | trimPrefix "v" }} - {{ datetime "2006-01-02" .Tag.Date }}
{{ range .CommitGroups -}}

### {{ .Title }}
{{ range .Commits -}}

- {{ if .Notes }}[**breaking**] {{ end }}{{ .Subject }}
{{ end -}}
{{ end }}
{{ end -}}
`
)
