package codemode

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/tool/args"
)

func (*Tool) Name() string { return "codemode" }
func (*Tool) Description() string {
	return `Run JavaScript to compose the current agent's tools. Input code is an async function body: top-level await and return work. Call tools[name](args), which returns {text, images, details}; failed calls reject with Error.message and Error.result in the same shape. Only explicit text(value), console.log/info/warn/error/debug(...values), image(value), and non-undefined return values reach the model. Use result.text for text and image(result.images[i]) for images: serializing a whole result prints its base64 too. image accepts {type:"image",data:"base64",mimeType:"image/png"} or a base64 data URL and requires an image-capable openai-responses model. ALL_TOOLS and describeTool(name) describe available tools. exit() ends successfully and cannot be caught. store(key,value)/load(key) save JSON values on this session branch; undefined deletes, missing loads return undefined. A fresh JS runtime is created each call. Optional first line: // @options: {"max_output_tokens":10000,"timeout_ms":60000}. Output budget is nonnegative (0 keeps status/errors/file paths); timeout is positive milliseconds. Script completion cancels unawaited work; tool side effects are not rolled back. No Node.js modules, network or timer host APIs are installed.`
}
func (*Tool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"code": map[string]any{"type": "string", "description": "JavaScript async function body, without Markdown fences"}}, "required": []string{"code"}, "additionalProperties": false}
}
func (*Tool) PromptSnippet() string {
	return "Run JavaScript to batch, compose, and filter tool calls; persist small JSON values with store/load"
}
func (*Tool) PromptGuidelines() []string {
	return []string{"Use codemode for batches, dependent tool calls or filtering large results; direct tool calls remain available.", "Within codemode, tools return {text, images, details}; print selected text and call image() explicitly instead of returning base64-bearing result objects."}
}
func (*Tool) ExecutionMode() llm.ToolExecutionMode { return llm.ToolExecutionSequential }

// Execute 为每次调用建立独立 VM，最终输出与状态写入一起返回
func (t *Tool) Execute(ctx context.Context, values map[string]any) (llm.ToolResult, error) {
	params, err := args.Decode[Params](values)
	if err != nil {
		return llm.ToolResult{}, fmt.Errorf("codemode: %w", err)
	}
	source, options, err := parseSource(params.Code)
	if err != nil {
		return llm.ToolResult{}, err
	}
	host, ok := llm.CurrentToolContext(ctx)
	if !ok || host.Config.SessionID == "" || host.Runtime.CallID == "" || host.Runtime.ExecuteNested == nil || host.Runtime.Update == nil || t.history == nil {
		return llm.ToolResult{}, fmt.Errorf("codemode: current session and tool runtime capabilities are required")
	}
	if options.TimeoutMS != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeoutDuration(*options.TimeoutMS))
		defer cancel()
	}
	exec := newExecution(ctx, host)
	defer exec.release()
	if err := exec.store.restore(t.history, host.Runtime.EarlierResults, host.Config.SessionID); err != nil {
		exec.failure = err
		return exec.finish(options), nil
	}
	if err := exec.install(); err != nil {
		exec.failure = err
		return exec.finish(options), nil
	}
	exec.run(source)
	return exec.finish(options), nil
}

// shortText 限制展示摘要，不拆开 UTF-8 字符
func shortText(text string, limit int) string {
	count, cut := 0, 0
	for offset := range text {
		if count == limit-1 {
			cut = offset
		}
		if count == limit {
			return text[:cut] + "…"
		}
		count++
	}
	return text
}

// logicalLines 统计统一换行并去掉尾部空白后的正文行数
func logicalLines(text string) int {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	text = strings.TrimRightFunc(text, unicode.IsSpace)
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}
