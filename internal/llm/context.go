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

// ToolContext 把执行中 agent 的上下文交给需要派发或协调子任务的工具,
// 同时不暴露 Agent 实现
type ToolContext struct {
	Context Context
	Config  AgentConfig
	// ForkChild 表示当前 agent 本身是 fork 子 agent。fork 保留 Agent 工具
	// 是为了工具定义与父缓存一致,递归派发由调用方据此拒绝
	ForkChild bool
	// AgentID 是子 agent 的任务 ID,主 agent 为空。后台 bash 任务据此记录
	// 归属,子 agent 结束时可以回收它留下的孤儿进程
	AgentID string
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

// detailsRegistry 按 JSON 里的标志字段还原具体详情类型。
// 各实现包在 init 里注册，避免 llm 反向依赖上层包
var (
	detailsMu       sync.RWMutex
	detailsRegistry []detailsEntry
)

type detailsEntry struct {
	// marker 是能唯一标识该类型的 JSON 字段名
	marker string
	decode func(json.RawMessage) (ToolDetails, error)
}

// RegisterToolDetails 注册一种详情类型的还原方式。
// marker 必须选该类型独有的字段，否则会误判
func RegisterToolDetails[T ToolDetails](marker string) {
	detailsRegistry = append(detailsRegistry, detailsEntry{
		marker: marker,
		decode: func(raw json.RawMessage) (ToolDetails, error) {
			var value T
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, err
			}
			return value, nil
		},
	})
}

// DecodeToolDetails 把落盘的原始 JSON 还原成具体详情类型，
// 没有注册项或都不匹配时返回 nil，调用方按「无详情」处理
func DecodeToolDetails(raw json.RawMessage) (ToolDetails, error) {
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
