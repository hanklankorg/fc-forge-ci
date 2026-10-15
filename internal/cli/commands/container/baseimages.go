// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appbaseimages "github.com/diggsweden/forge-ci/internal/app/baseimages"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/cli/regflags"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
	"github.com/diggsweden/forge-ci/internal/runcontext"
)

// flagLocalRegistry selects the plain-OCI surface over the forge package API.
const flagLocalRegistry = "local-registry"

// flagExternalRegistry declares that the image repository is on a registry
// other than the forge's own, such as GHCR.
const flagExternalRegistry = "external-registry"

// externalRegistryFlag is shared by the base-image and release-image verbs:
// one declaration, made by the caller, rather than a guess from host names,
// since a forge's registry need not share its server's host (GitLab's does
// not).
func externalRegistryFlag() cli.Flag {
	return &cli.BoolFlag{
		Name:    flagExternalRegistry,
		Sources: cli.EnvVars("EXTERNAL_IMAGE_REGISTRY"),
		Usage:   "the image repository is on a registry other than the forge's own, such as GHCR: it is not held below the source owner, and tags are deleted through that registry, which may keep them",
	}
}

// localRegistryFlag is shared by every base-image verb that lists or deletes
// tags, so a consumer sets the same flag whichever verb it runs.
func localRegistryFlag() cli.Flag {
	return &cli.BoolFlag{
		Name:    flagLocalRegistry,
		Sources: cli.EnvVars("BASE_IMAGES_LOCAL_REGISTRY"),
		Usage:   "list and delete through a plain OCI registry (bases kept beside the runner) instead of the forge package API",
	}
}

var baseImagesRepositorySuffixRE = regexp.MustCompile(`^(-[A-Za-z0-9][A-Za-z0-9._-]*)?$`)

type baseImagesCommon struct {
	ServerURL            string
	Registry             string
	Repository           string
	RepositorySuffix     string
	ExpectedRepository   string
	ExpectedSource       string
	RegistryUsername     string
	RegistryPasswordFile string
	PublicKeyPath        string
	PublicKeySHA256      string
	ExpectedWorkflow     string
	// External is the caller's declaration that the base-image repository is
	// on a registry other than the forge's own (--external-registry).
	External bool
}

func baseImagesGroup() *cli.Command {
	return &cli.Command{
		Name:  "base-images",
		Usage: "sign, verify, and promote base-image caches",
		Description: `Workflow-facing base-image boundary. The commands validate
flavor/base-input metadata, verify Cosign signatures plus CycloneDX and SLSA
lineage attestations, sign digest-pinned base images, and promote verified
candidate images to immutable final base tags. Checkout and pinned-binary
bootstrap remain owned by the caller.`,
		Commands: []*cli.Command{
			baseImagesImportCmd(),
			baseImagesSignCmd(),
			baseImagesVerifyCmd(),
			baseImagesPromoteCmd(),
			baseImagesCleanupCmd(),
			baseImagesPruneCmd(),
			baseImagesFreshnessCmd(),
		},
	}
}

func baseImagesCommonFlags() []cli.Flag {
	return append(baseImagesRepositoryFlags(), baseImagesPublicKeyFlags()...)
}

func baseImagesRepositoryFlags() []cli.Flag {
	return append(baseImagesLocationFlags(),
		&cli.StringFlag{Name: flagExpectedSource, Sources: cli.EnvVars("EXPECTED_SOURCE", "BASE_IMAGES_EXPECTED_SOURCE"), Usage: "source repository URL expected in SLSA lineage (default: <server-url>/<repository>)"},
		&cli.StringFlag{Name: flagExpectedWorkflow, Sources: cli.EnvVars("EXPECTED_WORKFLOW"), Usage: "workflow filename expected in SLSA lineage"},
		baseImagesRegistryFlag(),
	)
}

