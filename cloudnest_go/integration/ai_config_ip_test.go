//go:build integration && aisearch

package integration

import (
	"strings"
	"testing"

	"cloudenest_go/model"
)

func TestAIHybridConfigAddressUsesPublishedEvidenceAndCurrentACL(t *testing.T) {
	f, _ := indexFixture(t)
	add := func(token, name, content string) model.File {
		file := f.upload(t, token, name, []byte(content))
		lexicalIndexFixture(t, f, file, content)
		return file
	}
	q := "服务器 IP 地址为 203.0.113.7 的配置文件"
	match := add(f.alice, "a.conf", "name=203.0.113.7.example\n#"+strings.Repeat("注", 400)+"\nhost=203.0.113.7:443")
	add(f.alice, "b.conf", "服务器 IP 地址设置\nhost=203.0.113.70")
	host := add(f.alice, "c.conf", "DNS 主机名服务配置文件\nhost=app.example")
	private := add(f.bob, "d.conf", "private secret\nhost=203.0.113.7")
	add(f.alice, "203.0.113.7.md", "host=203.0.113.7")
	f.upload(t, f.alice, "203.0.113.7-服务配置文件.conf", []byte("not indexed"))
	r := hybridQuery(t, f, f.alice, q)
	condition := r.NLPlan.Query.IPCondition
	if condition["presence"] != "required" || condition["family"] != "ipv4" || condition["address"] != "203.0.113.7" {
		t.Fatal("missing applied condition", condition)
	}
	if r.Retrieval.Mode != "keyword_fallback" || len(r.Items) != 1 || r.Pagination.Total != 1 || r.Items[0].ID != match.ID || !strings.Contains(r.Items[0].Snippet, "host=203.0.113.7:443") || !strings.Contains(strings.Join(r.Items[0].Sources, ","), "ip_address") {
		t.Fatalf("missing verified address evidence: %+v", r)
	}
	if anyIP := hybridQuery(t, f, f.alice, "找到包含服务器 IP 地址的配置文件。"); len(anyIP.Items) != 2 || anyIP.Pagination.Total != 2 {
		t.Fatal("generic address requirement lost valid files or admitted aliases", anyIP)
	}
	if r := hybridQuery(t, f, f.alice, "服务器 IP 地址为 198.51.100.237 的配置文件"); len(r.Items) != 0 || r.Pagination.Total != 0 {
		t.Fatal("absent address matched", r)
	}
	if r := hybridQuery(t, f, f.alice, "只用 DNS 主机名、不含固定 IP 地址的服务配置文件"); len(r.Items) != 1 || r.Items[0].ID != host.ID {
		t.Fatal("negative address condition failed", r)
	}
	grant := model.FilePermission{FileID: private.ID, UserID: f.aliceID, Permission: "read"}
	if err := f.db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	if r := hybridQuery(t, f, f.alice, q); len(r.Items) != 2 || r.Pagination.Total != 2 {
		t.Fatal("shared evidence missing", r)
	}
	if err := f.db.Delete(&grant).Error; err != nil {
		t.Fatal(err)
	}
	if r := hybridQuery(t, f, f.alice, q); len(r.Items) != 1 || r.Items[0].ID != match.ID {
		t.Fatal("revoked address visible", r)
	}
}
