// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	domainpublish "github.com/diggsweden/forge-ci/internal/domain/publish"
)

var (
	errNPMRecipeOrImage    = errors.New("require absolute native recipe directory and digest-pinned Node image")
	errNPMPublishableName  = errors.New("require publishable npm metadata with a package name")
	errNPMLibraryEntry     = errors.New("library fixture requires a stable package with a main or exports entry")
	errNPMApplicationBin   = errors.New("require npm application metadata with a bin")
	errNPMOracleRelative   = errors.New("require absolute installed-package oracle script")
	errNPMOracleNotRegular = errors.New("oracle must be a regular CI policy file")
	errNPMSourceBranch     = errors.New("require source branch different from main")
	errNPMRecipeStructure  = errors.New("native npm recipe structure changed")
)

// NPMApplicationFixtureConfig is trusted CI policy, never application source.
// Oracle runs in the pinned Node job against an installed retained tarball.
type NPMApplicationFixtureConfig struct {
	RecipeDirectory, NodeImage, Oracle, Branch string
	// Library admits a public package's main/exports entry instead of a CLI bin.
	Library bool
}

//go:embed testdata/npm-application-development.yml.tmpl
var npmApplicationDevelopment string

// NewNPMApplicationFixture overlays the native npm recipe, or its development
// variation, in Consumer's private index and commits it.
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

func npmApplicationFixture(ctx context.Context, source Consumer, config NPMApplicationFixtureConfig, development bool) (Consumer, error) {
	if !filepath.IsAbs(config.RecipeDirectory) || !container.ValidDigestReference(config.NodeImage) || strings.ContainsAny(config.NodeImage, "\r\n '\"") {
		return source, errNPMRecipeOrImage
	}

	if err := validateNPMPackage(ctx, source, config.Library, development); err != nil {
		return source, err
	}

	oracle, err := readNPMOracle(config.Oracle)
	if err != nil {
		return source, err
	}

	recipe, err := os.ReadFile(filepath.Join(config.RecipeDirectory, forgejoReleaseWorkflow))
	if err != nil {
		return source, err
	}

	pin, err := ReadConsumerPin(map[string]string{trialReleaseWorkflow: string(recipe)})
	if err != nil {
		return source, err
	}

	workflow := string(recipe)

	if development {
		workflow, err = npmDevelopmentWorkflow(ctx, source, config, pin)
	} else {
		workflow, err = npmStableReleaseWorkflow(workflow, pin, config.NodeImage)
	}

	if err != nil {
		return source, err
	}

	if _, err = source.git.run(ctx, source.stage, "read-tree", source.Commit); err != nil {
		return source, err
	}

	files, err := npmFixtureFiles(config.RecipeDirectory, workflow, oracle, development)
	if err != nil {
		return source, err
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
          CYCLONEDX_VERSION: '6.0.1'
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

// validateNPMPackage admits only publishable package metadata with a bin, or,
// for a library, a stable main or exports entry.
func validateNPMPackage(ctx context.Context, source Consumer, library, development bool) error {
	body, err := githubWorkflow(ctx, source, "package.json")
	if err != nil {
		return err
	}

	var model struct {
		Name    string          `json:"name"`
		Version string          `json:"version"`
		Bin     json.RawMessage `json:"bin"`
		Main    string          `json:"main"`
		Exports json.RawMessage `json:"exports"`
		Private bool            `json:"private"`
	}
	if err = json.Unmarshal([]byte(body), &model); err != nil {
		return err
	}

	if !regexp.MustCompile(`^(@[a-z0-9._-]+/)?[a-z0-9][a-z0-9._-]*$`).MatchString(model.Name) || model.Private {
		return errNPMPublishableName
	}

	if !library {
		if len(model.Bin) == 0 || string(model.Bin) == "null" {
			return errNPMApplicationBin
		}

		return nil
	}

	if development || (model.Main == "" && (len(model.Exports) == 0 || string(model.Exports) == "null")) {
		return errNPMLibraryEntry
	}

	if _, err := domainpublish.NPMPackedEntrypoint([]byte(body)); err != nil {
		return fmt.Errorf("library fixture packed metadata: %w", err)
	}

	return nil
}

// readNPMOracle reads the operator-supplied installed-package oracle script.
func readNPMOracle(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errNPMOracleRelative
	}

	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errNPMOracleNotRegular
	}

	return os.ReadFile(path) //nolint:gosec // operator-supplied oracle, checked above as an absolute regular file.
}

// npmDevelopmentWorkflow renders the development recipe for a source branch
// other than main that carries a Containerfile.
func npmDevelopmentWorkflow(ctx context.Context, source Consumer, config NPMApplicationFixtureConfig, pin ConsumerPin) (string, error) {
	if _, err := source.git.run(ctx, source.stage, "check-ref-format", "refs/heads/"+config.Branch); err != nil || config.Branch == defaultBranch || config.Branch == "" {
		return "", errNPMSourceBranch
	}

	if _, err := githubWorkflow(ctx, source, "Containerfile"); err != nil {
		return "", err
	}

	return strings.NewReplacer("@ACTIONS@", pin.Actions, "@WORKFLOWS@", pin.Workflows, "@PIN@", pin.Commit, "@IMAGE@", config.NodeImage, "@BRANCH@", "'"+strings.ReplaceAll(config.Branch, "'", "''")+"'").Replace(npmApplicationDevelopment), nil
}