// baseImagesLocationFlags name the source repository and the base-image
// repository derived from it. The signer takes these without the lineage
// expectations a verifier adds: it records the lineage instead.
func baseImagesLocationFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: flagServerURL, Sources: cienv.ServerURL(), Usage: "forge server URL used to derive the registry host and expected source"},
		&cli.StringFlag{Name: flagRepository, Sources: cienv.Repository(), Usage: "owner/repo used to derive the expected source and base-image repository"},
		&cli.StringFlag{Name: "repository-suffix", Sources: cli.EnvVars("REPOSITORY_SUFFIX"), Usage: "optional suffix appended to the repository package name, e.g. -base"},
		&cli.StringFlag{Name: "expected-repository", Sources: cli.EnvVars("EXPECTED_REPOSITORY", "BASE_IMAGES_EXPECTED_REPOSITORY"), Usage: "exact base-image repository allowed for tags/refs (default: host/lower(owner/repo)<suffix>); sign and promote require it on the login registry and, unless --external-registry, below the --repository owner"},
		externalRegistryFlag(),
	}
}

func baseImagesRegistryFlag() cli.Flag {
	return &cli.StringFlag{Name: flagRegistry, Sources: cli.EnvVars("CONTAINER_REGISTRY"), Usage: "registry host for the registry login (default: the base-image repository's host with --external-registry, else the host from --server-url)"}
}

func baseImagesPublicKeyFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "cosign-public-key-path", Sources: cli.EnvVars("COSIGN_PUBLIC_KEY_PATH"), Usage: "relative path to the trusted Cosign public key in the consumer checkout"},
		&cli.StringFlag{Name: "cosign-public-key-sha256", Sources: cli.EnvVars("COSIGN_PUBLIC_KEY_SHA256"), Usage: "expected sha256 digest of the trusted Cosign public key"},
	}
}

func baseImagesCommonFromCmd(cmd *cli.Command, requireRegistry, requirePublicKey bool) (baseImagesCommon, error) {
	common, err := baseImagesLocationFromCmd(cmd, requireRegistry)
	if err != nil {
		return baseImagesCommon{}, err
	}

	common.ExpectedSource, err = baseImagesExpectedSource(cmd, common.ServerURL, common.Repository)
	if err != nil {
		return baseImagesCommon{}, err
	}

	common.ExpectedWorkflow = strings.TrimSpace(cmd.String(flagExpectedWorkflow))

	common.PublicKeyPath, common.PublicKeySHA256, err = baseImagesPublicKeyFromCmd(cmd, requirePublicKey)
	if err != nil {
		return baseImagesCommon{}, err
	}

	return common, nil
}

// baseImagesLocationFromCmd resolves the part of the common inputs that says
// where the base images live and how to log in there, leaving the lineage
// expectations and the trusted key empty. A verifier adds those through
// baseImagesCommonFromCmd; the signer stops here, since it records the
// lineage the verifiers will later expect.
func baseImagesLocationFromCmd(cmd *cli.Command, requireRegistry bool) (baseImagesCommon, error) {
	serverURL := appcontainer.NormalizedServerURL(cmd.String(flagServerURL))

	serverHost, err := appcontainer.OptionalRegistryHost(serverURL)
	if err != nil {
		return baseImagesCommon{}, err
	}

	repositorySuffix := strings.TrimSpace(cmd.String("repository-suffix"))
	if !baseImagesRepositorySuffixRE.MatchString(repositorySuffix) {
		return baseImagesCommon{}, fmt.Errorf("base images: repository-suffix must be empty or match -[A-Za-z0-9][A-Za-z0-9._-]*: %w", errs.ErrUsage)
	}

	repository := strings.TrimSpace(cmd.String(flagRepository))

	expectedRepository, err := baseImagesExpectedRepository(cmd, serverHost, repository, repositorySuffix)
	if err != nil {
		return baseImagesCommon{}, err
	}

	registry, err := resolveRegistryHostFromFlags(cmd, serverHost, expectedRepository)
	if err != nil {
		return baseImagesCommon{}, err
	}

	if requireRegistry && registry == "" {
		return baseImagesCommon{}, fmt.Errorf("base images: --registry or --server-url is required for registry login: %w", errs.ErrUsage)
	}

	registryUsername, registryPasswordFile := baseImagesRegistryCreds(cmd, requireRegistry)

	return baseImagesCommon{
		External:             cmd.Bool(flagExternalRegistry),
		ServerURL:            serverURL,
		Registry:             registry,
		Repository:           repository,
		RepositorySuffix:     repositorySuffix,
		ExpectedRepository:   expectedRepository,
		RegistryUsername:     registryUsername,
		RegistryPasswordFile: registryPasswordFile,
	}, nil
}

