// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/publish"
)

func TestNPMPackedEntrypoint(t *testing.T) {
	for _, tc := range []struct{ metadata, want string }{
		{`{}`, "dist/cli.js"},
		{`{"bin":{"cli":"another.js"},"main":"src/index.js","exports":42}`, "dist/cli.js"},
		{`{"bin":null,"main":"src/index.js"}`, "src/index.js"},
		{`{"exports":null,"main":"src/index.js"}`, "src/index.js"},
		{`{"exports":"./src/index.js","main":"missing.js"}`, "./src/index.js"},
		{`{"exports":"./src/with space.js"}`, "./src/with space.js"},
	} {
		got, err := publish.NPMPackedEntrypoint([]byte(tc.metadata))
		if err != nil || got != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.metadata, got, err, tc.want)
		}
	}
	if got, err := publish.NPMPackedEntrypoint(nil); err != nil || got != "dist/cli.js" {
		t.Fatalf("absent metadata: %q, %v", got, err)
	}
}

func TestNPMPackedEntrypointRejectsUnsupportedMetadata(t *testing.T) {
	for _, body := range []string{
		``, `{`, `[]`, `{"main":42}`,
		`{"main":"src/index.js","exports":{}}`,
		`{"main":"src/index.js","exports":{ ".":"./missing.js"}}`,
		`{"main":"src/index.js","exports":["./src/index.js"]}`,
		`{"main":"src/index.js","exports":42}`,
		`{"main":"src/index.js","exports":true}`,
		`{"main":"src/index.js","exports":""}`,
		`{"main":"../outside.js"}`, `{"main":"/outside.js"}`, `{"main":"."}`,
	} {
		if _, err := publish.NPMPackedEntrypoint([]byte(body)); !errors.Is(err, errs.ErrValidation) {
			t.Errorf("accepted unsupported metadata %q: %v", body, err)
		}
	}
}

func TestNPMPackedEntrypointRejectsNoncanonicalExports(t *testing.T) {
	for _, target := range []string{
		"src/index.js", "../index.js", "/index.js", "./", "./src/", "./src//index.js",
		"./src/./index.js", "./src/../index.js", "./node_modules/index.js", "./src/Node_Modules/index.js",
		"./src/%69ndex.js", "./src/index.js?query", "./src/index.js#fragment", `./src\index.js`,
		"./src/index.js ", "./src/index.js\t", "./src/index.js\r", "./src/index.js\n", "./src/index.js\x01",
		"./src/in\x00dex.js", "./src/in\x1bdex.js", "./src/in\u0085dex.js",
	} {
		body, err := json.Marshal(map[string]string{"exports": target, "main": "src/index.js"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := publish.NPMPackedEntrypoint(body); !errors.Is(err, errs.ErrValidation) {
			t.Errorf("accepted noncanonical export %q: %v", target, err)
		}
	}
}
