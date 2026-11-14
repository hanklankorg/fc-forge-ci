// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// ResolveForgeMavenRegistry implements provider.ForgeMavenRegistryResolver for
// the GitLab project-level Maven registry:
// <CI_API_V4_URL>/projects/<CI_PROJECT_ID>/packages/maven, authenticated with a
// Job-Token httpHeader ($CI_JOB_TOKEN).
func (p *Provider) ResolveForgeMavenRegistry() (provider.ForgeMavenRegistry, error) {
	projects, err := p.packageProject("GitLab Package Registry deploy")
	if err != nil {
		return provider.ForgeMavenRegistry{}, err
	}

	return provider.ForgeMavenRegistry{
		ServerID:   "gitlab-maven",
		URL:        projects + "/packages/maven",
		AuthScheme: provider.MavenAuthJobTokenHeader,
		Token:      p.envFunc()("CI_JOB_TOKEN"),
	}, nil
}

// ResolveForgeNPMRegistry implements provider.ForgeNPMRegistryResolver for the
// GitLab project-level npm registry:
// <CI_API_V4_URL>/projects/<CI_PROJECT_ID>/packages/npm/, with a path-scoped
// _authToken. The token is $RELEASE_PACKAGE_TOKEN, the release's package
// credential, where the job holds one, and $CI_JOB_TOKEN otherwise. No scope
// is set (the project registry accepts any package name).
func (p *Provider) ResolveForgeNPMRegistry() (provider.ForgeNPMRegistry, error) {
	projects, err := p.packageProject("GitLab npm deploy")
	if err != nil {
		return provider.ForgeNPMRegistry{}, err
	}

	env := p.envFunc()

	token := env("RELEASE_PACKAGE_TOKEN")
	if token == "" {
		token = env("CI_JOB_TOKEN")
	}

	return provider.ForgeNPMRegistry{Registry: projects + "/packages/npm/", Token: token}, nil
}

// packageProject is the API root of the job's project,
// <CI_API_V4_URL>/projects/<CI_PROJECT_ID>. The ID is a number: any other
// value would be read as a path of the URL the token is sent to.
func (p *Provider) packageProject(operation string) (string, error) {
	env := p.envFunc()

	api := strings.TrimRight(strings.TrimSpace(env("CI_API_V4_URL")), "/")
	projectID := strings.TrimSpace(env("CI_PROJECT_ID"))

	if api == "" || projectID == "" {
		return "", fmt.Errorf("$CI_API_V4_URL and $CI_PROJECT_ID are required for %s: %w", operation, errs.ErrUsage)
	}

	if id, err := strconv.ParseUint(projectID, 10, 63); err != nil || strconv.FormatUint(id, 10) != projectID {
		return "", fmt.Errorf("$CI_PROJECT_ID must be a project's number for %s: %w", operation, errs.ErrUsage)
	}

	return api + "/projects/" + projectID, nil
}

// VerifyForgeProject implements provider.ForgeProjectVerifier: the project
// GitLab ran this job's pipeline in, read with the job's own token, must be
// $CI_PROJECT_ID. A project variable outranks the predefined one, so the ID
// alone could send a package and its credential to another project.
func (p *Provider) VerifyForgeProject(ctx context.Context) error {
	env := p.envFunc()

	api := strings.TrimRight(strings.TrimSpace(env("CI_API_V4_URL")), "/")
	token := env("CI_JOB_TOKEN")

	if api == "" || token == "" {
		return fmt.Errorf("$CI_API_V4_URL and $CI_JOB_TOKEN are required to verify the job's project: %w", errs.ErrUsage)
	}

	body, err := getJSON(ctx, p.HTTPClient, api+"/job", map[string]string{"JOB-TOKEN": token})
	if err != nil {
		return fmt.Errorf("read the job to verify its project: %w", err)
	}

	var job struct {
		Pipeline struct {
			ProjectID uint64 `json:"project_id"`
		} `json:"pipeline"`
	}

	if err := json.Unmarshal(body, &job); err != nil || job.Pipeline.ProjectID == 0 {
		return fmt.Errorf("GitLab returned a job without its pipeline's project: %w", errs.ErrMalformedInput)
	}

	if strconv.FormatUint(job.Pipeline.ProjectID, 10) != strings.TrimSpace(env("CI_PROJECT_ID")) {
		return fmt.Errorf("$CI_PROJECT_ID is not the project of this job's pipeline; remove the variable that sets it: %w", errs.ErrValidation)
	}

	return nil
}
