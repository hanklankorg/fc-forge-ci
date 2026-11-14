// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistrySmokeDirectBuildPreservesTheRetiredActionsContract(t *testing.T) {
	t.Parallel()

	for _, base := range []bool{false, true} {
		name, title, description, image, scan := "Build and push registry smoke image", "layer-registry-signer-smoke", "Disposable registry and signer acceptance image", "", "trivy-smoke-{arch}.json"
		if base {
			name, title, description, image, scan = "Build and push registry smoke base image", "layer-registry-signer-smoke-base", "Disposable base-image acceptance image", "registry.example/owner/base", "trivy-smoke-base-{arch}.json"
		}

		step := jobStep(t, "test/live/conformance/testdata/layer/registry-signer-smoke.yml", "build", name)
		box := newSandbox(t, "registry-smoke")
		box.write("bin/forge-ci", fakeEngine, 0o700)

		result := box.run(strings.ReplaceAll(step.Run, "RENDER_PLATFORM", "linux/amd64"),
			"PATH="+filepath.Join(box.root, "bin")+":"+os.Getenv("PATH"), "FORGE_CI_PROVIDER=forgejo",
			"IMAGE_TAG=v1.2.3", "IMAGE_NAME="+image, "REGISTRY_AUTH_FILE=/private/auth file.json",
			"SERVER_URL=https://forge.example", "REPOSITORY=owner/app", "RUNNER_TEMP=/runner-temp")
		if result.status != 0 {
			t.Fatalf("direct smoke build: %s", result.stderr)
		}

		argv := box.argv()
		if len(argv) < 2 || argv[0] != "container" || argv[1] != "build-push-oci-image" || len(argv)%2 != 0 {
			t.Fatalf("wrong engine invocation: %q", argv)
		}

		flags := map[string]string{}
		for index := 2; index < len(argv); index += 2 {
			flags[argv[index]] = argv[index+1]
		}

		want := map[string]string{"--tag": "v1.2.3", "--image": image, "--containerfile": "Containerfile",
			"--builds-json": `[{"platform":"linux/amd64","build-args":[]}]`, "--title": title,
			"--description": description, "--licenses": "CC0-1.0", "--context": ".", "--vendor": "", "--authors": "",
			"--documentation": "", "--ref-name": "", "--version": "", "--revision": "", "--tls-verify": "true",
			"--auth-file": "/private/auth file.json", "--server-url": "https://forge.example", "--repository": "owner/app",
			// Publication always scans.
			"--scan-output-template": "/runner-temp/" + scan}
		if !maps.Equal(flags, want) {
			t.Fatalf("smoke build flags = %q, want %q", flags, want)
		}
	}
}
