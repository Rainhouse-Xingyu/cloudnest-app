package aiindex

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"path"
	"strings"
)

const (
	MaxOfficeBytes    = 32 << 20
	maxOfficeExpanded = 64 << 20
	maxOfficePart     = 8 << 20
	maxOfficeRead     = 32 << 20
	maxOfficeEntries  = 2048
	maxXMLNodes       = 200000
	wordNS            = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	sheetNS           = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
	presentationNS    = "http://schemas.openxmlformats.org/presentationml/2006/main"
	drawingNS         = "http://schemas.openxmlformats.org/drawingml/2006/main"
	relNS             = "http://schemas.openxmlformats.org/package/2006/relationships"
	officeRelNS       = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
)

var ErrDocument = errors.New("invalid or unsupported document structure")
var ErrExtractionBusy = errors.New("document extraction capacity unavailable")
var officeSlots = make(chan struct{}, 2)

type officeNode struct {
	name     xml.Name
	attrs    []xml.Attr
	text     strings.Builder
	children []*officeNode
}

func namespaceMatches(got, want string) bool {
	if got == want {
		return true
	}
	// ISO Strict uses these namespace URIs instead of the Transitional ones.
	strict := map[string]string{wordNS: "http://purl.oclc.org/ooxml/wordprocessingml/main", sheetNS: "http://purl.oclc.org/ooxml/spreadsheetml/main", presentationNS: "http://purl.oclc.org/ooxml/presentationml/main", drawingNS: "http://purl.oclc.org/ooxml/drawingml/main", officeRelNS: "http://purl.oclc.org/ooxml/officeDocument/relationships"}
	return strict[want] != "" && got == strict[want]
}
func (n *officeNode) is(ns, name string) bool {
	return n != nil && n.name.Local == name && namespaceMatches(n.name.Space, ns)
}
func (n *officeNode) attr(ns, name string) string {
	for _, a := range n.attrs {
		if a.Name.Local == name && namespaceMatches(a.Name.Space, ns) {
			return a.Value
		}
	}
	return ""
}
func (n *officeNode) child(ns, name string) *officeNode {
	for _, c := range n.children {
		if c.is(ns, name) {
			return c
		}
	}
	return nil
}
func (n *officeNode) value() string {
	var b strings.Builder
	var visit func(*officeNode)
	visit = func(v *officeNode) {
		b.WriteString(v.text.String())
		for _, c := range v.children {
			visit(c)
		}
	}
	visit(n)
	return b.String()
}

type officePackage struct {
	ctx       context.Context
	files     map[string]*zip.File
	remaining int
	nodes     int
}
type officeRelation struct {
	id, kind, target string
	external         bool
}
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func SupportsOffice(ext string) bool {
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "docx", "xlsx", "pptx":
		return true
	}
	return false
}

