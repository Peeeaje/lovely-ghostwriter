package reviewer

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	gh "github.com/Peeeaje/lovely-ghostwriter/internal/github"
	"github.com/Peeeaje/lovely-ghostwriter/internal/state"
)

type Result struct {
	Decision string    `json:"decision"`
	Summary  string    `json:"summary"`
	Findings []Finding `json:"findings"`
}

type Finding struct {
	Severity string `json:"severity"`
	Body     string `json:"body"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Side     string `json:"side"`
}

func outputSchema() []byte {
	return []byte(`{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "additionalProperties": false,
  "required": ["decision", "summary", "findings"],
  "properties": {
    "decision": {"type": "string", "enum": ["BLOCKING", "CAUTION", "NO_BLOCKING_FINDINGS"]},
    "summary": {"type": "string"},
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["severity", "body", "path", "line", "side"],
        "properties": {
          "severity": {"type": "string", "enum": ["blocking", "caution", "nit"]},
          "body": {"type": "string"},
          "path": {"type": "string"},
          "line": {"type": "integer", "minimum": 0},
          "side": {"type": "string", "enum": ["RIGHT", "LEFT"]}
        }
      }
    }
  }
}`)
}

func readResult(data []byte) (Result, error) {
	var result Result
	if err := json.Unmarshal(data, &result); err != nil {
		return Result{}, fmt.Errorf("decode Codex review result: %w", err)
	}
	if strings.TrimSpace(result.Summary) == "" {
		return Result{}, errors.New("Codex review result has an empty summary")
	}
	for i, finding := range result.Findings {
		if strings.TrimSpace(finding.Body) == "" {
			return Result{}, fmt.Errorf("finding %d has an empty body", i+1)
		}
		if (finding.Path == "") != (finding.Line == 0) {
			return Result{}, fmt.Errorf("finding %d must set both path and line, or neither", i+1)
		}
	}
	return result, nil
}

func submission(result Result, marker, reviewer string, pr state.PullRequest, runID int64) gh.ReviewSubmission {
	counts := map[string]int{"blocking": 0, "caution": 0, "nit": 0}
	var comments []gh.ReviewComment
	var summaryFindings []string
	for _, finding := range result.Findings {
		counts[finding.Severity]++
		body := fmt.Sprintf("[%s] %s", finding.Severity, finding.Body)
		if finding.Path != "" {
			comments = append(comments, gh.ReviewComment{Path: finding.Path, Line: finding.Line, Side: finding.Side, Body: body})
		} else {
			summaryFindings = append(summaryFindings, "- "+body)
		}
	}

	body := fmt.Sprintf("自動レビュー判定: %s\n\n- blocking: %d\n- caution: %d\n- nit: %d\n\n%s",
		result.Decision, counts["blocking"], counts["caution"], counts["nit"], result.Summary)
	if len(summaryFindings) > 0 {
		body += "\n\n" + strings.Join(summaryFindings, "\n")
	}
	body += fmt.Sprintf("\n\n---\n_このレビューはCodexによる自動生成です。最終判断は人間のreviewerが行ってください。_\n<!-- %s reviewer=%s head=%s run=%d -->",
		marker, reviewer, pr.HeadSHA, runID)
	return gh.ReviewSubmission{CommitID: pr.HeadSHA, Event: "COMMENT", Body: body, Comments: comments}
}
