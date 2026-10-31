// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/container"
)

// NPMApplicationFixtureConfig is trusted CI policy, never application source.
// Oracle runs in the pinned Node job against an installed retained tarball.
type NPMApplicationFixtureConfig struct {
	RecipeDirectory, NodeImage, Oracle, Branch string
	// Library admits a public package's main/exports entry instead of a CLI bin.
	Library bool
}

//go:embed testdata/npm-application-development.yml
var npmApplicationDevelopment string

func NewNPMApplicationFixture(tb TB, source Consumer, config NPMApplicationFixtureConfig, development bool) Consumer {
	tb.Helper()
	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()
	result, err := npmApplicationFixture(ctx, source, config, development)
	if err != nil {
		tb.Fatalf("npm application fixture: %v", err)
	}
	return result
}

func npmApplicationFixture(ctx context.Context, source Consumer, c NPMApplicationFixtureConfig, development bool) (Consumer, error) {
	if !filepath.IsAbs(c.RecipeDirectory) || !container.ValidDigestReference(c.NodeImage) || strings.ContainsAny(c.NodeImage, "\r\n '\"") {
		return source, fmt.Errorf("require absolute native recipe directory and digest-pinned Node image")
	}
	body, err := githubWorkflow(ctx, source, "package.json")
	if err != nil {
		return source, err
	}
	var model struct {
		Name, Version string
		Bin           json.RawMessage
		Main          string
		Exports       json.RawMessage
		Private       bool
	}
	if err = json.Unmarshal([]byte(body), &model); err != nil {
		return source, err
	}
	if !regexp.MustCompile(`^(@[a-z0-9._-]+/)?[a-z0-9][a-z0-9._-]*$`).MatchString(model.Name) || model.Private {
		return source, fmt.Errorf("require publishable npm metadata with a package name")
	}
	if c.Library {
		if development || (model.Main == "" && (len(model.Exports) == 0 || string(model.Exports) == "null")) {
			return source, fmt.Errorf("library fixture requires a stable package with a main or exports entry")
		}
	} else if len(model.Bin) == 0 || string(model.Bin) == "null" {
		return source, fmt.Errorf("require npm application metadata with a bin")
	}
	if !filepath.IsAbs(c.Oracle) {
		return source, fmt.Errorf("require absolute installed-package oracle script")
	}
	info, err := os.Lstat(c.Oracle)
	if err != nil || !info.Mode().IsRegular() {
		return source, fmt.Errorf("oracle must be a regular CI policy file")
	}
	oracle, err := os.ReadFile(c.Oracle)
	if err != nil {
		return source, err
	}
	recipe, err := os.ReadFile(filepath.Join(c.RecipeDirectory, ".forgejo/workflows/release.yml"))
	if err != nil {
		return source, err
	}
	pin, err := ReadConsumerPin(map[string]string{"release.yml": string(recipe)})
	if err != nil {
		return source, err
	}
	workflow := string(recipe)
	if development {
		if _, err = source.git.run(ctx, source.stage, "check-ref-format", "refs/heads/"+c.Branch); err != nil || c.Branch == "main" || c.Branch == "" {
			return source, fmt.Errorf("require source branch different from main")
		}
		if _, err = githubWorkflow(ctx, source, "Containerfile"); err != nil {
			return source, err
		}
		workflow = strings.NewReplacer("@ACTIONS@", pin.Actions, "@WORKFLOWS@", pin.Workflows, "@PIN@", pin.Commit, "@IMAGE@", c.NodeImage, "@BRANCH@", "'"+strings.ReplaceAll(c.Branch, "'", "''")+"'").Replace(npmApplicationDevelopment)
	} else {
		// Reuse native preparation, artifact handoff and signing. This variation
		// publishes the application package independently of container publication.
		start, end := strings.Index(workflow, "  release-images:\n"), strings.Index(workflow, "  handoff:\n")
		buildStart, buildEnd := strings.Index(workflow, "      - uses: "+pin.Actions+"setup-toolchain@"), strings.Index(workflow, "      - uses: "+pin.Actions+"upload-dist@")
		if start < 0 || end <= start || buildStart < 0 || buildEnd <= buildStart {
			return source, fmt.Errorf("native npm recipe structure changed")
		}
		workflow = workflow[:start] + workflow[end:]
		workflow = workflow[:buildStart] + npmApplicationBuildSteps + workflow[buildEnd:]
		workflow = strings.NewReplacer("@IMAGE@", c.NodeImage, "publish-container-images: 'true'", "publish-container-images: 'false'", "needs: [build, release-images]", "needs: [build]", "prepare, build, release-images, handoff", "prepare, build, handoff", "          IMAGES_RESULT: ${{ needs.release-images.result }}\n", "", `--job "Images=${IMAGES_RESULT}" `, "", "          forge-ci artifact download --name release-images --dir dist\n", "", "      release-images-path: dist/release-images.json\n", "").Replace(workflow)
		workflow = strings.Replace(workflow, "      # The default request already matches package.json; select version-bump", "      version-bump: npm\n      version-bump-image: "+c.NodeImage+"\n      # The default request already matches package.json; select version-bump", 1)
		workflow = strings.Replace(workflow, "    env:\n      ARTIFACT_NAME:", "    container:\n      image: "+c.NodeImage+"\n    env:\n      ARTIFACT_NAME:", 1)
		workflow += strings.NewReplacer("@ACTIONS@", pin.Actions, "@PIN@", pin.Commit, "@IMAGE@", c.NodeImage).Replace(npmApplicationStableConsumers)
	}
	if _, err = source.git.run(ctx, source.stage, "read-tree", source.Commit); err != nil {
		return source, err
	}
	name := ".forgejo/workflows/release.yml"
	if development {
		name = ".forgejo/workflows/development.yml"
	}
	files := map[string]string{name: workflow, ".forge-ci/npm-oracle.sh": string(oracle), ".forge-ci/npm-application.yml": "artifacts:\n  - name: application\n    project-type: npm\n    working-directory: .\ncontainers:\n  - name: application\n    container-file: Containerfile\n    context: .\n    platforms: linux/amd64\n    enable-scan: false\n    enable-slsa: false\n"}
	if !development {
		for _, path := range []string{".chglog/config-keepachangelog.yml", ".chglog/config-minimal.yml", ".chglog/CHANGELOG-keepachangelog.tpl", ".chglog/CHANGELOG-minimal.tpl"} {
			text, readErr := os.ReadFile(filepath.Join(c.RecipeDirectory, path))
			if readErr != nil {
				return source, readErr
			}
			files[path] = string(text)
		}
	}
	// Snapshot planning must see a container-only declaration: npm's original
	// lifecycle runs in the checked-package job after the original source image.
	if development {
		files[".forge-ci/npm-application.yml"] = strings.SplitN(files[".forge-ci/npm-application.yml"], "containers:", 2)[1]
		files[".forge-ci/npm-application.yml"] = "containers:" + files[".forge-ci/npm-application.yml"]
	}
	for path, text := range files {
		if err = source.stageFile(ctx, "100644", path, text); err != nil {
			return source, err
		}
	}
	source.Commit, err = source.commit(ctx, consumerBaseMessage)
	return source, err
}