// baseImagesRegistryCreds reads the registry username and password FILE for
// verbs that log in (the password itself is resolved at login time); verbs
// without registry login leave both empty.
func baseImagesRegistryCreds(cmd *cli.Command, requireRegistry bool) (string, string) {
	if !requireRegistry {
		return "", ""
	}

	auth := regflags.Resolve(cmd)

	return auth.Username, auth.PasswordFile
}

// baseImagesExpectedRepository resolves the exact base-image repository:
// the explicit --expected-repository flag, else host/lower(owner/repo)
// plus the validated repository suffix.
func baseImagesExpectedRepository(cmd *cli.Command, serverHost, repository, repositorySuffix string) (string, error) {
	expectedRepository := strings.TrimSpace(cmd.String("expected-repository"))
	if expectedRepository != "" {
		return expectedRepository, nil
	}

	if serverHost == "" || repository == "" {
		return "", fmt.Errorf("base images: --expected-repository or both --server-url and --repository are required: %w", errs.ErrUsage)
	}

	return appcontainer.DefaultReleaseImageRepository(serverHost, repository+repositorySuffix)
}

// baseImagesExpectedSource resolves the source repository URL expected in
// SLSA lineage: the explicit --expected-source flag, else
// <server-url>/<repository>.
func baseImagesExpectedSource(cmd *cli.Command, serverURL, repository string) (string, error) {
	expectedSource := strings.TrimSpace(cmd.String(flagExpectedSource))
	if expectedSource != "" {
		return expectedSource, nil
	}

	source := baseImagesSource(serverURL, repository)
	if source == "" {
		return "", fmt.Errorf("base images: --expected-source or both --server-url and --repository are required: %w", errs.ErrUsage)
	}

	return source, nil
}

// baseImagesSource is the source repository URL both sides of the base-image
// boundary derive from --server-url and --repository: the signer records it
// and a verifier expects it. It is empty when either part is missing.
func baseImagesSource(serverURL, repository string) string {
	if serverURL == "" || repository == "" {
		return ""
	}

	return serverURL + "/" + repository
}

// baseImagesPublicKeyFromCmd reads and validates the trusted Cosign public
// key flags when the verb requires them; otherwise both values stay empty.
func baseImagesPublicKeyFromCmd(cmd *cli.Command, requirePublicKey bool) (string, string, error) {
	if !requirePublicKey {
		return "", "", nil
	}

	publicKeyPath := strings.TrimSpace(cmd.String("cosign-public-key-path"))
	if !pathsafe.Relative(publicKeyPath) {
		return "", "", fmt.Errorf("base images: unsafe Cosign public key path: %s: %w", publicKeyPath, errs.ErrUsage)
	}

	return publicKeyPath, strings.TrimSpace(cmd.String("cosign-public-key-sha256")), nil
}

// requireSourceOwnedRepository is the rule of the verbs that write to the
// base-image repository under the source's name: sign and promote. The
// registry is the one resolveRegistryHostFromFlags resolved for the login:
// --registry, else the repository's host when it is declared external, else
// the host of --server-url. The owner rule holds unless the caller declared
// the repository external (--external-registry).
func (c baseImagesCommon) requireSourceOwnedRepository() error {
	return appbaseimages.RequireSourceOwnedRepository(c.ExpectedRepository, c.Registry, c.Repository, c.External)
}

