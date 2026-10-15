// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	rootcli "github.com/diggsweden/forge-ci/internal/cli"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

// The inputs both signing paths in the parity test share.
const (
	basePinnedCommit = "cccccccccccccccccccccccccccccccccccccccc"
	baseRunCommit    = "dddddddddddddddddddddddddddddddddddddddd"
	baseWorkflow     = "build-base-images.yml"
	baseStartedOn    = "2026-01-02T03:04:05Z"
	baseSBOMPattern  = `^premade-sboms/base-sbom-[A-Za-z0-9._-]+[.]cyclonedx[.]json$`
	baseSlug         = "Owner/App"
)

// `base-images sign` is one verb for a sequence a caller can also run by hand:
// derive a subjects file from the ledger, run `release provenance` for the
// pinned commit, then hand the statement to `container ledger sign` with the
// base-image repository. This runs both against the same ledger and compares
// everything cosign is given: the arguments of every sign and attest call, and
// the content of every predicate.
func TestBaseImagesSign_HandsCosignWhatReleaseProvenanceThenLedgerSignHandIt(t *testing.T) {
	fixture := newBaseSignFixture(t)

	wantCalls, wantPredicates := fixture.given(t, func() {
		// `container ledger sign` resolves digests with the job's own
		// registry login, which the caller prepared with `container login`.
		t.Setenv("DOCKER_CONFIG", fixture.env.MkdirAll("signer-docker"))
		require.NoError(t, os.WriteFile(fixture.env.Path("signer-docker", "config.json"), []byte(`{"auths":{}}`), 0o600))

		t.Run("release provenance", func(t *testing.T) {
			for _, prefix := range []string{"FORGEJO_", "GITHUB_"} {
				t.Setenv(prefix+"SERVER_URL", fixture.serverURL)
				t.Setenv(prefix+"REPOSITORY", baseSlug)
				t.Setenv(prefix+"SHA", basePinnedCommit)
			}

			fixture.run(t, "release", "provenance", "--checksums-file", "base-subjects.sha256", "--go-sum", "",
				"--profile", "generic", "--workflow", baseWorkflow, "--started-on", baseStartedOn,
				"--output", "base-provenance.intoto.json")
		})

		fixture.run(t, "container", "ledger", "sign", "--ledger", "base-images.ledger.json",
			"--provenance-envelope", "base-provenance.intoto.json", "--method", "kms", "--key", "env://COSIGN_KEY",
			"--expected-image-repository", fixture.host+"/"+strings.ToLower(baseSlug)+"-base",
			"--sbom-path-pattern", baseSBOMPattern)
	})

	gotCalls, gotPredicates := fixture.given(t, func() {
		fixture.run(t, "container", "base-images", "sign", "--ledger", "base-images.ledger.json",
			"--server-url", fixture.serverURL, "--repository", baseSlug, "--repository-suffix", "-base",
			"--workflow", baseWorkflow, "--source-sha", basePinnedCommit, "--started-on", baseStartedOn,
			"--sbom-path-pattern", baseSBOMPattern, "--method", "kms", "--key", "env://COSIGN_KEY",
			"--registry-username", "fixture")
	})

	require.Len(t, wantCalls, 6, "two images, each signed and attested twice")
	require.Equal(t, wantCalls, gotCalls)
	require.Len(t, wantPredicates, 4)
	require.Equal(t, wantPredicates, gotPredicates)

	// Equal is not enough if both are wrong. The statement the two-call path
	// wrote names the flags' source and the pinned commit, not the commit the
	// signing run is on, and its predicate is what each per-image predicate
	// handed to cosign was enriched from.
	type predicate struct {
		BuildDefinition struct {
			ExternalParameters   map[string]any `json:"externalParameters"`
			ResolvedDependencies []any          `json:"resolvedDependencies"`
		} `json:"buildDefinition"`
		RunDetails any `json:"runDetails"`
	}

	statementBody, err := os.ReadFile(fixture.env.Path("base-provenance.intoto.json"))
	require.NoError(t, err)

	var statement struct {
		Predicate predicate `json:"predicate"`
	}
	require.NoError(t, json.Unmarshal(statementBody, &statement))

	base, source := statement.Predicate, "git+"+fixture.serverURL+"/"+baseSlug
	require.Equal(t, map[string]any{"source": source, "ref": "main", "workflow": baseWorkflow}, base.BuildDefinition.ExternalParameters)
	require.Equal(t, []any{map[string]any{"uri": source + "@main", "digest": map[string]any{"gitCommit": basePinnedCommit}}},
		base.BuildDefinition.ResolvedDependencies)

	// cosign attests each image's SBOM, then its SLSA predicate.
	for _, idx := range []int{1, 3} {
		var perImage predicate
		require.NoError(t, json.Unmarshal([]byte(gotPredicates[idx]), &perImage))

		for key, value := range base.BuildDefinition.ExternalParameters {
			require.Equal(t, value, perImage.BuildDefinition.ExternalParameters[key], key)
		}

		require.Equal(t, base.BuildDefinition.ResolvedDependencies, perImage.BuildDefinition.ResolvedDependencies)
		require.Equal(t, base.RunDetails, perImage.RunDetails)
	}
}

// baseSignFixture is a signing run with no registry and no cosign: a loopback
// server answers digest resolution, and a cosign stub records its arguments
// and keeps a copy of every predicate it is asked to attest.
type baseSignFixture struct {
	env       *testenv.Env
	serverURL string // forge server URL; its host is also the registry
	host      string
	cosign    *mockbinary.Mock
	captured  string
}

