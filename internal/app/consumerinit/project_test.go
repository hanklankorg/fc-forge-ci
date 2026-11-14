// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package consumerinit_test

import (
	"testing"
	"testing/fstest"

	appinit "github.com/diggsweden/forge-ci/internal/app/consumerinit"
	domain "github.com/diggsweden/forge-ci/internal/domain/consumerinit"
)

func TestDetectProjectReadsTheBuildFiles(t *testing.T) {
	t.Parallel()

	file := func(body string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(body)} }

	for name, tc := range map[string]struct {
		fs   fstest.MapFS
		want domain.Project
	}{
		"go main at root": {fstest.MapFS{
			"go.mod": file("module example.com/acme/hello/v2\n\ngo 1.24\n"), "main.go": file("package main\n"),
		}, domain.Project{Name: "hello", MainPackage: "."}},
		"go single cmd": {fstest.MapFS{
			"go.mod": file("module example.com/acme/tool\n"), "lib.go": file("package tool\n"),
			"cmd/tool-cli/main.go": file("// x\npackage main\n"), "cmd/tool-cli/main_test.go": file("package main\n"),
		}, domain.Project{Name: "tool", MainPackage: "./cmd/tool-cli"}},
		"go several cmds, one named after the module": {fstest.MapFS{
			"go.mod":           file("module example.com/acme/tool\n"),
			"cmd/tool/main.go": file("package main\n"), "cmd/gen/main.go": file("package main\n"),
		}, domain.Project{Name: "tool", MainPackage: "./cmd/tool"}},
		"go library": {fstest.MapFS{
			"go.mod": file("module example.com/acme/lib\n"), "lib.go": file("package lib\n"),
		}, domain.Project{Name: "lib"}},
		"npm scoped": {fstest.MapFS{"package.json": file(`{"name": "@acme/web-app"}`)}, domain.Project{Name: "web-app"}},
		"cargo":      {fstest.MapFS{"Cargo.toml": file("[workspace]\nname = \"no\"\n[package]\nname = \"rusty\"\n")}, domain.Project{Name: "rusty"}},
		"maven":      {fstest.MapFS{"pom.xml": file("<project><parent><artifactId>parent</artifactId></parent><artifactId>svc</artifactId></project>")}, domain.Project{Name: "svc"}},
		"gradle kts": {fstest.MapFS{"settings.gradle.kts": file("rootProject.name = \"app\"\n")}, domain.Project{Name: "app"}},
		"nothing":    {fstest.MapFS{"README.md": file("hi")}, domain.Project{}},
	} {
		if got := appinit.DetectProject(tc.fs); got != tc.want {
			t.Errorf("%s: DetectProject = %+v, want %+v", name, got, tc.want)
		}
	}
}
