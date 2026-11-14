package indexqueue

import (
	"context"
	"errors"
	"log"
	"time"

	"cloudenest_go/internal/blob"
	"cloudenest_go/model"
	"gorm.io/gorm"
)

// Processor reads/extracts/embeds outside a database transaction and returns only
// the short result write. Its context carries an independent blob read scope.
// Implementations must honor cancellation and bound parsing/model resources.
type Processor func(context.Context, model.AIIndexJob) (func(*gorm.DB) error, error)

type Failure struct {
	Code      string
	Retryable bool
}

func (e *Failure) Error() string { return "AI index processing failed: " + e.Code }

type Worker struct {
	queue   *Queue
	blobs   *blob.Manager
	process Processor
}

func NewWorker(db *gorm.DB, blobs *blob.Manager, process Processor) *Worker {
	return &Worker{queue: New(db), blobs: blobs, process: process}
}

// Run is deliberately not started by constructing a router. The server owns
// its lifetime and must join Run on shutdown once a real processor is installed.
func (w *Worker) Run(ctx context.Context) {
	for ctx.Err() == nil {
		worked, err := w.Step(ctx)
		if err != nil {
			log.Print("AI index worker attempt failed; inspect durable task error_code")
		}
		if worked && err == nil {
			continue
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Step consumes at most one durable job. It is also the integration-test seam;
// tests do not need to start an unrelated HTTP server or an endless poll loop.
func (w *Worker) Step(parent context.Context) (worked bool, resultErr error) {
	if w.process == nil || w.blobs == nil {
		return false, ErrInvalid
	}
	claimCtx, endClaim := context.WithTimeout(parent, 5*time.Second)
	j, err := w.queue.Claim(claimCtx)
	endClaim()
	if err != nil || j == nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	defer cancel()
	ctx, scope := w.blobs.Begin(ctx)
	defer func() { resultErr = errors.Join(resultErr, scope.Close()) }()
	stopRenew, renewDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(renewDone)
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopRenew:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				op, done := context.WithTimeout(ctx, 5*time.Second)
				err := w.queue.Renew(op, *j)
				done()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	write, err := w.process(ctx, *j)
	close(stopRenew)
	<-renewDone
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil {
		err = blob.Check(ctx)
	}
	if err == nil {
		publishCtx, endPublish := context.WithTimeout(ctx, 5*time.Second)
		err = w.queue.Publish(publishCtx, *j, write)
		endPublish()
		if err == nil {
			return true, nil
		}
	}
	if errors.Is(err, ErrLostLease) {
		return true, err
	}
	code, retryable := "index_write_failed", true
	var failure *Failure
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code = "worker_stopped"
	case errors.Is(err, ErrSourceChanged):
		code, retryable = "source_changed", false
	case errors.As(err, &failure):
		code, retryable = failure.Code, failure.Retryable
	}
	// Shutdown still releases a claim when possible. If the DB is unavailable,
	// the durable lease expires and another process can reclaim it later.
	failureCtx, endFailure := context.WithTimeout(context.Background(), 5*time.Second)
	defer endFailure()
	return true, errors.Join(err, w.queue.Fail(failureCtx, *j, code, retryable))
}
