package contract

import "errors"

// 业务层统一错误定义，handler 层可据此映射 HTTP 状态码。
var (
	ErrInvalidInput = errors.New("invalid input")
	ErrNotFound     = errors.New("not found")
	ErrForbidden    = errors.New("forbidden")
	ErrConflict     = errors.New("conflict")
	ErrInternal     = errors.New("internal error")
	ErrShareExpired = errors.New("share expired")
)
