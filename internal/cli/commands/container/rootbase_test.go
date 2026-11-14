// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
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
	"slices"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appbaseimages "github.com/diggsweden/forge-ci/internal/app/baseimages"
	rootcli "github.com/diggsweden/forge-ci/internal/cli"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

// Real CLI and OCI transport, exclusively on loopback. The builder, scanners
// and signing tool are inert: this proves composition, not their efficacy.
func TestRootBaseNativeCLIJourney(t *testing.T) {
	nativeBaseCLIJourney(t, false)
}

func TestChildBaseNativeCLIJourney(t *testing.T) {
	nativeBaseCLIJourney(t, true)
}

func nativeBaseCLIJourney(t *testing.T, child bool) {
	t.Helper()

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

	parent := appbaseimages.BaseImageParent{}
	stage, extra := "root-base", []string{}
	checkoutSHA := revision

	if child {
		// Only a loopback fixture is seeded. Its signature/trust acceptance is
		// the protected caller's separate responsibility, not this producer.
		parentImage, parentErr := mutate.AppendLayers(image)
		require.NoError(t, parentErr)
		parentName, parentErr := name.NewTag(host + "/shared/parent:fixture")
		require.NoError(t, parentErr)
		require.NoError(t, remote.Write(parentName, parentImage, remote.WithContext(t.Context())))
		hash, parentErr := parentImage.Digest()
		require.NoError(t, parentErr)

		parent = appbaseimages.BaseImageParent{Ref: host + "/shared/parent@" + hash.String(), InputID: strings.Repeat("e", 64)}
		plan.Images[0].ProvenanceJSON = fmt.Sprintf(`{"parent_base":{"ref":%q,"input_id":%q}}`, parent.Ref, parent.InputID)
		plan.Images[0].Revision, plan.Builds[0].Revision = revision, revision
		checkoutSHA = strings.Repeat("c", 40)
		plan.Builds[0].BuildArgs = "PARENT_IMAGE=" + parent.Ref
		plan.Builds[0].RegistryInputs = map[string]string{"PARENT_IMAGE": parent.Ref}
		body, err = json.Marshal(plan)
		require.NoError(t, err)
		write("consumer-plan.json", body)

		projection, parentErr := json.Marshal(parent)
		require.NoError(t, parentErr)

		stage, extra = "child-base", []string{"--parent-base-json", string(projection)}
	}

	bins := mockbinary.New(t)
	bins.OnPath()
	bins.Add("buildah", fmt.Sprintf(`
test -z "${REGISTRY_TOKEN:-}${COSIGN_KEY:-}${COSIGN_PASSWORD:-}" || exit 93
if [ "$1" = pull ]; then printf 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n'; fi
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
	  if [ "$kind" = cyclonedx ]; then cp -- "$predicate" %q; fi
fi
if [ "$1" = verify-attestation ]; then
  printf '{"payload":"'
  { printf '{"predicateType":"https://slsa.dev/provenance/v1","predicate":'; cat %q; printf '}'; } | base64 | tr -d '\n'
  printf '"}\n'
fi`, captured, captured+".sbom", captured))

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

	job := make([]string, 0, 10+len(extra))
	job = append(job, "--image-repository", source, "--release-tag", "v1.2.3", "--release-sha", checkoutSHA,
		"--server-url", "https://source.example", "--repository", "org/app")
	job = append(job, extra...)

	const amd64Runner = `build-runs-on-linux-amd64=["builder"]`
	// The layer schedules its native build jobs from the builds output.
	planned := run(append([]string{"container", "base-images", stage, "plan", "--plan", "consumer-plan.json", "--output", "plan.json", "--runner-input", amd64Runner}, job...)...)
	require.Contains(t, planned, "builds")
	// A job collects the layouts by the names the plan printed, and a build
	// job of a stage with several images builds the one it is given.
	require.Contains(t, planned, "root-amd64")

	// The GitLab jobs run the same verb, and their output file is a dotenv
	// report, which takes one line a value. Every output of the plan must
	// fit it, or the first job of a base stage fails on GitLab alone.
	dotenv := env.Temp + "/gitlab-outputs.env"
	t.Setenv("CI_OUTPUT", dotenv)
	require.NoError(t, rootcli.New(rootcli.BuildInfo{Version: "dev"}).Run(t.Context(), append([]string{
		"forge-ci", "--provider", "gitlab", "--runner", "gitlab", "container", "base-images", stage, "plan", "--plan", "consumer-plan.json",
		"--output", "gitlab-plan.json", "--runner-input", `build-runner-tags-linux-amd64=["builder"]`,
	}, job...)))
	require.NoError(t, os.Unsetenv("FORGE_CI_RUNNER"))

	gitlabOutputs, readErr := os.ReadFile(dotenv)
	require.NoError(t, readErr)
	require.Contains(t, string(gitlabOutputs), "\nLAYOUTS=root-amd64\n")
	require.Contains(t, string(gitlabOutputs), "BUILDS=[{")

	// A build runs on its own platform's image build runner or not at all:
	// neither the ordinary runner nor another architecture's stands in, and
	// a plan given no runner input is not planned unchecked.
	refused := func(inputs ...string) error {
		args := append([]string{"forge-ci", "--provider", "forgejo", "container", "base-images", stage, "plan", "--plan", "consumer-plan.json",
			"--output", "refused-plan.json"}, job...)
		for _, input := range inputs {
			args = append(args, "--runner-input", input)
		}

		return rootcli.New(rootcli.BuildInfo{Version: "dev"}).Run(t.Context(), args)
	}

	err = refused(`runs-on=["ordinary"]`, "build-runs-on-linux-amd64=", `build-runs-on-linux-arm64=["builder-arm"]`)
	require.ErrorIs(t, err, errs.ErrUsage)
	require.ErrorContains(t, err, "build-runs-on-linux-amd64")
	require.ErrorIs(t, refused(), errs.ErrUsage)
	require.NoFileExists(t, "refused-plan.json")

	arm64Base := plan
	arm64Base.Images, arm64Base.Builds = slices.Clone(plan.Images), slices.Clone(plan.Builds)
	arm64Base.Images[0].Archs, arm64Base.Builds[0].Arch, arm64Base.Builds[0].Platform = "arm64", "arm64", "linux/arm64"
	body, err = json.Marshal(arm64Base)
	require.NoError(t, err)
	write("arm64-plan.json", body)

	arm64Args := append([]string{"forge-ci", "--provider", "forgejo", "container", "base-images", stage, "plan", "--plan", "arm64-plan.json",
		"--output", "refused-plan.json", "--runner-input", amd64Runner, "--runner-input", "build-runs-on-linux-arm64=[]"}, job...)
	err = rootcli.New(rootcli.BuildInfo{Version: "dev"}).Run(t.Context(), arm64Args)
	require.ErrorIs(t, err, errs.ErrUsage)
	require.ErrorContains(t, err, "build-runs-on-linux-arm64")
	require.NoFileExists(t, "refused-plan.json")

	// A caller that names the stage's flavors holds the plan to exactly them,
	// and no plan gives a final tag another name than its content address.
	planWith := func(file string, flavors string) error {
		args := append([]string{"forge-ci", "--provider", "forgejo", "container", "base-images", stage, "plan", "--plan", file,
			"--output", "refused-plan.json", "--runner-input", amd64Runner, "--stage-flavors", flavors}, job...)

		return rootcli.New(rootcli.BuildInfo{Version: "dev"}).Run(t.Context(), args)
	}

	require.NoError(t, planWith("consumer-plan.json", "opaque"))

	for _, flavors := range []string{"other", "opaque,other"} {
		err = planWith("consumer-plan.json", flavors)
		require.ErrorIs(t, err, errs.ErrInvalidConfig)
		require.ErrorContains(t, err, "a stage's plan holds exactly its flavors")
	}

	moving := plan
	moving.Images, moving.Builds = slices.Clone(plan.Images), slices.Clone(plan.Builds)
	moving.Images[0].FinalTagName, moving.Builds[0].FinalTagName = "latest", "latest"
	moving.Images[0].CandidateTag, moving.Builds[0].CandidateTag = "staging-latest", "staging-latest"
	body, err = json.Marshal(moving)
	require.NoError(t, err)
	write("moving-plan.json", body)

	err = planWith("moving-plan.json", "opaque")
	require.ErrorIs(t, err, errs.ErrInvalidConfig)
	require.ErrorContains(t, err, "must be <base-input-id>-<flavor>")

	buildArgs := append([]string{"container", "base-images", stage, "build", "--plan", "plan.json", "--arch", "amd64", "--image", "root", "--metadata-dir", "layouts", "--created", baseStartedOn}, job...)

	if child {
		require.NoError(t, os.Mkdir("inputs", 0o700))

		// One call fetches the parent of every build of the stage, into the
		// directories the build reads; the per-build verb fetches the same.
		run(append([]string{"container", "base-images", stage, "fetch", "--plan", "plan.json", "--inputs-dir", "stage-inputs", "--auth-file", "auth.json"}, job...)...)
		require.DirExists(t, "stage-inputs/root-arch-amd64/PARENT_IMAGE")

		buildJSON, encodeErr := json.Marshal(plan.Builds[0])
		require.NoError(t, encodeErr)
		run("container", "release-images", "fetch-build-inputs", "--build-json", string(buildJSON), "--release-sha", checkoutSHA,
			"--server-url", "https://source.example", "--repository", "org/app", "--inputs-dir", "inputs/root-arch-amd64", "--auth-file", "auth.json")

		staged, readErr := os.ReadFile("stage-inputs/root-arch-amd64/PARENT_IMAGE/forge-ci-source-manifest.json")
		require.NoError(t, readErr)
		fetched, readErr := os.ReadFile("inputs/root-arch-amd64/PARENT_IMAGE/forge-ci-source-manifest.json")
		require.NoError(t, readErr)
		require.Equal(t, fetched, staged)
		require.Empty(t, bins.Invocations("buildah"), "credentialed fetch must not execute a builder")

		buildArgs = append(buildArgs, "--inputs-dir", "inputs")
		refuseReplacedPreparedParent(t, bins, buildArgs)
	}

	unplanned := slices.Replace(slices.Clone(buildArgs), slices.Index(buildArgs, "root"), slices.Index(buildArgs, "root")+1, "absent")
	require.ErrorIs(t, rootcli.New(rootcli.BuildInfo{Version: "dev"}).Run(t.Context(), append([]string{"forge-ci", "--provider", "forgejo"}, unplanned...)), errs.ErrUsage)

	run(buildArgs...)
	require.Empty(t, bins.Invocations("syft"))
	run(append([]string{"container", "base-images", stage, "publish", "--plan", "plan.json", "--layouts-dir", "layouts", "--output-dir", "payload", "--auth-file", "auth.json"}, job...)...)

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
		"--workflow", "bases.yml", "--source-sha", checkoutSHA, "--started-on", baseStartedOn, "--method", "kms", "--key", "env://COSIGN_KEY"}, common...)...)
	require.Len(t, bins.Invocations("syft"), before, "trusted signer must not rescan")

	retainedSBOM, err := os.ReadFile("verified/premade-sboms/base-sbom-opaque.cyclonedx.json")
	require.NoError(t, err)
	signedSBOM, err := os.ReadFile(captured + ".sbom")
	require.NoError(t, err)
	require.Equal(t, retainedSBOM, signedSBOM)
	write("trusted.pub", []byte("inert-public-key"))

	keyHash := fmt.Sprintf("%x", sha256.Sum256([]byte("inert-public-key")))
	if child {
		verifyChildCLI(t, run, bins, parent, destination, id, keyHash, common)

		return
	}

	inputs := strings.TrimSpace(run("container", "base-images", "declared-inputs", "--ledger", "verified/dist/base-images.json", "--expected-repository", source))
	require.JSONEq(t, `[{"flavor":"opaque","base_input_id":"`+id+`"}]`, inputs)

	output := run(append([]string{"container", "base-images", "promote", "--ledger", "verified/dist/base-images-publication.json", "--base-inputs-json", inputs,
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
