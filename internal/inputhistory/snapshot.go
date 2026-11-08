package inputhistory

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const historyReadBuffer = 32 * 1024

// Snapshot 在共享锁内捕获文件边界，只索引该前缀中的完整合法行
func (store *Store) Snapshot(ctx context.Context) (view *View, issues ScanIssues, err error) {
	if err := ctx.Err(); err != nil {
		return nil, issues, err
	}
	store.file.mu.Lock()
	defer store.file.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, issues, err
	}
	file, err := os.Open(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return &View{}, issues, nil
	}
	if err != nil {
		return nil, issues, err
	}
	locked := false
	defer func() { err = errors.Join(err, store.finishFile(file, locked)) }()
	if err := lockFile(ctx, file, false); err != nil {
		return nil, issues, err
	}
	locked = true
	info, err := file.Stat()
	if err != nil {
		return nil, issues, err
	}
	if !info.Mode().IsRegular() {
		return nil, issues, fmt.Errorf("input history is not a regular file: %s", store.path)
	}
	view, issues, err = scanHistory(ctx, file, info)
	return view, issues, err
}

func scanHistory(ctx context.Context, file *os.File, info os.FileInfo) (*View, ScanIssues, error) {
	view := &View{fileInfo: info}
	var issues ScanIssues
	reader := bufio.NewReaderSize(io.LimitReader(file, info.Size()), historyReadBuffer)
	var offset int64
	for {
		line, err := readHistoryLine(ctx, reader)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return nil, issues, err
			}
			issues.UnterminatedTailBytes = int64(len(line))
			return view, issues, nil
		}
		position := recordPosition{Offset: offset, Length: int64(len(line))}
		offset += position.Length
		view.endOffset = offset
		if len(bytes.TrimSpace(line)) == 0 {
			issues.EmptyRows++
			continue
		}
		record, err := decodeRecord(line)
		if err != nil {
			issues.MalformedRows++
			continue
		}
		if record.Text == "" {
			issues.EmptyRows++
			continue
		}
		position.Digest = sha256.Sum256(line)
		view.positions = append(view.positions, position)
	}
}

// readHistoryLine 对长行分段检查取消，普通行直接复用 Reader 缓冲区
func readHistoryLine(ctx context.Context, reader *bufio.Reader) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	line, err := reader.ReadSlice('\n')
	if !errors.Is(err, bufio.ErrBufferFull) {
		return line, err
	}
	data := append([]byte(nil), line...)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fragment, err := reader.ReadSlice('\n')
		data = append(data, fragment...)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return data, err
		}
	}
}

// Read 每次重新校验文件身份、固定边界和目标摘要，再按 byte range 解码
func (store *Store) Read(ctx context.Context, view *View, offset int) (record Record, err error) {
	if view == nil || offset < 0 || offset >= view.Count() {
		return Record{}, fmt.Errorf("invalid input history offset: %d", offset)
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	store.file.mu.Lock()
	defer store.file.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	file, err := os.Open(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, ErrViewChanged
	}
	if err != nil {
		return Record{}, err
	}
	locked := false
	defer func() { err = errors.Join(err, store.finishFile(file, locked)) }()
	if err := lockFile(ctx, file, false); err != nil {
		return Record{}, err
	}
	locked = true
	info, err := file.Stat()
	if err != nil {
		return Record{}, err
	}
	if view.fileInfo == nil || !os.SameFile(view.fileInfo, info) || info.Size() < view.endOffset {
		return Record{}, ErrViewChanged
	}
	position := view.positions[offset]
	line := make([]byte, position.Length)
	if _, err := file.ReadAt(line, position.Offset); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return Record{}, ErrViewChanged
		}
		return Record{}, err
	}
	if sha256.Sum256(line) != position.Digest {
		return Record{}, ErrViewChanged
	}
	record, err = decodeRecord(line)
	if err != nil {
		return Record{}, err
	}
	if record.Text == "" {
		return Record{}, ErrViewChanged
	}
	return record, nil
}

func decodeRecord(line []byte) (Record, error) {
	var wire recordWire
	if err := json.Unmarshal(line, &wire); err != nil {
		return Record{}, err
	}
	if wire.SessionID == "" || wire.Timestamp == nil || *wire.Timestamp < 0 || wire.Text == nil {
		return Record{}, errors.New("invalid input history record fields")
	}
	return Record{SessionID: wire.SessionID, Timestamp: *wire.Timestamp, Text: *wire.Text}, nil
}
