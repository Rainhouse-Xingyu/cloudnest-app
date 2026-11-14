package mysql

import (
	"errors"
	"fmt"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var workspaceReadRoles = []string{"admin", "editor", "viewer", "member"}
var workspaceWriteRoles = []string{"admin", "editor"}

// Legacy workspaces have no root and retain their original file ACLs.
func fileAccessScope(db *gorm.DB, alias string, uid int64, write bool) *gorm.DB {
	roles, permissions := workspaceReadRoles, []string{"read", "write"}
	if write {
		roles, permissions = workspaceWriteRoles, []string{"write"}
	}
	return db.Where(fmt.Sprintf(`(
		EXISTS (SELECT 1 FROM workspace w WHERE w.id=%[1]s.workspace_id AND w.root_file_id IS NOT NULL
			AND (w.owner_id=? OR EXISTS (SELECT 1 FROM workspace_user wm WHERE wm.workspace_id=w.id AND wm.user_id=? AND wm.role IN ?)))
		OR (NOT EXISTS (SELECT 1 FROM workspace w WHERE w.id=%[1]s.workspace_id AND w.root_file_id IS NOT NULL)
			AND (%[1]s.user_id=? OR EXISTS (SELECT 1 FROM file_permission p WHERE p.file_id=%[1]s.id AND p.user_id=? AND p.permission IN ?)))
	)`, alias), uid, uid, roles, uid, uid, permissions)
}

func fileWorkspace(tx *gorm.DB, file model.File) (model.Workspace, error) {
	var ws model.Workspace
	if file.WorkspaceID == nil {
		return ws, nil
	}
	err := tx.First(&ws, "id=? AND root_file_id IS NOT NULL", *file.WorkspaceID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ws, nil
	}
	return ws, err
}

func requireWorkspaceRole(tx *gorm.DB, uid int64, ws model.Workspace, write bool) error {
	if ws.OwnerID == uid {
		return nil
	}
	roles := workspaceReadRoles
	if write {
		roles = workspaceWriteRoles
	}
	var member model.WorkspaceUser
	err := tx.Clauses(clause.Locking{Strength: "SHARE"}).
		First(&member, "workspace_id=? AND user_id=? AND role IN ?", ws.ID, uid, roles).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return contract.ErrForbidden
	}
	return err
}

func destinationWorkspace(tx *gorm.DB, parentID int64) (model.Workspace, error) {
	if parentID == 0 {
		return model.Workspace{}, nil
	}
	var parent model.File
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&parent, parentID).Error; err != nil {
		return model.Workspace{}, err
	}
	return fileWorkspace(tx, parent)
}

func requireMutableWorkspaceEntry(tx *gorm.DB, uid int64, file model.File) error {
	ws, err := fileWorkspace(tx, file)
	if err != nil {
		return err
	}
	if ws.ID == 0 {
		return requireFilePermission(tx, uid, file, true)
	}
	if *ws.RootFileID == file.ID {
		return contract.ErrForbidden
	}
	return requireWorkspaceRole(tx, uid, ws, true)
}
