package llm

import "errors"

// ErrContextOverflow 表示请求超出模型上下文窗口，由 responses client 在各失败面按错误签名包装
var ErrContextOverflow = errors.New("llm: 上下文超出模型窗口")

// IsContextOverflow 判断错误是否为上下文溢出类错误
func IsContextOverflow(err error) bool {
	return errors.Is(err, ErrContextOverflow)
}
