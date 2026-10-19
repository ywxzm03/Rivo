package llm

import "errors"

// ErrContextOverflow 表示请求超出模型上下文窗口，由 responses client 在失败面包装
var ErrContextOverflow = errors.New("llm: context exceeds model window")

func IsContextOverflow(err error) bool {
	return errors.Is(err, ErrContextOverflow)
}
