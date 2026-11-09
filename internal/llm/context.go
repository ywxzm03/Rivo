package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
)

type userMessageContextKey struct{}

type toolContextKey struct{}
type workingDirKey struct{}

// ToolRuntime 保存当前父调用的执行能力，嵌套工具不继承这些回调
type ToolRuntime struct {
	CallID         string                                     // 当前调用 ID
	EarlierResults []ToolResultMessage                        // 同批已完成但未提交的只读结果
	ExecuteNested  func(context.Context, ToolCall) ToolResult // 当前工具快照的归一化执行入口
	Update         func(ToolResult) error                     // 由工具 owner 发布父调用进度
}

// ToolContext 把执行中 agent 的上下文交给需要派发或协调子任务的工具,
// 同时不暴露 Agent 实现
type ToolContext struct {
	Context Context
	Config  AgentConfig
	// ForkChild 表示当前 agent 本身是 fork 子 agent。fork 保留 Agent 工具
	// 是为了工具定义与父缓存一致,递归派发由调用方据此拒绝
	ForkChild bool
	// TaskID 是子 agent 的任务 ID,主 agent 为空。后台 bash 任务据此记录
	// 归属,子 agent 结束时可以回收它留下的孤儿进程
	TaskID  string
	Runtime ToolRuntime // 当前调用能力，不暴露 agent 或 session 对象
}

func WithToolContext(ctx context.Context, toolContext ToolContext) context.Context {
	return context.WithValue(ctx, toolContextKey{}, toolContext)
}

func CurrentToolContext(ctx context.Context) (ToolContext, bool) {
	value, ok := ctx.Value(toolContextKey{}).(ToolContext)
	return value, ok
}

func WithWorkingDir(ctx context.Context, dir string) context.Context {
	if dir == "" {
		return ctx
	}
	return context.WithValue(ctx, workingDirKey{}, dir)
}

func CurrentWorkingDir(ctx context.Context) string {
	dir, _ := ctx.Value(workingDirKey{}).(string)
	return dir
}

// WithCurrentUserMessageID 把当前 tool turn 关联到已持久化的 user entry
func WithCurrentUserMessageID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, userMessageContextKey{}, id)
}

func CurrentUserMessageID(ctx context.Context) string {
	id, _ := ctx.Value(userMessageContextKey{}).(string)
	return id
}

type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]any
	// PromptSnippet 返回 tools 分节中的一行说明，无贡献时返回空串
	PromptSnippet() string
	// PromptGuidelines 返回 rules 分节中的条目，无贡献时返回 nil
	PromptGuidelines() []string
	ExecutionMode() ToolExecutionMode
	Execute(ctx context.Context, args map[string]any) (ToolResult, error)
}

type ToolExecutionMode string

const (
	ToolExecutionParallel   ToolExecutionMode = "parallel"
	ToolExecutionSequential ToolExecutionMode = "sequential"
)

// ToolDetails 标记工具结果里的结构化详情。
// 实现者必须是可 JSON 往返的结构体，因为会话落盘与 UI 展示都依赖序列化。
// 方法导出是因为实现者分布在 tool/task/subagent 等多个包
type ToolDetails interface {
	ToolDetails()
}

// detailsRegistry 按工具名与 JSON 标志字段还原具体详情类型。
// 各实现包在 init 里注册，避免 llm 反向依赖上层包
var (
	detailsMu       sync.RWMutex
	detailsRegistry []detailsEntry
)

type detailsEntry struct {
	// toolName 限定该详情所属的工具,同名标志字段不会跨工具误判
	toolName string
	// marker 是能唯一标识该工具内某种详情变体的 JSON 字段名
	marker string
	decode func(json.RawMessage) (ToolDetails, error)
}

// RegisterToolDetails 注册一种详情类型的还原方式。
// toolName 必须与工具 Name() 一致;marker 只需在该工具内部唯一
func RegisterToolDetails[T ToolDetails](toolName, marker string) {
	detailsRegistry = append(detailsRegistry, detailsEntry{
		toolName: toolName,
		marker:   marker,
		decode: func(raw json.RawMessage) (ToolDetails, error) {
			var value T
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, err
			}
			return value, nil
		},
	})
}

// DecodeToolDetails 把落盘的原始 JSON 还原成指定工具的具体详情类型，
// 没有注册项或都不匹配时返回 nil，调用方按「无详情」处理
func DecodeToolDetails(toolName string, raw json.RawMessage) (ToolDetails, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	detailsMu.RLock()
	defer detailsMu.RUnlock()
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	for _, entry := range detailsRegistry {
		if entry.toolName != toolName {
			continue
		}
		if _, ok := probe[entry.marker]; !ok {
			continue
		}
		return entry.decode(raw)
	}
	return nil, nil
}

type ToolResult struct {
	Content []Content
	Details ToolDetails
	IsError bool
	Usage   *Usage
}

type Context struct {
	SystemPrompt string
	Messages     []Message
	Tools        []Tool
}
