package readmodel

import "github.com/amasok/asagao/internal/domain/plan"

// The text of a feature the workspace's plan lacks (Asana answered 402). domain/plan holds
// the rules (which feature, for how long) and its features are codes; the words the user
// reads are here, with the other shared error values (Issue #82).

// PlanUnavailableMessage says that a feature cannot be used with the workspace's plan.
func PlanUnavailableMessage(feature string) string {
	what := map[string]string{
		plan.Sections:         "セクションの作成・削除・並べ替え",
		plan.Tags:             "タグの名前・色の変更と削除",
		plan.MySection:        "マイタスクのセクション",
		plan.TimeTracking:     "タイムトラッキング (作業時間の記録)",
		plan.Search:           "タスクの検索",
		plan.Typeahead:        "名前での検索 (typeahead)",
		plan.CustomID:         "タスクの ID (custom ID) での取得",
		plan.Goals:            "ゴール",
		plan.Portfolios:       "ポートフォリオ",
		plan.CustomFields:     "カスタムフィールドの選択肢の追加とプロジェクトへの追加",
		plan.ProjectBrief:     "プロジェクトブリーフ (概要のページ)",
		plan.StartOn:          "タスクの開始日",
		plan.CustomTypes:      "カスタムタイプ (タスクの種類とステータス)",
		plan.TaskTemplates:    "タスクテンプレート",
		plan.ProjectTemplates: "プロジェクトテンプレート",
	}[feature]
	// Every feature is tried again after plan.RecheckAfter.
	return "このワークスペースのプランでは、" + what + "を API で使えません (Asana が 402 を返しました。1 日たったら、もう一度試します)"
}

// PlanUnavailableError is returned for a feature that Asana refused because the workspace's
// plan lacks it. The message says which.
type PlanUnavailableError struct{ Feature string }

func (e *PlanUnavailableError) Error() string { return PlanUnavailableMessage(e.Feature) }
