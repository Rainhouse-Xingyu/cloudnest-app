package service

import (
	"net/netip"
	"strings"
	"testing"

	"cloudenest_go/internal/service/contract"
)

func TestExplicitConfigIPSeparatesConditionsFromBroadQuestions(t *testing.T) {
	for _, tc := range []struct {
		q, addr, family string
		exclude         bool
	}{
		{"找到包含服务器 IP 地址的配置文件。", "", "ip", false},
		{"搜索含有 IPv6 地址的设置文件", "", "ipv6", false},
		{"只用 DNS 主机名、不含固定 IP 地址的服务配置文件", "", "ip", true},
		{"服务地址为 203.0.113.7 的配置文件", "203.0.113.7", "", false},
		{"排除 2001:db8::a 的配置文件", "2001:db8::a", "", true},
	} {
		c := explicitConfigIP(tc.q)
		if c == nil || c.exclude != tc.exclude || c.family != tc.family || (c.address.IsValid() && c.address.String() != tc.addr) || (tc.addr != "" && !c.address.IsValid()) {
			t.Fatalf("wrong clause for %q: %+v", tc.q, c)
		}
	}
	for _, q := range []string{
		"涉及 IP 协议的配置文件", "包含 IP 地址字段的配置文件", "包含 IP 地址的正则表达式的配置文件", "不是不含 IP 地址的配置文件", "关于 203.0.113.7 的说明文档",
		"包含 203.0.113.7 或 203.0.113.8 的配置文件", "包含 203.0.113.0/24 的配置文件",
		"无需包含 IP 地址的配置文件", "包含IP地址的照片",
	} {
		if explicitConfigIP(q) != nil {
			t.Fatalf("unexpected hard filter for %q", q)
		}
	}
}

func TestIPConditionsUseWholeTokensAndCompleteDocument(t *testing.T) {
	want := netip.MustParseAddr("203.0.113.7")
	for _, tc := range []struct {
		text                   string
		leftCut, rightCut, hit bool
	}{
		{"host=203.0.113.7:5432", false, false, true},
		{"host=203.0.113.70", false, false, false},
		{"host=203.0.113.7.example", false, false, false},
		{"host=v203.0.113.7", false, false, false},
		{"host=203.0.113.7", false, true, false},
		{"203.0.113.7\n", true, false, false},
	} {
		found := false
		visitIP(tc.text, tc.leftCut, tc.rightCut, func(a netip.Addr, at int) bool {
			if a == want {
				found = true
			}
			return true
		})
		if found != tc.hit {
			t.Fatal(tc, found)
		}
	}
	testRows := func(texts []string, declared int) bool {
		var evidence configIPEvidence
		c := explicitConfigIP("不含 IPv6 地址的配置文件")
		for i, s := range texts {
			evidence.observe(c, contract.AIHybridRow{Generation: 1, Ordinal: i, ChunkCount: declared, StartRune: i * 600, Content: s})
		}
		return evidence.accepts(c)
	}
	if testRows([]string{"service=example.test", "endpoint=[2001:DB8::7]:8443"}, 2) || testRows([]string{"service=example.test"}, 2) || !testRows([]string{"service=example.test", "port=8443"}, 2) {
		t.Fatal("absence decision ignored another or missing chunk")
	}
	b := strings.Repeat("字", 320) + "host=[2001:0DB8:0:0::7]:8443"
	var evidence configIPEvidence
	m, ok := evidence.observe(explicitConfigIP("地址为 2001:db8::7 的配置文件"), contract.AIHybridRow{Generation: 1, ChunkCount: 1, Content: b})
	if !ok || m.atRune != 326 {
		t.Fatal("valid IPv6 literal/rune coordinate lost", m)
	}
}
