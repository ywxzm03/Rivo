package codemode

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dop251/goja"
	"github.com/ywxzm03/Rivo/internal/imageutil"
	"github.com/ywxzm03/Rivo/internal/llm"
)

const (
	maxOutputBytes      = 16 * 1024 * 1024
	maxOutputItems      = 100000
	defaultOutputTokens = 10000
)

// outputState 保存已脱离 VM 的输出，console 单独延后到正文末尾
type outputState struct {
	items     []llm.Content
	console   strings.Builder
	bytes     int
	itemCount int
}

// reserve 在追加前检查累计限额，超限不会再写入任何输出
func (e *execution) reserve(bytes int) error {
	if e.output.itemCount >= maxOutputItems || bytes > maxOutputBytes-e.output.bytes {
		err := fmt.Errorf("codemode: output exceeds %d bytes or %d items", maxOutputBytes, maxOutputItems)
		e.abort(err)
		return err
	}
	e.output.itemCount++
	e.output.bytes += bytes
	return nil
}

func (e *execution) appendValue(value goja.Value) error {
	formatted, err := e.vm.format(goja.Undefined(), value)
	if err != nil {
		return err
	}
	text := formatted.String()
	if err := e.reserve(len(text)); err != nil {
		return err
	}
	e.output.items = append(e.output.items, llm.TextContent{Text: text})
	return nil
}

func (e *execution) appendConsole(values []goja.Value) error {
	var line strings.Builder
	for index, value := range values {
		formatted, err := e.vm.format(goja.Undefined(), value)
		if err != nil {
			return err
		}
		if index > 0 {
			line.WriteByte(' ')
		}
		line.WriteString(formatted.String())
	}
	if err := e.reserve(line.Len() + 1); err != nil {
		return err
	}
	e.output.console.WriteString(line.String())
	e.output.console.WriteByte('\n')
	return nil
}

// appendImage 在协议能力与 MIME 校验后统一调用图片前处理
func (e *execution) appendImage(value goja.Value) error {
	model := e.host.Config.Model
	if model.API != llm.APIOpenAIResponses || !model.SupportsImages {
		return fmt.Errorf("codemode: image output requires an image-capable openai-responses model")
	}
	var block ScriptImage
	if value.ExportType() == stringType {
		dataURL := value.String()
		prefix, data, ok := strings.Cut(dataURL, ";base64,")
		if !ok || !strings.HasPrefix(prefix, "data:") {
			return fmt.Errorf("codemode: image expects a base64 data URL or image object")
		}
		block = ScriptImage{Type: "image", MIMEType: strings.TrimPrefix(prefix, "data:"), Data: data}
	} else {
		raw, err := e.stringify(value)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &block); err != nil {
			return fmt.Errorf("codemode: decode image: %w", err)
		}
	}
	if block.Type != "image" {
		return fmt.Errorf("codemode: image object type must be image")
	}
	data, err := base64.StdEncoding.DecodeString(block.Data)
	if err != nil {
		return fmt.Errorf("codemode: decode image base64: %w", err)
	}
	actual := imageutil.Sniff(data)
	if actual == "" || actual != block.MIMEType {
		return fmt.Errorf("codemode: image MIME does not match supported image data")
	}
	processed, err := imageutil.Process(data, actual)
	if err != nil {
		return fmt.Errorf("codemode: process image: %w", err)
	}
	if err := e.reserve(len(processed.Data)); err != nil {
		return err
	}
	e.output.items = append(e.output.items, llm.TextContent{Text: "[image: " + processed.MIMEType + "]"}, llm.ImageContent{Data: processed.Data, MIMEType: processed.MIMEType})
	return nil
}