// npmStableReleaseWorkflow rewrites the native release recipe so it publishes
// the application package independently of container publication, reusing
// native preparation, artifact handoff and signing.
func npmStableReleaseWorkflow(workflow string, pin ConsumerPin, image string) (string, error) {
	// Reject ambiguous mapping keys before rendering. Counting the expected
	// value alone would miss a duplicate runtime input with a different value.
	var document map[string]any
	if err := yaml.Unmarshal([]byte(workflow), &document); err != nil {
		return "", errNPMRecipeStructure
	}

	// Reuse native preparation, artifact handoff and signing. This variation
	// publishes the application package independently of container publication.
	start, end := strings.Index(workflow, "  release-images:\n"), strings.Index(workflow, "  handoff:\n")

	buildStart, buildEnd := strings.Index(workflow, "      - uses: "+pin.Actions+"setup-toolchain@"), strings.Index(workflow, "      - uses: "+pin.Actions+"upload-dist@")
	// The recipe's last job publishes its own package to the forge's
	// registry; this variation's publish-package, appended below, takes its
	// place.
	ownPublish := strings.Index(workflow, "  publish-package:\n")
	if start < 0 || end <= start || buildStart < 0 || buildEnd <= buildStart || ownPublish <= end {
		return "", errNPMRecipeStructure
	}

	workflow = workflow[:ownPublish]
	workflow = workflow[:start] + workflow[end:]
	workflow = workflow[:buildStart] + npmApplicationBuildSteps + workflow[buildEnd:]
	workflow = strings.NewReplacer("@IMAGE@", image, "publish-container-images: 'true'", "publish-container-images: 'false'", "needs: [build, release-images]", "needs: [build]", "prepare, build, release-images, handoff", "prepare, build, handoff", "          IMAGES_RESULT: ${{ needs.release-images.result }}\n", "", `--job "Images=${IMAGES_RESULT}" `, "", "          forge-ci artifact download --name release-images --dir dist\n", "", "      release-images-path: dist/release-images.json\n", "").Replace(workflow)
	// The plan's npm bump runs in the image the fixture builds with.
	const runtimeImageInput = "      runtime-image-node: ${{ vars.CI_JOB_IMAGE }}\n"
	if strings.Count(workflow, runtimeImageInput) != 1 {
		return "", errNPMRecipeStructure
	}

	workflow = strings.Replace(workflow, runtimeImageInput, "      runtime-image-node: "+image+"\n", 1)
	workflow = strings.Replace(workflow, "    env:\n      ARTIFACT_NAME:", "    container:\n      image: "+image+"\n    env:\n      ARTIFACT_NAME:", 1)
	workflow += strings.NewReplacer("@ACTIONS@", pin.Actions, "@PIN@", pin.Commit, "@IMAGE@", image).Replace(npmApplicationStableConsumers)

	return workflow, nil
}

// npmFixtureFiles is the CI overlay: the rendered workflow, the oracle and the
// declaration, plus the recipe's changelog inputs for a stable release.
func npmFixtureFiles(recipeDirectory, workflow string, oracle []byte, development bool) (map[string]string, error) {
	name := forgejoReleaseWorkflow
	if development {
		name = forgejoDevelopmentWorkflow
	}

	files := map[string]string{name: workflow, ".forge-ci/npm-oracle.sh": string(oracle), ".forge-ci/npm-application.yml": "artifacts:\n  - name: application\n    project-type: npm\n    working-directory: .\ncontainers:\n  - name: application\n    container-file: Containerfile\n    context: .\n    platforms: linux/amd64\n    enable-slsa: false\n"}

	if !development {
		if err := readNPMChangelogInputs(recipeDirectory, files); err != nil {
			return nil, err
		}
	}
	// Snapshot planning must see a container-only declaration: npm's original
	// lifecycle runs in the checked-package job after the original source image.
	if development {
		files[".forge-ci/npm-application.yml"] = strings.SplitN(files[".forge-ci/npm-application.yml"], "containers:", 2)[1]
		files[".forge-ci/npm-application.yml"] = "containers:" + files[".forge-ci/npm-application.yml"]
	}

	return files, nil
}

// readNPMChangelogInputs copies the recipe's changelog inputs into files.
func readNPMChangelogInputs(recipeDirectory string, files map[string]string) error {
	for _, path := range []string{chglogKeepAChangelogConfig, chglogMinimalConfig, ".chglog/CHANGELOG-keepachangelog.tpl", ".chglog/CHANGELOG-minimal.tpl"} {
		text, err := os.ReadFile(filepath.Join(recipeDirectory, path)) //nolint:gosec // operator-supplied recipe directory joined with a fixed changelog file name.
		if err != nil {
			return err
		}

		files[path] = string(text)
	}

	return nil
}
