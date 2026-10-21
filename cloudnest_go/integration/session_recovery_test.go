//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cloudenest_go/internal/service"
	storageMysql "cloudenest_go/internal/storage/mysql"
	"cloudenest_go/internal/store"
	"cloudenest_go/model"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
)

func fixtureLogin(t *testing.T, f *fixture, userID int64, password string) string {
	t.Helper()
	var user model.User
	if err := f.db.First(&user, userID).Error; err != nil {
		t.Fatal(err)
	}
	w := f.json(t, "POST", "/api/auth/login", "", map[string]string{"username": user.Username, "password": password}, http.StatusOK)
	var result struct {
		Token string `json:"token"`
	}
	decode(t, w, &result)
	if result.Token == "" {
		t.Fatal("missing login token")
	}
	return result.Token
}

func TestLogoutAllRevokesLegacyAndConcurrentSessions(t *testing.T) {
	f := newFixture(t)
	second := fixtureLogin(t, f, f.aliceID, "disposable-fixture-password")
	var alice model.User
	if err := f.db.First(&alice, f.aliceID).Error; err != nil {
		t.Fatal(err)
	}
	// A real pre-upgrade token has neither JTI nor the new session-version claim.
	legacy, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id": alice.ID, "username": alice.Username, "sub": fmt.Sprint(alice.ID), "exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString([]byte(f.cfg.JWTSecret))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.rdb.Set(context.Background(), "auth:token:"+legacy, alice.ID, time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	f.json(t, "GET", "/api/auth/me", legacy, nil, http.StatusOK)
	grant := model.UserPathPermission{UserID: alice.ID, Method: "GET", Path: "/api/files"}
	if err := f.db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/auth/logout-all", "", nil, http.StatusUnauthorized)
	// A caller-supplied ID cannot select someone else's sessions.
	f.json(t, "POST", "/api/auth/logout-all", f.alice, map[string]int64{"user_id": f.bobID}, http.StatusOK)
	for _, token := range []string{f.alice, second, legacy} {
		f.json(t, "GET", "/api/auth/me", token, nil, http.StatusUnauthorized)
	}
	f.json(t, "GET", "/api/auth/me", f.bob, nil, http.StatusOK)
	fresh := fixtureLogin(t, f, f.aliceID, "disposable-fixture-password")
	f.json(t, "GET", "/api/auth/me", fresh, nil, http.StatusOK)
	// Delayed issuance/cache writes from a pre-revocation login cannot revive it.
	if err := f.rdb.Set(context.Background(), "auth:token:"+legacy, alice.ID, time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	f.json(t, "GET", "/api/auth/me", legacy, nil, http.StatusUnauthorized)
}

func TestOperatorPasswordResetCLI(t *testing.T) {
	f := newFixture(t)
	var before model.User
	if err := f.db.First(&before, f.aliceID).Error; err != nil {
		t.Fatal(err)
	}
	file := f.upload(t, f.alice, "recovery-preserved.bin", []byte("preserve user's files"))
	temp := t.TempDir()
	binary := filepath.Join(temp, "cloudnest")
	if output, err := exec.Command("go", "build", "-o", binary, "../cmd/server").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v: %s", err, output)
	}
	password := "new-test-only-recovery-password"
	run := func(id int64, username, input string, success bool) {
		t.Helper()
		cmd := exec.Command(binary, "reset-password", fmt.Sprint(id), username)
		cmd.Dir = temp // No project .env or executable-side .env can be loaded.
		cmd.Env = []string{"MYSQL_DSN=" + f.cfg.MySQLDSN, "JWT_SECRET=" + f.cfg.JWTSecret, "JWT_EXPIRE_HOURS=1"}
		cmd.Stdin = strings.NewReader(input)
		output, err := cmd.CombinedOutput()
		if strings.Contains(string(output), password) || strings.Contains(string(output), before.Password) {
			t.Fatal("CLI leaked password material")
		}
		if (err == nil) != success {
			t.Fatalf("CLI success=%v, want %v; output=%s", err == nil, success, output)
		}
	}
	// Explicit target cross-check and strong-password validation must not mutate.
	run(before.ID, strings.ToUpper(before.Username), password, false)
	run(before.ID, before.Username, "short", false)
	run(9223372036854775807, before.Username, password, false)
	f.json(t, "GET", "/api/auth/me", f.alice, nil, http.StatusOK)
	run(before.ID, before.Username, password+"\n", true)
	var after model.User
	if err := f.db.First(&after, before.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.AuthVersion != before.AuthVersion+1 || after.Username != before.Username || after.Email != before.Email || after.Status != before.Status || after.Password == before.Password {
		t.Fatal("reset failed to preserve account or advance session version")
	}
	f.json(t, "GET", "/api/auth/me", f.alice, nil, http.StatusUnauthorized)
	f.json(t, "GET", "/api/auth/me", f.bob, nil, http.StatusOK)
	f.json(t, "POST", "/api/auth/login", "", map[string]string{"username": before.Username, "password": "disposable-fixture-password"}, http.StatusUnauthorized)
	fresh := fixtureLogin(t, f, before.ID, password)
	w := f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", file.ID), fresh, nil, http.StatusOK)
	if w.Body.String() != "preserve user's files" {
		t.Fatal("password recovery altered files")
	}
	var logs []model.OperationLog
	if err := f.db.Where("user_id = ? AND action = ?", before.ID, "operator_password_reset").Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || strings.Contains(logs[0].Detail, password) || strings.Contains(logs[0].Detail, after.Password) {
		t.Fatal("invalid recovery audit")
	}
	// Account recovery never silently enables disabled users or grants a role.
	if err := f.db.Model(&model.User{}).Where("id = ?", f.bobID).Update("status", 0).Error; err != nil {
		t.Fatal(err)
	}
	var bob model.User
	if err := f.db.First(&bob, f.bobID).Error; err != nil {
		t.Fatal(err)
	}
	run(bob.ID, bob.Username, password, true)
	if err := f.db.First(&bob, f.bobID).Error; err != nil {
		t.Fatal(err)
	}
	if bob.Status != 0 {
		t.Fatal("reset enabled disabled account")
	}
	var roles int64
	if err := f.db.Model(&model.UserRole{}).Where("user_id IN ?", []int64{f.aliceID, f.bobID}).Count(&roles).Error; err != nil {
		t.Fatal(err)
	}
	if roles != 0 {
		t.Fatal("reset granted a role")
	}
}

func TestSessionChangesRollbackWithAuditFailure(t *testing.T) {
	f := newFixture(t)
	var before model.User
	if err := f.db.First(&before, f.aliceID).Error; err != nil {
		t.Fatal(err)
	}
	const hook = "test:reject_auth_audit"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "operation_log" {
			tx.AddError(errors.New("test audit unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Callback().Create().Remove(hook) })
	svc := service.NewAuthService(storageMysql.NewAuthRepo(f.db), f.rdb, f.cfg)
	if err := svc.ResetPasswordForOperator(context.Background(), before.ID, before.Username, "new-test-only-recovery-password"); err == nil {
		t.Fatal("reset ignored audit failure")
	}
	f.json(t, "POST", "/api/auth/logout-all", f.alice, nil, http.StatusServiceUnavailable)
	var after model.User
	if err := f.db.First(&after, before.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Password != before.Password || after.AuthVersion != before.AuthVersion {
		t.Fatal("failed audit did not roll back password/session update")
	}
	f.json(t, "GET", "/api/auth/me", f.alice, nil, http.StatusOK)
}

func TestAuthSchemaUpgradePreservesAccounts(t *testing.T) {
	f := newFixture(t)
	var before model.User
	if err := f.db.First(&before, f.aliceID).Error; err != nil {
		t.Fatal(err)
	}
	var usersBefore int64
	if err := f.db.Model(&model.User{}).Count(&usersBefore).Error; err != nil {
		t.Fatal(err)
	}
	// This is the disposable database validated by newFixture, never a project DB.
	// Simulate its pre-G01 schema to exercise an actual additive MySQL upgrade.
	if err := f.db.Migrator().DropColumn(&model.User{}, "auth_version"); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckAuthSchema(f.db); err == nil {
		t.Fatal("legacy schema accepted for server startup")
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckAuthSchema(f.db); err != nil {
		t.Fatal(err)
	}
	var after model.User
	if err := f.db.First(&after, before.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Username != before.Username || after.Password != before.Password || after.Status != before.Status || after.AuthVersion != 0 {
		t.Fatal("additive migration changed existing account")
	}
	var usersAfter int64
	if err := f.db.Model(&model.User{}).Count(&usersAfter).Error; err != nil {
		t.Fatal(err)
	}
	if usersAfter != usersBefore {
		t.Fatal("migration changed account count")
	}
	f.json(t, "GET", "/api/auth/me", f.alice, nil, http.StatusOK)
	f.json(t, "POST", "/api/auth/logout-all", f.alice, nil, http.StatusOK)
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&after, before.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.AuthVersion != 1 {
		t.Fatal("repeated migration reset revocation state")
	}
	f.json(t, "GET", "/api/auth/me", f.alice, nil, http.StatusUnauthorized)
}

func TestLegacyPlaintextAccountRecovery(t *testing.T) {
	f := newFixture(t)
	if err := f.db.Model(&model.User{}).Where("id = ?", f.aliceID).Update("password", "legacy-test-only-plaintext").Error; err != nil {
		t.Fatal(err)
	}
	var user model.User
	if err := f.db.First(&user, f.aliceID).Error; err != nil {
		t.Fatal(err)
	}
	role := model.Role{RoleName: fmt.Sprintf("recovery-role-%d", f.aliceID)}
	if err := f.db.Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	binding := model.UserRole{UserID: f.aliceID, RoleID: role.ID}
	if err := f.db.Create(&binding).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/auth/login", "", map[string]string{"username": user.Username, "password": "legacy-test-only-plaintext"}, http.StatusUnauthorized)
	svc := service.NewAuthService(storageMysql.NewAuthRepo(f.db), f.rdb, f.cfg)
	const newPassword = "new-test-only-recovery-password"
	if err := svc.ResetPasswordForOperator(context.Background(), user.ID, user.Username, newPassword); err != nil {
		t.Fatal(err)
	}
	fresh := fixtureLogin(t, f, user.ID, newPassword)
	f.json(t, "GET", "/api/auth/me", fresh, nil, http.StatusOK)
	f.json(t, "GET", "/api/auth/me", f.alice, nil, http.StatusUnauthorized)
	var roleCount int64
	if err := f.db.Model(&model.UserRole{}).Where("user_id = ? AND role_id = ?", user.ID, role.ID).Count(&roleCount).Error; err != nil {
		t.Fatal(err)
	}
	if roleCount != 1 {
		t.Fatal("recovery removed an existing role")
	}
}
