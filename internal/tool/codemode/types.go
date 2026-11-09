// Package codemode 用单次 goja 执行编排当前 agent 的工具
package codemode

import (
	"encoding/json"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// History 读取原始活跃父链中的成功工具详情
type History interface {
	BranchToolDetails(toolName string) ([]json.RawMessage, error)
}

// Tool 只绑定会话历史，不保存跨调用 VM 或可变 store
type Tool struct {
	history History // 当前会话的原始父链读取入口
}

// New 构造绑定指定会话历史的 codemode 工具
func New(history History) *Tool { return &Tool{history: history} }

// Params 是模型提交的 JavaScript 源码
type Params struct {
	Code string `json:"code"` // async 函数体，不带 Markdown fence
}

// SourceOptions 只允许出现在源码首行
type SourceOptions struct {
	MaxOutputTokens *int   `json:"max_output_tokens,omitempty"` // 默认 10000，0 隐藏普通文本，图片与类型标签仍成对保留
	TimeoutMS       *int64 `json:"timeout_ms,omitempty"`        // 正整数毫秒，缺省只受父 context 约束
}

// ScriptImage 是脚本可显式输出的图片
type ScriptImage struct {
	Type     string `json:"type"`     // 固定为 image
	Data     string `json:"data"`     // base64 内容
	MIMEType string `json:"mimeType"` // 支持的实际图片格式
}

// ScriptToolResult 为所有嵌套工具提供统一 JSON 返回值
type ScriptToolResult struct {
	Text    string          `json:"text"`    // 原工具文本
	Images  []ScriptImage   `json:"images"`  // 空时仍编码为 []
	Details json.RawMessage `json:"details"` // 原工具详情，无详情时为 null
}

// ToolInfo 是脚本工具发现返回的完整条目
type ToolInfo struct {
	Name        string          `json:"name"`        // 工具原始名称
	Description string          `json:"description"` // 原工具描述
	Parameters  json.RawMessage `json:"parameters"`  // 完整参数 schema
	Guidelines  []string        `json:"guidelines"`  // 无规则时为 []
}

type CallStatus string

const (
	CallQueued    CallStatus = "queued"
	CallRunning   CallStatus = "running"
	CallOK        CallStatus = "ok"
	CallError     CallStatus = "error"
	CallCancelled CallStatus = "cancelled"
)

// Call 记录一次嵌套调用的展示摘要
type Call struct {
	ID         string     `json:"id"`                   // 父 ID 加递增序号
	Name       string     `json:"name"`                 // 原工具名称
	Args       string     `json:"args"`                 // 最多 200 字符的参数 JSON 预览
	Status     CallStatus `json:"status"`               // 排队、运行或终态
	DurationMS *int64     `json:"durationMs,omitempty"` // 未启动为 nil，0 有效
	Error      string     `json:"error,omitempty"`      // 最多 500 字符的失败说明
}

// StoreWrites 随一次成功父工具结果原子提交
type StoreWrites struct {
	SessionID string                     `json:"sessionId"` // 隔离 fork 复制的父历史
	Set       map[string]json.RawMessage `json:"set"`       // JSON null 与删除不同
	Delete    []string                   `json:"delete"`    // 最终操作为删除的键
}

// Details 是进度快照和最终结果共用的领域数据
type Details struct {
	Codemode       bool         `json:"codemode"`                 // 唯一类型 marker，恒为 true
	Calls          []Call       `json:"calls"`                    // 按发行顺序，空时为 []
	WallTimeMS     int64        `json:"wallTimeMs"`               // 本次 Execute 耗时，单位毫秒
	OutputLines    int          `json:"outputLines"`              // 预算裁剪前的脚本文本逻辑行数
	Error          string       `json:"error,omitempty"`          // 父脚本错误，保留源码位置
	FullOutputPath string       `json:"fullOutputPath,omitempty"` // 预算裁剪时完整文本的路径
	StoreWrites    *StoreWrites `json:"storeWrites,omitempty"`    // 仅最终成功且存在变更时非 nil
}

func (Details) ToolDetails() {}
func init()                  { llm.RegisterToolDetails[Details]("codemode", "codemode") }
