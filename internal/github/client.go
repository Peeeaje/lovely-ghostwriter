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
	Number            int             `json:"number"`
	Title             string          `json:"title"`
	URL               string          `json:"url"`
	HeadSHA           string          `json:"headRefOid"`
	HeadBranch        string          `json:"headRefName"`
	BaseBranch        string          `json:"baseRefName"`
	BaseSHA           string          `json:"baseRefOid"`
	Draft             bool            `json:"isDraft"`
	State             string          `json:"state"`
	IsCrossRepository bool            `json:"isCrossRepository"`
	Author            Actor           `json:"author"`
	ReviewRequests    []ReviewRequest `json:"reviewRequests"`
	Reviews           []Review        `json:"reviews"`
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
	Body   string `json:"body"`
	Author Actor  `json:"author"`
}

type ReviewSubmission struct {
	CommitID string          `json:"commit_id"`
	Event    string          `json:"event"`
	Body     string          `json:"body"`
	Comments []ReviewComment `json:"comments,omitempty"`
}

type ReviewComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"`
	Body string `json:"body"`
}

type CommandRunner interface {
	Output(context.Context, string, ...string) ([]byte, error)
	Input(context.Context, string, []byte, ...string) ([]byte, error)
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

func (ExecRunner) Input(ctx context.Context, name string, input []byte, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin = strings.NewReader(string(input))
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
		"--json", "number,title,url,headRefOid,headRefName,baseRefName,baseRefOid,isDraft,state,isCrossRepository,author,reviewRequests,reviews",
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
		"--json", "number,title,url,headRefOid,headRefName,baseRefName,baseRefOid,isDraft,state,isCrossRepository,author,reviewRequests,reviews",
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

func HasMarker(pr PullRequest, marker, headSHA, baseSHA, reviewer string) bool {
	markerPrefix := "<!-- " + marker + " "
	headAttribute := "head=" + headSHA + " "
	baseAttribute := "base=" + baseSHA + " "
	for _, review := range pr.Reviews {
		if review.Author.Login == reviewer && strings.Contains(review.Body, markerPrefix) && strings.Contains(review.Body, headAttribute) && strings.Contains(review.Body, baseAttribute) {
			return true
		}
	}
	return false
}

func HasRunMarker(pr PullRequest, marker, headSHA, baseSHA, reviewer string, runID int64) bool {
	markerPrefix := "<!-- " + marker + " "
	headAttribute := "head=" + headSHA + " "
	baseAttribute := "base=" + baseSHA + " "
	runAttribute := "run=" + strconv.FormatInt(runID, 10) + " "
	for _, review := range pr.Reviews {
		if review.Author.Login == reviewer && strings.Contains(review.Body, markerPrefix) && strings.Contains(review.Body, headAttribute) && strings.Contains(review.Body, baseAttribute) && strings.Contains(review.Body, runAttribute) {
			return true
		}
	}
	return false
}

func (c *Client) SubmitReview(ctx context.Context, repository string, number int, submission ReviewSubmission) (Review, error) {
	input, err := json.Marshal(submission)
	if err != nil {
		return Review{}, fmt.Errorf("encode review submission: %w", err)
	}
	endpoint := fmt.Sprintf("repos/%s/pulls/%d/reviews", repository, number)
	output, err := c.runner.Input(ctx, "gh", input, "api", "--method", "POST", endpoint, "--input", "-")
	if err != nil {
		return Review{}, fmt.Errorf("submit review for %s#%d: %w", repository, number, err)
	}
	var review Review
	if err := json.Unmarshal(output, &review); err != nil {
		return Review{}, fmt.Errorf("decode submitted review: %w", err)
	}
	return review, nil
}
