package aiindex

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"path/filepath"
	"strings"

	"cloudenest_go/internal/blob"
	"cloudenest_go/internal/imageprep"
	"cloudenest_go/internal/indexqueue"
	"cloudenest_go/internal/pdfextract"
	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Processor struct {
	db     *gorm.DB
	embed  *Ollama
	pdf    pdfextract.Reader
	vision *Ollama
}

func NewProcessor(db *gorm.DB, embed *Ollama) *Processor { return &Processor{db: db, embed: embed} }

// WithPDF configures native extraction and the separate OCR model. Configure
// before starting the worker; the processor is immutable while running.
func (p *Processor) WithPDF(binDir string, vision *Ollama) *Processor {
	p.pdf = pdfextract.Reader{BinDir: binDir}
	p.vision = vision
	return p
}

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
	isPDF := strings.EqualFold(ext, ".pdf")
	if !SupportsText(ext) && !SupportsOffice(ext) && !isPDF && !SupportsImage(ext) {
		return nil, failure("unsupported_type", false)
	}
	maxSource := int64(MaxTextBytes)
	if SupportsOffice(ext) {
		maxSource = MaxOfficeBytes
	}
	if isPDF {
		maxSource = pdfextract.MaxSourceBytes
	}
	if SupportsImage(ext) {
		maxSource = imageprep.MaxEncodedBytes
	}
	if f.Size > maxSource {
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
	var text string
	kind, visionDigest, description, tags := "text", "", "", ""
	hasOCR := false
	var capturedLocal any
	captureOffset := ""
	if SupportsImage(ext) {
		kind = "image"
		var prepared imageprep.Prepared
		prepared, err = imageprep.Prepare(ctx, in)
		if err == nil {
			if p.vision == nil {
				err = ErrModelUnavailable
			} else {
				var analysis ImageAnalysis
				analysis, visionDigest, err = p.vision.AnalyzeImage(ctx, prepared.PNG)
				if err == nil {
					description = analysis.Description
					tags = strings.Join(analysis.Tags, ",")
					hasOCR = analysis.HasText
					text = "[Visual description]\n" + description + "\n[Visual tags]\n" + tags
					if hasOCR {
						text += "\n[Visible text OCR]\n" + analysis.Text
					}
					if prepared.Metadata.CapturedLocal != nil {
						capturedLocal = prepared.Metadata.CapturedLocal.Format("2006-01-02 15:04:05")
					}
					captureOffset = prepared.Metadata.Offset
				}
			}
		}
	} else if isPDF {
		kind = "pdf"
		var ocr pdfextract.OCR
		if p.vision != nil {
			ocr = func(ctx context.Context, image []byte) (string, error) {
				value, digest, err := p.vision.TranscribeWithDigest(ctx, image)
				if err != nil {
					return "", err
				}
				if visionDigest != "" && visionDigest != digest {
					return "", ErrModelChanged
				}
				visionDigest = digest
				hasOCR = true
				return value, nil
			}
		}
		text, err = p.pdf.Read(ctx, in, ocr)
	} else if SupportsOffice(ext) {
		kind = "office"
		info, statErr := in.Stat()
		if statErr != nil {
			in.Close()
			return nil, failure("source_integrity", false)
		}
		text, err = ReadOffice(ctx, in, info.Size(), ext)
	} else {
		text, err = ReadText(ctx, in, ext)
	}
	closeErr := in.Close()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, failure("extraction_limit", false)
		}
		if errors.Is(err, pdfextract.ErrUnavailable) {
			return nil, failure("extraction_unavailable", true)
		}
		if errors.Is(err, ErrModelUnavailable) || errors.Is(err, ErrVisionResponse) || errors.Is(err, ErrEmbedding) || errors.Is(err, pdfextract.ErrOCRRequired) {
			return nil, failure("model_unavailable", true)
		}
		if errors.Is(err, ErrModelChanged) {
			return nil, failure("model_changed", true)
		}
		if errors.Is(err, ErrExtractionBusy) || errors.Is(err, pdfextract.ErrBusy) || errors.Is(err, imageprep.ErrBusy) {
			return nil, failure("extraction_busy", true)
		}
		if errors.Is(err, ErrTextLimit) || errors.Is(err, pdfextract.ErrLimit) || errors.Is(err, imageprep.ErrLimit) {
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
		visionModel := ""
		if visionDigest != "" {
			visionModel = p.vision.Model()
		}
		document := map[string]any{
			"extraction_kind": kind, "vision_model": visionModel, "vision_digest": visionDigest, "has_ocr": hasOCR, "captured_at_local": capturedLocal, "capture_offset": captureOffset, "visual_description": description, "visual_tags": tags,
			"file_id": f.ID, "generation": j.Generation, "source_hash": j.SourceHash, "pipeline": j.Pipeline,
			"embedding_model": p.embed.Model(), "model_digest": digest, "dimensions": dimensions,
			"chunk_count": len(rows), "text_bytes": len(text), "indexed_at": gorm.Expr("UTC_TIMESTAMP(6)"),
		}
		if err := tx.Model(&model.AIIndexDocument{}).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "file_id"}}, DoUpdates: clause.AssignmentColumns([]string{"generation", "source_hash", "pipeline", "embedding_model", "model_digest", "dimensions", "chunk_count", "text_bytes", "indexed_at", "extraction_kind", "vision_model", "vision_digest", "has_ocr", "captured_at_local", "capture_offset", "visual_description", "visual_tags"})}).Create(document).Error; err != nil {
			return err
		}
		return tx.Create(&model.OperationLog{UserID: &f.UserID, FileID: &f.ID, Action: "ai_index", Status: "success", Detail: "content chunks and embeddings published"}).Error
	}, nil
}
