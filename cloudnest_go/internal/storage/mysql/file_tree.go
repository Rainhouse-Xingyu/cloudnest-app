package mysql

import (
	"context"
	"database/sql"
	"sort"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxFileTreeNodes = 10000
const maxFileTreeDepth = 256

type fileTreeNode struct {
	File  model.File
	Depth int
}

// Collect in parent-before-child order. Repeated nodes (cycles or overlapping
// roots) and oversized trees fail before any mutation or archive is published.
func collectFileTree(tx *gorm.DB, rootIDs []int64, rootDeleted int8, includeDeleted, lockRows bool) ([]fileTreeNode, error) {
	ids := uniqueInt64ForFileRepo(rootIDs)
	if len(ids) == 0 || len(ids) > maxFileTreeNodes {
		return nil, contract.ErrInvalidInput
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	nodes := make([]fileTreeNode, 0, len(ids))
	seen := make(map[int64]bool)
	query := tx
	if lockRows {
		query = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Session(&gorm.Session{})
	}
	for _, id := range ids {
		if id <= 0 {
			return nil, contract.ErrInvalidInput
		}
		var root model.File
		if err := query.First(&root, "id = ? AND is_deleted = ?", id, rootDeleted).Error; err != nil {
			return nil, err
		}
		seen[id] = true
		nodes = append(nodes, fileTreeNode{File: root})
	}
	for i := 0; i < len(nodes); i++ {
		node := nodes[i]
		q := query.Where("parent_id = ?", node.File.ID)
		if !includeDeleted {
			q = q.Where("is_deleted = 0")
		}
		var children []model.File
		if err := q.Order("id").Limit(maxFileTreeNodes - len(nodes) + 1).Find(&children).Error; err != nil {
			return nil, err
		}
		if len(children) > 0 && (node.File.IsFolder != 1 || node.Depth+1 >= maxFileTreeDepth) {
			return nil, contract.ErrConflict
		}
		for _, child := range children {
			if seen[child.ID] || len(nodes) >= maxFileTreeNodes {
				return nil, contract.ErrConflict
			}
			seen[child.ID] = true
			nodes = append(nodes, fileTreeNode{File: child, Depth: node.Depth + 1})
		}
	}
	return nodes, nil
}

func treeNamespaceKeys(nodes []fileTreeNode) []string {
	keys := make([]string, 0, len(nodes)*2)
	for _, node := range nodes {
		keys = append(keys, fileNamespaceKey(node.File.UserID, node.File.ParentID))
		if node.File.IsFolder == 1 {
			keys = append(keys, fileNamespaceKey(node.File.UserID, node.File.ID))
		}
	}
	return keys
}

// All metadata writers use namespace guards. After locking the observed closure,
// re-read with row locks and reject any new namespace rather than operating on an
// unprotected branch. READ COMMITTED sees writers that finished while we waited.
func lockFileTree(tx *gorm.DB, rootIDs []int64, deleted int8, includeDeleted bool, extraKeys ...string) ([]fileTreeNode, error) {
	snapshot, err := collectFileTree(tx, rootIDs, deleted, includeDeleted, false)
	if err != nil {
		return nil, err
	}
	keys := append(treeNamespaceKeys(snapshot), extraKeys...)
	protected := make(map[string]bool, len(keys))
	for _, key := range keys {
		protected[key] = true
	}
	if err := lockFileNamespaces(tx, keys...); err != nil {
		return nil, err
	}
	current, err := collectFileTree(tx, rootIDs, deleted, includeDeleted, true)
	if err != nil {
		return nil, err
	}
	for _, key := range treeNamespaceKeys(current) {
		if !protected[key] {
			return nil, contract.ErrConflict
		}
	}
	return current, nil
}

// Archives use one consistent metadata/ACL snapshot, without locking a tree for
// the duration of disk compression. Permission changes affect subsequent requests.
func fileReadSnapshot(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	return db.WithContext(ctx).Transaction(fn, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
}

func requireTreeRead(tx *gorm.DB, uid int64, nodes []fileTreeNode) error {
	ids := make([]int64, 0, len(nodes))
	for _, node := range nodes {
		if node.File.UserID != uid {
			ids = append(ids, node.File.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var grants []model.FilePermission
	if err := tx.Where("file_id IN ? AND user_id = ? AND permission IN ?", ids, uid, []string{"read", "write"}).Find(&grants).Error; err != nil {
		return err
	}
	allowed := make(map[int64]bool, len(grants))
	for _, grant := range grants {
		allowed[grant.FileID] = true
	}
	for _, id := range ids {
		if !allowed[id] {
			return contract.ErrForbidden
		}
	}
	return nil
}
