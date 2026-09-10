package agent

import (
	"fmt"
	"time"
)

// 打印 Hello World
func HelloWorld() string {
	fmt.Println("Hello World")
	return "Hello World"
}

// 返回 pong
func Ping() string {
	return "pong"
}

// 返回当前时间
func Now() string {
	return time.Now().Format(time.RFC3339)
}