const npmApplicationBuildSteps = `      - name: Build original npm application
        env:
          RELEASE_TAG: ${{ needs.prepare.outputs.release-tag }}
          NODE_VERSION: '24.17.0'
          CYCLONEDX_VERSION: '6.0.0'
        run: forge-ci build npm run --expect-version "${RELEASE_TAG#v}"
      - name: Retain and install the package
        env:
          RELEASE_TAG: ${{ needs.prepare.outputs.release-tag }}
          RELEASE_SHA: ${{ needs.prepare.outputs.release-sha }}
        run: |
          set -euo pipefail
          mkdir -p retained-package dist
          cp -- ./*.tgz retained-package/
          node --input-type=module -e 'import fs from "node:fs"; const p=JSON.parse(fs.readFileSync("package.json")); fs.writeFileSync("retained-package/package-metadata.json", JSON.stringify({name:p.name,version:p.version}));'
          (cd retained-package && sha256sum ./*.tgz > SHA256SUMS)
          forge-ci artifact upload --name retained-npm-package --dir retained-package --if-no-files error
          mkdir -p oracle-policy
          cp -- .forge-ci/npm-oracle.sh oracle-policy/
          forge-ci artifact upload --name npm-oracle-policy --dir oracle-policy --if-no-files error
          cp -- retained-package/*.tgz dist/
          cp -- bom.json dist/application.cdx.json
          forge-ci release notes --source-file /nonexistent --target-file dist/release-notes.md --release-version "${RELEASE_TAG}" --release-commit "${RELEASE_SHA}"
          forge-ci release files declare --asset 'dist/*.tgz' --sbom dist/application.cdx.json
`

