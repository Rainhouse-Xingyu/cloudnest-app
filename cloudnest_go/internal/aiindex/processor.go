package aiindex

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"path/filepath"

	"cloudenest_go/internal/blob"
	"cloudenest_go/internal/indexqueue"
	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Processor struct {
	db    *gorm.DB
	embed *Ollama
}

func NewProcessor(db *gorm.DB, embed *Ollama) *Processor { return &Processor{db, embed} }

func failure(code string, retry bool) error { return &indexqueue.Failure{Code: code, Retryable: retry} }

func (p *Processor) Process(ctx context.Context, j model.AIIndexJob) (func(*gorm.DB) error, error) {
	var f model.File
	if err := p.db.WithContext(ctx).First(&f, "id=? AND is_deleted=0 AND is_folder=0", j.FileID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, indexqueue.ErrSourceChanged
		}
		return nil, err
	}
	if f.StorageID == nil || j.Pipeline != indexqueue.Pipeline {
		return nil, indexqueue.ErrSourceChanged
	}
	ext := filepath.Ext(f.Filename)
	if !SupportsText(ext) {
		return nil, failure("unsupported_type", false)
	}
	if f.Size > MaxTextBytes {
		return nil, failure("extraction_limit", false)
	}
	in, object, err := blob.OpenPinnedStorage(ctx, model.Storage{ID: *f.StorageID})
	if err != nil {
		return nil, failure("source_integrity", false)
	}
	if object.FileHash != j.SourceHash {
		in.Close()
		return nil, indexqueue.ErrSourceChanged
	}
	text, err := ReadText(ctx, in, ext)
	closeErr := in.Close()
	if err != nil {
		if errors.Is(err, ErrTextLimit) {
			return nil, failure("extraction_limit", false)
		}
		return nil, failure("extraction_failed", false)
	}
	if closeErr != nil {
		return nil, failure("source_integrity", false)
	}
	parts, err := Split(text)
	if err != nil {
		return nil, failure("extraction_limit", false)
	}
	digest, err := p.embed.Digest(ctx)
	if err != nil {
		return nil, failure("model_unavailable", true)
	}
	rows := make([]model.AIIndexChunk, 0, len(parts))
	dimensions := 0
	for start := 0; start < len(parts); start += EmbedBatch {
		end := min(start+EmbedBatch, len(parts))
		input := make([]string, end-start)
		for i := range input {
			input[i] = parts[start+i].Text
		}
		vectors, err := p.embed.Embed(ctx, input)
		if err != nil {
			if errors.Is(err, ErrEmbedding) {
				return nil, failure("invalid_embedding", true)
			}
			return nil, failure("model_unavailable", true)
		}
		for i, vector := range vectors {
			if dimensions != 0 && dimensions != len(vector) {
				return nil, failure("invalid_embedding", true)
			}
			dimensions = len(vector)
			encoded := make([]byte, 4*len(vector))
			for k, v := range vector {
				binary.LittleEndian.PutUint32(encoded[k*4:], math.Float32bits(v))
			}
			part := parts[start+i]
			rows = append(rows, model.AIIndexChunk{FileID: f.ID, Ordinal: part.Ordinal, StartRune: part.StartRune, EndRune: part.EndRune, Content: part.Text, Vector: encoded})
		}
	}
	confirmed, err := p.embed.Digest(ctx)
	if err != nil {
		return nil, failure("model_unavailable", true)
	}
	if confirmed != digest {
		return nil, failure("model_changed", true)
	}
	if err := blob.Check(ctx); err != nil {
		return nil, err
	}
	return func(tx *gorm.DB) error {
		if err := tx.Where("file_id=?", f.ID).Delete(&model.AIIndexChunk{}).Error; err != nil {
			return err
		}
		if len(rows) > 0 {
			if err := tx.CreateInBatches(rows, 32).Error; err != nil {
				return err
			}
		}
		document := map[string]any{
			"file_id": f.ID, "generation": j.Generation, "source_hash": j.SourceHash, "pipeline": j.Pipeline,
			"embedding_model": p.embed.Model(), "model_digest": digest, "dimensions": dimensions,
			"chunk_count": len(rows), "text_bytes": len(text), "indexed_at": gorm.Expr("UTC_TIMESTAMP(6)"),
		}
		if err := tx.Model(&model.AIIndexDocument{}).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "file_id"}}, DoUpdates: clause.AssignmentColumns([]string{"generation", "source_hash", "pipeline", "embedding_model", "model_digest", "dimensions", "chunk_count", "text_bytes", "indexed_at"})}).Create(document).Error; err != nil {
			return err
		}
		return tx.Create(&model.OperationLog{UserID: &f.UserID, FileID: &f.ID, Action: "ai_index", Status: "success", Detail: "content chunks and embeddings published"}).Error
	}, nil
}
