package format

import (
	"fmt"
	"strings"

	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/domain/project"
	"github.com/amasok/asagao/internal/domain/task"
	"github.com/amasok/asagao/internal/domain/timetrack"
	"github.com/amasok/asagao/internal/readmodel"
)

// describers say in a few words what an outbox entry of each kind changes (see
// DescribeChange). Every kind has one (TestEveryKindIsDescribed).
var describers = map[outbox.Kind]func(op outbox.Op) string{
	outbox.KindCreateTask: func(op outbox.Op) string {
		str := payloadString(op)
		if str("parent") != "" {
			return fmt.Sprintf("サブタスク「%s」の作成", str("name"))
		}
		return fmt.Sprintf("タスク「%s」の作成", str("name"))
	},
	outbox.KindDuplicateTask: func(op outbox.Op) string {
		str := payloadString(op)
		return fmt.Sprintf("複製「%s」の作成", str("name"))
	},
	outbox.KindAddComment: func(op outbox.Op) string {
		str := payloadString(op)
		return fmt.Sprintf("コメント「%s」", clip(str("text"), 40))
	},
	outbox.KindMoveTask: func(op outbox.Op) string {
		str := payloadString(op)
		if str("insert_before") != "" || str("insert_after") != "" {
			return fmt.Sprintf("セクション「%s」への移動 (並び順の指定あり)", str("section_name"))
		}
		return fmt.Sprintf("セクション「%s」への移動", str("section_name"))
	},
	outbox.KindDeleteTask: func(op outbox.Op) string {
		return "タスクの削除"
	},
	outbox.KindSetParent: func(op outbox.Op) string {
		str := payloadString(op)
		if str("parent") == "" {
			return "親タスクから外す"
		}
		return fmt.Sprintf("「%s」のサブタスクにする", str("parent_name"))
	},
	outbox.KindAddTag: func(op outbox.Op) string {
		str := payloadString(op)
		return fmt.Sprintf("タグ「%s」の追加", str("tag_name"))
	},
	outbox.KindRemoveTag: func(op outbox.Op) string {
		str := payloadString(op)
		if str("tag_name") != "" {
			return fmt.Sprintf("タグ「%s」の削除", str("tag_name"))
		}
		return "タグの削除"
	},
	outbox.KindAddFollower: func(op outbox.Op) string {
		str := payloadString(op)
		return fmt.Sprintf("フォロワー %s の追加", str("user_name"))
	},
	outbox.KindRemoveFollower: func(op outbox.Op) string {
		str := payloadString(op)
		return fmt.Sprintf("フォロワー %s の削除", str("user_name"))
	},
	outbox.KindAddProject: func(op outbox.Op) string {
		str := payloadString(op)
		return fmt.Sprintf("プロジェクト「%s」への追加", str("project_name"))
	},
	outbox.KindRemoveProject: func(op outbox.Op) string {
		str := payloadString(op)
		return fmt.Sprintf("プロジェクト「%s」からの削除", str("project_name"))
	},
	outbox.KindUploadAttachment: func(op outbox.Op) string {
		str := payloadString(op)
		return fmt.Sprintf("添付「%s」", str("name"))
	},
	outbox.KindUpdateStory: func(op outbox.Op) string {
		str := payloadString(op)
		return fmt.Sprintf("コメントを「%s」に編集", clip(str("text"), 40))
	},
	outbox.KindDeleteStory: func(op outbox.Op) string {
		str := payloadString(op)
		return fmt.Sprintf("コメント「%s」の削除", clip(str("text"), 40))
	},
	outbox.KindDeleteAttachment: func(op outbox.Op) string {
		str := payloadString(op)
		return fmt.Sprintf("添付「%s」の削除", str("name"))
	},
	outbox.KindAddTimeEntry: func(op outbox.Op) string {
		str := payloadString(op)
		return fmt.Sprintf("作業時間 %s (%s) の記録", timetrack.Format(intOf(op.Payload["duration_minutes"])), str("entered_on"))
	},
	outbox.KindUpdateTimeEntry: func(op outbox.Op) string {
		str := payloadString(op)
		return fmt.Sprintf("作業時間を %s (%s) に変更", timetrack.Format(intOf(op.Payload["duration_minutes"])), str("entered_on"))
	},
	outbox.KindDeleteTimeEntry: func(op outbox.Op) string {
		str := payloadString(op)
		return fmt.Sprintf("作業時間 %s (%s) の削除", timetrack.Format(intOf(op.Payload["duration_minutes"])), str("entered_on"))
	},
	outbox.KindAddDependency: func(op outbox.Op) string {
		return fmt.Sprintf("「%s」を待つ", dependencyName(op))
	},
	outbox.KindRemoveDependency: func(op outbox.Op) string {
		return fmt.Sprintf("「%s」を待つのをやめる", dependencyName(op))
	},
	outbox.KindSetCustomField: func(op outbox.Op) string {
		str := payloadString(op)
		if op.Payload["value"] == nil {
			return fmt.Sprintf("「%s」を空にする", str("field_name"))
		}
		return fmt.Sprintf("「%s」を「%s」に変更", str("field_name"), clip(str("display"), 40))
	},
	outbox.KindCreateStatus: func(op outbox.Op) string {
		str := payloadString(op)
		if str("title") == "" {
			return fmt.Sprintf("ステータス更新（%s）", StatusLabel(str("status_type")))
		}
		return fmt.Sprintf("ステータス更新「%s」", str("title"))
	},
	outbox.KindMarkNotification: func(op outbox.Op) string {
		if op.Payload["read"] == true {
			return "通知を既読にする"
		}
		return "通知を未読に戻す"
	},
	outbox.KindCreateSection: func(op outbox.Op) string {
		str := payloadString(op)
		return fmt.Sprintf("セクション「%s」の作成", str("name"))
	},
	outbox.KindDeleteSection: func(op outbox.Op) string {
		str := payloadString(op)
		return fmt.Sprintf("セクション「%s」の削除", str("name"))
	},
	outbox.KindInsertSection: func(op outbox.Op) string {
		str := payloadString(op)
		return fmt.Sprintf("セクション「%s」の並べ替え", str("name"))
	},
	outbox.KindUpdateTag: func(op outbox.Op) string {
		old, _ := op.Base["name"].(string)
		if old == "" {
			old, _ = op.Payload["name"].(string)
		}
		var parts []string
		if v, ok := op.Payload["name"].(string); ok {
			parts = append(parts, fmt.Sprintf("名前を「%s」に変更", v))
		}
		if v, ok := op.Payload["color"].(string); ok {
			if v == "" {
				v = "なし"
			}
			parts = append(parts, fmt.Sprintf("色を %s に変更", v))
		}
		return fmt.Sprintf("タグ「%s」の%s", old, strings.Join(parts, "、"))
	},
	outbox.KindDeleteTag: func(op outbox.Op) string {
		str := payloadString(op)
		return fmt.Sprintf("タグ「%s」の削除", str("name"))
	},
	outbox.KindMoveMySection: func(op outbox.Op) string {
		str := payloadString(op)
		return fmt.Sprintf("マイタスクのセクション「%s」への移動", str("section_name"))
	},
	outbox.KindCreateEnumOption: func(op outbox.Op) string {
		str := payloadString(op)
		return fmt.Sprintf("カスタムフィールド「%s」に選択肢「%s」を追加", str("field_name"), str("name"))
	},
	outbox.KindAddFieldSetting: func(op outbox.Op) string {
		str := payloadString(op)
		return fmt.Sprintf("カスタムフィールド「%s」をプロジェクトに追加", str("field_name"))
	},
	outbox.KindRemoveFieldSetting: func(op outbox.Op) string {
		str := payloadString(op)
		return fmt.Sprintf("カスタムフィールド「%s」をプロジェクトから外す", str("field_name"))
	},
	outbox.KindUpdateProjectBrief: func(op outbox.Op) string {
		str := payloadString(op)
		if op.Payload["delete"] == true {
			return "ブリーフの削除"
		}
		if state, _ := op.Base["state"].(string); state != readmodel.BriefAvailable {
			return fmt.Sprintf("ブリーフ「%s」の作成", clip(str("title"), 40))
		}
		if _, ok := op.Payload["title"]; !ok { // the body only
			return "ブリーフの本文の変更"
		}
		return fmt.Sprintf("ブリーフ「%s」の変更", clip(str("title"), 40))
	},
	outbox.KindUpdateProject: func(op outbox.Op) string {
		var parts []string
		for _, k := range []string{"notes", "html_notes", "owner", "start_on", "due_on", "color"} {
			v, ok := op.Payload[k]
			if !ok {
				continue
			}
			label := project.FieldLabel(k)
			switch {
			case k == "html_notes":
				if _, plain := op.Payload["notes"]; !plain {
					parts = append(parts, "説明の変更")
				}
			case k == "notes", k == "owner" && v != "":
				parts = append(parts, label+"の変更")
			case v == "":
				parts = append(parts, label+"を外す")
			default:
				parts = append(parts, fmt.Sprintf("%sを %v に変更", label, v))
			}
		}
		return "プロジェクトの" + strings.Join(parts, "、")
	},
	outbox.KindRenameSection: func(op outbox.Op) string {
		str := payloadString(op)
		if old, _ := op.Base["name"].(string); old != "" {
			return fmt.Sprintf("セクション名を「%s」から「%s」に変更", old, str("name"))
		}
		return fmt.Sprintf("セクション名を「%s」に変更", str("name"))
	},
	outbox.KindUpdateTask: func(op outbox.Op) string {
		var parts []string
		for _, k := range []string{"name", "completed", "start_on", "due_on", "assignee", "notes", "liked", "resource_subtype", "approval_status", "custom_type", "custom_type_status_option"} {
			v, ok := op.Payload[k]
			if !ok {
				continue
			}
			switch k {
			case "name":
				parts = append(parts, fmt.Sprintf("名前を「%s」に変更", v))
			case "completed":
				parts = append(parts, map[bool]string{true: "完了にする", false: "未完了に戻す"}[v == true])
			case "due_on":
				if v == "" {
					parts = append(parts, "期日を削除")
				} else {
					parts = append(parts, fmt.Sprintf("期日を %s に変更", v))
				}
			case "start_on":
				if v == "" {
					parts = append(parts, "開始日を削除")
				} else {
					parts = append(parts, fmt.Sprintf("開始日を %s に変更", v))
				}
			case "assignee":
				if v == "" {
					parts = append(parts, "担当者を外す")
				} else {
					parts = append(parts, "担当者の変更")
				}
			case "notes":
				parts = append(parts, "説明の変更")
			case "resource_subtype":
				if _, typ := op.Payload["custom_type"]; !typ || v != task.SubtypeCustom { // else said by custom_type
					parts = append(parts, fmt.Sprintf("種類を「%s」に変更", task.SubtypeLabel(fmt.Sprint(v))))
				}
			case "custom_type":
				parts = append(parts, "種類をカスタムタイプに変更")
			case "custom_type_status_option":
				parts = append(parts, "ステータスの変更")
			case "approval_status":
				parts = append(parts, fmt.Sprintf("承認を「%s」に変更", task.ApprovalLabel(fmt.Sprint(v))))
			case "liked":
				if v == true {
					parts = append(parts, "いいね")
				} else {
					parts = append(parts, "いいねを外す")
				}
			}
		}
		if len(parts) == 0 && op.Payload["html_notes"] != nil {
			parts = append(parts, "説明の変更")
		}
		return strings.Join(parts, "、")
	},
}

func describeChange(op outbox.Op) string {
	if d, ok := describers[op.Kind]; ok {
		return d(op)
	}
	return string(op.Kind) // an entry of a newer version
}

// payloadString returns a function that reads a string of op's payload ("" if none).
func payloadString(op outbox.Op) func(k string) string {
	return func(k string) string { v, _ := op.Payload[k].(string); return v }
}
