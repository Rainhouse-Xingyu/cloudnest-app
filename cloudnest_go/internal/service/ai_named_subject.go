package service

import (
	"regexp"
	"strings"
)

// Only explicit syntax binds a technology name. Similarity alone does not
// establish that a document concerns the named database or configured component.
var namedSubjectSyntax = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.-])([A-Za-z][A-Za-z0-9_.-]{1,63})\s*数据库|使用\s*([A-Za-z][A-Za-z0-9_.-]{1,63})\s*(?:配置|设置)`)

func namedSubject(question string) string {
	if strings.ContainsAny(question, "或、") || strings.Contains(question, "以及") ||
		strings.Contains(question, "而非") || strings.Contains(question, "不使用") || strings.Contains(question, "不用") ||
		strings.Contains(question, "排除") || strings.Contains(question, "不是") {
		return ""
	}
	matches := namedSubjectSyntax.FindAllStringSubmatch(question, 2)
	if len(matches) != 1 {
		return ""
	}
	for _, name := range matches[0][1:] {
		if name != "" {
			name = strings.ToLower(name)
			switch name {
			case "sql", "nosql", "newsql", "database":
				return ""
			}
			return name
		}
	}
	return ""
}

func hasNamedSubject(text, name string, leftCut, rightCut bool) bool {
	word := func(b byte) bool {
		return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_' || b == '-' || b == '.'
	}
	text = strings.ToLower(text)
	for offset := 0; offset < len(text); {
		i := strings.Index(text[offset:], name)
		if i < 0 {
			return false
		}
		start := offset + i
		end := start + len(name)
		if (start == 0 && !leftCut || start > 0 && !word(text[start-1])) &&
			(end == len(text) && !rightCut || end < len(text) && !word(text[end])) {
			return true
		}
		offset = end
	}
	return false
}
