package aiindex

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type officeEntry struct{ name, body string }

func officeArchive(t *testing.T, entries ...officeEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, e := range entries {
		part, err := w.Create(e.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func officeRels(items string) string {
	return `<Relationships xmlns="` + relNS + `">` + items + `</Relationships>`
}
func officeRel(id, kind, target string) string {
	return `<Relationship Id="` + id + `" Type="` + officeRelNS + `/` + kind + `" Target="` + target + `"/>`
}
func officeRead(t *testing.T, ext string, entries ...officeEntry) (string, error) {
	t.Helper()
	raw := officeArchive(t, entries...)
	return ReadOffice(context.Background(), bytes.NewReader(raw), int64(len(raw)), ext)
}
func wordEntries(body string) []officeEntry {
	return []officeEntry{{"_rels/.rels", officeRels(officeRel("main", "officeDocument", "custom/body.xml"))}, {"custom/body.xml", `<w:document xmlns:w="` + wordNS + `" xmlns:r="` + officeRelNS + `"><w:body>` + body + `</w:body></w:document>`}}
}

func TestOfficeWordPreservesRunsEntitiesTablesAndLinkedStories(t *testing.T) {
	entries := wordEntries(`<w:sectPr><w:headerReference r:id="h"/></w:sectPr><w:p><w:r><w:t>数据</w:t></w:r><w:r><w:t>库迁移 &lt;config&gt; &amp; 验证🚀</w:t><w:tab/><w:t>安全</w:t></w:r><w:del><w:r><w:delText>deleted-secret</w:delText></w:r></w:del></w:p><w:tbl><w:tr><w:tc><w:p><w:r><w:t>服务器 IP</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>192.0.2.42</w:t></w:r></w:p></w:tc></w:tr></w:tbl>`)
	entries = append(entries, officeEntry{"custom/_rels/body.xml.rels", officeRels(officeRel("h", "header", "../stories/header.xml") + officeRel("unused", "header", "../stories/unused.xml"))}, officeEntry{"stories/header.xml", `<w:hdr xmlns:w="` + wordNS + `"><w:p><w:r><w:t>合同页眉</w:t></w:r></w:p></w:hdr>`}, officeEntry{"stories/unused.xml", `<w:hdr xmlns:w="` + wordNS + `"><w:p><w:r><w:t>ORPHAN HEADER</w:t></w:r></w:p></w:hdr>`}, officeEntry{"custom/styles.xml", `<secret>UNUSED STYLE</secret>`})
	got, err := officeRead(t, ".DOCX", entries...)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"数据库迁移 <config> & 验证🚀", "安全", "192.0.2.42", "合同页眉"} {
		if !strings.Contains(got, want) {
			t.Fatal(want, got)
		}
	}
	for _, bad := range []string{"deleted-secret", "UNUSED STYLE", "ORPHAN HEADER"} {
		if strings.Contains(got, bad) {
			t.Fatal(got)
		}
	}
	// Namespace identity, not a hardcoded prefix, defines a text node.
	entries = wordEntries(`<w:p><w:r><w:t>严格格式</w:t></w:r></w:p>`)
	entries[1].body = strings.ReplaceAll(entries[1].body, wordNS, "http://purl.oclc.org/ooxml/wordprocessingml/main")
	if got, err := officeRead(t, "docx", entries...); err != nil || got != "严格格式" {
		t.Fatal(got, err)
	}
}

func TestOfficeSheetResolvesSharedInlineFormulaAndWorkbookOrder(t *testing.T) {
	entries := []officeEntry{
		{"_rels/.rels", officeRels(officeRel("main", "officeDocument", "xl/workbook.xml"))},
		{"xl/workbook.xml", `<workbook xmlns="` + sheetNS + `" xmlns:r="` + officeRelNS + `"><sheets><sheet name="合同" r:id="two"/><sheet name="发票" r:id="one"/></sheets></workbook>`},
		{"xl/_rels/workbook.xml.rels", officeRels(officeRel("one", "worksheet", "worksheets/1.xml") + officeRel("two", "worksheet", "worksheets/2.xml") + officeRel("str", "sharedStrings", "sharedStrings.xml"))},
		{"xl/sharedStrings.xml", `<sst xmlns="` + sheetNS + `"><si><r><t>电脑</t></r><r><t>购买发票</t></r><rPh><t>phonetic-not-content</t></rPh></si><si><t>unused-secret</t></si></sst>`},
		{"xl/worksheets/1.xml", `<worksheet xmlns="` + sheetNS + `"><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1"><v>5999</v></c><c r="C1" t="b"><v>1</v></c><c r="D1"><f>SUM(B1:B2)</f><v>6000</v></c></row></sheetData></worksheet>`},
		{"xl/worksheets/2.xml", `<worksheet xmlns="` + sheetNS + `"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>服务器 IP 192.0.2.42</t></is></c></row></sheetData></worksheet>`},
	}
	got, err := officeRead(t, "xlsx", entries...)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"A1: 电脑购买发票", "B1: 5999", "C1: true", "=SUM(B1:B2) [cached: 6000]", "服务器 IP 192.0.2.42"} {
		if !strings.Contains(got, want) {
			t.Fatal(want, got)
		}
	}
	if strings.Index(got, "合同") > strings.Index(got, "发票") || strings.Contains(got, "unused-secret") || strings.Contains(got, "phonetic-not-content") {
		t.Fatal(got)
	}
	entries[4].body = strings.Replace(entries[4].body, "<v>0</v>", "<v>99</v>", 1)
	if _, err := officeRead(t, "xlsx", entries...); err == nil {
		t.Fatal("dangling shared string accepted")
	}
}

func TestOfficePresentationUsesSlideRelationsAndNotes(t *testing.T) {
	slide := func(text string) string {
		return `<p:sld xmlns:p="` + presentationNS + `" xmlns:a="` + drawingNS + `"><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>` + text + `</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`
	}
	entries := []officeEntry{
		{"_rels/.rels", officeRels(officeRel("m", "officeDocument", "ppt/main.xml"))},
		{"ppt/main.xml", `<p:presentation xmlns:p="` + presentationNS + `" xmlns:r="` + officeRelNS + `"><p:sldIdLst><p:sldId id="2" r:id="b"/><p:sldId id="1" r:id="a"/></p:sldIdLst></p:presentation>`},
		{"ppt/_rels/main.xml.rels", officeRels(officeRel("a", "slide", "slides/first.xml") + officeRel("b", "slide", "slides/second.xml"))},
		{"ppt/slides/first.xml", slide("权限审计")}, {"ppt/slides/second.xml", slide("Spring Boot")},
		{"ppt/slides/_rels/second.xml.rels", officeRels(officeRel("n", "notesSlide", "../notes/notes.xml"))},
		{"ppt/notes/notes.xml", strings.ReplaceAll(slide("演讲备注"), "p:sld", "p:notes")},
	}
	got, err := officeRead(t, "pptx", entries...)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(got, "Spring Boot") > strings.Index(got, "权限审计") || !strings.Contains(got, "演讲备注") {
		t.Fatal(got)
	}
	entries[3].body = slide("")
	entries[4].body = slide("")
	entries[6].body = `<p:notes xmlns:p="` + presentationNS + `" xmlns:a="` + drawingNS + `"><a:p><a:fld type="slidenum"><a:t>1</a:t></a:fld></a:p></p:notes>`
	if got, err := officeRead(t, "pptx", entries...); err == nil || got != "" {
		t.Fatal("slide labels/page numbers pretended to be extracted content", got, err)
	}
}

func TestOfficeRejectsMalformedUnsafeAndBoundedInputs(t *testing.T) {
	good := wordEntries(`<w:p><w:r><w:t>valid</w:t></w:r></w:p>`)
	tests := map[string][]officeEntry{}
	clone := func() []officeEntry { return append([]officeEntry(nil), good...) }
	tests["duplicate"] = append(clone(), good[1])
	tests["traversal"] = append(clone(), officeEntry{"../escape.xml", "bad"})
	tests["root escape"] = clone()
	tests["root escape"][0].body = officeRels(officeRel("m", "officeDocument", "../escape.xml"))
	tests["external"] = clone()
	tests["external"][0].body = officeRels(strings.Replace(officeRel("m", "officeDocument", "https://invalid.example/doc.xml"), "/>", ` TargetMode="External"/>`, 1))
	tests["malformed"] = clone()
	tests["malformed"][1].body = `<document>`
	tests["wrong namespace"] = clone()
	tests["wrong namespace"][1].body = strings.ReplaceAll(good[1].body, wordNS, "urn:wrong")
	tests["DTD"] = clone()
	tests["DTD"][1].body = `<!DOCTYPE foo SYSTEM "file:///etc/passwd">` + good[1].body
	tests["depth"] = wordEntries(strings.Repeat("<w:p>", 130) + "deep" + strings.Repeat("</w:p>", 130))
	tests["output limit"] = wordEntries(`<w:p><w:r><w:t>` + strings.Repeat("x", MaxTextBytes+1) + `</w:t></w:r></w:p>`)
	tests["part limit"] = wordEntries(strings.Repeat(" ", maxOfficePart+1))
	tests["unsupported altChunk"] = wordEntries(`<w:altChunk/>`)
	tests["empty"] = wordEntries(`<w:p/>`)
	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			if text, err := officeRead(t, "docx", entries...); err == nil || text != "" {
				t.Fatal("bad input produced partial result", len(text), err)
			}
		})
	}
	raw := officeArchive(t, good...)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadOffice(ctx, bytes.NewReader(raw), int64(len(raw)), "docx"); err != context.Canceled {
		t.Fatal(err)
	}
	if _, err := ReadOffice(context.Background(), bytes.NewReader(raw), MaxOfficeBytes+1, "docx"); err != ErrTextLimit {
		t.Fatal(err)
	}
	if _, err := ReadOffice(context.Background(), bytes.NewReader(raw), int64(len(raw)), "doc"); err != ErrUnsupported {
		t.Fatal(err)
	}
	// A lying central-directory entry count is rejected before zip allocation.
	binary.LittleEndian.PutUint16(raw[len(raw)-22+10:], 65535)
	if _, err := ReadOffice(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx"); err == nil {
		t.Fatal("unbounded directory accepted")
	}
	many := clone()
	for i := 0; i < maxOfficeEntries; i++ {
		many = append(many, officeEntry{fmt.Sprintf("unused/%d", i), ""})
	}
	if _, err := officeRead(t, "docx", many...); err != ErrTextLimit {
		t.Fatal(err)
	}
}