// ReadOffice reads only bounded package parts from the already verified FD. It
// never expands files on disk, follows external relationships or executes macros.
func ReadOffice(ctx context.Context, in io.ReaderAt, size int64, ext string) (result string, err error) {
	defer func() {
		if err != nil && !errors.Is(err, ErrUnsupported) && !errors.Is(err, ErrDocument) && !errors.Is(err, ErrTextLimit) && !errors.Is(err, ErrExtractionBusy) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			err = errors.Join(ErrDocument, err)
		}
	}()
	if !SupportsOffice(ext) {
		return "", ErrUnsupported
	}
	if size < 0 || size > MaxOfficeBytes {
		return "", ErrTextLimit
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	select {
	case officeSlots <- struct{}{}:
		defer func() { <-officeSlots }()
	default:
		return "", ErrExtractionBusy
	}
	if err := preflightOfficeZip(ctx, in, size); err != nil {
		return "", err
	}
	zr, err := zip.NewReader(in, size)
	if err != nil {
		return "", err
	}
	if len(zr.File) > maxOfficeEntries {
		return "", ErrTextLimit
	}
	pkg := &officePackage{ctx: ctx, files: make(map[string]*zip.File), remaining: maxOfficeRead, nodes: maxXMLNodes}
	var expanded uint64
	for _, f := range zr.File {
		name := f.Name
		if f.FileInfo().IsDir() {
			continue
		}
		if name == "" || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\\x00") || f.Mode()&fs.ModeSymlink != 0 || f.Flags&1 != 0 {
			return "", ErrDocument
		}
		if _, exists := pkg.files[name]; exists {
			return "", ErrDocument
		}
		if f.UncompressedSize64 > maxOfficeExpanded || expanded > maxOfficeExpanded-f.UncompressedSize64 {
			return "", ErrTextLimit
		}
		expanded += f.UncompressedSize64
		pkg.files[name] = f
	}
	rels, err := pkg.relations("")
	if err != nil {
		return "", err
	}
	main := ""
	for _, r := range rels {
		if relationshipKind(r.kind, "officeDocument") {
			if main != "" || r.external {
				return "", ErrDocument
			}
			main = r.target
		}
	}
	if main == "" {
		return "", ErrDocument
	}
	root, err := pkg.part(main)
	if err != nil {
		return "", err
	}
	out := &officeText{}
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "docx":
		err = pkg.word(main, root, out)
	case "xlsx":
		err = pkg.sheet(main, root, out)
	case "pptx":
		err = pkg.presentation(main, root, out)
	}
	if err != nil {
		return "", err
	}
	if out.err != nil {
		return "", out.err
	}
	result = strings.TrimSpace(out.b.String())
	if result == "" || !out.content {
		return "", ErrDocument
	}
	return result, nil
}

func (p *officePackage) part(name string) (*officeNode, error) {
	f := p.files[name]
	if f == nil {
		return nil, ErrDocument
	}
	if f.UncompressedSize64 > maxOfficePart || f.UncompressedSize64 > uint64(p.remaining) {
		return nil, ErrTextLimit
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(contextReader{p.ctx, rc}, int64(min(maxOfficePart, p.remaining))+1))
	closeErr := rc.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(raw) > maxOfficePart || len(raw) > p.remaining {
		return nil, ErrTextLimit
	}
	p.remaining -= len(raw)
	d := xml.NewDecoder(bytes.NewReader(raw))
	var root *officeNode
	var stack []*officeNode
	for {
		if err := p.ctx.Err(); err != nil {
			return nil, err
		}
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch v := tok.(type) {
		case xml.StartElement:
			p.nodes--
			if p.nodes < 0 || len(stack) >= 128 {
				return nil, ErrTextLimit
			}
			node := &officeNode{name: v.Name, attrs: v.Attr}
			if len(stack) == 0 {
				if root != nil {
					return nil, ErrDocument
				}
				root = node
			} else {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, node)
			}
			stack = append(stack, node)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, ErrDocument
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) == 0 {
				if strings.TrimSpace(string(v)) != "" {
					return nil, ErrDocument
				}
			} else {
				stack[len(stack)-1].text.Write(v)
			}
		case xml.Directive:
			return nil, ErrDocument // no DTD/custom entity expansion
		}
	}
	if root == nil || len(stack) != 0 {
		return nil, ErrDocument
	}
	return root, nil
}