func (c baseImagesCommon) login(authFile string) error {
	password, err := regflags.LoginPassword(c.RegistryUsername, c.RegistryPasswordFile)
	if err != nil {
		return err
	}

	return appcontainer.RegistryLogin(os.Stderr, appcontainer.RegistryLoginInput{
		Registry: c.Registry,
		Username: c.RegistryUsername,
		Password: password,
		AuthFile: authFile,
	})
}

func withBaseImagesDockerConfig(fn func(authFile string) error) error {
	root := runcontext.TempRoot(os.Getenv)

	dir, err := os.MkdirTemp(root, "forge-ci-base-images-docker-config-*")
	if err != nil {
		return fmt.Errorf("base images: create Docker config dir: %w", err)
	}

	defer func() { _ = os.RemoveAll(dir) }() //nolint:gosec // dir is a fresh MkdirTemp path under the runner temp root, not caller input.

	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // 0o700: a directory needs the owner execute bit; it stays owner-only.
		return fmt.Errorf("base images: chmod Docker config dir: %w", err)
	}

	restoreDockerConfig, setErr := cienv.SetTemporarily("DOCKER_CONFIG", dir)
	if setErr != nil {
		return fmt.Errorf("base images: %w", setErr)
	}

	defer restoreDockerConfig()

	authFile := filepath.Join(dir, "config.json")
	if err := os.WriteFile(authFile, []byte("{\"auths\":{}}\n"), 0o600); err != nil { //nolint:gosec // path is inside the fresh MkdirTemp dir created above.
		return fmt.Errorf("base images: write empty Docker config: %w", err)
	}

	return fn(authFile)
}

func validateBaseImagesPublicKey(path, want string) error {
	return verifyCosignPublicKeyDigest("base images", path, want)
}

// verifyCosignPublicKeyDigest checks that the Cosign public key at path is a
// regular file whose SHA-256 is want. It is the one trust-root check behind
// the base-image and release-image verbs; verb prefixes its messages.
func verifyCosignPublicKeyDigest(verb, path, want string) error {
	if !domaincontainer.ValidSHA256Hex(want) {
		return fmt.Errorf("%s: cosign-public-key-sha256 must be a sha256 hex digest: %w", verb, errs.ErrValidation)
	}

	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s: Cosign public key is missing, not a regular file, or a symlink: %s: %w", verb, path, errs.ErrMissingInput)
	}

	file, err := os.Open(path) //nolint:gosec // caller-selected public key path validated as a relative regular file.
	if err != nil {
		return fmt.Errorf("%s: open Cosign public key %s: %w", verb, path, err)
	}

	defer func() { _ = file.Close() }()

	// The path can be swapped between the check and the open; judge the file opened.
	if opened, statErr := file.Stat(); statErr != nil || !os.SameFile(info, opened) {
		return fmt.Errorf("%s: Cosign public key changed while opening: %s: %w", verb, path, errs.ErrValidation)
	}

	body, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("%s: read Cosign public key %s: %w", verb, path, err)
	}

	sum := sha256.Sum256(body)

	actual := hex.EncodeToString(sum[:])
	if actual != want {
		return fmt.Errorf("%s: Cosign public key digest does not match expected trust root\n  path:     %s\n  expected: %s\n  actual:   %s: %w", verb, path, want, actual, errs.ErrValidation)
	}

	return nil
}

func parseBaseInputs(raw string) ([]appbaseimages.BaseInput, error) {
	var inputs []appbaseimages.BaseInput
	if err := json.Unmarshal([]byte(raw), &inputs); err != nil {
		return nil, fmt.Errorf("base images: base-inputs-json must be a JSON array: %w: %w", err, errs.ErrMalformedInput)
	}

	if inputs == nil {
		return nil, fmt.Errorf("base images: base-inputs-json must be a JSON array: %w", errs.ErrMalformedInput)
	}

	return inputs, nil
}

