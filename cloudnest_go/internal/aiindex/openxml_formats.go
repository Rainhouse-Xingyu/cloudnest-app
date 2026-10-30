package aiindex

import (
	"strconv"
	"strings"
)

func (p *officePackage) word(main string, root *officeNode, out *officeText) error {
	if !root.is(wordNS, "document") {
		return ErrDocument
	}
	body := root.child(wordNS, "body")
	if body == nil {
		return ErrDocument
	}
	if err := wordText(body, out); err != nil {
		return err
	}
	refs := map[string]map[string]bool{"header": {}, "footer": {}, "footnotes": {}, "endnotes": {}}
	var collect func(*officeNode) error
	collect = func(n *officeNode) error {
		if n.is(wordNS, "del") || n.is(wordNS, "moveFrom") {
			return nil
		}
		kind, ns := "", wordNS
		switch {
		case n.is(wordNS, "headerReference"):
			kind, ns = "header", officeRelNS
		case n.is(wordNS, "footerReference"):
			kind, ns = "footer", officeRelNS
		case n.is(wordNS, "footnoteReference"):
			kind = "footnotes"
		case n.is(wordNS, "endnoteReference"):
			kind = "endnotes"
		}
		if kind != "" {
			id := n.attr(ns, "id")
			if id == "" {
				return ErrDocument
			}
			refs[kind][id] = true
		}
		for _, c := range n.children {
			if err := collect(c); err != nil {
				return err
			}
		}
		return nil
	}
	if err := collect(body); err != nil {
		return err
	}
	rels, err := p.relations(main)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, kind := range []string{"header", "footer"} {
		for _, r := range rels {
			if !relationshipKind(r.kind, kind) || !refs[kind][r.id] {
				continue
			}
			delete(refs[kind], r.id)
			if r.external {
				return ErrDocument
			}
			if seen[r.target] {
				continue
			}
			seen[r.target] = true
			part, err := p.part(r.target)
			if err != nil {
				return err
			}
			rootName := "hdr"
			if kind == "footer" {
				rootName = "ftr"
			}
			if !part.is(wordNS, rootName) {
				return ErrDocument
			}
			if err := collect(part); err != nil {
				return err
			}
			out.add("\n")
			if err := wordText(part, out); err != nil {
				return err
			}
		}
		if len(refs[kind]) > 0 {
			return ErrDocument
		}
	}
	for _, kind := range []string{"footnotes", "endnotes"} {
		for _, r := range rels {
			if !relationshipKind(r.kind, kind) || len(refs[kind]) == 0 {
				continue
			}
			if r.external {
				return ErrDocument
			}
			part, err := p.part(r.target)
			if err != nil {
				return err
			}
			if !part.is(wordNS, kind) {
				return ErrDocument
			}
			for _, note := range part.children {
				if !note.is(wordNS, strings.TrimSuffix(kind, "s")) {
					continue
				}
				id := note.attr(wordNS, "id")
				if !refs[kind][id] {
					continue
				}
				delete(refs[kind], id)
				out.add("\n")
				if err := wordText(note, out); err != nil {
					return err
				}
			}
		}
		if len(refs[kind]) > 0 {
			return ErrDocument
		}
	}
	return out.err
}
func wordText(n *officeNode, out *officeText) error {
	if n.is(wordNS, "altChunk") {
		return ErrDocument
	} // linked HTML is not silently lost
	if n.is(wordNS, "del") || n.is(wordNS, "moveFrom") {
		return nil
	}
	if n.is(wordNS, "t") {
		out.content = out.content || strings.TrimSpace(n.value()) != ""
		out.add(n.value())
		return out.err
	}
	if n.is(wordNS, "tab") {
		out.add("\t")
	}
	if n.is(wordNS, "br") || n.is(wordNS, "cr") {
		out.add("\n")
	}
	for _, c := range n.children {
		if err := wordText(c, out); err != nil {
			return err
		}
	}
	if n.is(wordNS, "p") || n.is(wordNS, "tr") {
		out.add("\n")
	}
	if n.is(wordNS, "tc") {
		out.add("\t")
	}
	return out.err
}
func drawingText(n *officeNode, out *officeText) {
	if n.is(drawingNS, "fld") && (n.attr("", "type") == "slidenum" || strings.HasPrefix(n.attr("", "type"), "datetime")) {
		return
	}
	if n.is(drawingNS, "t") {
		out.content = out.content || strings.TrimSpace(n.value()) != ""
		out.add(n.value())
		return
	}
	if n.is(drawingNS, "br") {
		out.add("\n")
	}
	for _, c := range n.children {
		drawingText(c, out)
	}
	if n.is(drawingNS, "p") {
		out.add("\n")
	}
}
func (p *officePackage) presentation(main string, root *officeNode, out *officeText) error {
	if !root.is(presentationNS, "presentation") {
		return ErrDocument
	}
	list := root.child(presentationNS, "sldIdLst")
	if list == nil {
		return ErrDocument
	}
	rels, err := p.relations(main)
	if err != nil {
		return err
	}
	for i, n := range list.children {
		if !n.is(presentationNS, "sldId") {
			return ErrDocument
		}
		target, err := relationByID(rels, n.attr(officeRelNS, "id"), "slide")
		if err != nil {
			return err
		}
		slide, err := p.part(target)
		if err != nil {
			return err
		}
		if !slide.is(presentationNS, "sld") {
			return ErrDocument
		}
		out.add("\n[Slide " + strconv.Itoa(i+1) + "]\n")
		drawingText(slide, out)
		rels, err := p.relations(target)
		if err != nil {
			return err
		}
		for _, r := range rels {
			if relationshipKind(r.kind, "notesSlide") {
				if r.external {
					return ErrDocument
				}
				notes, err := p.part(r.target)
				if err != nil {
					return err
				}
				if !notes.is(presentationNS, "notes") {
					return ErrDocument
				}
				out.add("\n[Notes]\n")
				drawingText(notes, out)
			}
		}
		if out.err != nil {
			return out.err
		}
	}
	return out.err
}

