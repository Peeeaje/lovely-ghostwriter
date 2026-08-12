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
	Decision        string           `json:"decision"`
	Summary         string           `json:"summary"`
	Findings        []Finding        `json:"findings"`
	PatchedFindings []PatchedFinding `json:"patched_findings"`
}

type PatchedFinding struct {
	Problem string `json:"problem"`
	Fix     string `json:"fix"`
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
  "required": ["decision", "summary", "findings", "patched_findings"],
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
    },
    "patched_findings": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["problem", "fix"],
        "properties": {
          "problem": {"type": "string", "minLength": 1},
          "fix": {"type": "string", "minLength": 1}
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
	for i, finding := range result.PatchedFindings {
		if strings.TrimSpace(finding.Problem) == "" {
			return Result{}, fmt.Errorf("patched finding %d has an empty problem", i+1)
		}
		if strings.TrimSpace(finding.Fix) == "" {
			return Result{}, fmt.Errorf("patched finding %d has an empty fix", i+1)
		}
	}
	return result, nil
}

func submission(result Result, marker, reviewer string, pr state.PullRequest, runID int64, patchURL string) gh.ReviewSubmission {
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

	var body string
	if patchURL != "" {
		counts["blocking"] += len(result.PatchedFindings)
		body = fmt.Sprintf("自動レビュー判定: PATCH_PROPOSED\n\n- blocking: %d\n- caution: %d\n- nit: %d\n\n今回検出したblockingは、patch PRで解消見込みです。元PRへ取り込まれるまで、対象headでは未解消です。",
			counts["blocking"], counts["caution"], counts["nit"])
		body += "\n\n### 問題と修正"
		for i, finding := range result.PatchedFindings {
			body += fmt.Sprintf("\n\n%d. **問題**: %s\n   **修正**: %s", i+1, finding.Problem, finding.Fix)
		}
		if len(summaryFindings) > 0 {
			body += "\n\n### Patch適用後も残るfinding"
		}
	} else {
		publicSummary := "最終状態のコード差分と関連コードを確認した範囲では、未解決の指摘事項はありません。"
		if len(result.Findings) > 0 {
			publicSummary = "コード差分と関連コードを確認し、未解決の指摘事項を投稿しました。"
		}
		body = fmt.Sprintf("自動レビュー判定: %s\n\n- blocking: %d\n- caution: %d\n- nit: %d\n\n%s",
			result.Decision, counts["blocking"], counts["caution"], counts["nit"], publicSummary)
	}
	if len(summaryFindings) > 0 {
		body += "\n\n" + strings.Join(summaryFindings, "\n")
	}
	if patchURL != "" {
		body += "\n\nPatch PR: " + patchURL
	}
	body += fmt.Sprintf("\n\n---\n_このレビューはCodexによる自動生成です。最終判断は人間のreviewerが行ってください。_\n<!-- %s reviewer=%s head=%s base_branch=%s base=%s run=%d -->",
		marker, reviewer, pr.HeadSHA, pr.BaseBranch, pr.BaseSHA, runID)
	return gh.ReviewSubmission{CommitID: pr.HeadSHA, Event: "COMMENT", Body: body, Comments: comments}
}
