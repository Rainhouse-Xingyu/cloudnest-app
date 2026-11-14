//go:build integration && aisearch

package integration

import "testing"

func TestAIHybridRequiresExplicitTechnologyName(t *testing.T) {
	f, _ := indexFixture(t)
	add := func(name, text string) int64 {
		file := f.upload(t, f.alice, name, []byte(text))
		lexicalIndexFixture(t, f, file, text)
		return file.ID
	}
	good := add("redis-migration.txt", "Redis 数据库迁移回退操作的方案")
	add("lookalike.txt", "RedisEnterprise 数据库迁移回退操作的方案")
	add("Oracle.txt", "其他数据库迁移回退操作的方案")
	if got := hybridQuery(t, f, f.alice, "Redis 数据库迁移回退操作的方案"); len(got.Items) != 1 || got.Items[0].ID != good {
		t.Fatal("explicit name matched another product", got)
	}
	if got := hybridQuery(t, f, f.alice, "记录 Oracle 数据库迁移回退操作的方案"); len(got.Items) != 0 {
		t.Fatal("filename alone satisfied an explicit technology name", got)
	}
	add("auth.txt", "使用 KeycloakX 配置 OAuth2 单点登录的指南")
	if got := hybridQuery(t, f, f.alice, "使用 Keycloak 配置 OAuth2 单点登录的指南"); len(got.Items) != 0 {
		t.Fatal("partial component name satisfied explicit constraint", got)
	}
}