func sheetString(n *officeNode) string {
	var b strings.Builder
	var visit func(*officeNode)
	visit = func(n *officeNode) {
		if n.is(sheetNS, "rPh") {
			return
		}
		if n.is(sheetNS, "t") {
			b.WriteString(n.value())
			return
		}
		for _, c := range n.children {
			visit(c)
		}
	}
	visit(n)
	return b.String()
}
func (p *officePackage) sheet(main string, root *officeNode, out *officeText) error {
	if !root.is(sheetNS, "workbook") {
		return ErrDocument
	}
	sheets := root.child(sheetNS, "sheets")
	if sheets == nil {
		return ErrDocument
	}
	rels, err := p.relations(main)
	if err != nil {
		return err
	}
	var shared []string
	foundShared := false
	for _, r := range rels {
		if relationshipKind(r.kind, "sharedStrings") {
			if foundShared || r.external {
				return ErrDocument
			}
			foundShared = true
			table, err := p.part(r.target)
			if err != nil {
				return err
			}
			if !table.is(sheetNS, "sst") {
				return ErrDocument
			}
			for _, n := range table.children {
				if !n.is(sheetNS, "si") {
					return ErrDocument
				}
				shared = append(shared, sheetString(n))
			}
		}
	}
	for _, s := range sheets.children {
		if !s.is(sheetNS, "sheet") {
			return ErrDocument
		}
		target, err := relationByID(rels, s.attr(officeRelNS, "id"), "worksheet")
		if err != nil {
			return err
		}
		sheet, err := p.part(target)
		if err != nil {
			return err
		}
		if !sheet.is(sheetNS, "worksheet") {
			return ErrDocument
		}
		out.add("\n[Sheet: " + s.attr("", "name") + "]\n")
		data := sheet.child(sheetNS, "sheetData")
		if data == nil {
			return ErrDocument
		}
		for _, row := range data.children {
			if !row.is(sheetNS, "row") {
				return ErrDocument
			}
			for _, cell := range row.children {
				if !cell.is(sheetNS, "c") {
					return ErrDocument
				}
				v, err := cellValue(cell, shared)
				if err != nil {
					return err
				}
				if v != "" {
					out.content = out.content || strings.TrimSpace(v) != ""
					out.add(cell.attr("", "r") + ": " + v + "\t")
				}
			}
			out.add("\n")
			if out.err != nil {
				return out.err
			}
		}
	}
	return out.err
}
func cellValue(cell *officeNode, shared []string) (string, error) {
	v := ""
	if n := cell.child(sheetNS, "v"); n != nil {
		v = n.value()
	}
	switch cell.attr("", "t") {
	case "s":
		i, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || i < 0 || i >= len(shared) {
			return "", ErrDocument
		}
		v = shared[i]
	case "inlineStr":
		n := cell.child(sheetNS, "is")
		if n == nil {
			return "", ErrDocument
		}
		v = sheetString(n)
	case "b":
		switch v {
		case "0":
			v = "false"
		case "1":
			v = "true"
		default:
			return "", ErrDocument
		}
	case "", "n", "str", "e", "d":
	default:
		return "", ErrDocument
	}
	if formula := cell.child(sheetNS, "f"); formula != nil {
		// Neither calculate formulas nor imply that a cached result is fresh.
		return "=" + formula.value() + " [cached: " + v + "]", nil
	}
	return v, nil
}
