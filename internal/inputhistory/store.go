package inputhistory

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

var (
	ErrViewChanged = errors.New("input history view changed")
	ErrClosed      = errors.New("input history store closed")
)

type Record struct {
	SessionID string `json:"session_id"` // 原始输入所属主会话或任务
	Timestamp int64  `json:"ts"`         // 非负 Unix 秒，不作为排序依据
	Text      string `json:"text"`       // 非空原文，保留空白与换行
}

type recordWire struct {
	SessionID string  `json:"session_id"`
	Timestamp *int64  `json:"ts"`   // 区分缺失/null 与合法的 0
	Text      *string `json:"text"` // 区分缺失/null 与空文本
}

type recordPosition struct {
	Offset int64    // 完整行在文件中的真实字节位置
	Length int64    // 包含结尾换行的字节长度
	Digest [32]byte // 校验同 inode 的等长改写
}

// View 只保留固定文件前缀的位置索引，发布后不可变
type View struct {
	fileInfo  os.FileInfo
	endOffset int64 // 最后一个完整换行的边界，不包括残尾
	positions []recordPosition
}

type ScanIssues struct {
	MalformedRows         int
	EmptyRows             int
	UnterminatedTailBytes int64
}

type appendRequest struct {
	ticket     uint64
	record     Record
	onComplete func(error)
}

type appendQueue struct {
	mu              sync.Mutex
	changed         *sync.Cond
	pending         []appendRequest
	lastTicket      uint64
	completedTicket uint64
	workerActive    bool
	closed          bool
}

type fileAccess struct {
	mu       sync.Mutex // 串行化同一 Store 的文件操作
	poisoned error      // 部分写入且回滚失败后禁止继续写入
	ops      fileOps    // nil 操作使用真实文件方法，替身仅用于故障测试
}

type Store struct {
	path  string
	queue appendQueue
	file  fileAccess
}

// New 只初始化队列，不扫描文件或启动空转 worker
func New(path string) *Store {
	store := &Store{path: path}
	store.queue.changed = sync.NewCond(&store.queue.mu)
	return store
}

func (view *View) Count() int {
	return len(view.positions)
}

// Append 同步校验并入 FIFO，文件操作和完成回调由单个 worker 执行
func (store *Store) Append(record Record, onComplete func(error)) error {
	queue := &store.queue
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if queue.closed {
		return ErrClosed
	}
	if err := validateRecord(record); err != nil {
		return err
	}
	if onComplete == nil {
		return errors.New("input history completion callback is required")
	}
	queue.lastTicket++
	queue.pending = append(queue.pending, appendRequest{
		ticket: queue.lastTicket, record: record, onComplete: onComplete,
	})
	if !queue.workerActive {
		queue.workerActive = true
		go store.drain()
	}
	return nil
}

// Barrier 只等待调用时已接纳的水位，包含完成回调的执行
func (store *Store) Barrier() {
	queue := &store.queue
	queue.mu.Lock()
	defer queue.mu.Unlock()
	target := queue.lastTicket
	for queue.completedTicket < target {
		queue.changed.Wait()
	}
}

// Close 拒绝新项并等待已接纳项和 worker 排空，可重复调用
func (store *Store) Close() {
	queue := &store.queue
	queue.mu.Lock()
	defer queue.mu.Unlock()
	queue.closed = true
	for queue.workerActive {
		queue.changed.Wait()
	}
}

func (store *Store) drain() {
	queue := &store.queue
	for {
		queue.mu.Lock()
		if len(queue.pending) == 0 {
			queue.pending = nil
			queue.workerActive = false
			queue.changed.Broadcast()
			queue.mu.Unlock()
			return
		}
		request := queue.pending[0]
		queue.pending[0] = appendRequest{}
		queue.pending = queue.pending[1:]
		queue.mu.Unlock()

		err := store.appendRecord(request.record)
		request.onComplete(err)
		ticket := request.ticket
		request = appendRequest{}
		queue.mu.Lock()
		queue.completedTicket = ticket
		queue.changed.Broadcast()
		queue.mu.Unlock()
	}
}

func validateRecord(record Record) error {
	if record.SessionID == "" {
		return errors.New("input history session_id is required")
	}
	if record.Timestamp < 0 {
		return fmt.Errorf("input history ts must be nonnegative: %d", record.Timestamp)
	}
	if record.Text == "" {
		return errors.New("input history text must be nonempty")
	}
	return nil
}
