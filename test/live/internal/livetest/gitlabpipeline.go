// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	gitlabapi "gitlab.com/gitlab-org/api/client-go"

	"github.com/diggsweden/forge-ci/internal/retry"
)

// gitlabAccountDepth is how many levels of child pipelines an account
// follows: the release policy triggers one child, which triggers none.
const gitlabAccountDepth = 2

// maxForgeReason bounds one reason the forge gives, such as a pipeline's
// configuration errors, in a test's log.
const maxForgeReason = 4000

// PollGitLabPipeline reads a pipeline for a scenario that waits on it. A read
// the forge left unanswered is asked again, a few times: a lab forge under
// load may time one out, and that says nothing of the pipeline. Every other
// failure is returned as it is, and the caller's context still ends the wait.
func PollGitLabPipeline(ctx context.Context, client *gitlabapi.Client, project string, id int64) (*gitlabapi.Pipeline, error) {
	var pipeline *gitlabapi.Pipeline

	err := retry.Run(ctx, nil, 5, 10*time.Second, func() error {
		read, _, err := client.Pipelines.GetPipeline(project, id, gitlabapi.WithContext(ctx))
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return retry.Permanent(err)
		}

		pipeline = read

		return err
	})

	return pipeline, err
}

// GitLabPipelineAccount is GitLab's own account of why a pipeline did not
// succeed, one line per fact: the pipeline's status and configuration
// errors, each job that did not succeed with its failure_reason, and each
// trigger job with its downstream pipeline, whose account follows.
//
// It is read before anything else is judged, because a scenario's cleanup
// deletes the scratch project, and with it everything GitLab said. A
// pipeline GitLab refused as it created it has no jobs and no traces: its
// yaml_errors are the only evidence there is. Reads that fail are reported
// as lines too; the account never ends the test.
func GitLabPipelineAccount(ctx context.Context, client *gitlabapi.Client, project string, pipeline int64) []string {
	return gitlabPipelineAccount(ctx, client, project, pipeline, "", gitlabAccountDepth)
}

// LogGitLabPipelineFailure logs GitLabPipelineAccount for a pipeline that did
// not succeed, and nothing for one that did.
func LogGitLabPipelineFailure(tb TB, ctx context.Context, client *gitlabapi.Client, project string, pipeline *gitlabapi.Pipeline) {
	tb.Helper()

	if pipeline == nil || pipeline.Status == runSuccess {
		return
	}

	for _, line := range GitLabPipelineAccount(ctx, client, project, pipeline.ID) {
		tb.Logf("livetest: GitLab: %s", line)
	}
}

func gitlabPipelineAccount(ctx context.Context, client *gitlabapi.Client, project string, id int64, indent string, depth int) []string {
	detail, _, err := client.Pipelines.GetPipeline(project, id, gitlabapi.WithContext(ctx))
	if err != nil {
		return []string{fmt.Sprintf("%spipeline %d: unreadable: %v", indent, id, err)}
	}

	lines := []string{fmt.Sprintf("%spipeline %d (%s): %s", indent, id, detail.Source, detail.Status)}
	if detail.YamlErrors != "" {
		lines = append(lines, fmt.Sprintf("%s  configuration refused: %s", indent, boundedReason(detail.YamlErrors)))
	}

	jobs, _, err := client.Jobs.ListPipelineJobs(project, id, &gitlabapi.ListJobsOptions{PerPage: 100}, gitlabapi.WithContext(ctx))
	if err != nil {
		lines = append(lines, fmt.Sprintf("%s  jobs unreadable: %v", indent, err))
	}

	for _, job := range jobs {
		if job.Status == runSuccess {
			continue
		}

		lines = append(lines, fmt.Sprintf("%s  job %q (%s): %s%s", indent, job.Name, job.Stage, job.Status, gitlabReason(job.FailureReason)))
	}

	bridges, _, err := client.Jobs.ListPipelineBridges(project, id, nil, gitlabapi.WithContext(ctx))
	if err != nil {
		lines = append(lines, fmt.Sprintf("%s  trigger jobs unreadable: %v", indent, err))
	}

	lines = append(lines, gitlabBridgesAccount(ctx, client, project, bridges, indent, depth)...)

	if detail.YamlErrors == "" && len(jobs) == 0 && len(bridges) == 0 && detail.Status != runSuccess {
		for _, message := range gitlabPipelineMessages(ctx, client, project, detail.IID) {
			lines = append(lines, fmt.Sprintf("%s  configuration refused: %s", indent, boundedReason(message)))
		}
	}

	return lines
}

// gitlabBridgesAccount is the account of a pipeline's trigger jobs: each
// with its downstream pipeline, and that pipeline's own account while depth
// allows and it did not succeed.
func gitlabBridgesAccount(ctx context.Context, client *gitlabapi.Client, project string, bridges []*gitlabapi.Bridge, indent string, depth int) []string {
	var lines []string

	for _, bridge := range bridges {
		if bridge.DownstreamPipeline == nil {
			lines = append(lines, fmt.Sprintf("%s  trigger %q (%s): %s%s, no downstream pipeline", indent, bridge.Name, bridge.Stage, bridge.Status, gitlabReason(bridge.FailureReason)))

			continue
		}

		lines = append(lines, fmt.Sprintf("%s  trigger %q (%s): %s%s, downstream pipeline %d", indent, bridge.Name, bridge.Stage, bridge.Status,
			gitlabReason(bridge.FailureReason), bridge.DownstreamPipeline.ID))

		if depth > 1 && bridge.DownstreamPipeline.Status != runSuccess {
			lines = append(lines, gitlabPipelineAccount(ctx, client, project, bridge.DownstreamPipeline.ID, indent+"    ", depth-1)...)
		}
	}

	return lines
}

// gitlabPipelineMessages are the errors GitLab recorded when it made a
// pipeline. The REST API leaves yaml_errors empty for some refusals, such as
// a job in a stage the configuration does not declare; GraphQL has them.
func gitlabPipelineMessages(ctx context.Context, client *gitlabapi.Client, project string, iid int64) []string {
	var answer struct {
		Data struct {
			Project struct {
				Pipeline struct {
					ErrorMessages struct {
						Nodes []struct{ Content string }
					}
				}
			}
		}
	}

	query := gitlabapi.GraphQLQuery{
		Query:     `query($path: ID!, $iid: ID!) { project(fullPath: $path) { pipeline(iid: $iid) { errorMessages { nodes { content } } } } }`,
		Variables: map[string]any{"path": project, "iid": strconv.FormatInt(iid, 10)},
	}
	if _, err := client.GraphQL.Do(query, &answer, gitlabapi.WithContext(ctx)); err != nil {
		return nil
	}

	messages := make([]string, 0, len(answer.Data.Project.Pipeline.ErrorMessages.Nodes))
	for _, node := range answer.Data.Project.Pipeline.ErrorMessages.Nodes {
		messages = append(messages, node.Content)
	}

	return messages
}

// boundedReason is a reason cut to maxForgeReason bytes.
func boundedReason(reason string) string {
	if len(reason) <= maxForgeReason {
		return reason
	}

	return reason[:maxForgeReason] + " [truncated]"
}

func gitlabReason(reason string) string {
	if reason == "" {
		return ""
	}

	return " (" + reason + ")"
}
