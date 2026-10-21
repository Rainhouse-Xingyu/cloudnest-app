package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"cloudenest_go/internal/auth"
	"cloudenest_go/internal/config"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"

	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const redisTokenKeyPrefix = "auth:token:"

var (
	// ErrInvalidCredentials 表示用户名或密码错误。
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrUserDisabled 表示用户被禁用。
	ErrUserDisabled          = errors.New("user disabled")
	ErrPasswordResetRequired = errors.New("password reset required")
)

// LoginResult 登录成功后返回的业务结果。
type LoginResult struct {
	Token     string
	ExpiresAt time.Time
	User      model.User
	IsAdmin   bool
}

// AuthService 承接认证模块业务逻辑。
type AuthService struct {
	repo contract.AuthRepository
	rdb  *redis.Client
	cfg  config.Config
}

func NewAuthService(repo contract.AuthRepository, rdb *redis.Client, cfg config.Config) *AuthService {
	return &AuthService{repo: repo, rdb: rdb, cfg: cfg}
}

// Login 执行账号密码登录与 token 写入。
func (s *AuthService) Login(ctx context.Context, username, password string) (LoginResult, error) {
	var u model.User
	u, err := s.repo.GetUserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return LoginResult{}, ErrInvalidCredentials
		}
		return LoginResult{}, contract.ErrInternal
	}

	if u.Status == 0 {
		return LoginResult{}, ErrUserDisabled
	}

	if !verifyPassword(password, u.Password) {
		return LoginResult{}, ErrInvalidCredentials
	}
	if u.Username == "admin" && password == "admin" {
		return LoginResult{}, ErrPasswordResetRequired
	}

	isAdmin, err := s.repo.CheckUserIsAdmin(ctx, u.ID)
	if err != nil {
		return LoginResult{}, contract.ErrInternal
	}

	token, expiresAt, err := auth.GenerateToken(s.cfg.JWTSecret, u.ID, u.Username, isAdmin, u.AuthVersion, s.cfg.JWTTTL())
	if err != nil {
		return LoginResult{}, contract.ErrInternal
	}

	if err := s.rdb.Set(ctx, redisTokenKeyPrefix+token, u.ID, time.Until(expiresAt)).Err(); err != nil {
		return LoginResult{}, contract.ErrInternal
	}

	return LoginResult{
		Token:     token,
		ExpiresAt: expiresAt,
		User:      u,
		IsAdmin:   isAdmin,
	}, nil
}

// Logout 删除 Redis token 缓存。
func (s *AuthService) Logout(ctx context.Context, token string) error {
	return s.rdb.Del(ctx, redisTokenKeyPrefix+token).Err()
}

// GetUserProfile 获取当前用户信息。
func (s *AuthService) GetUserProfile(ctx context.Context, userID int64) (model.User, error) {
	u, err := s.repo.GetUserProfile(ctx, userID)
	if err != nil {
		return model.User{}, contract.ErrInternal
	}
	return u, nil
}

func verifyPassword(inputPassword string, storedPassword string) bool {
	if isBcryptHash(storedPassword) {
		return bcrypt.CompareHashAndPassword([]byte(storedPassword), []byte(inputPassword)) == nil
	}
	return false
}

func isBcryptHash(raw string) bool {
	return strings.HasPrefix(raw, "$2a$") || strings.HasPrefix(raw, "$2b$") || strings.HasPrefix(raw, "$2y$")
}

// 管理员判断已下沉至 AuthRepository。

// LogoutAll invalidates the caller's sessions durably, including old tokens and
// tokens issued by a login that read an earlier account snapshot.
func (s *AuthService) LogoutAll(ctx context.Context, userID int64, clientIP string) error {
	return s.repo.RevokeUserSessions(ctx, userID, clientIP)
}

// ResetPasswordForOperator is used only by the local maintenance command. There
// is no public HTTP password-recovery route or automatic legacy-password upgrade.
func (s *AuthService) ResetPasswordForOperator(ctx context.Context, userID int64, expectedUsername, password string) error {
	if userID <= 0 || expectedUsername == "" {
		return errors.New("positive user ID and exact username are required")
	}
	if err := auth.ValidateNewPassword(password); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return errors.New("password hashing failed")
	}
	return s.repo.ResetUserPassword(ctx, userID, expectedUsername, string(hash))
}
