// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"fmt"
	"strings"

	"github.com/urfave/cli/v3"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/cli/regflags"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// imageRegistry is where the one image repository of a base-image or
// release-image verb lives. The login goes to the repository's own host.
// Foreign says that host is not the forge's registry, so the forge's package
// API knows none of its tags: they are deleted through that registry instead.
type imageRegistry struct {
	Host    string
	Foreign bool
}

// forgeRegistryFlag names the forge's own registry for the verbs bound to one
// image repository. Only a forge that serves its registry apart from its
// server needs it: GitLab passes $CI_REGISTRY.
func forgeRegistryFlag() cli.Flag {
	return &cli.StringFlag{
		Name:    flagRegistry,
		Sources: cli.EnvVars("CONTAINER_REGISTRY"),
		Usage:   "the forge's own registry host, when it is not the host of --server-url (GitLab's $CI_REGISTRY); the login goes to the image repository's host, and a repository on any other host has its tags deleted through that registry",
	}
}

// resolveImageRegistry derives where repository lives from its host: the
// forge's own registry when that host is forgeRegistry (--registry), or else
// the server's host; another registry, such as GHCR, otherwise. Without
// either there is nothing to tell them apart by, and the repository counts as
// the forge's: the caller requires one before it logs in or deletes a tag.
func resolveImageRegistry(forgeRegistry, serverHost, repository string) (imageRegistry, error) {
	host, err := appcontainer.OptionalRegistryHost(repository)
	if err != nil {
		return imageRegistry{}, err
	}

	forgeHost := serverHost
	if forgeRegistry != "" {
		if forgeHost, err = appcontainer.RegistryHost(forgeRegistry); err != nil {
			return imageRegistry{}, err
		}
	}

	// Host names compare without case: a server URL written in capitals still
	// names the registry its lowercased default repository is on.
	return imageRegistry{Host: host, Foreign: forgeHost != "" && !strings.EqualFold(host, forgeHost)}, nil
}

// registryLoginUser is the user the registry token logs in as: the one given,
// or on the forge's own registry the source repository's owner, whom the
// forge's tokens log in as. Another registry has users of its own, so there
// the user must be named; guessing the forge owner fails late and obscurely.
//
// nameIt names the caller's own settings for the user, so a refusal points
// at a setting the caller has.
func registryLoginUser(username, sourceRepository string, registry imageRegistry, nameIt string) (string, error) {
	if username != "" {
		return username, nil
	}

	owner, _, _ := strings.Cut(sourceRepository, "/")
	if !registry.Foreign && owner != "" {
		return owner, nil
	}

	return "", fmt.Errorf("%s is not this forge's registry, so name the user the registry token logs in as there: %s: %w",
		registry.Host, nameIt, errs.ErrUsage)
}

// releaseUserSettings and loginUserSettings name, for a refusal, where the
// release and base-image verbs and a plain login take the registry user.
var (
	releaseUserSettings = fmt.Sprintf("the RELEASE_REGISTRY_USER variable or the workflow input registry-user (--%s, $%s)", regflags.FlagUsername, regflags.EnvUser)
	loginUserSettings   = fmt.Sprintf("registry-login's input username (--%s, $%s)", regflags.FlagUsername, regflags.EnvUser)
)