func relationshipKind(got, kind string) bool {
	return got == officeRelNS+"/"+kind || got == "http://purl.oclc.org/ooxml/officeDocument/relationships/"+kind
}
func (p *officePackage) relations(part string) ([]officeRelation, error) {
	name := "_rels/.rels"
	if part != "" {
		name = path.Join(path.Dir(part), "_rels", path.Base(part)+".rels")
		if p.files[name] == nil {
			return nil, nil
		}
	}
	root, err := p.part(name)
	if err != nil {
		return nil, err
	}
	if !root.is(relNS, "Relationships") {
		return nil, ErrDocument
	}
	var result []officeRelation
	seen := map[string]bool{}
	for _, n := range root.children {
		if !n.is(relNS, "Relationship") {
			return nil, ErrDocument
		}
		r := officeRelation{id: n.attr("", "Id"), kind: n.attr("", "Type"), target: n.attr("", "Target"), external: n.attr("", "TargetMode") == "External"}
		if r.id == "" || seen[r.id] || r.target == "" {
			return nil, ErrDocument
		}
		seen[r.id] = true
		if !r.external {
			target, err := url.Parse(r.target)
			if err != nil || target.Scheme != "" || target.Host != "" || target.RawQuery != "" || target.Fragment != "" || strings.ContainsAny(target.Path, "\\\x00") {
				return nil, ErrDocument
			}
			if strings.HasPrefix(target.Path, "/") {
				r.target = path.Clean(strings.TrimPrefix(target.Path, "/"))
			} else {
				r.target = path.Clean(path.Join(path.Dir(part), target.Path))
			}
			if r.target == "." || r.target == ".." || strings.HasPrefix(r.target, "../") {
				return nil, ErrDocument
			}
		}
		result = append(result, r)
	}
	return result, nil
}
func relationByID(rels []officeRelation, id, kind string) (string, error) {
	for _, r := range rels {
		if r.id == id {
			if r.external || !relationshipKind(r.kind, kind) {
				return "", ErrDocument
			}
			return r.target, nil
		}
	}
	return "", ErrDocument
}

type officeText struct {
	b       strings.Builder
	err     error
	content bool
}

func (o *officeText) add(text string) {
	if o.err != nil {
		return
	}
	if len(text) > MaxTextBytes-o.b.Len() {
		o.err = ErrTextLimit
		return
	}
	o.b.WriteString(text)
}

// Bound the central directory before archive/zip allocates File headers. A small
// compressed archive can otherwise describe hundreds of thousands of entries.
// ZIP64/multi-disk packages are unnecessary at this 32 MiB input limit and rejected.
func preflightOfficeZip(ctx context.Context, in io.ReaderAt, size int64) error {
	if size < 22 {
		return ErrDocument
	}
	tail := make([]byte, min(size, int64(65535+22)))
	if _, err := in.ReadAt(tail, size-int64(len(tail))); err != nil {
		return err
	}
	at := -1
	for i := len(tail) - 22; i >= 0; i-- {
		if binary.LittleEndian.Uint32(tail[i:]) == 0x06054b50 && i+22+int(binary.LittleEndian.Uint16(tail[i+20:])) == len(tail) {
			at = i
			break
		}
	}
	if at < 0 {
		return ErrDocument
	}
	e := tail[at:]
	count := int(binary.LittleEndian.Uint16(e[10:]))
	dirSize := int64(binary.LittleEndian.Uint32(e[12:]))
	offset := int64(binary.LittleEndian.Uint32(e[16:]))
	if count > maxOfficeEntries || dirSize > 1<<20 {
		return ErrTextLimit
	}
	if binary.LittleEndian.Uint16(e[4:]) != 0 || binary.LittleEndian.Uint16(e[6:]) != 0 || int(binary.LittleEndian.Uint16(e[8:])) != count || offset+dirSize != size-int64(len(tail))+int64(at) {
		return ErrDocument
	}
	end := offset + dirSize
	seen := 0
	for offset < end {
		if err := ctx.Err(); err != nil {
			return err
		}
		seen++
		if seen > maxOfficeEntries {
			return ErrTextLimit
		}
		var header [46]byte
		if offset+46 > end {
			return ErrDocument
		}
		if _, err := in.ReadAt(header[:], offset); err != nil {
			return err
		}
		if binary.LittleEndian.Uint32(header[:]) != 0x02014b50 {
			return ErrDocument
		}
		next := offset + 46 + int64(binary.LittleEndian.Uint16(header[28:])) + int64(binary.LittleEndian.Uint16(header[30:])) + int64(binary.LittleEndian.Uint16(header[32:]))
		if next > end {
			return ErrDocument
		}
		offset = next
	}
	if seen != count {
		return ErrDocument
	}
	return nil
}
