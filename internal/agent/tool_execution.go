package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// toolExecutor 在批次开始时捕获执行边界，工具 goroutine 不读取 Agent 可变字段
type toolExecutor struct {
	context    llm.ToolContext // 当前工具集、模型和任务归属的只读快照
	workingDir string          // 当前批次工作目录
}

func (a *Agent) toolExecutor() toolExecutor {
	return toolExecutor{context: llm.ToolContext{Context: a.ctx, Config: a.cfg, ForkChild: a.forkChild, TaskID: a.taskID}, workingDir: a.workingDir}
}

func (e toolExecutor) find(name string) llm.Tool {
	for _, tool := range e.context.Context.Tools {
		if tool.Name() == name {
			return tool
		}
	}
	return nil
}

// execute 注入当前调用 ID，嵌套调用只接收自身 ID，不继承父回调
func (e toolExecutor) execute(ctx context.Context, call llm.ToolCall, runtime llm.ToolRuntime) (llm.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return llm.ToolResult{}, err
	}
	host := e.context
	host.Runtime = runtime
	ctx = llm.WithToolContext(ctx, host)
	ctx = llm.WithWorkingDir(ctx, e.workingDir)
	tool := e.find(call.Name)
	if tool == nil {
		return llm.ToolResult{}, fmt.Errorf("agent: no tool named %q", call.Name)
	}
	return tool.Execute(ctx, call.Arguments)
}

func (e toolExecutor) nested(ctx context.Context, call llm.ToolCall) llm.ToolResult {
	result, err := e.execute(ctx, call, llm.ToolRuntime{CallID: call.ID})
	return normalizedToolResult(ctx, result, err)
}

// normalizedToolResult 统一直调与嵌套工具的普通失败，不处理事件消费者故障
func normalizedToolResult(ctx context.Context, result llm.ToolResult, err error) llm.ToolResult {
	if err == nil {
		return result
	}
	text := "tool error: " + err.Error()
	if ctx.Err() != nil {
		text = "Operation aborted"
	}
	result.Content = []llm.Content{llm.TextContent{Text: text}}
	result.IsError = true
	return result
}

// executeToolCall 保留基础设施错误与部分结果，避免普通错误包装覆盖正文
func executeToolCall(ctx context.Context, executor toolExecutor, call llm.ToolCall, earlier []llm.ToolResultMessage, emit llm.AgentEventSink) (llm.ToolResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var eventErr error
	runtime := llm.ToolRuntime{CallID: call.ID, EarlierResults: earlier, ExecuteNested: executor.nested}
	runtime.Update = func(partial llm.ToolResult) error {
		if eventErr != nil {
			return eventErr
		}
		if err := emit(llm.ToolExecutionUpdateEvent{ToolCallID: call.ID, ToolName: call.Name, Arguments: call.Arguments, PartialResult: partial}); err != nil {
			eventErr = err
			cancel()
			return err
		}
		return nil
	}
	result, err := executor.execute(ctx, call, runtime)
	if eventErr != nil {
		result.IsError = true
		return result, eventErr
	}
	return normalizedToolResult(ctx, result, err), nil
}

func (a *Agent) executeToolBatch(ctx context.Context, calls []llm.ToolCall) ([]llm.ToolResultMessage, error) {
	executor := a.toolExecutor()
	for _, call := range calls {
		if tool := executor.find(call.Name); tool != nil && tool.ExecutionMode() == llm.ToolExecutionSequential {
			return a.executeSequentialBatch(ctx, calls)
		}
	}
	return a.executeParallelBatch(ctx, calls)
}

// executeSequentialBatch 每次执行前注入本批已经完成的只读结果视图
func (a *Agent) executeSequentialBatch(ctx context.Context, calls []llm.ToolCall) ([]llm.ToolResultMessage, error) {
	executor := a.toolExecutor()
	results := make([]llm.ToolResultMessage, 0, len(calls))
	var eventErr error
	completed := 0
	for completed < len(calls) {
		call := calls[completed]
		if eventErr = a.emitToolStart(call); eventErr != nil {
			break
		}
		var result llm.ToolResult
		if ctx.Err() != nil {
			result = abortedToolResult()
		} else {
			result, eventErr = executeToolCall(ctx, executor, call, results[:completed:completed], a.emit)
		}
		results = append(results, toolResultMessage(call, result))
		completed++
		if eventErr != nil {
			break
		}
		if eventErr = a.emit(toolEndEvent(call, result)); eventErr != nil {
			break
		}
		if result.IsError && ctx.Err() != nil {
			break
		}
	}
	for _, call := range calls[completed:] {
		results = append(results, toolResultMessage(call, abortedToolResult()))
	}
	if err := a.commitToolResults(results, eventErr == nil); err != nil {
		return results, err
	}
	return results, eventErr
}

// toolCompletion 将真实结果与更新出口故障分别交回批次
type toolCompletion struct {
	index    int
	result   llm.ToolResult
	eventErr error
}

func (a *Agent) executeParallelBatch(ctx context.Context, calls []llm.ToolCall) ([]llm.ToolResultMessage, error) {
	executor := a.toolExecutor()
	for _, call := range calls {
		if err := a.emitToolStart(call); err != nil {
			return nil, err
		}
	}
	batchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// 并行工具的进度与 owner 的结束通知共享出口，保持既有消费者串行接收
	var eventMu sync.Mutex
	emit := func(event llm.AgentEvent) error { eventMu.Lock(); defer eventMu.Unlock(); return a.emit(event) }
	completed := make(chan toolCompletion, len(calls))
	var workers sync.WaitGroup
	for index, call := range calls {
		workers.Add(1)
		go func() {
			defer workers.Done()
			var result llm.ToolResult
			var eventErr error
			if batchCtx.Err() != nil {
				result = abortedToolResult()
			} else {
				result, eventErr = executeToolCall(batchCtx, executor, call, nil, emit)
			}
			completed <- toolCompletion{index: index, result: result, eventErr: eventErr}
		}()
	}
	results := make([]llm.ToolResultMessage, len(calls))
	var eventErr error
	for range calls {
		done := <-completed
		results[done.index] = toolResultMessage(calls[done.index], done.result)
		if eventErr == nil && done.eventErr != nil {
			eventErr = done.eventErr
			cancel()
		}
		if eventErr == nil {
			if err := emit(toolEndEvent(calls[done.index], done.result)); err != nil {
				eventErr = err
				cancel()
			}
		}
	}
	workers.Wait()
	if err := a.commitToolResults(results, eventErr == nil); err != nil {
		return results, err
	}
	return results, eventErr
}

func abortedToolResult() llm.ToolResult {
	return llm.ToolResult{Content: []llm.Content{llm.TextContent{Text: "Operation aborted"}}, IsError: true}
}

func (a *Agent) emitToolStart(call llm.ToolCall) error {
	return a.emit(llm.ToolExecutionStartEvent{ToolCallID: call.ID, ToolName: call.Name, Arguments: call.Arguments})
}

func toolEndEvent(call llm.ToolCall, result llm.ToolResult) llm.ToolExecutionEndEvent {
	return llm.ToolExecutionEndEvent{ToolCallID: call.ID, ToolName: call.Name, Result: result, IsError: result.IsError}
}

// toolResultMessage 保留已执行工具的完整结果，提交失败也不重新执行
func toolResultMessage(call llm.ToolCall, result llm.ToolResult) llm.ToolResultMessage {
	return llm.ToolResultMessage{ToolCallID: call.ID, ToolName: call.Name, Content: result.Content, Details: result.Details, IsError: result.IsError, Usage: result.Usage}
}
