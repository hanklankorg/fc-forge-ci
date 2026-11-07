// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	rootcli "github.com/diggsweden/forge-ci/internal/cli"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

// Real CLI and OCI transport, exclusively on loopback. The builder, scanners
// and signing tool are inert: this proves composition, not their efficacy.
func TestRootBaseNativeCLIJourney(t *testing.T) {
	env := testenv.New(t)
	t.Chdir(env.Temp)

	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(server.Close)

	host := strings.TrimPrefix(server.URL, "http://")
	source, destination := host+"/build/bases", host+"/publication/bases"

	id, revision := strings.Repeat("a", 64), strings.Repeat("b", 40)
	for key, value := range map[string]string{
		"REGISTRY_TOKEN": "inert-token", "COSIGN_KEY": "inert-key", "COSIGN_PASSWORD": "inert-password",
		"FORGEJO_SERVER_URL": "https://source.example", "FORGEJO_REPOSITORY": "org/app", "FORGEJO_SHA": revision,
		"FORGEJO_REF_NAME": "main", "FORGEJO_RUN_ID": "7", "RUNNER_TEMP": env.MkdirAll("runner"),
		"GITHUB_ACTIONS": "true", "FORGEJO_ACTIONS": "true", "GITHUB_REF_NAME": "main", "GITHUB_RUN_ID": "7",
		"GITHUB_SERVER_URL": "https://source.example", "GITHUB_REPOSITORY": "org/app", "GITHUB_SHA": revision,
	} {
		t.Setenv(key, value)
	}

	write := func(path string, body []byte) { t.Helper(); require.NoError(t, os.WriteFile(path, body, 0o600)) }
	write("Containerfile", []byte("FROM scratch\n"))
	write("auth.json", []byte(`{"auths":{}}`))

	plan := pipeline.ReleaseImagesPlan{
		Images: []pipeline.ReleaseImage{{Name: "root", Role: "base", ImageKind: "base", Flavor: "opaque", BaseInputID: id,
			ImageRepository: source, FinalTagName: id + "-opaque", CandidateTag: "staging-" + id + "-opaque", Archs: "amd64", Scan: true}},
		Builds: []pipeline.ReleaseImageBuild{{Name: "root", Arch: "amd64", Platform: "linux/amd64", ContainerFile: "Containerfile", Context: ".",
			ImageRepository: source, FinalTagName: id + "-opaque", CandidateTag: "staging-" + id + "-opaque"}},
	}
	body, err := json.Marshal(plan)
	require.NoError(t, err)
	write("consumer-plan.json", body)

	// A pre-created image is exported by the fake builder; no container runs.
	config, err := empty.Image.ConfigFile()
	require.NoError(t, err)

	config.OS, config.Architecture = "linux", "amd64"
	config.Config.Labels = map[string]string{"org.opencontainers.image.revision": revision}
	image, err := mutate.ConfigFile(empty.Image, config)
	require.NoError(t, err)

	seed := env.Path("seed")
	path, err := layout.Write(seed, empty.Index)
	require.NoError(t, err)
	require.NoError(t, path.AppendImage(image))

	bins := mockbinary.New(t)
	bins.OnPath()
	bins.Add("buildah", fmt.Sprintf(`
if [ "$1" = push ]; then
  for arg in "$@"; do
    case "$arg" in oci:*) dest=${arg#oci:}; dest=${dest%%:*}; mkdir -p "$dest"; cp -R -- %q/. "$dest/";; esac
  done
fi`, seed))
	bins.Add("syft", `for arg in "$@"; do case "$arg" in cyclonedx-json=*) printf '%s\n' '{"bomFormat":"CycloneDX","specVersion":"1.6","version":1}' >"${arg#*=}";; esac; done`)
	bins.Add("trivy", `while [ "$#" -gt 0 ]; do if [ "$1" = --output ]; then shift; printf '%s\n' '{"SchemaVersion":2,"Results":[]}' >"$1"; fi; shift; done`)

	captured := env.Path("predicate.json")
	bins.Add("cosign", fmt.Sprintf(`
if [ "$1" = attest ]; then
  kind=; predicate=
  while [ "$#" -gt 0 ]; do
    case "$1" in --type) shift; kind=$1;; --predicate) shift; predicate=$1;; esac
    shift
  done
  if [ "$kind" = slsaprovenance1 ]; then cp -- "$predicate" %q; fi
fi
if [ "$1" = verify-attestation ]; then
  printf '{"payload":"'
  { printf '{"predicateType":"https://slsa.dev/provenance/v1","predicate":'; cat %q; printf '}'; } | base64 | tr -d '\n'
  printf '"}\n'
fi`, captured, captured))

	run := func(args ...string) string {
		t.Helper()

		stdout, createErr := os.CreateTemp(env.Temp, "cli-stdout-")
		require.NoError(t, createErr)

		original := os.Stdout

		os.Stdout = stdout
		defer func() { os.Stdout = original; _ = stdout.Close() }()

		var output bytes.Buffer

		command := rootcli.New(rootcli.BuildInfo{Version: "dev"})
		command.Writer, command.ErrWriter = &output, &output
		require.NoError(t, command.Run(t.Context(), append([]string{"forge-ci", "--provider", "forgejo", "--format", "json"}, args...)), output.String())

		capturedOutput, readErr := os.ReadFile(stdout.Name())
		require.NoError(t, readErr)

		return string(capturedOutput) + output.String()
	}

	job := []string{"--image-repository", source, "--release-tag", "v1.2.3", "--release-sha", revision,
		"--server-url", "https://source.example", "--repository", "org/app"}
	run(append([]string{"container", "base-images", "root-base", "plan", "--plan", "consumer-plan.json", "--output", "plan.json", "--runner-archs", "amd64"}, job...)...)
	run(append([]string{"container", "base-images", "root-base", "build", "--plan", "plan.json", "--arch", "amd64", "--metadata-dir", "layouts", "--created", baseStartedOn}, job...)...)
	require.Empty(t, bins.Invocations("syft"))
	run(append([]string{"container", "base-images", "root-base", "publish", "--plan", "plan.json", "--layouts-dir", "layouts", "--output-dir", "payload", "--auth-file", "auth.json"}, job...)...)

	digest := strings.TrimSpace(run("release", "dist-digest", "--canonical-payload", "--dist-dir", "payload"))
	write("digest", []byte(digest))
	// Artifact transport retains both roots, without flattening their paths.
	require.NoError(t, os.CopyFS("download", os.DirFS("payload")))
	run("release", "validate-dist", "--canonical-payload", "--dist-dir", "download", "--digest-file", "digest", "--stage-dir", "verified")
	write("download/premade-sboms/base-sbom-opaque.cyclonedx.json", []byte("changed after admission"))

	common := []string{"--server-url", "https://source.example", "--repository", "org/app", "--registry", host,
		"--expected-repository", destination, "--registry-username", "inert"}
	run(append([]string{"container", "base-images", "import", "--ledger", "verified/dist/base-images.json", "--dist-dir", "verified/dist",
		"--source-repository", source, "--output-ledger", "verified/dist/base-images-publication.json"}, common...)...)

	before := len(bins.Invocations("syft"))

	run(append([]string{"container", "base-images", "sign", "--ledger", "verified/dist/base-images-publication.json", "--evidence-root", "verified",
		"--workflow", "bases.yml", "--source-sha", revision, "--started-on", baseStartedOn, "--method", "kms", "--key", "env://COSIGN_KEY"}, common...)...)
	require.Len(t, bins.Invocations("syft"), before, "trusted signer must not rescan")
	write("trusted.pub", []byte("inert-public-key"))

	keyHash := fmt.Sprintf("%x", sha256.Sum256([]byte("inert-public-key")))
	output := run(append([]string{"container", "base-images", "promote", "--ledger", "verified/dist/base-images-publication.json",
		"--expected-workflow", "bases.yml", "--cosign-public-key-path", "trusted.pub", "--cosign-public-key-sha256", keyHash}, common...)...)

	var result struct {
		Images string `json:"base_images_json"`
	}
	require.NoError(t, json.Unmarshal([]byte(output), &result))

	var refs []struct {
		Ref string `json:"ref"`
		ID  string `json:"base_input_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.Images), &refs))
	require.Len(t, refs, 1)
	require.Equal(t, id, refs[0].ID)
	require.True(t, strings.HasPrefix(refs[0].Ref, destination+"@sha256:"))

	final, err := ociregistry.NewAnonymous().ResolveDigest(t.Context(), destination+":"+id+"-opaque")
	require.NoError(t, err)
	require.Equal(t, destination+"@"+final, refs[0].Ref)
}
