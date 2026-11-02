//go:build integration

package integration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"cloudenest_go/internal/blob"
	"cloudenest_go/internal/indexqueue"
	"cloudenest_go/internal/store"
	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Worker tests need an empty queue, whereas the shared disposable database keeps
// previous API fixtures. Only this new queue's synthetic rows are cleared; the
// normal fixture enforces loopback/container-only access before this helper runs.
func indexFixture(t *testing.T) (*fixture, *indexqueue.Queue) {
	t.Helper()
	f := newFixture(t)
	if err := f.db.Where("1=1").Delete(&model.AIIndexJob{}).Error; err != nil {
		t.Fatal(err)
	}
	return f, indexqueue.New(f.db)
}

func claimIndex(t *testing.T, q *indexqueue.Queue) model.AIIndexJob {
	t.Helper()
	j, err := q.Claim(context.Background())
	if err != nil || j == nil {
		t.Fatal("expected durable claim", j, err)
	}
	return *j
}

func expireIndex(t *testing.T, f *fixture, j model.AIIndexJob) {
	t.Helper()
	if err := f.db.Model(&model.AIIndexJob{}).Where("file_id=?", j.FileID).Update("lease_until", gorm.Expr("TIMESTAMPADD(SECOND,-1,UTC_TIMESTAMP(6))")).Error; err != nil {
		t.Fatal(err)
	}
}

func readIndex(t *testing.T, f *fixture, id int64) model.AIIndexJob {
	t.Helper()
	var j model.AIIndexJob
	if err := f.db.First(&j, "file_id=?", id).Error; err != nil {
		t.Fatal(err)
	}
	return j
}

