package op

import (
	"context"
	"fmt"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

// SiteBatchEdit 将一组修改补丁（添加/移除标签、设置/删除 Header）逐站点合并落库，
// 每站点一次加载、一次更新；单站失败计入 FailedItems 后继续。
// 标签先添加后移除（同名时移除优先），传入的标签需已规范化。
// 返回结果与需要刷新投影的站点 ID（仅 Header 变更影响投影）。
func SiteBatchEdit(req *model.SiteBatchEditRequest, ctx context.Context) (*model.SiteBatchResult, []int, error) {
	if req == nil {
		return nil, nil, fmt.Errorf("site batch edit request is nil")
	}
	editTags := len(req.AddTags) > 0 || len(req.RemoveTags) > 0
	editHeader := len(req.Upserts) > 0 || len(req.DeleteKeys) > 0
	if !editTags && !editHeader {
		return nil, nil, fmt.Errorf("nothing to edit")
	}
	removed := make(map[string]struct{}, len(req.RemoveTags))
	for _, tag := range req.RemoveTags {
		removed[tag] = struct{}{}
	}
	result := &model.SiteBatchResult{
		SuccessIDs:  make([]int, 0, len(req.IDs)),
		FailedItems: make([]model.SiteBatchFailure, 0),
	}
	affected := make([]int, 0, len(req.IDs))
	for _, id := range req.IDs {
		err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var site model.Site
			if err := tx.First(&site, id).Error; err != nil {
				return fmt.Errorf("site not found")
			}
			columns := make([]string, 0, 2)
			updates := model.Site{ID: id}
			if editTags {
				merged := make([]string, 0, len(site.Tags)+len(req.AddTags))
				for _, tag := range site.Tags {
					if _, ok := removed[tag]; !ok {
						merged = append(merged, tag)
					}
				}
				for _, tag := range req.AddTags {
					if _, ok := removed[tag]; !ok {
						merged = append(merged, tag)
					}
				}
				next := model.NormalizeSiteTags(merged)
				if err := model.ValidateSiteTags(next); err != nil {
					return err
				}
				updates.Tags = next
				columns = append(columns, "tags")
			}
			if editHeader {
				updates.CustomHeader = mergeHeaders(site.CustomHeader, req.Upserts, req.DeleteKeys)
				columns = append(columns, "custom_header")
			}
			return tx.Model(&model.Site{}).
				Where("id = ?", id).
				Select(columns).
				Updates(&updates).Error
		})
		if err != nil {
			result.FailedItems = append(result.FailedItems, model.SiteBatchFailure{ID: id, Message: err.Error()})
			continue
		}
		result.SuccessIDs = append(result.SuccessIDs, id)
		if editHeader {
			affected = append(affected, id)
		}
	}
	return result, affected, nil
}

// SiteBatchApply 对一组站点逐个执行批量动作（enable/disable/delete），
// 单站失败计入 FailedItems 后继续。deleteSite 由调用方注入，避免 op 反向依赖站点同步层。
// 返回结果与需要刷新投影的站点 ID（仅 enable/disable 影响投影）。
func SiteBatchApply(req *model.SiteBatchRequest, deleteSite func(context.Context, int) error, ctx context.Context) (*model.SiteBatchResult, []int, error) {
	if req == nil {
		return nil, nil, fmt.Errorf("site batch request is nil")
	}
	result := &model.SiteBatchResult{
		SuccessIDs:  make([]int, 0, len(req.IDs)),
		FailedItems: make([]model.SiteBatchFailure, 0),
	}
	for _, id := range req.IDs {
		var err error
		switch req.Action {
		case "enable":
			err = SiteEnabled(id, true, ctx)
		case "disable":
			err = SiteEnabled(id, false, ctx)
		case "delete":
			err = deleteSite(ctx, id)
		default:
			err = fmt.Errorf("invalid action: %s", req.Action)
		}
		if err != nil {
			result.FailedItems = append(result.FailedItems, model.SiteBatchFailure{ID: id, Message: err.Error()})
			continue
		}
		result.SuccessIDs = append(result.SuccessIDs, id)
	}
	var affected []int
	if req.Action == "enable" || req.Action == "disable" {
		affected = result.SuccessIDs
	}
	return result, affected, nil
}
