package contract

import "errors"

// 业务层统一错误定义，handler 层可据此映射 HTTP 状态码。
var (
	ErrInvalidInput        = errors.New("invalid input")
	ErrNotFound            = errors.New("not found")
	ErrForbidden           = errors.New("forbidden")
	ErrFolderNotEmpty      = errors.New("folder is not empty")
	ErrConflict            = errors.New("conflict")
	ErrInternal            = errors.New("internal error")
	ErrShareExpired        = errors.New("share expired")
	ErrTargetNotFound      = errors.New("target not found")
	ErrTargetNotFolder     = errors.New("target is not folder")
	ErrCannotMoveToItself  = errors.New("cannot move to itself")
	ErrCannotMoveToDesc    = errors.New("cannot move folder into its descendant")
	ErrRecycleItemNotFound = errors.New("recycle item not found")
)
