{{ range .Versions -}}

## {{ .Tag.Name | trimPrefix "v" }} - {{ datetime "2006-01-02" .Tag.Date }}

{{ range .CommitGroups -}}

### {{ .Title }}

{{ range .Commits -}}
{{- if not (or (eq .Scope "release") (eq .Scope "deps") (hasPrefix .Scope "deps-") (eq .Scope "pr") (eq .Scope "pull")) -}}

- {{ if .Notes }}[**breaking**] {{ end }}{{ .Subject }}
{{ end -}}
{{ end }}
{{ end -}}
{{ end -}}
