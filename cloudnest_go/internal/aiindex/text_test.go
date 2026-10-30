package aiindex

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunksPreserveUnicodeSourceOffsetsAndOverlap(t *testing.T) {
	text := strings.Repeat("数据库迁移方案：先备份，再校验。\n", 180) + "末尾🚀"
	parts, err := Split(text)
	if err != nil || len(parts) < 2 {
		t.Fatal(parts, err)
	}
	runes := []rune(text)
	for i, p := range parts {
		if p.Ordinal != i || p.Text != string(runes[p.StartRune:p.EndRune]) || !utf8.ValidString(p.Text) || p.EndRune-p.StartRune > ChunkRunes {
			t.Fatal("chunk lost source position", p)
		}
		if i > 0 && parts[i-1].EndRune-p.StartRune != OverlapRunes {
			t.Fatal("missing overlap")
		}
	}
	if parts[0].StartRune != 0 || parts[len(parts)-1].EndRune != len(runes) {
		t.Fatal("lost first/last content")
	}
}

func TestTextLimitsEncodingAndCancellationAreExplicit(t *testing.T) {
	for _, tc := range []struct {
		input, ext string
		want       error
	}{
		{strings.Repeat("x", MaxTextBytes+1), ".txt", ErrTextLimit},
		{"\xff", ".txt", ErrTextEncoding}, {"hello\x00world", ".txt", ErrTextEncoding},
		{"", ".pdf", ErrUnsupported},
	} {
		if _, err := ReadText(context.Background(), strings.NewReader(tc.input), tc.ext); !errors.Is(err, tc.want) {
			t.Fatal(tc.ext, err, tc.want)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadText(ctx, strings.NewReader("hello"), ".md"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	text, err := ReadText(context.Background(), strings.NewReader("\ufeff服务器 IP：192.0.2.1\n"), ".conf")
	if err != nil || text != "服务器 IP：192.0.2.1\n" {
		t.Fatal(text, err)
	}
	parts, err := Split(" \n\t")
	if err != nil || len(parts) != 0 {
		t.Fatal("whitespace invented a chunk")
	}
	if _, err := Split(strings.Repeat("x", MaxTextBytes+1)); !errors.Is(err, ErrTextLimit) {
		t.Fatal("direct split bypassed budget")
	}
}
