// Package aiindex implements bounded extraction and real embedding for durable
// indexing. Database publication is fenced by indexqueue, never by this package.
package aiindex

import (
	"context"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	MaxTextBytes = 256 << 10
	ChunkRunes   = 768
	OverlapRunes = 96
	MaxChunks    = 512
)

var (
	ErrTextLimit    = errors.New("index text exceeds extraction limit")
	ErrTextEncoding = errors.New("index text is not valid UTF-8")
	ErrUnsupported  = errors.New("index format not yet supported")
)

type Chunk struct {
	Ordinal   int
	StartRune int
	EndRune   int
	Text      string
}

// ReadText never silently truncates. The caller must supply an already verified
// and pinned descriptor; formats needing parsers are handled separately.
func ReadText(ctx context.Context, in io.Reader, ext string) (string, error) {
	if !SupportsText(ext) {
		return "", ErrUnsupported
	}
	buf := make([]byte, 0, 32<<10)
	scratch := make([]byte, 16<<10)
	limited := io.LimitReader(in, MaxTextBytes+1)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := limited.Read(scratch)
		buf = append(buf, scratch[:n]...)
		if len(buf) > MaxTextBytes {
			return "", ErrTextLimit
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	if !utf8.Valid(buf) || strings.IndexByte(string(buf), 0) >= 0 {
		return "", ErrTextEncoding
	}
	return strings.TrimPrefix(string(buf), "\ufeff"), nil
}

func SupportsText(ext string) bool {
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "txt", "md", "markdown", "json", "xml", "yaml", "yml", "toml", "ini", "conf", "config", "env", "properties", "csv", "log", "sql", "go", "swift", "java", "py", "js", "ts", "html", "css", "sh":
		return true
	default:
		return false
	}
}

// Split uses rune offsets, preserves source text, prefers paragraph/sentence
// ends in the final quarter of a window, and retains overlap across boundaries.
func Split(text string) ([]Chunk, error) {
	if !utf8.ValidString(text) {
		return nil, ErrTextEncoding
	}
	if len(text) > MaxTextBytes {
		return nil, ErrTextLimit
	}
	runes := []rune(text)
	var chunks []Chunk
	for start := 0; start < len(runes); {
		end := min(start+ChunkRunes, len(runes))
		if end < len(runes) {
			for i := end - 1; i >= start+ChunkRunes*3/4; i-- {
				if strings.ContainsRune("\n。！？.!?", runes[i]) {
					end = i + 1
					break
				}
			}
		}
		part := string(runes[start:end])
		if strings.TrimSpace(part) != "" {
			if len(chunks) >= MaxChunks {
				return nil, ErrTextLimit
			}
			chunks = append(chunks, Chunk{len(chunks), start, end, part})
		}
		if end == len(runes) {
			break
		}
		start = end - OverlapRunes
	}
	return chunks, nil
}