// finish 在 VM 收尾后形成独立状态头、预算正文和可选错误尾块
func (e *execution) finish(options SourceOptions) llm.ToolResult {
	e.stop()
	if e.failure == nil && e.ctx.Err() != nil {
		e.failure = contextFailure(e.ctx.Err())
	}
	items := e.output.items
	if e.output.console.Len() > 0 {
		console := "<console_output>\n" + strings.TrimSuffix(e.output.console.String(), "\n") + "\n</console_output>"
		items = append(items, llm.TextContent{Text: console})
	}
	text := llm.TextOf(items)
	details := Details{Codemode: true, Calls: e.calls.records, OutputLines: logicalLines(text)}
	budget := defaultOutputTokens
	if options.MaxOutputTokens != nil {
		budget = *options.MaxOutputTokens
	}
	if (utf8.RuneCountInString(text)+3)/4 > budget {
		path, err := spillOutput(text)
		if err != nil {
			e.failure = errors.Join(e.failure, err)
		} else {
			details.FullOutputPath = path
		}
		items = trimOutput(items, text, budget*4)
	}
	if e.failure == nil && e.ctx.Err() != nil {
		e.failure = contextFailure(e.ctx.Err())
	}
	details.WallTimeMS = time.Since(e.started).Milliseconds()
	status := "Script completed"
	if e.failure != nil {
		status = "Script failed"
		details.Error = e.failure.Error()
	} else {
		details.StoreWrites = e.store.writes(e.host.Config.SessionID)
	}
	header := fmt.Sprintf("%s in %.3fs", status, float64(details.WallTimeMS)/1000)
	if details.FullOutputPath != "" {
		header += "\nFull output: " + details.FullOutputPath
	}
	content := make([]llm.Content, 0, len(items)+2)
	content = append(content, llm.TextContent{Text: header})
	content = append(content, items...)
	if details.Error != "" {
		content = append(content, llm.TextContent{Text: details.Error})
	}
	return llm.ToolResult{Content: content, Details: details, IsError: e.failure != nil, Usage: e.calls.usage}
}

// spillOutput 仅在完整写入并关闭成功后发布路径
func spillOutput(text string) (string, error) {
	file, err := os.CreateTemp("", "rivo-codemode-*.txt")
	if err != nil {
		return "", fmt.Errorf("codemode: create full output: %w", err)
	}
	_, writeErr := file.WriteString(text)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		removeErr := os.Remove(file.Name())
		return "", fmt.Errorf("codemode: save full output: %w", errors.Join(err, removeErr))
	}
	return file.Name(), nil
}

// trimOutput 裁剪普通文本，图片与类型标签成对保留
func trimOutput(items []llm.Content, full string, characters int) []llm.Content {
	out := make([]llm.Content, 0, min(len(items), 8))
	if characters == 0 {
		for index, item := range items {
			if _, ok := item.(llm.ImageContent); ok {
				out = append(out, items[index-1], item)
			}
		}
		return out
	}
	head, tail := characters/2, characters-characters/2
	headEnd, tailStart := 0, len(full)
	for range head {
		_, size := utf8.DecodeRuneInString(full[headEnd:])
		headEnd += size
	}
	for range tail {
		_, size := utf8.DecodeLastRuneInString(full[:tailStart])
		tailStart -= size
	}
	offset, firstText, marked := 0, true, false
	for index, item := range items {
		text, ok := item.(llm.TextContent)
		if !ok {
			out = append(out, item)
			continue
		}
		if !firstText {
			offset++
		}
		firstText = false
		start, end := offset, offset+len(text.Text)
		offset = end
		if index+1 < len(items) {
			if _, paired := items[index+1].(llm.ImageContent); paired {
				out = append(out, text)
				continue
			}
		}
		prefixEnd := max(0, min(len(text.Text), headEnd-start))
		suffixStart := max(0, min(len(text.Text), tailStart-start))
		if prefixEnd > 0 {
			out = append(out, llm.TextContent{Text: strings.Clone(text.Text[:prefixEnd])})
		}
		if end >= headEnd && start < tailStart && !marked {
			out = append(out, llm.TextContent{Text: "… output truncated …"})
			marked = true
		}
		if suffixStart < len(text.Text) {
			out = append(out, llm.TextContent{Text: strings.Clone(text.Text[suffixStart:])})
		}
	}
	return out
}
