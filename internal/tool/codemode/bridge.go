package codemode

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/dop251/goja"
	"github.com/ywxzm03/Rivo/internal/llm"
)

var stringType = reflect.TypeFor[string]()

var bridgeProgram = goja.MustCompile("codemode-host.js", `(()=>{
 const parse=JSON.parse,stringify=JSON.stringify,ErrorType=Error,RangeType=RangeError,Type=TypeError;
 return {parse,stringify,
 format(value){if(typeof value==="string")return value;if(value===undefined)return "undefined";const result=stringify(value);if(result===undefined)throw new Type("Value is not JSON serializable");return result},
 failure(value){return value instanceof ErrorType && typeof value.stack==="string" ? value.stack : String(value)},
 makeError(message,result){const error=new ErrorType(message);error.result=result;return error},
 rangeError(message){return new RangeType(message)}};
})()`, false)

// install 只绑定显式脚本 API，不加载宿主模块或暴露 Go 包装对象
func (e *execution) install() error {
	value, err := e.vm.runtime.RunProgram(bridgeProgram)
	if err != nil {
		return err
	}
	helpers, ok := value.(*goja.Object)
	if !ok {
		return fmt.Errorf("codemode: invalid bridge helpers")
	}
	for _, binding := range []struct {
		name   string
		target *goja.Callable
	}{
		{"parse", &e.vm.parse}, {"stringify", &e.vm.stringify}, {"format", &e.vm.format}, {"failure", &e.vm.failure}, {"makeError", &e.vm.makeError}, {"rangeError", &e.vm.rangeError},
	} {
		fn, ok := goja.AssertFunction(helpers.Get(binding.name))
		if !ok {
			return fmt.Errorf("codemode: bridge helper %s is not callable", binding.name)
		}
		*binding.target = fn
	}
	tools := e.vm.runtime.NewObject()
	if err := tools.SetPrototype(nil); err != nil {
		return err
	}
	infos := make([]ToolInfo, 0, len(e.host.Context.Tools))
	for _, tool := range e.host.Context.Tools {
		name := tool.Name()
		if name == "codemode" {
			continue
		}
		e.tools[name] = tool
		parameters, err := json.Marshal(tool.Parameters())
		if err != nil {
			return fmt.Errorf("codemode: describe %s: %w", name, err)
		}
		guidelines := tool.PromptGuidelines()
		if guidelines == nil {
			guidelines = []string{}
		}
		infos = append(infos, ToolInfo{Name: name, Description: tool.Description(), Parameters: parameters, Guidelines: guidelines})
		if err := tools.Set(name, func(call goja.FunctionCall) goja.Value { return e.request(name, call.Argument(0)) }); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(infos)
	if err != nil {
		return err
	}
	all, err := e.parseJSON(raw)
	if err != nil {
		return err
	}
	return e.installGlobals(tools, all, infos)
}

// installGlobals 把脚本 API 与工具元数据绑定到当前 VM
func (e *execution) installGlobals(tools *goja.Object, all goja.Value, infos []ToolInfo) error {
	bindings := []struct {
		name  string
		value any
	}{
		{"tools", tools}, {"ALL_TOOLS", all},
		{"describeTool", func(call goja.FunctionCall) goja.Value {
			name := e.stringKey(call.Argument(0))
			for _, info := range infos {
				if info.Name == name {
					raw, err := json.Marshal(info)
					if err != nil {
						return e.raise(err)
					}
					value, err := e.parseJSON(raw)
					if err != nil {
						return e.raise(err)
					}
					return value
				}
			}
			panic(e.vm.runtime.NewTypeError("Unknown or unavailable tool %q", name))
		}},
		{"text", func(call goja.FunctionCall) goja.Value {
			if err := e.appendValue(call.Argument(0)); err != nil {
				return e.raise(err)
			}
			return goja.Undefined()
		}},
		{"image", func(call goja.FunctionCall) goja.Value {
			if err := e.appendImage(call.Argument(0)); err != nil {
				return e.raise(err)
			}
			return goja.Undefined()
		}},
		{"exit", func(goja.FunctionCall) goja.Value {
			e.exitRequested = true
			e.vm.runtime.Interrupt(exitSignal)
			return goja.Undefined()
		}},
		{"store", func(call goja.FunctionCall) goja.Value {
			key := e.stringKey(call.Argument(0))
			var raw json.RawMessage
			if !goja.IsUndefined(call.Argument(1)) {
				encoded, err := e.stringify(call.Argument(1))
				if err != nil {
					return e.raise(err)
				}
				raw = encoded
			}
			if err := e.store.put(key, raw); err != nil {
				value, makeErr := e.vm.rangeError(goja.Undefined(), e.vm.runtime.ToValue(err.Error()))
				if makeErr != nil {
					return e.raise(makeErr)
				}
				panic(value)
			}
			return goja.Undefined()
		}},
		{"load", func(call goja.FunctionCall) goja.Value {
			raw, ok := e.store.values[e.stringKey(call.Argument(0))]
			if !ok {
				return goja.Undefined()
			}
			value, err := e.parseJSON(raw)
			if err != nil {
				return e.raise(err)
			}
			return value
		}},
	}
	for _, binding := range bindings {
		if err := e.vm.runtime.Set(binding.name, binding.value); err != nil {
			return err
		}
	}
	console := e.vm.runtime.NewObject()
	for _, name := range []string{"log", "info", "warn", "error", "debug"} {
		if err := console.Set(name, func(call goja.FunctionCall) goja.Value {
			if err := e.appendConsole(call.Arguments); err != nil {
				return e.raise(err)
			}
			return goja.Undefined()
		}); err != nil {
			return err
		}
	}
	return e.vm.runtime.Set("console", console)
}

func (e *execution) parseJSON(raw []byte) (goja.Value, error) {
	return e.vm.parse(goja.Undefined(), e.vm.runtime.ToValue(string(raw)))
}

func (e *execution) stringify(value goja.Value) ([]byte, error) {
	encoded, err := e.vm.stringify(goja.Undefined(), value)
	if err != nil {
		return nil, err
	}
	if goja.IsUndefined(encoded) {
		return nil, fmt.Errorf("codemode: value is not JSON serializable")
	}
	return []byte(encoded.String()), nil
}

// raise 在 host 回调内抛 JS 异常，不用 Go panic 模拟退出
func (e *execution) raise(err error) goja.Value {
	if cause := e.ctx.Err(); cause != nil {
		e.abort(contextFailure(cause))
		return goja.Undefined()
	}
	var interrupted *goja.InterruptedError
	if errors.As(err, &interrupted) {
		if interrupted.Value() == exitSignal && e.exitRequested {
			// JSON 回调已消费内层中断，重新中断外层 VM，保持成功退出
			e.vm.runtime.Interrupt(exitSignal)
		} else {
			e.abort(err)
		}
		return goja.Undefined()
	}
	var overflow *goja.StackOverflowError
	if errors.As(err, &overflow) {
		e.abort(err)
		return goja.Undefined()
	}
	var exception *goja.Exception
	if errors.As(err, &exception) {
		panic(exception.Value())
	}
	if e.failure != nil {
		return goja.Undefined()
	}
	panic(e.vm.runtime.NewTypeError("%s", err.Error()))
}

func (e *execution) stringKey(value goja.Value) string {
	if value.ExportType() != reflect.TypeFor[string]() {
		panic(e.vm.runtime.NewTypeError("Expected a string key or tool name"))
	}
	return value.String()
}

// request 先脱离 VM 参数，再按发行顺序建立请求和 Promise
func (e *execution) request(name string, value goja.Value) goja.Value {
	arguments := map[string]any{}
	var raw []byte
	if !goja.IsUndefined(value) {
		object, ok := value.(*goja.Object)
		if !ok || object.ClassName() == "Array" || object.ClassName() == "Function" {
			panic(e.vm.runtime.NewTypeError("Tool arguments must be an object"))
		}
		encoded, err := e.stringify(value)
		if err != nil {
			return e.raise(err)
		}
		if err := json.Unmarshal(encoded, &arguments); err != nil || arguments == nil {
			panic(e.vm.runtime.NewTypeError("Tool arguments must encode a JSON object"))
		}
		raw = encoded
	} else {
		raw = []byte("{}")
	}
	e.calls.nextID++
	id := fmt.Sprintf("%s/%d", e.host.Runtime.CallID, e.calls.nextID)
	promise, resolve, reject := e.vm.runtime.NewPromise()
	e.calls.handlers[id] = promiseHandlers{resolve: resolve, reject: reject}
	e.calls.indices[id] = len(e.calls.records)
	e.calls.records = append(e.calls.records, Call{ID: id, Name: name, Args: shortText(string(raw), 200), Status: CallQueued})
	e.calls.queue = append(e.calls.queue, request{call: llm.ToolCall{ID: id, Name: name, Arguments: arguments}, mode: e.tools[name].ExecutionMode()})
	e.dispatch()
	e.notify()
	return e.vm.runtime.ToValue(promise)
}

// dispatch 只从队首派发，已经入队的 sequential 请求阻挡所有后续请求
func (e *execution) dispatch() {
	for !e.calls.stopping && e.ctx.Err() == nil && e.failure == nil && !e.exitRequested && !e.calls.exclusive && len(e.calls.queue) > 0 {
		next := e.calls.queue[0]
		if next.mode == llm.ToolExecutionSequential && e.calls.active > 0 {
			return
		}
		e.calls.queue[0] = request{}
		e.calls.queue = e.calls.queue[1:]
		e.calls.active++
		e.calls.exclusive = next.mode == llm.ToolExecutionSequential
		e.calls.records[e.calls.indices[next.call.ID]].Status = CallRunning
		execute := e.host.Runtime.ExecuteNested
		e.calls.workers.Add(1)
		go func() {
			defer e.calls.workers.Done()
			started := time.Now()
			result := execute(e.toolCtx, next.call)
			e.calls.completed <- completion{id: next.call.ID, result: result, elapsed: time.Since(started), canceled: result.IsError && e.toolCtx.Err() != nil}
		}()
	}
}

// accept 收到一次结果即减少 active，收尾时不再进入 JavaScript
func (e *execution) accept(done completion) {
	e.calls.active--
	e.calls.exclusive = false
	record := &e.calls.records[e.calls.indices[done.id]]
	record.Status = CallOK
	elapsed := done.elapsed.Milliseconds()
	record.DurationMS = &elapsed
	if done.result.IsError {
		record.Status = CallError
		if done.canceled {
			record.Status = CallCancelled
		}
		record.Error = shortText(llm.TextOf(done.result.Content), 500)
	}
	e.addUsage(done.result.Usage)
	if e.calls.stopping || e.failure != nil || e.ctx.Err() != nil {
		return
	}
	envelope, conversionErr := scriptResult(done.result)
	message := envelope.Text
	if conversionErr != nil {
		record.Status = CallError
		record.Error = shortText(conversionErr.Error(), 500)
		message = conversionErr.Error()
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		e.abort(err)
		return
	}
	value, err := e.parseJSON(raw)
	if err != nil {
		e.vmError(err)
		return
	}
	handlers := e.calls.handlers[done.id]
	delete(e.calls.handlers, done.id)
	if done.result.IsError || conversionErr != nil {
		if message == "" {
			message = fmt.Sprintf("Tool %q failed", record.Name)
		}
		value, err = e.vm.makeError(goja.Undefined(), e.vm.runtime.ToValue(message), value)
		if err == nil {
			err = handlers.reject(value)
		}
	} else {
		err = handlers.resolve(value)
	}
	if err != nil {
		e.vmError(err)
	}
	e.notify()
}

// scriptResult 只将普通 JSON 数据送回 VM，避免 Go 宿主包装对象泄漏
func scriptResult(result llm.ToolResult) (ScriptToolResult, error) {
	envelope := ScriptToolResult{Text: llm.TextOf(result.Content), Images: []ScriptImage{}, Details: json.RawMessage("null")}
	for _, content := range result.Content {
		if image, ok := content.(llm.ImageContent); ok {
			envelope.Images = append(envelope.Images, ScriptImage{Type: "image", Data: image.Data, MIMEType: image.MIMEType})
		}
	}
	if result.Details != nil {
		raw, err := json.Marshal(result.Details)
		if err != nil {
			return envelope, fmt.Errorf("codemode: encode tool details: %w", err)
		}
		envelope.Details = raw
	}
	return envelope, nil
}
