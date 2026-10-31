// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"encoding/base64"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
)

// Consume the previously published hello-npm-package fixture without its source,
// retained archive, publisher credentials or the product's npm implementation.
func TestForgejoNPMLibrary_RegistryConsumer(t *testing.T) {
	name := os.Getenv("FORGE_CI_LIVE_NPM_CONSUMER_NAME")
	version := os.Getenv("FORGE_CI_LIVE_NPM_CONSUMER_VERSION")
	if name == "" && version == "" {
		t.Skip("optional published-library consumer: name the disposable package and exact version")
	}
	if name == "" || version == "" {
		t.Fatal("published-library consumer requires both package name and exact version")
	}
	if !livetest.Requires(t, provider.ForgeForgejo, livetest.NeedsInRunner, livetest.NeedsPackages) {
		livetest.NotCoveredf(t, "registry consumption requires Forgejo packages and a runner")
	}
	target := livetest.Accept(t, provider.ForgeForgejo)
	if !regexp.MustCompile(`^@`+regexp.QuoteMeta(target.Owner)+`/fci-[a-z0-9._-]+$`).MatchString(name) ||
		!regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(version) {
		t.Fatal("require an exact stable version of a disposable package under the admitted owner")
	}
	repo := livetest.NewScratchRepo(t, target, "npm-library-consumer")
	if !slices.Contains(livetest.PublishedPackageVersions(t, target, repo, "npm", name), version) {
		t.Fatal("the requested package version is not present before consumption")
	}
	ca, err := os.ReadFile(target.CAFile)
	if err != nil {
		t.Fatal(err)
	}
	registry := strings.TrimRight(target.BaseURL(), "/") + "/api/packages/" + target.Owner + "/npm/"
	workflow := fmt.Sprintf(`# SPDX-FileCopyrightText: The Forge CI Authors
# SPDX-License-Identifier: CC0-1.0
name: Published npm library consumer
on: [push]
jobs:
  consume:
    runs-on: %q
    container:
      image: docker.io/library/node:24.17.0-trixie@sha256:61db8992b5c481488fe236ea69fe94035ba73df76a474051ed2e9713f3a15e5a
    timeout-minutes: 5
    env:
      PACKAGE_NAME: %q
      PACKAGE_VERSION: %q
      REGISTRY_URL: %q
      LAB_CA_B64: %q
    steps:
      - name: Fresh anonymous registry install and public API oracle
        shell: bash
        run: |
          set -euo pipefail
          scratch=$(mktemp -d)
          trap 'rm -rf "$scratch"' EXIT
          export HOME="$scratch/home"
          export npm_config_cache="$scratch/cache"
          export npm_config_cafile="$scratch/ca.crt"
          mkdir -p "$HOME" "$scratch/consumer"
          printf '%%s' "$LAB_CA_B64" | base64 -d > "$npm_config_cafile"
          unset NODE_AUTH_TOKEN NPM_TOKEN CI_JOB_TOKEN GITHUB_TOKEN FORGEJO_TOKEN GH_TOKEN RELEASE_TOKEN
          unset ACTIONS_RUNTIME_TOKEN ACTIONS_ID_TOKEN_REQUEST_TOKEN
          cd "$scratch/consumer"
          npm init -y >/dev/null
          npm install --ignore-scripts --no-audit --no-fund --registry "$REGISTRY_URL" "$PACKAGE_NAME@$PACKAGE_VERSION"
          node --input-type=module <<'JS'
          import assert from 'node:assert/strict';
          import fs from 'node:fs';
          import path from 'node:path';
          const name = process.env.PACKAGE_NAME;
          const installed = JSON.parse(fs.readFileSync(path.join('node_modules', name, 'package.json')));
          assert.equal(installed.name, name);
          assert.equal(installed.version, process.env.PACKAGE_VERSION);
          const {greet} = await import(name);
          assert.equal(greet('  Forge CI  '), 'Hello, Forge CI!');
          assert.throws(() => greet(''), TypeError);
          console.log('ANONYMOUS_REGISTRY_LIBRARY_CONSUMPTION_OK', name, installed.version);
          JS
`, livetest.RunnerLabel(t, target), name, version, registry, base64.StdEncoding.EncodeToString(ca))
	const workflowName = "npm-published-library-consumer"
	conclusion := livetest.RunWorkflow(t, target, repo, workflowName, workflow)
	run := livetest.WaitForConsumerRun(t, target, repo, workflowName+".yml", "push", "")
	scenario := layerScenario{target: target, repo: repo}
	jobs := scenario.requireStatus(t, run, jobSuccess)
	if conclusion != jobSuccess || len(jobs) != 1 {
		t.Fatalf("registry consumer did not execute its one successful job: %s, %d jobs", conclusion, len(jobs))
	}
	t.Logf("fresh anonymous registry consumer verified %s@%s in run %d", name, version, run.ID)
}
