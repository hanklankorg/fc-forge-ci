// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	_ "embed"
	"strconv"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// The probe shell bodies live as reviewable script files under probes/ and
// are embedded verbatim. __UPPER_SNAKE__ placeholders are substituted with
// values that already passed the validated lab contract (asset URLs, the
// Fulcio URL, the OIDC issuer) or with generated guard fragments;
// no credential value is ever substituted into a probe body.

//go:embed probes/registry-auth-verify.sh.tmpl
var registryAuthVerifyTemplate string

//go:embed probes/registry-extract-stored.sh.tmpl
var registryExtractStoredTemplate string

//go:embed probes/credential-setup-gitlab.sh
var credentialSetupGitLab string

//go:embed probes/credential-setup-forgejo.sh
var credentialSetupForgejo string

//go:embed probes/token-fetch-forgejo.sh.tmpl
var tokenFetchForgejoTemplate string

//go:embed probes/keyless-sign.sh.tmpl
var keylessSignTemplate string

// RegistryAuthProbe returns the in-runner registry credential probe without
// embedding or printing any credential value or registry auth file. The job
// images carry no jq, so the stored credential is read by the probe-json
// asset, digest-checked like every other asset.
func RegistryAuthProbe(target Target, assetURL string, proxy, probeJSON ProbeAsset) string {
	verify := registryAuthVerify(target, proxy)

	registryExtractStored := strings.NewReplacer(
		"__JSON_ASSET_URL__", probeJSON.URL,
		"__JSON_DIGEST_CHECK__", assetDigestCheck(probeJSONAssetName, probeJSON.SHA256),
	).Replace(registryExtractStoredTemplate)
	if target.Forge == provider.ForgeGitLab {
		return `detect:
  image: ` + ProbeImage + `
  script:
    - |
` + indentProbe(ProbePrelude(target, assetURL), 6) + `

      registry="$CI_REGISTRY"
      echo "CI_REGISTRY=$registry"
      test -n "$registry"

      run_product container login --registry "$registry" --auth-file auth.json
` + indentProbe(registryExtractStored, 6) + "\n" + indentProbe(verify, 6) + "\n"
	}

	return `on: [push]
jobs:
  detect:
    runs-on: ubuntu-latest
    container:
      image: ` + ProbeImage + `
    steps:
      - name: the forge-injected registry credential must be found and work
        run: |
` + indentProbe(ProbePrelude(target, assetURL), 10) + `

          server="${FORGEJO_SERVER_URL:-$GITHUB_SERVER_URL}"
          registry="${server#https://}"
          registry="${registry#http://}"
          echo "server=$server registry=$registry"
          test -n "$registry"

          run_product container login --registry "$registry" --auth-file auth.json
` + indentProbe(registryExtractStored, 10) + "\n" + indentProbe(verify, 10) + "\n"
}

func registryAuthVerify(target Target, proxy ProbeAsset) string {
	registryGuard := credentialAuthorityGuard([]string{target.RegistryOrigin}, "registry_url", false)

	return strings.NewReplacer(
		"__REGISTRY_AUTHORITY_GUARD__", registryGuard,
		"__PROXY_ASSET_URL__", proxy.URL,
		"__PROXY_DIGEST_CHECK__", assetDigestCheck(proxyAssetName, proxy.SHA256),
	).Replace(registryAuthVerifyTemplate)
}

func credentialAuthorityGuard(authorities []string, variable string, allowPaths bool) string {
	var guard strings.Builder
	guard.WriteString("  case \"$" + variable + "\" in\n")

	for _, authority := range authorities {
		guard.WriteString("    " + strconv.Quote(authority))

		if allowPaths {
			guard.WriteString("|" + strconv.Quote(authority+"/") + "*")
		}

		guard.WriteString(") ;;\n")
	}

	guard.WriteString(`    *)
      echo "FAIL: credential authority is outside the validated contract allowlist"
      exit 1
      ;;
  esac`)

	return guard.String()
}

// KeylessSignProbe returns a keyless signing workflow whose token-bearing
// network phase runs behind the credential proxy, so it reaches only Forge Lab
// hosts and never a public Sigstore service.
func KeylessSignProbe(target Target, assetURL string, proxy, cosign, probeJSON ProbeAsset, fulcioURL, oidcIssuer string) string {
	body := keylessSignScript(target, assetURL, proxy, cosign, probeJSON, fulcioURL, oidcIssuer)

	if target.Forge == provider.ForgeGitLab {
		return `sign:
  image: ` + ProbeImage + `
  id_tokens:
    SIGSTORE_ID_TOKEN:
      aud: sigstore
  script:
    - |
` + indentProbe(body, 6) + `
`
	}

	return `on: [push]
enable-openid-connect: true
jobs:
  sign:
    runs-on: ubuntu-latest
    container:
      image: ` + ProbeImage + `
    steps:
      - name: keyless-sign against the lab CA
        run: |
` + indentProbe(body, 10) + `
`
}

func keylessSignScript(target Target, assetURL string, proxy, cosign, probeJSON ProbeAsset, fulcioURL, oidcIssuer string) string {
	credentialSetup := credentialSetupGitLab
	tokenFetch := ""
	identity := `^https://.*/${CI_PROJECT_PATH}//?\.gitlab-ci\.yml@`

	if target.Forge == provider.ForgeForgejo {
		credentialSetup = credentialSetupForgejo
		tokenFetch = strings.Replace(tokenFetchForgejoTemplate, "__FORGE_TOKEN_URL_GUARD__",
			credentialAuthorityGuard([]string{target.BaseURL()}, "rc_actions_id_token_request_url", true), 1)
		identity = `^https://.*/${GITHUB_REPOSITORY}/\.forgejo/workflows/.*@`
	}

	return strings.NewReplacer(
		"__CREDENTIAL_SETUP__", credentialSetup,
		"__PROBE_PRELUDE__", ProbePrelude(target, assetURL),
		"__COSIGN_ASSET_URL__", cosign.URL,
		"__COSIGN_DIGEST_CHECK__", assetDigestCheck("cosign.gz", cosign.SHA256),
		"__JSON_ASSET_URL__", probeJSON.URL,
		"__JSON_DIGEST_CHECK__", assetDigestCheck(probeJSONAssetName, probeJSON.SHA256),
		"__PROXY_ASSET_URL__", proxy.URL,
		"__PROXY_DIGEST_CHECK__", assetDigestCheck(proxyAssetName, proxy.SHA256),
		"__TOKEN_FETCH__", tokenFetch,
		"__FULCIO_URL__", fulcioURL,
		"__OIDC_ISSUER__", oidcIssuer,
		"__IDENTITY_REGEXP__", identity,
	).Replace(keylessSignTemplate)
}

func indentProbe(value string, spaces int) string {
	prefix := strings.Repeat(" ", spaces)

	return prefix + strings.ReplaceAll(value, "\n", "\n"+prefix)
}
