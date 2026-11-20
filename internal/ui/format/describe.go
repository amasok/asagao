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
// DescribeChange). Every kind has one (TestEveryKindIsDescribed). Each reads the entry's
// payload as its kind's struct, as far as it reads (outbox.BestEffortPayload): an entry
// this version cannot send is still described.
var describers = map[outbox.Kind]func(op outbox.Op) string{}

// describe sets the describer of kind k, whose payload struct is P.
func describe[P any](k outbox.Kind, f func(op outbox.Op, p P) string) {
	outbox.CheckPayloadType[P](k)
	describers[k] = func(op outbox.Op) string {
		p, _ := outbox.BestEffortPayload[P](op)
		return f(op, p)
	}
}

func init() {
	describe(outbox.KindCreateTask, func(_ outbox.Op, p outbox.CreateTask) string {
		if p.Parent != "" {
			return fmt.Sprintf("サブタスク「%s」の作成", p.Name)
		}
		return fmt.Sprintf("タスク「%s」の作成", p.Name)
	})
	describe(outbox.KindDuplicateTask, func(_ outbox.Op, p outbox.DuplicateTask) string {
		return fmt.Sprintf("複製「%s」の作成", p.Name)
	})
	describe(outbox.KindAddComment, func(_ outbox.Op, p outbox.AddComment) string {
		return fmt.Sprintf("コメント「%s」", clip(p.Text, 40))
	})
	describe(outbox.KindMoveTask, func(_ outbox.Op, p outbox.MoveTask) string {
		if p.InsertBefore != "" || p.InsertAfter != "" {
			return fmt.Sprintf("セクション「%s」への移動 (並び順の指定あり)", p.SectionName)
		}
		return fmt.Sprintf("セクション「%s」への移動", p.SectionName)
	})
	describe(outbox.KindDeleteTask, func(outbox.Op, outbox.DeleteTask) string {
		return "タスクの削除"
	})
	describe(outbox.KindSetParent, func(_ outbox.Op, p outbox.SetParent) string {
		if p.Parent == "" {
			return "親タスクから外す"
		}
		return fmt.Sprintf("「%s」のサブタスクにする", p.ParentName)
	})
	describe(outbox.KindAddTag, func(_ outbox.Op, p outbox.AddTag) string {
		return fmt.Sprintf("タグ「%s」の追加", p.TagName)
	})
	describe(outbox.KindRemoveTag, func(_ outbox.Op, p outbox.RemoveTag) string {
		if p.TagName != "" {
			return fmt.Sprintf("タグ「%s」の削除", p.TagName)
		}
		return "タグの削除"
	})
	describe(outbox.KindAddFollower, func(_ outbox.Op, p outbox.Follower) string {
		return fmt.Sprintf("フォロワー %s の追加", p.UserName)
	})
	describe(outbox.KindRemoveFollower, func(_ outbox.Op, p outbox.Follower) string {
		return fmt.Sprintf("フォロワー %s の削除", p.UserName)
	})
	describe(outbox.KindAddProject, func(_ outbox.Op, p outbox.ProjectMembership) string {
		return fmt.Sprintf("プロジェクト「%s」への追加", p.ProjectName)
	})
	describe(outbox.KindRemoveProject, func(_ outbox.Op, p outbox.ProjectMembership) string {
		return fmt.Sprintf("プロジェクト「%s」からの削除", p.ProjectName)
	})
	describe(outbox.KindUploadAttachment, func(_ outbox.Op, p outbox.UploadAttachment) string {
		return fmt.Sprintf("添付「%s」", p.Name)
	})
	describe(outbox.KindUpdateStory, func(_ outbox.Op, p outbox.UpdateStory) string {
		return fmt.Sprintf("コメントを「%s」に編集", clip(p.Text, 40))
	})
	describe(outbox.KindDeleteStory, func(_ outbox.Op, p outbox.DeleteStory) string {
		return fmt.Sprintf("コメント「%s」の削除", clip(p.Text, 40))
	})
	describe(outbox.KindDeleteAttachment, func(_ outbox.Op, p outbox.DeleteAttachment) string {
		return fmt.Sprintf("添付「%s」の削除", p.Name)
	})
	describe(outbox.KindAddTimeEntry, func(_ outbox.Op, p outbox.TimeEntry) string {
		return fmt.Sprintf("作業時間 %s (%s) の記録", timetrack.Format(p.DurationMinutes), p.EnteredOn)
	})
	describe(outbox.KindUpdateTimeEntry, func(_ outbox.Op, p outbox.TimeEntry) string {
		return fmt.Sprintf("作業時間を %s (%s) に変更", timetrack.Format(p.DurationMinutes), p.EnteredOn)
	})
	describe(outbox.KindDeleteTimeEntry, func(_ outbox.Op, p outbox.TimeEntry) string {
		return fmt.Sprintf("作業時間 %s (%s) の削除", timetrack.Format(p.DurationMinutes), p.EnteredOn)
	})
	describe(outbox.KindAddDependency, func(_ outbox.Op, p outbox.Dependency) string {
		return fmt.Sprintf("「%s」を待つ", dependencyName(p))
	})
	describe(outbox.KindRemoveDependency, func(_ outbox.Op, p outbox.Dependency) string {
		return fmt.Sprintf("「%s」を待つのをやめる", dependencyName(p))
	})
	describe(outbox.KindSetCustomField, func(_ outbox.Op, p outbox.SetCustomField) string {
		if p.Value == nil {
			return fmt.Sprintf("「%s」を空にする", p.FieldName)
		}
		return fmt.Sprintf("「%s」を「%s」に変更", p.FieldName, clip(p.Display, 40))
	})
	describe(outbox.KindCreateStatus, func(_ outbox.Op, p outbox.CreateStatus) string {
		if p.Title == "" {
			return fmt.Sprintf("ステータス更新（%s）", StatusLabel(p.StatusType))
		}
		return fmt.Sprintf("ステータス更新「%s」", p.Title)
	})
	describe(outbox.KindMarkNotification, func(_ outbox.Op, p outbox.MarkNotification) string {
		if p.Read {
			return "通知を既読にする"
		}
		return "通知を未読に戻す"
	})
	describe(outbox.KindCreateSection, func(_ outbox.Op, p outbox.CreateSection) string {
		return fmt.Sprintf("セクション「%s」の作成", p.Name)
	})
	describe(outbox.KindDeleteSection, func(_ outbox.Op, p outbox.DeleteSection) string {
		return fmt.Sprintf("セクション「%s」の削除", p.Name)
	})
	describe(outbox.KindInsertSection, func(_ outbox.Op, p outbox.InsertSection) string {
		return fmt.Sprintf("セクション「%s」の並べ替え", p.Name)
	})
	describe(outbox.KindUpdateTag, func(op outbox.Op, p outbox.UpdateTag) string {
		old, _ := op.Base["name"].(string)
		if old == "" {
			old = outbox.Str(p.Name)
		}
		var parts []string
		if p.Name != nil {
			parts = append(parts, fmt.Sprintf("名前を「%s」に変更", *p.Name))
		}
		if p.Color != nil {
			v := *p.Color
			if v == "" {
				v = "なし"
			}
			parts = append(parts, fmt.Sprintf("色を %s に変更", v))
		}
		return fmt.Sprintf("タグ「%s」の%s", old, strings.Join(parts, "、"))
	})
	describe(outbox.KindDeleteTag, func(_ outbox.Op, p outbox.DeleteTag) string {
		return fmt.Sprintf("タグ「%s」の削除", p.Name)
	})
	describe(outbox.KindMoveMySection, func(_ outbox.Op, p outbox.MoveMySection) string {
		return fmt.Sprintf("マイタスクのセクション「%s」への移動", p.SectionName)
	})
	describe(outbox.KindCreateEnumOption, func(_ outbox.Op, p outbox.CreateEnumOption) string {
		return fmt.Sprintf("カスタムフィールド「%s」に選択肢「%s」を追加", p.FieldName, p.Name)
	})
	describe(outbox.KindAddFieldSetting, func(_ outbox.Op, p outbox.FieldSetting) string {
		return fmt.Sprintf("カスタムフィールド「%s」をプロジェクトに追加", p.FieldName)
	})
	describe(outbox.KindRemoveFieldSetting, func(_ outbox.Op, p outbox.FieldSetting) string {
		return fmt.Sprintf("カスタムフィールド「%s」をプロジェクトから外す", p.FieldName)
	})
	describe(outbox.KindUpdateProjectBrief, func(op outbox.Op, p outbox.UpdateProjectBrief) string {
		if p.Delete {
			return "ブリーフの削除"
		}
		if state, _ := op.Base["state"].(string); state != readmodel.BriefAvailable {
			return fmt.Sprintf("ブリーフ「%s」の作成", clip(outbox.Str(p.Title), 40))
		}
		if p.Title == nil { // the body only
			return "ブリーフの本文の変更"
		}
		return fmt.Sprintf("ブリーフ「%s」の変更", clip(*p.Title, 40))
	})
	describe(outbox.KindUpdateProject, func(_ outbox.Op, p outbox.UpdateProject) string {
		var parts []string
		for _, f := range []struct {
			key string
			v   *string
		}{{"notes", p.Notes}, {"html_notes", p.HTMLNotes}, {"owner", p.Owner}, {"start_on", p.StartOn}, {"due_on", p.DueOn}, {"color", p.Color}} {
			if f.v == nil {
				continue
			}
			k, v := f.key, *f.v
			label := project.FieldLabel(k)
			switch {
			case k == "html_notes":
				if p.Notes == nil {
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
	})
	describe(outbox.KindRenameSection, func(op outbox.Op, p outbox.RenameSection) string {
		if old, _ := op.Base["name"].(string); old != "" {
			return fmt.Sprintf("セクション名を「%s」から「%s」に変更", old, p.Name)
		}
		return fmt.Sprintf("セクション名を「%s」に変更", p.Name)
	})
	describe(outbox.KindUpdateTask, func(_ outbox.Op, p outbox.UpdateTask) string {
		var parts []string
		if p.Name != nil {
			parts = append(parts, fmt.Sprintf("名前を「%s」に変更", *p.Name))
		}
		if p.Completed != nil {
			parts = append(parts, map[bool]string{true: "完了にする", false: "未完了に戻す"}[*p.Completed])
		}
		if p.StartOn != nil {
			if *p.StartOn == "" {
				parts = append(parts, "開始日を削除")
			} else {
				parts = append(parts, fmt.Sprintf("開始日を %s に変更", *p.StartOn))
			}
		}
		if p.DueOn != nil {
			if *p.DueOn == "" {
				parts = append(parts, "期日を削除")
			} else {
				parts = append(parts, fmt.Sprintf("期日を %s に変更", *p.DueOn))
			}
		}
		if p.Assignee != nil {
			if *p.Assignee == "" {
				parts = append(parts, "担当者を外す")
			} else {
				parts = append(parts, "担当者の変更")
			}
		}
		if p.Notes != nil {
			parts = append(parts, "説明の変更")
		}
		if p.Liked != nil {
			if *p.Liked {
				parts = append(parts, "いいね")
			} else {
				parts = append(parts, "いいねを外す")
			}
		}
		if v := p.ResourceSubtype; v != nil && (p.CustomType == nil || *v != task.SubtypeCustom) { // else said by custom_type
			parts = append(parts, fmt.Sprintf("種類を「%s」に変更", task.SubtypeLabel(*v)))
		}
		if p.ApprovalStatus != nil {
			parts = append(parts, fmt.Sprintf("承認を「%s」に変更", task.ApprovalLabel(*p.ApprovalStatus)))
		}
		if p.CustomType != nil {
			parts = append(parts, "種類をカスタムタイプに変更")
		}
		if p.CustomTypeStatusOption != nil {
			parts = append(parts, "ステータスの変更")
		}
		if len(parts) == 0 && p.HTMLNotes != nil {
			parts = append(parts, "説明の変更")
		}
		return strings.Join(parts, "、")
	})
}

func describeChange(op outbox.Op) string {
	if d, ok := describers[op.Kind]; ok {
		return d(op)
	}
	return string(op.Kind) // an entry of a newer version
}
