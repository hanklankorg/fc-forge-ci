# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
{{- if .Unreleased.CommitGroups }}

## [Unreleased]
{{- $lastTitle := "" }}
{{- range .Unreleased.CommitGroups }}
{{- if ne .Title $lastTitle }}

### {{ .Title }}
{{- $lastTitle = .Title }}
{{- end }}
{{- range .Commits -}}
{{- if not (or (eq .Scope "release") (eq .Scope "deps") (hasPrefix .Scope "deps-") (eq .Scope "pr") (eq .Scope "pull")) }}
- {{ .Subject }}
{{- end -}}
{{- end }}
{{- end }}
{{- end }}
{{- range .Versions }}

## [{{ .Tag.Name | trimPrefix "v" }}] - {{ datetime "2006-01-02" .Tag.Date }}
{{- $lastTitle := "" }}
{{- range .CommitGroups }}
{{- if ne .Title $lastTitle }}

### {{ .Title }}
{{- $lastTitle = .Title }}
{{- end }}
{{- range .Commits -}}
{{- if not (or (eq .Scope "release") (eq .Scope "deps") (hasPrefix .Scope "deps-") (eq .Scope "pr") (eq .Scope "pull")) }}
- {{ .Subject }}
{{- end -}}
{{- end }}
{{- end }}
{{- if .NoteGroups }}
{{ range .NoteGroups }}
### {{ .Title }}
{{ range .Notes }}
{{ .Body }}
{{ end }}
{{- end }}
{{- end }}
{{- end }}
{{- if .Versions }}

[Unreleased]: {{ $.Info.RepositoryURL }}/compare/{{ (index .Versions 0).Tag.Name }}...HEAD
{{- range .Versions }}
{{- if .Tag.Previous }}
[{{ .Tag.Name | trimPrefix "v" }}]: {{ $.Info.RepositoryURL }}/compare/{{ .Tag.Previous.Name }}...{{ .Tag.Name }}
{{- else }}
[{{ .Tag.Name | trimPrefix "v" }}]: {{ $.Info.RepositoryURL }}/releases/tag/{{ .Tag.Name }}
{{- end }}
{{- end }}
{{- end }}
