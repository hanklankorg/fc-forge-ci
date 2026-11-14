// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"context"
	"crypto/sha1" //nolint:gosec // npm's shasum, compared only where a registry reports no integrity.
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// NPMTarballs returns, for each npm project directory under sourceDir, the
// name of the packed tarball at the root of distDir that is that project's:
// its packed package.json carries the name and version of the project's own.
// Every project has exactly one tarball and distDir holds no other *.tgz, so
// a build cannot hand on a package the plan does not name, or another
// package under a planned one's place.
func NPMTarballs(sourceDir, distDir string, projectDirs []string) ([]string, error) {
	source, err := pathsafe.OpenRoot(sourceDir)
	if err != nil {
		return nil, err
	}

	defer func() { _ = source.Close() }()

	dist, err := pathsafe.OpenRoot(distDir)
	if err != nil {
		return nil, err
	}

	defer func() { _ = dist.Close() }()

	names, err := fs.Glob(dist.FS(), "*.tgz")
	if err != nil {
		return nil, fmt.Errorf("scan %q for npm tarballs: %w", distDir, err)
	}

	packed := map[string]string{}

	for _, name := range names {
		info, statErr := dist.Lstat(name)
		if statErr != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("npm tarball %q must be a confined regular file: %w", name, errs.ErrValidation)
		}

		meta, metaErr := packedNPMMetadata(filepath.Join(distDir, name))
		if metaErr != nil {
			return nil, fmt.Errorf("npm tarball %q: %w", name, metaErr)
		}

		identity := meta.Name + "@" + meta.Version
		if other, taken := packed[identity]; taken {
			return nil, fmt.Errorf("npm tarballs %q and %q both pack %s: %w", other, name, identity, errs.ErrValidation)
		}

		packed[identity] = name
	}

	tarballs := make([]string, 0, len(projectDirs))

	for _, dir := range projectDirs {
		identity, identityErr := npmProjectIdentity(source, dir)
		if identityErr != nil {
			return nil, identityErr
		}

		name, found := packed[identity]
		if !found {
			return nil, fmt.Errorf("no *.tgz at the root of %q packs %s, the npm project %q the plan publishes: %w", distDir, identity, dir, errs.ErrValidation)
		}

		delete(packed, identity)

		tarballs = append(tarballs, name)
	}

	for identity, name := range packed {
		return nil, fmt.Errorf("npm tarball %q packs %s, which is no npm project the plan publishes: %w", name, identity, errs.ErrValidation)
	}

	return tarballs, nil
}

func npmProjectIdentity(source *os.Root, dir string) (string, error) {
	file, err := source.Open(path.Join(filepath.ToSlash(dir), "package.json"))
	if err != nil {
		return "", fmt.Errorf("read the package.json of npm project %q: %w", dir, errs.ErrMissingInput)
	}

	defer func() { _ = file.Close() }()

	body, err := io.ReadAll(io.LimitReader(file, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read the package.json of npm project %q: %w", dir, err)
	}

	var meta npmPackedMetadata
	if err := json.Unmarshal(body, &meta); err != nil || meta.Name == "" || meta.Version == "" {
		return "", fmt.Errorf("the package.json of npm project %q names no package and version: %w", dir, errs.ErrMalformedInput)
	}

	return meta.Name + "@" + meta.Version, nil
}

// npmVersionPublished reports whether the registry already holds the tarball
// as meta's name and version, which a retry of a release that published some
// of its packages finds. The same version with other content is refused: a
// published version is never replaced, and never taken for this release's.
func npmVersionPublished(ctx context.Context, ops NPMPublishOps, dir string, said io.Writer, config userConfig, meta npmPackedMetadata, tarball string) error {
	var stdout, stderr npmRegistryOutput

	identity := meta.Name + "@" + meta.Version

	err := ops.RunInherit(ctx, dir, &stdout, &stderr, "view", identity, "dist", "--json", "--userconfig", config.path, "--registry", config.registry)
	if err != nil {
		return npmReadFailed(said, stderr.String(), config.token, "read the published "+identity, err)
	}

	var published struct {
		Integrity string `json:"integrity"`
		Shasum    string `json:"shasum"`
	}

	if err := json.Unmarshal(stdout.Bytes(), &published); err != nil {
		return fmt.Errorf("npm registry returned no digest of the published %s: %w", identity, errs.ErrMalformedInput)
	}

	file, err := os.Open(tarball) //nolint:gosec // The confined regular file findSingleTarball selected.
	if err != nil {
		return err
	}

	defer func() { _ = file.Close() }()

	strong, weak := sha512.New(), sha1.New() //nolint:gosec // See the import.
	if _, err := io.Copy(io.MultiWriter(strong, weak), file); err != nil {
		return err
	}

	var same bool

	switch {
	case strings.HasPrefix(published.Integrity, "sha512-"):
		same = published.Integrity == "sha512-"+base64.StdEncoding.EncodeToString(strong.Sum(nil))
	case published.Integrity == "" && published.Shasum != "":
		same = published.Shasum == hex.EncodeToString(weak.Sum(nil))
	default:
		return fmt.Errorf("npm registry returned no usable digest of the published %s: %w", identity, errs.ErrMalformedInput)
	}

	if !same {
		return fmt.Errorf("the registry already holds %s with other content than this release's tarball: %w", identity, errs.ErrValidation)
	}

	return nil
}

// NPMPublishEnvironment is the environment npm publishes in: the process's,
// without the variables that configure npm, load code into Node or switch
// off its certificate check. A project variable could otherwise send the
// package and its credential to another registry, or run its own code beside
// the publication. The route and trust a proxied or private-CA runner needs
// stay, as they do for every tool that holds a credential: the proxy
// variables and NODE_EXTRA_CA_CERTS, Node's SSL_CERT_FILE. Their provenance
// is the administrator's to control (docs/gitlab.md, Secrets and tokens).
func NPMPublishEnvironment(environ []string) []string {
	kept := make([]string, 0, len(environ))

	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToLower(name), "npm_config_") || name == "NODE_OPTIONS" || name == "NODE_PATH" || name == "NODE_TLS_REJECT_UNAUTHORIZED" {
			continue
		}

		kept = append(kept, entry)
	}

	return kept
}