func TestOfficeLibraryGeneratedDocuments(t *testing.T) {
	for _, tc := range []struct {
		name string
		want []string
	}{
		{"migration.docx", []string{"数据库迁移方案", "备份数据库", "192.0.2.42", "恢复备份", "CloudNest 合成测试文档"}},
		{"invoice.xlsx", []string{"电脑购买发票", "笔记本电脑", "5999", "=SUM(B2:B2) [cached: ]"}},
		{"permissions.pptx", []string{"Spring Boot 权限管理", "资源授权", "演讲备注", "跨用户读取", "安全回归测试"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("../../integration/testdata/office", tc.name))
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "migration.docx" && len(raw) <= MaxTextBytes {
				t.Fatal("fixture no longer tests larger Office source")
			}
			got, err := ReadOffice(context.Background(), bytes.NewReader(raw), int64(len(raw)), filepath.Ext(tc.name))
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatal(want, got)
				}
			}
		})
	}
}

type pausedOfficeReader struct {
	*bytes.Reader
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (r *pausedOfficeReader) ReadAt(p []byte, off int64) (int, error) {
	r.once.Do(func() { r.entered <- struct{}{}; <-r.release })
	return r.Reader.ReadAt(p, off)
}
func TestOfficeConcurrencyAdmissionAndCRCFailure(t *testing.T) {
	entries := wordEntries(`<w:p><w:r><w:t>valid</w:t></w:r></w:p>`)
	raw := officeArchive(t, entries...)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan error, 2)
	for range 2 {
		go func() {
			reader := &pausedOfficeReader{Reader: bytes.NewReader(raw), entered: entered, release: release}
			_, err := ReadOffice(context.Background(), reader, int64(len(raw)), "docx")
			done <- err
		}()
	}
	<-entered
	<-entered
	if _, err := ReadOffice(context.Background(), bytes.NewReader(raw), int64(len(raw)), "docx"); err != ErrExtractionBusy {
		close(release)
		t.Fatal("unbounded concurrent parser", err)
	}
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, e := range entries {
		part, err := w.CreateHeader(&zip.FileHeader{Name: e.name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	broken := b.Bytes()
	at := bytes.Index(broken, []byte("valid"))
	if at < 0 {
		t.Fatal("missing fixture bytes")
	}
	broken[at] = 'X'
	if got, err := ReadOffice(context.Background(), bytes.NewReader(broken), int64(len(broken)), "docx"); err == nil || got != "" {
		t.Fatal("CRC failure produced partial text", got, err)
	}
}
