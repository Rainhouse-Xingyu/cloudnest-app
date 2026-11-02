//go:build integration && aisearch

package integration

import (
	"cloudenest_go/internal/indexqueue"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"strings"
	"testing"
)

func TestAIIndexIncrementalVersionsInvalidatePreviousContent(t *testing.T) {
	f, q := indexFixture(t)
	a := f.upload(t, f.alice, "neutral.md", []byte("original needlealpha"))
	lexicalIndexFixture(t, f, a, "original needlealpha")
	b := f.upload(t, f.alice, "replacement.md", []byte("new needlebeta"))
	lexicalIndexFixture(t, f, b, "new needlebeta")
	// A worker holding the previous source cannot publish after a version write.
	f.json(t, "POST", "/api/ai/index/rebuild", f.alice, rebuildBody(contract.AIRebuildItem{FileID: a.ID, Generation: 1}), 202)
	old := claimIndex(t, q)
	base := fmt.Sprintf("/api/files/%d", a.ID)
	f.json(t, "POST", base+"/versions", f.alice, map[string]any{"storage_id": b.StorageID, "base_version": 1}, 200)
	j := readIndex(t, f, a.ID)
	if j.Status != "queued" || j.Generation != 3 || j.SourceHash != readIndex(t, f, b.ID).SourceHash {
		t.Fatal(j)
	}
	if err := q.Publish(context.Background(), old, func(*gorm.DB) error { return nil }); !errors.Is(err, indexqueue.ErrSourceChanged) {
		t.Fatal("old version published", err)
	}
	if r := hybridQuery(t, f, f.alice, "needlealpha"); len(r.Items) != 0 {
		t.Fatal("old version remained searchable", r)
	}
	lexicalIndexFixture(t, f, a, "new needlebeta")
	f.json(t, "POST", base+"/versions/1/restore", f.alice, map[string]any{"base_version": 2}, 200)
	if j := readIndex(t, f, a.ID); j.Generation != 4 || j.Status != "queued" || j.SourceHash != old.SourceHash {
		t.Fatal("version restore not queued", j)
	}
	lexicalIndexFixture(t, f, a, "original needlealpha")
	if r := hybridQuery(t, f, f.alice, "needlealpha"); len(r.Items) != 1 || r.Items[0].ID != a.ID {
		t.Fatal("restored source not searchable", r)
	}
}

func TestAIIndexIncrementalCopiesHaveIndependentJobsAndACL(t *testing.T) {
	f, _ := indexFixture(t)
	folder := fixtureFolder(t, f, f.alice, "copy-tree", 0)
	file := f.upload(t, f.alice, "nested.md", []byte("copyneedle"))
	f.json(t, "POST", "/api/files/move", f.alice, map[string]any{"file_ids": []int64{file.ID}, "target_parent_id": folder.ID}, 200)
	lexicalIndexFixture(t, f, file, "copyneedle")
	f.json(t, "POST", "/api/files/copy", f.alice, map[string]any{"file_ids": []int64{folder.ID}}, 200)
	var copied model.File
	if err := f.db.Where("user_id=? AND storage_id=? AND id<>?", f.aliceID, file.StorageID, file.ID).First(&copied).Error; err != nil {
		t.Fatal(err)
	}
	j := readIndex(t, f, copied.ID)
	if j.Status != "queued" || j.Generation != 1 || j.SourceHash != readIndex(t, f, file.ID).SourceHash {
		t.Fatal(j)
	}
	if indexStatusAPI(t, f, f.alice, copied.ID).IndexedCurrent {
		t.Fatal("copy reused another file's index")
	}
	lexicalIndexFixture(t, f, copied, "copyneedle")
	f.json(t, "DELETE", fmt.Sprintf("/api/files/%d", file.ID), f.alice, nil, 200)
	f.json(t, "DELETE", fmt.Sprintf("/api/files/%d/permanent", file.ID), f.alice, nil, 200)
	if r := hybridQuery(t, f, f.alice, "copyneedle"); len(r.Items) != 1 || r.Items[0].ID != copied.ID {
		t.Fatal(r)
	}
	if r := hybridQuery(t, f, f.bob, "copyneedle"); len(r.Items) != 0 {
		t.Fatal("dedup source granted permission", r)
	}
	var folderJobs int64
	if err := f.db.Table("ai_index_job j").Joins("JOIN file f ON f.id=j.file_id").Where("f.is_folder=1").Count(&folderJobs).Error; err != nil || folderJobs != 0 {
		t.Fatal(folderJobs, err)
	}
}

