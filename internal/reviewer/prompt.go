package reviewer

import (
	"fmt"
	"strings"

	"github.com/Peeeaje/lovely-ghostwriter/internal/config"
	"github.com/Peeeaje/lovely-ghostwriter/internal/state"
)

func Prompt(cfg config.Config, pr state.PullRequest, worktreePath, artifactPath, reviewer string) string {
	posting := `GitHubへの投稿は禁止です。レビュー結果だけをfinal responseとartifactへ残してください。`
	if cfg.Review.PostReviews {
		posting = fmt.Sprintf(`レビュー終了時に、元PRへPull Request ReviewのCOMMENTを1件投稿してください。
- approve、request changes、merge、commit、push、ファイル編集は禁止
- 位置を特定できる指摘は可能な限りinline review commentにする
- 各指摘は [blocking]、[caution]、[nit] のいずれかで始める
- summary冒頭に「自動レビュー判定」とseverity件数を書く
- findingがなくてもNO BLOCKING FINDINGSのsummary reviewを投稿する
- gh pr commentは使わず、Pull Request Reviewとして投稿する
- review body末尾に次のfooterを正確に入れる

---
_このレビューはCodexによる自動生成です。最終判断は人間のreviewerが行ってください。_
<!-- %s reviewer=%s head=%s -->`, cfg.Review.Marker, reviewer, pr.HeadSHA)
	}

	return strings.TrimSpace(fmt.Sprintf(`%s PR #%dをレビューしてください。

対象:
- repository: %s
- PR URL: %s
- target head SHA: %s
- base branch: %s
- dedicated worktree: %s
- artifact directory: %s

このrunは対象head専用のdetached worktreeで実行されています。レビュー対象はbase branchとの差分です。
利用可能ならrepositoryのreview skillとAGENTS.mdに従ってください。
既存のPR comments、reviews、diff、関連コード、testsを確認し、既に指摘済みの内容を重複投稿しないでください。
コードスタイルの好みではなく、実害のあるbug、regression、security、data integrity、重要なtest不足を優先してください。

投稿またはfinal responseの直前に、次を再確認してください。
  gh pr view %d --repo %s --json state,headRefOid
PRがopenでない、またはheadRefOidがtarget head SHAと異なる場合はGitHubへ何も投稿せず、理由をartifactへ残してください。

%s

調査内容と最終結果はartifact directoryにも残してください。`,
		pr.Repository, pr.Number, pr.Repository, pr.URL, pr.HeadSHA, pr.BaseBranch, worktreePath, artifactPath,
		pr.Number, pr.Repository, posting))
}
