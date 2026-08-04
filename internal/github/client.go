package github

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type PullRequest struct {
	Number         int             `json:"number"`
	Title          string          `json:"title"`
	URL            string          `json:"url"`
	HeadSHA        string          `json:"headRefOid"`
	HeadBranch     string          `json:"headRefName"`
	BaseBranch     string          `json:"baseRefName"`
	Draft          bool            `json:"isDraft"`
	State          string          `json:"state"`
	Author         Actor           `json:"author"`
	ReviewRequests []ReviewRequest `json:"reviewRequests"`
	Reviews        []Review        `json:"reviews"`
}

type Actor struct {
	Login string `json:"login"`
}

type ReviewRequest struct {
	Login string `json:"login"`
	Slug  string `json:"slug"`
	Name  string `json:"name"`
}

type Review struct {
	Body string `json:"body"`
}

type CommandRunner interface {
	Output(context.Context, string, ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.Output()
	if err == nil {
		return output, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return nil, fmt.Errorf("%s %v: %s", name, args, exitErr.Stderr)
	}
	return nil, fmt.Errorf("%s %v: %w", name, args, err)
}

type Client struct {
	runner CommandRunner
}

func NewClient(runner CommandRunner) *Client {
	return &Client{runner: runner}
}

func (c *Client) OpenPullRequests(ctx context.Context, repository string) ([]PullRequest, error) {
	output, err := c.runner.Output(ctx, "gh", "pr", "list",
		"--repo", repository,
		"--state", "open",
		"--limit", "100",
		"--json", "number,title,url,headRefOid,headRefName,baseRefName,isDraft,state,author,reviewRequests,reviews",
	)
	if err != nil {
		return nil, fmt.Errorf("list pull requests for %s: %w", repository, err)
	}

	var prs []PullRequest
	if err := json.Unmarshal(output, &prs); err != nil {
		return nil, fmt.Errorf("decode pull requests for %s: %w", repository, err)
	}
	return prs, nil
}

func (c *Client) CurrentUser(ctx context.Context) (string, error) {
	output, err := c.runner.Output(ctx, "gh", "api", "user", "--jq", ".login")
	if err != nil {
		return "", fmt.Errorf("resolve GitHub user: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func (c *Client) PullRequest(ctx context.Context, repository string, number int) (PullRequest, error) {
	output, err := c.runner.Output(ctx, "gh", "pr", "view", strconv.Itoa(number),
		"--repo", repository,
		"--json", "number,title,url,headRefOid,headRefName,baseRefName,isDraft,state,author,reviewRequests,reviews",
	)
	if err != nil {
		return PullRequest{}, fmt.Errorf("view pull request %s#%d: %w", repository, number, err)
	}

	var pr PullRequest
	if err := json.Unmarshal(output, &pr); err != nil {
		return PullRequest{}, fmt.Errorf("decode pull request %s#%d: %w", repository, number, err)
	}
	return pr, nil
}

func HasMarker(pr PullRequest, marker, headSHA string) bool {
	markerPrefix := "<!-- " + marker + " "
	headAttribute := "head=" + headSHA
	for _, review := range pr.Reviews {
		if strings.Contains(review.Body, markerPrefix) && strings.Contains(review.Body, headAttribute) {
			return true
		}
	}
	return false
}