func TestAIIndexIncrementalRenameAndRecycleRestoreFenceWorker(t *testing.T) {
	f, q := indexFixture(t)
	file := f.upload(t, f.alice, "original.bin", []byte("renameneedle"))
	j := claimIndex(t, q)
	base := fmt.Sprintf("/api/files/%d", file.ID)
	f.json(t, "PUT", base+"/rename", f.alice, map[string]any{"name": "readable.md"}, 200)
	if err := q.Publish(context.Background(), j, func(*gorm.DB) error { return nil }); !errors.Is(err, indexqueue.ErrLostLease) {
		t.Fatal("suffix change did not fence worker", err)
	}
	if j := readIndex(t, f, file.ID); j.Generation != 2 || j.Status != "queued" {
		t.Fatal(j)
	}
	var renamed model.File
	if err := f.db.First(&renamed, file.ID).Error; err != nil || renamed.FileType != "md" {
		t.Fatal("stale file_type", renamed, err)
	}
	lexicalIndexFixture(t, f, file, "renameneedle")
	f.json(t, "PUT", base+"/rename", f.alice, map[string]any{"name": "another.md"}, 200)
	if j := readIndex(t, f, file.ID); j.Generation != 2 || j.Status != "done" {
		t.Fatal("basename rename reembedded", j)
	}
	f.json(t, "DELETE", base, f.alice, nil, 200)
	if r := hybridQuery(t, f, f.alice, "renameneedle"); len(r.Items) != 0 {
		t.Fatal("recycled content searched", r)
	}
	f.json(t, "POST", base+"/restore", f.alice, nil, 200)
	if j := readIndex(t, f, file.ID); j.Generation != 3 || j.Status != "queued" {
		t.Fatal("restore not scheduled", j)
	}
	if r := hybridQuery(t, f, f.alice, "renameneedle"); len(r.Items) != 0 {
		t.Fatal("restore exposed previous generation", r)
	}
	lexicalIndexFixture(t, f, file, "renameneedle")
	if r := hybridQuery(t, f, f.alice, "renameneedle"); len(r.Items) != 1 {
		t.Fatal(r)
	}
	f.json(t, "PUT", base+"/rename", f.alice, map[string]any{"name": "long." + strings.Repeat("x", 51)}, 200)
	if err := f.db.First(&renamed, file.ID).Error; err != nil || renamed.FileType != "" || readIndex(t, f, file.ID).Generation != 4 {
		t.Fatal("long suffix broke rename", renamed, err)
	}
}

func TestAIIndexIncrementalQueueFailureRollsBackFileMutation(t *testing.T) {
	f, _ := indexFixture(t)
	a := f.upload(t, f.alice, "atomic.md", []byte("original"))
	b := f.upload(t, f.alice, "source.md", []byte("replacement"))
	base := fmt.Sprintf("/api/files/%d", a.ID)
	const hook = "test:index-mutation-failure"
	inject := func(tx *gorm.DB) {
		if tx.Statement.Table == "ai_index_job" {
			tx.AddError(errors.New("injected index failure"))
		}
	}
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, inject); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Callback().Update().Before("gorm:update").Register(hook, inject); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Callback().Create().Remove(hook); f.db.Callback().Update().Remove(hook) })
	f.json(t, "PUT", base+"/rename", f.alice, map[string]any{"name": "atomic.txt"}, 500)
	f.json(t, "POST", base+"/versions", f.alice, map[string]any{"storage_id": b.StorageID, "base_version": 1}, 500)
	f.json(t, "POST", "/api/files/copy", f.alice, map[string]any{"file_ids": []int64{a.ID}}, 500)
	var current model.File
	if err := f.db.First(&current, a.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.Filename != a.Filename || current.CurrentVersion != 1 || *current.StorageID != *a.StorageID {
		t.Fatal("partial mutation committed", current)
	}
	var count int64
	if err := f.db.Model(&model.File{}).Where("user_id=?", f.aliceID).Count(&count).Error; err != nil || count != 2 {
		t.Fatal("failed copy left a file", count, err)
	}
	f.json(t, "DELETE", base, f.alice, nil, 200)
	f.json(t, "POST", base+"/restore", f.alice, nil, 500)
	if err := f.db.First(&current, a.ID).Error; err != nil || current.IsDeleted != 1 {
		t.Fatal("failed restore committed", current, err)
	}
	if readIndex(t, f, a.ID).Generation != 1 {
		t.Fatal("failed mutation changed job")
	}
}