func parseBaseImages(raw string) ([]appbaseimages.BaseImageMetadata, error) {
	var images []appbaseimages.BaseImageMetadata
	if err := json.Unmarshal([]byte(raw), &images); err != nil {
		return nil, fmt.Errorf("base images: all-images-json must be a JSON array: %w: %w", err, errs.ErrMalformedInput)
	}

	if images == nil {
		return nil, fmt.Errorf("base images: all-images-json must be a JSON array: %w", errs.ErrMalformedInput)
	}

	return images, nil
}

// baseImagePackageRegistry resolves the surface that lists and deletes base-image
// tags: a plain OCI registry when --local-registry is set, otherwise the active
// forge's package API.
//
// Both exist because both are real deployments. A consumer may keep its bases
// beside the runner, or in the forge's package registry, and that choice is the
// consumer's -- so it is a flag rather than something the engine decides.
//
// The forge API deletes package versions. The plain OCI adapter requires
// tag-addressed deletion support and conservatively refuses known shared
// manifests, with no digest fallback. Refuse a contradictory topology here
// rather than silently selecting the wrong API.
func baseImagePackageRegistry(cmd *cli.Command, common baseImagesCommon, capability string) (baseImagePackageAPI, error) {
	// A repository on an external registry, such as GHCR, has no forge
	// package API to ask, just as one beside the runner has none.
	local := cmd.Bool(flagLocalRegistry) || common.External

	if err := requireRegistrySurfaceMatches(local, common); err != nil {
		return nil, err
	}

	if local {
		host, err := appcontainer.OptionalRegistryHost(common.ExpectedRepository)
		if err != nil {
			return nil, err
		}

		local := baseImageLocalRegistry{Adapter: ociregistry.WithAuthFile(cmd.String(flagAuthFile)), host: host}
		if !common.External {
			return local, nil
		}

		deleter, err := externalTagDeleter(host, common.RegistryUsername, common.RegistryPasswordFile, local.Adapter)
		if err != nil {
			return nil, err
		}

		return externalBaseImageRegistry{baseImageLocalRegistry: local, deleter: deleter}, nil
	}

	forge, err := deps.ProviderWith(deps.ProviderConfig{ServerURL: common.ServerURL})
	if err != nil {
		return nil, err
	}

	return deps.RoleFrom[baseImagePackageAPI](forge, capability)
}

// The app passes forge-style owner/name arguments. Plain OCI listing needs
// the configured host too; without it the library defaults to Docker Hub.
type baseImageLocalRegistry struct {
	*ociregistry.Adapter
	host string
}

func (r baseImageLocalRegistry) ListContainerPackageVersions(ctx context.Context, owner, name string) ([]string, error) {
	return r.Adapter.ListContainerPackageVersions(ctx, r.host+"/"+owner, name)
}

// requireRegistrySurfaceMatches refuses a --local-registry setting that
// contradicts where the base images actually live.
//
// Caught here because neither mismatch fails in a way that reads as a
// configuration error: asking a forge package API to delete a version of a
// repository on loopback returns a confusing 404, and driving a forge's own
// registry through the OCI adapter would refuse every shared manifest instead
// of deleting the one version it was asked to.
func requireRegistrySurfaceMatches(local bool, common baseImagesCommon) error {
	repoHost, err := appcontainer.OptionalRegistryHost(common.ExpectedRepository)
	if err != nil || repoHost == "" {
		return err
	}

	if ociregistry.LoopbackHost(repoHost) && !local {
		return fmt.Errorf(
			"base images: %s is on loopback but --local-registry is not set.\n"+
				"    A forge package API cannot manage a registry running beside the job: %w",
			common.ExpectedRepository, errs.ErrUsage)
	}

	forgeHost, err := appcontainer.OptionalRegistryHost(common.ServerURL)
	if err != nil {
		return err
	}

	if local && forgeHost != "" && repoHost == forgeHost {
		return fmt.Errorf(
			"base images: --local-registry is set but %s is the forge's own registry.\n"+
				"    Drop the flag so deletion goes through the package API, which is tag-scoped: %w",
			common.ExpectedRepository, errs.ErrUsage)
	}

	return nil
}
