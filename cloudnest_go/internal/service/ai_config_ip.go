package service

import (
	"net/netip"
	"regexp"
	"strings"
	"unicode/utf8"

	"cloudenest_go/internal/service/contract"
)

// These are explicit content conditions on configuration files. Other phrasing
// keeps normal hybrid retrieval. Never infer absence from a filename or a
// missing/unpublished index.
type configIPClause struct {
	exclude bool
	family  string
	address netip.Addr
}

func (c *configIPClause) descriptor() *AIConfigIPCondition {
	if c == nil {
		return nil
	}
	result := &AIConfigIPCondition{Presence: "required", Family: c.family}
	if c.exclude {
		result.Presence = "excluded"
	}
	if c.address.IsValid() {
		result.Address = c.address.String()
		if c.address.Is4() {
			result.Family = "ipv4"
		} else {
			result.Family = "ipv6"
		}
	} else if result.Family == "ip" {
		result.Family = "any"
	}
	return result
}

var containsIP = regexp.MustCompile(`(不包含|不含有|不含|没有|包含|含有|带有|写有)(?:明确|固定|静态|服务器|具体|任何)*(ipv4|ipv6|ip)(?:地址)?(?:的(?:服务|服务器|网络|数据库)?(?:配置文件|设置文件)|[。？]|$)`)
var beforeIP = regexp.MustCompile(`(不包含|不含有|不含|排除|包含|含有|写有|设置为|设为|地址为|主机为)(?:明确|固定|静态|服务器|具体|ipv4|ipv6|ip|地址|为|是)*$`)

func explicitConfigIP(question string) *configIPClause {
	if !strings.Contains(question, "配置文件") && !strings.Contains(question, "设置文件") {
		return nil
	}
	if strings.Contains(question, "照片") || strings.Contains(question, "图片") {
		return nil
	}
	q := strings.ToLower(question)
	// A single predicate cannot represent a disjunction, CIDR, range, or a
	// comparison. Leave those questions to the general retrieval pipeline.
	if strings.ContainsAny(q, "或/<>=~～") || strings.Contains(q, "范围") || strings.Contains(q, "无需") || strings.Contains(q, "可选") {
		return nil
	}
	compact := func(s string) string { return strings.Join(strings.Fields(s), "") }
	var addresses []netip.Addr
	var prefix string
	visitIP(q, false, false, func(a netip.Addr, at int) bool {
		addresses = append(addresses, a)
		prefix = compact(q[:at])
		return len(addresses) < 2
	})
	if len(addresses) > 1 {
		return nil
	}
	var clause configIPClause
	var relation string
	if len(addresses) == 1 {
		match := beforeIP.FindStringSubmatch(strings.TrimRight(prefix, "：:［[\"'“"))
		if match == nil {
			return nil
		}
		relation = match[1]
		clause.address = addresses[0]
	} else {
		matches := containsIP.FindAllStringSubmatch(compact(q), 2)
		if len(matches) != 1 {
			return nil
		}
		relation, clause.family = matches[0][1], matches[0][2]
	}
	clause.exclude = strings.HasPrefix(relation, "不") || relation == "排除" || relation == "没有"
	rest := strings.Replace(q, relation, "", 1)
	if strings.Contains(rest, "不") || strings.Contains(rest, "排除") || strings.Contains(rest, "没有") || strings.Contains(rest, "而非") {
		return nil
	}
	return &clause
}

// Scan whole ASCII tokens so a hostname, version prefix, or longer IPv4
// address cannot match a substring. Overlapping chunks cover all address
// literals; don't interpret a cut token at a non-document edge as complete.
func visitIP(s string, leftCut, rightCut bool, visit func(netip.Addr, int) bool) {
	word := func(c byte) bool {
		return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == ':' || c == '%' || c == '-' || c == '_'
	}
	for i := 0; i < len(s); {
		if !word(s[i]) {
			i++
			continue
		}
		start := i
		for i < len(s) && word(s[i]) {
			i++
		}
		if leftCut && start == 0 || rightCut && i == len(s) {
			continue
		}
		raw := strings.TrimSuffix(s[start:i], ".")
		if !strings.ContainsAny(raw, ".:") {
			continue
		}
		address, err := netip.ParseAddr(raw)
		if err != nil {
			hostPort, err := netip.ParseAddrPort(raw)
			if err != nil {
				continue
			}
			address = hostPort.Addr()
		}
		if !visit(address, start) {
			return
		}
	}
}

type configIPEvidence struct {
	seen, expected int
	broken, found  bool
}

type ipMatch struct {
	found  bool
	atRune int
}

func (e *configIPEvidence) observe(c *configIPClause, row contract.AIHybridRow) (ipMatch, bool) {
	if c == nil {
		return ipMatch{}, true
	}
	if e.seen == 0 {
		e.expected = row.ChunkCount
	}
	if row.Generation <= 0 || row.ChunkCount != e.expected || row.Ordinal != e.seen || row.Content == "" {
		e.broken = true
	}
	e.seen++
	var hit ipMatch
	visitIP(row.Content, row.StartRune > 0, row.Ordinal+1 < row.ChunkCount, func(a netip.Addr, at int) bool {
		if c.address.IsValid() && a != c.address || c.family == "ipv4" && !a.Is4() || c.family == "ipv6" && !a.Is6() {
			return true
		}
		e.found = true
		hit = ipMatch{found: true, atRune: utf8.RuneCountInString(row.Content[:at])}
		return false
	})
	if c.exclude {
		return ipMatch{}, true
	}
	return hit, hit.found
}

func (e *configIPEvidence) accepts(c *configIPClause) bool {
	return c == nil || (!e.broken && e.expected > 0 && e.seen == e.expected && e.found != c.exclude)
}