func newBaseSignFixture(t *testing.T) *baseSignFixture {
	t.Helper()

	env := testenv.New(t)
	t.Chdir(env.Temp)

	registry := newBaseSignRegistry(t)

	fixture := &baseSignFixture{
		env:       env,
		serverURL: registry.URL,
		host:      strings.TrimPrefix(registry.URL, "http://"),
		cosign:    mockbinary.New(t),
		captured:  env.MkdirAll("captured"),
	}

	// The signing run's own environment: a Forgejo runner on a commit other
	// than the one the base images were built from.
	for key, value := range map[string]string{
		"GITHUB_ACTIONS": "true", "FORGEJO_ACTIONS": "true",
		"FORGEJO_SERVER_URL": registry.URL, "FORGEJO_REPOSITORY": baseSlug, "FORGEJO_SHA": baseRunCommit,
		"GITHUB_SERVER_URL": registry.URL, "GITHUB_REPOSITORY": baseSlug, "GITHUB_SHA": baseRunCommit,
		"FORGEJO_REF_NAME": "main", "FORGEJO_RUN_ID": "7",
		"FORGEJO_WORKFLOW_REF": baseSlug + "/.forgejo/workflows/sign-container-images.yml@refs/heads/main",
		"RUNNER_TEMP":          env.MkdirAll("runner-temp"),
		"COSIGN_KEY":           "fixture-signing-key", "COSIGN_PASSWORD": "fixture-passphrase",
		"REGISTRY_TOKEN": "fixture-token",
	} {
		t.Setenv(key, value)
	}

	fixture.writeLedger(t, fixture.host+"/owner/app-base")

	fixture.cosign.OnPath()
	fixture.cosign.Add("cosign", fmt.Sprintf(`
set -eu
previous=
for argument in "$@"; do
  if [ "$previous" = --predicate ]; then
    cp "$argument" %[1]q/"$(ls %[1]q | wc -l)".json
  fi
  previous=$argument
done
`, fixture.captured))

	return fixture
}

// newBaseSignRegistry is a registry that serves the digest of any manifest it
// is asked for and nothing else.
func newBaseSignRegistry(t *testing.T) *httptest.Server {
	t.Helper()

	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, digest, isManifest := strings.Cut(r.URL.Path, "/manifests/")

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/":
		case r.Method == http.MethodHead && isManifest && strings.HasPrefix(r.URL.Path, "/v2/"):
			w.Header().Set("Docker-Content-Digest", digest)
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.Header().Set("Content-Length", "1")
		default:
			t.Errorf("unexpected registry request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(registry.Close)

	return registry
}

// writeLedger writes two base images of repository with premade SBOMs, and
// the subjects file the caller derived from that ledger with jq: per entry the
// hex digest, two spaces, and the ref without its digest.
func (f *baseSignFixture) writeLedger(t *testing.T, repository string) {
	t.Helper()

	inputID := strings.Repeat("e", 64)

	f.env.MkdirAll("premade-sboms")

	var (
		entries  = make([]imageledger.Entry, 0, 2)
		subjects strings.Builder
	)

	for _, image := range []struct{ flavor, hex string }{
		{"alpine", strings.Repeat("a", 64)},
		{"debian", strings.Repeat("b", 64)},
	} {
		sbomPath := "premade-sboms/base-sbom-" + image.flavor + ".cyclonedx.json"
		sbom := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6","version":1,"metadata":{"component":{"name":"` + image.flavor + `"}}}`)
		require.NoError(t, os.WriteFile(f.env.Path(sbomPath), sbom, 0o600))

		entries = append(entries, imageledger.Entry{
			Role: "base", ImageKind: imageledger.ImageKindBase, Flavor: image.flavor,
			Ref: repository + "@sha256:" + image.hex, Digest: "sha256:" + image.hex,
			FinalTag: repository + ":" + inputID + "-" + image.flavor, MovingTag: repository + ":" + image.flavor,
			BaseInputID: inputID, SBOM: sbomPath, SBOMSHA256: fmt.Sprintf("%x", sha256.Sum256(sbom)),
		})

		subjects.WriteString(image.hex + "  " + repository + "\n")
	}

	ledger, err := imageledger.Marshal(entries)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(f.env.Path("base-images.ledger.json"), ledger, 0o600))
	require.NoError(t, os.WriteFile(f.env.Path("base-subjects.sha256"), []byte(subjects.String()), 0o600))
}

func (f *baseSignFixture) run(t *testing.T, args ...string) {
	t.Helper()

	require.NoError(t, f.runErr(t, args...))
}

func (f *baseSignFixture) runErr(t *testing.T, args ...string) error {
	t.Helper()

	command := rootcli.New(rootcli.BuildInfo{Version: "dev"})

	return command.Run(t.Context(), append([]string{"forge-ci", "--provider", "forgejo", "--format", "text"}, args...))
}

// given runs one signing path and returns what cosign received from it: the
// arguments of each call, and the predicates in the order they were attested.
func (f *baseSignFixture) given(t *testing.T, sign func()) ([][]string, []string) {
	t.Helper()

	before := len(f.cosign.Invocations("cosign"))

	sign()

	invocations := f.cosign.Invocations("cosign")[before:]
	calls := make([][]string, 0, len(invocations))

	for _, call := range invocations {
		args := slices.Clone(call.Args)

		// A per-image predicate is written to a fresh directory on every
		// run; its content is compared instead of its path.
		if at := slices.Index(args, "--predicate"); at >= 0 && !strings.HasPrefix(args[at+1], "premade-sboms/") {
			args[at+1] = "<per-image predicate>"
		}

		calls = append(calls, args)
	}

	files, err := os.ReadDir(f.captured)
	require.NoError(t, err)

	predicates := make([]string, 0, len(files))

	for _, file := range files {
		path := filepath.Join(f.captured, file.Name())

		body, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NoError(t, os.Remove(path))

		predicates = append(predicates, string(body))
	}

	return calls, predicates
}