func TestAIIndexUploadAndQueueCommitTogether(t *testing.T) {
	f, _ := indexFixture(t)
	id := fixturePendingUpload(t, f, f.alice, "atomic-index.txt", []byte("durable outbox"))
	const hook = "test:index-enqueue-failure"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "ai_index_job" {
			tx.AddError(errors.New("injected outbox write failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Callback().Create().Remove(hook) })
	reply := completeUploadRecorder(f, f.alice, id)
	if reply.Code != 500 {
		t.Fatalf("outbox failure did not fail upload: %d %s", reply.Code, reply.Body.String())
	}
	task := receiptTask(t, f, id)
	if task.Status != 0 || task.ResultFileID != nil || task.ReservedBytes == 0 {
		t.Fatal("outbox failure committed receipt/quota")
	}
	var count int64
	if err := f.db.Model(&model.File{}).Where("user_id=? AND filename=?", f.aliceID, "atomic-index.txt").Count(&count).Error; err != nil || count != 0 {
		t.Fatal("file escaped rollback", count, err)
	}
	if err := f.db.Model(&model.AIIndexJob{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("job escaped rollback", count, err)
	}
	f.db.Callback().Create().Remove(hook)
	original := completeUploadRecorder(f, f.alice, id)
	file := completedBlob(t, f, original)
	job := readIndex(t, f, file.ID)
	var storage model.Storage
	if err := f.db.First(&storage, *file.StorageID).Error; err != nil {
		t.Fatal(err)
	}
	if job.SourceHash != storage.FileHash || job.Pipeline != indexqueue.Pipeline {
		t.Fatal("wrong source identity")
	}
	assertReceiptReply(t, f, id, original)
	after := readIndex(t, f, file.ID)
	if after.Generation != job.Generation || !after.UpdatedAt.Equal(job.UpdatedAt) {
		t.Fatal("receipt rescheduled index")
	}
}

func TestAIIndexOnlyOneConcurrentWorkerClaimsFile(t *testing.T) {
	f, _ := indexFixture(t)
	file := f.upload(t, f.alice, "claim.txt", []byte("concurrent queue"))
	const workers = 16
	claims := make(chan *model.AIIndexJob, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, err := indexqueue.New(f.db).Claim(context.Background())
			claims <- j
			errs <- err
		}()
	}
	wg.Wait()
	close(claims)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for j := range claims {
		if j != nil {
			n++
			if j.FileID != file.ID || j.Status != "running" || j.Attempts != 1 || len(j.LeaseToken) != 64 || j.LeaseUntil == nil {
				t.Fatalf("invalid claim: %+v", j)
			}
		}
	}
	if n != 1 {
		t.Fatalf("got %d workers for one job", n)
	}
}

func TestAIIndexCrashReclaimFencesFormerWorker(t *testing.T) {
	f, q := indexFixture(t)
	f.upload(t, f.alice, "crash.txt", []byte("retry after process restart"))
	old := claimIndex(t, q)
	if err := q.Renew(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	expireIndex(t, f, old)
	q = indexqueue.New(f.db) // no process-local queue state survives reconstruction
	current := claimIndex(t, q)
	if current.Attempts != 2 || current.LeaseToken == old.LeaseToken {
		t.Fatal("reclaim did not replace ownership")
	}
	if err := q.Renew(context.Background(), old); !errors.Is(err, indexqueue.ErrLostLease) {
		t.Fatal("old renew", err)
	}
	if err := q.Fail(context.Background(), old, "worker_stopped", true); !errors.Is(err, indexqueue.ErrLostLease) {
		t.Fatal("old retry", err)
	}
	called := false
	if err := q.Publish(context.Background(), old, func(*gorm.DB) error { called = true; return nil }); !errors.Is(err, indexqueue.ErrLostLease) || called {
		t.Fatal("stale publisher ran", err, called)
	}
	// Synthetic metadata tests atomicity/fencing, not model quality.
	if err := q.Publish(context.Background(), current, func(tx *gorm.DB) error {
		return tx.Create(&model.AiMetadata{FileID: current.FileID, Description: "fencing test marker"}).Error
	}); err != nil {
		t.Fatal(err)
	}
	if j := readIndex(t, f, current.FileID); j.Status != "done" || j.LeaseUntil != nil || j.LeaseToken != "" {
		t.Fatal("result did not finish claim")
	}
	if err := q.Publish(context.Background(), current, func(*gorm.DB) error { called = true; return nil }); !errors.Is(err, indexqueue.ErrLostLease) || called {
		t.Fatal("finished claim reused", err)
	}
}

func TestAIIndexRetryBackoffAndAttemptLimit(t *testing.T) {
	f, q := indexFixture(t)
	file := f.upload(t, f.alice, "retry.txt", []byte("model unavailable"))
	for attempt := 1; attempt <= indexqueue.MaxAttempts; attempt++ {
		j := claimIndex(t, q)
		if j.Attempts != attempt {
			t.Fatal("attempt counter", j.Attempts, attempt)
		}
		if err := q.Fail(context.Background(), j, "http://secret-token/private-file", true); !errors.Is(err, indexqueue.ErrInvalid) {
			t.Fatal("accepted private diagnostic", err)
		}
		if err := q.Fail(context.Background(), j, "model_unavailable", true); err != nil {
			t.Fatal(err)
		}
		got := readIndex(t, f, file.ID)
		want := "retry"
		if attempt == indexqueue.MaxAttempts {
			want = "failed"
		}
		if got.Status != want || got.ErrorCode != "model_unavailable" || got.LeaseToken != "" || got.LeaseUntil != nil || !got.AvailableAt.After(got.UpdatedAt) {
			t.Fatalf("bad retry state: %+v", got)
		}
		if next, err := q.Claim(context.Background()); err != nil || next != nil {
			t.Fatal("backoff/limit ignored", next, err)
		}
		if err := f.db.Model(&model.AIIndexJob{}).Where("file_id=?", file.ID).Update("available_at", gorm.Expr("TIMESTAMPADD(SECOND,-1,UTC_TIMESTAMP(6))")).Error; err != nil {
			t.Fatal(err)
		}
	}
	if next, err := q.Claim(context.Background()); err != nil || next != nil {
		t.Fatal("failed job retried", next, err)
	}
}

func TestAIIndexRepeatedCrashesExhaustAttempts(t *testing.T) {
	f, q := indexFixture(t)
	file := f.upload(t, f.alice, "crash-limit.txt", []byte("crash limit"))
	for attempt := 1; attempt <= indexqueue.MaxAttempts; attempt++ {
		j := claimIndex(t, q)
		if j.Attempts != attempt {
			t.Fatal(j.Attempts, attempt)
		}
		expireIndex(t, f, j)
	}
	if next, err := q.Claim(context.Background()); err != nil || next != nil {
		t.Fatal("exhausted crash claim", next, err)
	}
	j := readIndex(t, f, file.ID)
	if j.Status != "failed" || j.ErrorCode != "attempts_exhausted" {
		t.Fatal("crash loop not terminal", j.Status, j.ErrorCode)
	}
}

func TestAIIndexRebuildChangesGenerationAndInvalidatesClaim(t *testing.T) {
	f, q := indexFixture(t)
	file := f.upload(t, f.alice, "rebuild.txt", []byte("generation fence"))
	old := claimIndex(t, q)
	if err := f.db.Transaction(func(tx *gorm.DB) error { return indexqueue.Enqueue(tx, file.ID, false) }); err != nil {
		t.Fatal(err)
	}
	if got := readIndex(t, f, file.ID); got.Generation != old.Generation || got.LeaseToken != old.LeaseToken {
		t.Fatal("no-op enqueue revoked live worker")
	}
	if err := f.db.Transaction(func(tx *gorm.DB) error { return indexqueue.Enqueue(tx, file.ID, true) }); err != nil {
		t.Fatal(err)
	}
	current := claimIndex(t, q)
	if current.Generation != old.Generation+1 || current.Attempts != 1 {
		t.Fatal("rebuild failed to reset generation")
	}
	if err := q.Publish(context.Background(), old, func(*gorm.DB) error { t.Error("obsolete result callback ran"); return nil }); !errors.Is(err, indexqueue.ErrLostLease) {
		t.Fatal(err)
	}
}

func TestAIIndexPublicationRollsBackAndChecksCurrentFile(t *testing.T) {
	f, q := indexFixture(t)
	file := f.upload(t, f.alice, "publish.txt", []byte("current source"))
	j := claimIndex(t, q)
	injected := errors.New("result transaction failed")
	err := q.Publish(context.Background(), j, func(tx *gorm.DB) error {
		if err := tx.Create(&model.AiMetadata{FileID: file.ID, Description: "must roll back"}).Error; err != nil {
			return err
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatal(err)
	}
	var count int64
	if err := f.db.Model(&model.AiMetadata{}).Where("file_id=?", file.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("partial result escaped", count, err)
	}
	if got := readIndex(t, f, file.ID); got.Status != "running" || got.LeaseToken != j.LeaseToken {
		t.Fatal("failed transaction acknowledged work")
	}
	other := f.upload(t, f.alice, "new-source.txt", []byte("different source"))
	if err := f.db.Model(&model.File{}).Where("id=?", file.ID).Update("storage_id", other.StorageID).Error; err != nil {
		t.Fatal(err)
	}
	if err := q.Publish(context.Background(), j, func(*gorm.DB) error { t.Error("old source published"); return nil }); !errors.Is(err, indexqueue.ErrSourceChanged) {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.File{}).Where("id=?", file.ID).Updates(map[string]any{"storage_id": file.StorageID, "is_deleted": 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := q.Publish(context.Background(), j, func(*gorm.DB) error { t.Error("recycled source published"); return nil }); !errors.Is(err, indexqueue.ErrSourceChanged) {
		t.Fatal(err)
	}
}

func TestAIIndexSchemaRequiresExplicitMigration(t *testing.T) {
	f, _ := indexFixture(t)
	if err := store.CheckAIIndexSchema(f.db); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec("ALTER TABLE ai_index_job DROP INDEX idx_ai_index_ready").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Migrate(f.db); err != nil {
			t.Error(err)
		}
	})
	if err := store.CheckAIIndexSchema(f.db); err == nil {
		t.Fatal("missing claim index accepted")
	}
	if f.db.Migrator().HasIndex(&model.AIIndexJob{}, "idx_ai_index_ready") {
		t.Fatal("check silently repaired schema")
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckAIIndexSchema(f.db); err != nil {
		t.Fatal(err)
	}
}

func TestAIIndexSkipLockedMakesProgressOnOtherFiles(t *testing.T) {
	f, q := indexFixture(t)
	first := f.upload(t, f.alice, "locked-index.txt", []byte("locked first task"))
	second := f.upload(t, f.alice, "ready-index.txt", []byte("ready second task"))
	tx := f.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	var locked model.AIIndexJob
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "file_id=?", first.ID).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	j, err := q.Claim(ctx)
	if err != nil || j == nil || j.FileID != second.ID {
		t.Fatal("locked first job blocked unrelated work", j, err)
	}
}

func TestAIIndexLeaseExpiryDuringPublicationRollsBackResult(t *testing.T) {
	f, q := indexFixture(t)
	file := f.upload(t, f.alice, "expiry-index.txt", []byte("expire during publication"))
	j := claimIndex(t, q)
	err := q.Publish(context.Background(), j, func(tx *gorm.DB) error {
		if err := tx.Create(&model.AiMetadata{FileID: file.ID, Description: "expired transaction marker"}).Error; err != nil {
			return err
		}
		// Deterministically advance the lease deadline instead of waiting 90s.
		return tx.Model(&model.AIIndexJob{}).Where("file_id=?", file.ID).Update("lease_until", gorm.Expr("TIMESTAMPADD(SECOND,-1,UTC_TIMESTAMP(6))")).Error
	})
	if !errors.Is(err, indexqueue.ErrLostLease) {
		t.Fatal("expired publication committed", err)
	}
	var count int64
	if err := f.db.Model(&model.AiMetadata{}).Where("file_id=?", file.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("expired result escaped rollback", count, err)
	}
}

func TestAIIndexWorkerShutdownReleasesClaimAndBlobLease(t *testing.T) {
	f, _ := indexFixture(t)
	file := f.upload(t, f.alice, "shutdown-index.txt", []byte("shutdown while processing"))
	entered := make(chan struct{})
	worker := indexqueue.NewWorker(f.db, blob.NewManager(f.db), func(ctx context.Context, j model.AIIndexJob) (func(*gorm.DB) error, error) {
		in, _, err := blob.OpenPinnedStorage(ctx, model.Storage{ID: *file.StorageID})
		if err != nil {
			return nil, err
		}
		if err := in.Close(); err != nil {
			return nil, err
		}
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := worker.Step(ctx); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not read source")
	}
	if leaseCount(t, f, *file.StorageID) != 1 {
		t.Fatal("worker missing source protection")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("shutdown error", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("worker ignored shutdown")
	}
	job := readIndex(t, f, file.ID)
	if job.Status != "retry" || job.ErrorCode != "worker_stopped" || job.LeaseToken != "" || job.LeaseUntil != nil {
		t.Fatal("shutdown did not release claim")
	}
	waitNoReadLeases(t, f, *file.StorageID)
}
