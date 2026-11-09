package codemode

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/ywxzm03/Rivo/internal/llm"
)

type promiseHandlers struct{ resolve, reject func(any) error }
type request struct {
	call llm.ToolCall
	mode llm.ToolExecutionMode
}
type completion struct {
	id       string
	result   llm.ToolResult
	elapsed  time.Duration
	canceled bool
}

// callState 由 owner 管理请求顺序和 sequential 屏障
type callState struct {
	nextID    uint64
	queue     []request
	handlers  map[string]promiseHandlers
	records   []Call
	indices   map[string]int
	completed chan completion
	active    int
	exclusive bool
	stopping  bool
	workers   sync.WaitGroup
	usage     *llm.Usage
}

// vmState 的全部值只由 Execute 所在 goroutine 访问
type vmState struct {
	runtime                                                  *goja.Runtime
	root                                                     *goja.Promise
	parse, stringify, format, failure, makeError, rangeError goja.Callable
}

// execution 保存一次脚本的执行状态，不跨调用复用
type execution struct {
	vm            vmState
	host          llm.ToolContext
	ctx           context.Context
	toolCtx       context.Context
	cancelTools   context.CancelFunc
	stopInterrupt func() bool
	interruptDone chan struct{}
	started       time.Time
	lastProgress  time.Time // 运行中合并快照，终态始终返回完整记录
	tools         map[string]llm.Tool
	calls         callState
	output        outputState
	store         storeState
	failure       error
	exitRequested bool
}

var exitSignal = &struct{}{}
var sourcePositions = regexp.MustCompile(`codemode\.js:(?: Line )?([0-9]+):`)

// newExecution 安装唯一允许跨 goroutine 的 VM 中断入口
func newExecution(ctx context.Context, host llm.ToolContext) *execution {
	toolCtx, cancel := context.WithCancel(ctx)
	e := &execution{host: host, ctx: ctx, toolCtx: toolCtx, cancelTools: cancel, started: time.Now(), interruptDone: make(chan struct{}), tools: make(map[string]llm.Tool)}
	e.vm.runtime = goja.New()
	e.calls = callState{handlers: make(map[string]promiseHandlers), records: []Call{}, indices: make(map[string]int), completed: make(chan completion)}
	e.stopInterrupt = context.AfterFunc(ctx, func() { defer close(e.interruptDone); e.vm.runtime.Interrupt(ctx.Err()) })
	return e
}

// release 等中断回调退出，返回之后不再有 goroutine 访问 VM
func (e *execution) release() {
	e.cancelTools()
	if !e.stopInterrupt() {
		<-e.interruptDone
	}
}

// run 只在 VM 交回控制权后接收完成事件并推进 Promise
func (e *execution) run(source string) {
	program, err := goja.Compile("codemode.js", "(async function(){\n"+source+"\n})()", false)
	if err != nil {
		e.failure = errors.New(rebaseSourceError(err.Error()))
		return
	}
	value, err := e.vm.runtime.RunProgram(program)
	if err != nil {
		e.vmError(err)
		return
	}
	root, ok := value.Export().(*goja.Promise)
	if !ok {
		e.failure = fmt.Errorf("codemode: async body did not produce a Promise")
		return
	}
	e.vm.root = root
	for e.failure == nil && !e.exitRequested {
		if err := e.ctx.Err(); err != nil {
			e.failure = contextFailure(err)
			break
		}
		if root.State() != goja.PromiseStatePending {
			break
		}
		if e.calls.active == 0 && len(e.calls.queue) == 0 {
			e.failure = fmt.Errorf("codemode: stalled Promise has no pending tool work")
			break
		}
		select {
		case done := <-e.calls.completed:
			e.accept(done)
			e.dispatch()
		case <-e.ctx.Done():
			e.failure = contextFailure(e.ctx.Err())
		}
	}
	if e.failure != nil {
		return
	}
	if e.exitRequested {
		return
	}
	if root.State() == goja.PromiseStateRejected {
		formatted, err := e.vm.failure(goja.Undefined(), root.Result())
		if err != nil {
			e.vmError(err)
			return
		}
		e.failure = errors.New(rebaseSourceError(formatted.String()))
		return
	}
	if !goja.IsUndefined(root.Result()) {
		if err := e.appendValue(root.Result()); err != nil {
			e.vmError(err)
		}
	}
}

// vmError 区分正常提前结束、取消和不可捕获的运行故障
func (e *execution) vmError(err error) {
	if e.ctx.Err() != nil {
		e.failure = contextFailure(e.ctx.Err())
		return
	}
	if e.failure != nil {
		return
	}
	var interrupted *goja.InterruptedError
	if errors.As(err, &interrupted) && interrupted.Value() == exitSignal && e.exitRequested {
		return
	}
	e.failure = errors.New(rebaseSourceError(err.Error()))
}

func contextFailure(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("codemode: timed out: %w", err)
	}
	return fmt.Errorf("codemode: aborted: %w", err)
}

// rebaseSourceError 扣除 async 包装增加的一行，保留原始源码列号
func rebaseSourceError(message string) string {
	return sourcePositions.ReplaceAllStringFunc(message, func(position string) string {
		match := sourcePositions.FindStringSubmatchIndex(position)
		line, err := strconv.Atoi(position[match[2]:match[3]])
		if err != nil {
			return position
		}
		return position[:match[2]] + strconv.Itoa(max(1, line-1)) + position[match[3]:]
	})
}

// notify 只复制发布边界上的调用摘要，不复制脚本正文和 store
func (e *execution) notify() {
	if e.failure != nil || e.calls.stopping {
		return
	}
	// 最多每 50ms 复制一次增长中的 Calls，避免每个完成项都复制完整前缀
	now := time.Now()
	if !e.lastProgress.IsZero() && now.Sub(e.lastProgress) < 50*time.Millisecond {
		return
	}
	e.lastProgress = now
	calls := append([]Call{}, e.calls.records...)
	if err := e.host.Runtime.Update(llm.ToolResult{Details: Details{Codemode: true, Calls: calls}}); err != nil {
		e.abort(err)
	}
}

// abort 用不可捕获中断结束脚本，不能由 JS catch 转成成功
func (e *execution) abort(err error) {
	if e.failure == nil {
		e.failure = err
	}
	e.cancelTools()
	e.vm.runtime.Interrupt(err)
}

// stop 先取消请求再持续收齐 completion，不能先等待 worker 退出
func (e *execution) stop() {
	e.calls.stopping = true
	e.cancelTools()
	for _, queued := range e.calls.queue {
		e.calls.records[e.calls.indices[queued.call.ID]].Status = CallCancelled
	}
	e.calls.queue = nil
	for e.calls.active > 0 {
		e.accept(<-e.calls.completed)
	}
	e.calls.workers.Wait()
	clear(e.calls.handlers)
}
