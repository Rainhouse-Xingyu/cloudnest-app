package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

/*
Claims结构体定义了JWT的载荷，包括UserID、Username、IsAdmin等字段，
以及jwt.RegisteredClaims嵌套结构体，用于存储标准的JWT声明。
GenerateToken函数用于生成JWT，接受密钥、用户ID、用户名、管理员标志和过期时间作为参数，返回生成的JWT字符串、过期时间和错误信息。
ParseToken函数用于解析JWT，接受密钥和JWT字符串作为参数，返回Claims结构体和错误信息。
*/
type Claims struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	IsAdmin  bool   `json:"is_admin"`
	jwt.RegisteredClaims
}

// GenerateToken函数生成JWT，接收上述参数，返回JWT字符串、过期时间和错误信息
func GenerateToken(secret string, userID int64, username string, isAdmin bool, ttl time.Duration) (string, time.Time, error) {
	return generateTokenAt(secret, userID, username, isAdmin, ttl, time.Now())
}

func generateTokenAt(secret string, userID int64, username string, isAdmin bool, ttl time.Duration, now time.Time) (string, time.Time, error) {
	// A timestamp is not a session identifier: logins in the same second must
	// remain independently revocable in Redis. Never derive this ID from user data.
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return "", time.Time{}, fmt.Errorf("generate token ID: %w", err)
	}
	expiresAt := now.Add(ttl)
	//Claims包含所需的用户信息和JWT声明。
	claims := Claims{
		UserID:   userID,
		Username: username,
		IsAdmin:  isAdmin,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        base64.RawURLEncoding.EncodeToString(nonce),
			Subject:   fmt.Sprintf("%d", userID),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			NotBefore: jwt.NewNumericDate(now),
		},
	}
	//使用HS256算法签名生成JWT密钥
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString([]byte(secret))
	//如果签名失败，返回空字符串，0时间和错误信息。
	if err != nil {
		return "", time.Time{}, err
	}
	//返回生成的JWT字符串、过期时间和错误信息。
	return tokenStr, expiresAt, nil
}

// ParseToken函数解析JWT，接收密钥和字符串，返回claims结构体以及错误信息。
func ParseToken(secret string, tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		//验证签名是否使用HS256算法，不是则返回错误。
		if token.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	if claims.UserID <= 0 || claims.Subject != fmt.Sprintf("%d", claims.UserID) || claims.Username == "" {
		return nil, fmt.Errorf("invalid token identity")
	}

	return claims, nil
}