// Neither consumer checks out project code. Signing completes before either
// consumer starts, so a registry/oracle finding cannot block signed files.
const npmApplicationStableConsumers = `  installed-oracle:
    needs: sign-and-publish
    runs-on: ${{ fromJSON(vars.CI_RUNNER_LABEL) }}
    container:
      image: @IMAGE@
    steps:
      - uses: @ACTIONS@setup-forge-ci@@PIN@
      - run: forge-ci artifact download --name retained-npm-package --dir retained-package
      - run: forge-ci artifact download --name npm-oracle-policy --dir oracle-policy
      - run: bash oracle-policy/npm-oracle.sh "$PWD/retained-package"
  publish-package:
    needs: [prepare, sign-and-publish]
    runs-on: ${{ fromJSON(vars.CI_RUNNER_LABEL) }}
    container:
      image: @IMAGE@
    steps:
      - uses: @ACTIONS@setup-forge-ci@@PIN@
      - run: forge-ci artifact download --name retained-npm-package --dir retained-package
      - name: Validate retained bytes and metadata without project code
        env:
          EXPECTED_VERSION: ${{ needs.prepare.outputs.release-tag }}
        run: |
          set -euo pipefail
          cd retained-package
          sha256sum -c SHA256SUMS
          node --input-type=module -e 'import fs from "node:fs"; const p=JSON.parse(fs.readFileSync("package-metadata.json")); if(p.version!==process.env.EXPECTED_VERSION.replace(/^v/,"")) throw Error("release version mismatch");'
          mkdir verification
          cp -- ./*.tgz verification/
          (cd verification && forge-ci publish npm validate-tarball)
          node --input-type=module -e 'import fs from "node:fs"; const a=JSON.parse(fs.readFileSync("package-metadata.json")),b=JSON.parse(fs.readFileSync("verification/package.json")); if(a.name!==b.name||a.version!==b.version) throw Error("packed metadata mismatch");'
          rm -rf verification
          sha256sum -c SHA256SUMS
      - name: Publish retained bytes with version-specific tag
        env:
          NPM_REGISTRY: ${{ vars.NPM_APPLICATION_REGISTRY }}
          NODE_AUTH_TOKEN: ${{ secrets.RELEASE_TOKEN }}
          # Forgejo 16's version parser recognizes release-<version> as a
          # version and rejects it as a dist-tag, after storing the package.
          NPM_TAG: 'release.{version}'
        run: |
          set -euo pipefail
          set +x
          unset FORGEJO_TOKEN GITHUB_TOKEN GH_TOKEN RELEASE_TOKEN RELEASE_SSH_SIGNING_KEY RELEASE_COSIGN_PRIVATE_KEY RELEASE_GPG_PRIVATE_KEY
          cd retained-package
          export HOME=$(mktemp -d)
          trap 'rm -rf "$HOME"' EXIT
          export npm_config_cache="$HOME/cache"
          forge-ci publish npm write-npmrc --registry "$NPM_REGISTRY" --output "$HOME/.npmrc"
          export npm_config_userconfig="$HOME/.npmrc"
          name=$(node --input-type=module -e 'import fs from "node:fs"; console.log(JSON.parse(fs.readFileSync("package-metadata.json")).name)')
          version=$(node --input-type=module -e 'import fs from "node:fs"; console.log(JSON.parse(fs.readFileSync("package-metadata.json")).version)')
          forge-ci publish npm validate-tag --version "$version"
          forge-ci publish npm validate-version --name "$name" --version "$version" --registry "$NPM_REGISTRY"
          forge-ci publish npm deploy --working-dir . --registry "$NPM_REGISTRY" --npm-tag "$NPM_TAG"
          sha256sum -c SHA256SUMS
`
