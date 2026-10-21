package service

import (
	"context"
	"testing"

	"cloudenest_go/internal/config"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

func TestPasswordsRequireBcrypt(t *testing.T) {
	for _, password := range []string{"admin", "", "a-long-legacy-plaintext-password", "$2a$malformed"} {
		if verifyPassword(password, password) {
			t.Fatal("plaintext or invalid hash accepted")
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("test-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyPassword("test-password", string(hash)) || verifyPassword("wrong", string(hash)) {
		t.Fatal("bcrypt verification failed")
	}
}

type defaultAdminRepository struct {
	contract.AuthRepository
	hash string
}

func (r defaultAdminRepository) GetUserByUsername(context.Context, string) (model.User, error) {
	return model.User{ID: 1, Username: "admin", Status: 1, Password: r.hash}, nil
}

func TestLegacyDefaultAdministratorRequiresReset(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("admin"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewAuthService(defaultAdminRepository{hash: string(hash)}, nil, config.Config{})
	if _, err := svc.Login(context.Background(), "admin", "admin"); err != ErrPasswordResetRequired {
		t.Fatal("legacy default administrator login was not blocked")
	}
}

func TestLogoutReportsRevocationFailure(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	if err := rdb.Close(); err != nil {
		t.Fatal(err)
	}
	svc := NewAuthService(nil, rdb, config.Config{})
	if err := svc.Logout(context.Background(), "test-token"); err == nil {
		t.Fatal("revocation failure was hidden")
	}
}
