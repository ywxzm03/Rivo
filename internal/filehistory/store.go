// Package filehistory 保存按 user message 组织的文件快照，并把快照恢复到工作区。
package filehistory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	TypeSnapshot = "file-history-snapshot"
	TypeDelta    = "file-history-delta"
	MaxSnapshots = 100
)

// RestoreMode 是 tree 操作选择的恢复范围。
type RestoreMode string

const (
	RestoreContext        RestoreMode = "context"
	RestoreCode           RestoreMode = "code"
	RestoreCodeAndContext RestoreMode = "code_and_context"
)

// Backup 描述一个路径在某个快照中的备份版本。BackupFileName 为空表示目标当时不存在。
type Backup struct {
	BackupFileName *string   `json:"backupFileName"`
	Version        int       `json:"version"`
	BackupTime     time.Time `json:"backupTime"`
	Mode           uint32    `json:"mode,omitempty"`
	RealParentDir  string    `json:"realParentDir,omitempty"`
}

// Snapshot 是某个 user turn 开始前的文件状态。
type Snapshot struct {
	MessageID          string            `json:"messageId"`
	TrackedFileBackups map[string]Backup `json:"trackedFileBackups"`
	Timestamp          time.Time         `json:"timestamp"`
	PreCheckpoint      bool              `json:"preCheckpoint,omitempty"`
}

// SnapshotRecord 是不参与会话树的 JSONL metadata record。
type SnapshotRecord struct {
	Type          string   `json:"type"`
	MessageID     string   `json:"messageId"`
	Snapshot      Snapshot `json:"snapshot"`
	PreCheckpoint bool     `json:"preCheckpoint,omitempty"`
}

// DeltaRecord 记录一次文件首次被当前 user turn 修改前的备份。
type DeltaRecord struct {
	Type              string    `json:"type"`
	MessageID         string    `json:"messageId"`
	SnapshotMessageID string    `json:"snapshotMessageId"`
	TrackingPath      string    `json:"trackingPath"`
	Backup            Backup    `json:"backup"`
	Timestamp         time.Time `json:"timestamp"`
}

// Record 是可追加到会话 JSONL 的文件历史记录。
type Record interface{ fileHistoryRecord() }

func (SnapshotRecord) fileHistoryRecord() {}
func (DeltaRecord) fileHistoryRecord()    {}

// DecodeRecord 解析 file-history metadata；未知类型由 session 层继续按普通未知记录处理。
func DecodeRecord(typ string, data []byte) (Record, error) {
	switch typ {
	case TypeSnapshot:
		var record SnapshotRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return nil, err
		}
		if record.Type == "" {
			record.Type = TypeSnapshot
		}
		if record.Snapshot.MessageID == "" {
			record.Snapshot.MessageID = record.MessageID
		}
		if record.Snapshot.MessageID == "" {
			return nil, errors.New("filehistory: snapshot record missing message id")
		}
		return record, nil
	case TypeDelta:
		var record DeltaRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return nil, err
		}
		if record.Type == "" {
			record.Type = TypeDelta
		}
		if record.SnapshotMessageID == "" {
			return nil, errors.New("filehistory: delta record missing snapshot message id")
		}
		if record.TrackingPath == "" {
			return nil, errors.New("filehistory: delta record missing tracking path")
		}
		return record, nil
	default:
		return nil, fmt.Errorf("filehistory: unsupported record type %q", typ)
	}
}

// RestoreResult 描述一次代码恢复的逐文件结果。
type RestoreResult struct {
	MessageID    string
	FilesChanged []string
	FilesSkipped []string
	FilesFailed  []string
}

// Store 管理一个 session 的备份文件和运行态快照。
type Store struct {
	mu           sync.Mutex
	ioMu         sync.Mutex
	root         string
	sessionID    string
	snapshots    []Snapshot
	trackedFiles map[string]struct{}
	appendRecord func(Record) error
}

// New 从 JSONL records 重建 file-history 状态。activeIDs 为空时保留所有快照。
func New(root, sessionID string, records []Record, activeIDs map[string]bool, appendRecord func(Record) error) *Store {
	s := &Store{
		root:         root,
		sessionID:    sessionID,
		trackedFiles: make(map[string]struct{}),
		appendRecord: appendRecord,
	}
	for _, record := range records {
		s.applyRecord(record)
	}
	if len(activeIDs) > 0 {
		s.filterActive(activeIDs)
	}
	return s
}

// TrackEdit 在文件写入前保存当前内容，并把备份增量附加到最近 snapshot。
func (s *Store) TrackEdit(ctx context.Context, messageID, path string) error {
	return s.trackEdit(ctx, messageID, path, nil, 0, false)
}

// TrackEditContent 使用调用方已经读取的内容生成备份，避免再次读取时出现竞态。
func (s *Store) TrackEditContent(ctx context.Context, messageID, path string, content []byte, mode os.FileMode) error {
	data := append([]byte(nil), content...)
	return s.trackEdit(ctx, messageID, path, data, mode, true)
}

func (s *Store) trackEdit(ctx context.Context, messageID, path string, content []byte, mode os.FileMode, hasContent bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := normalizePath(path)
	if err != nil {
		return err
	}
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	s.mu.Lock()
	if len(s.snapshots) == 0 {
		s.mu.Unlock()
		return errors.New("filehistory: no snapshot for current user turn")
	}
	latest := &s.snapshots[len(s.snapshots)-1]
	if latest.MessageID != messageID {
		latestID := latest.MessageID
		s.mu.Unlock()
		return fmt.Errorf("filehistory: snapshot %s does not match user turn %s", latestID, messageID)
	}
	if _, tracked := latest.TrackedFileBackups[path]; tracked {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	var backup Backup
	var created bool
	if hasContent {
		backup, created, err = s.createBackupFromData(path, 1, content, mode)
	} else {
		backup, created, err = s.createBackup(path, 1)
	}
	if err != nil {
		return err
	}
	record := DeltaRecord{
		Type:              TypeDelta,
		MessageID:         messageID,
		SnapshotMessageID: latest.MessageID,
		TrackingPath:      path,
		Backup:            backup,
		Timestamp:         time.Now().UTC(),
	}
	if s.appendRecord != nil {
		if err := s.appendRecord(record); err != nil {
			if created {
				s.removeBackup(backup)
			}
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.snapshots) == 0 || s.snapshots[len(s.snapshots)-1].MessageID != messageID {
		if created {
			s.removeBackup(backup)
		}
		return fmt.Errorf("filehistory: snapshot for user %s is no longer current", messageID)
	}
	latest = &s.snapshots[len(s.snapshots)-1]
	if _, tracked := latest.TrackedFileBackups[path]; tracked {
		if created {
			s.removeBackup(backup)
		}
		return nil
	}
	if latest.TrackedFileBackups == nil {
		latest.TrackedFileBackups = make(map[string]Backup)
	}
	latest.TrackedFileBackups[path] = backup
	s.trackedFiles[path] = struct{}{}
	return nil
}

// MakeSnapshot 在 user turn 开始前记录所有已追踪文件的状态。
func (s *Store) MakeSnapshot(messageID string) error {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	s.mu.Lock()
	if messageID == "" {
		s.mu.Unlock()
		return errors.New("filehistory: empty snapshot message id")
	}
	previousSnapshots := cloneSnapshots(s.snapshots)
	trackedFiles := make(map[string]struct{}, len(s.trackedFiles))
	for path := range s.trackedFiles {
		trackedFiles[path] = struct{}{}
	}
	s.mu.Unlock()
	filtered := make([]Snapshot, 0, len(previousSnapshots))
	for _, snapshot := range previousSnapshots {
		if snapshot.MessageID != messageID || !snapshot.PreCheckpoint {
			filtered = append(filtered, snapshot)
		}
	}
	previous := Snapshot{}
	if len(filtered) > 0 {
		previous = filtered[len(filtered)-1]
	}
	backups := make(map[string]Backup, len(trackedFiles))
	createdBackups := make([]Backup, 0)
	var errs []error
	paths := make([]string, 0, len(trackedFiles))
	for path := range trackedFiles {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		old, hasOld := previous.TrackedFileBackups[path]
		version := 1
		if hasOld && old.Version > 0 {
			version = old.Version + 1
		}
		info, statErr := os.Lstat(path)
		if errors.Is(statErr, os.ErrNotExist) {
			backups[path] = Backup{Version: version, BackupTime: time.Now().UTC()}
			continue
		}
		if statErr != nil {
			if hasOld {
				backups[path] = old
			}
			errs = append(errs, fmt.Errorf("stat %s: %w", path, statErr))
			continue
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			errs = append(errs, fmt.Errorf("tracked path is not a regular file: %s", path))
			continue
		}
		if hasOld && old.BackupFileName != nil {
			same, compareErr := s.backupMatchesFile(path, old)
			if compareErr == nil {
				if same {
					backups[path] = old
					continue
				}
			} else if !errors.Is(compareErr, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("validate backup %s: %w", path, compareErr))
			}
		}
		backup, created, backupErr := s.createBackup(path, version)
		if backupErr != nil {
			if hasOld {
				backups[path] = old
			}
			errs = append(errs, backupErr)
			continue
		}
		if created {
			createdBackups = append(createdBackups, backup)
		}
		backups[path] = backup
	}
	snapshot := Snapshot{
		MessageID:          messageID,
		TrackedFileBackups: backups,
		Timestamp:          time.Now().UTC(),
		PreCheckpoint:      true,
	}
	snapshots := append(filtered, snapshot)
	if len(snapshots) > MaxSnapshots {
		snapshots = append([]Snapshot(nil), snapshots[len(snapshots)-MaxSnapshots:]...)
	}
	if s.appendRecord != nil {
		if err := s.appendRecord(SnapshotRecord{Type: TypeSnapshot, MessageID: messageID, Snapshot: snapshot, PreCheckpoint: true}); err != nil {
			errs = append(errs, err)
			for _, backup := range createdBackups {
				s.removeBackup(backup)
			}
			return errors.Join(errs...)
		}
	}
	s.mu.Lock()
	s.snapshots = snapshots
	s.mu.Unlock()
	return errors.Join(errs...)
}

// Restore 把目标 user turn 开始前的文件状态恢复到工作区。
func (s *Store) Restore(messageID string) (RestoreResult, error) {
	result, _, err := s.restore(messageID)
	return result, err
}

// RestoreWithRollback 恢复代码并返回一次性补偿函数，供组合恢复在上下文失败时撤销代码改动。
func (s *Store) RestoreWithRollback(messageID string) (RestoreResult, func() error, error) {
	result, plans, err := s.restore(messageID)
	if err != nil {
		return result, nil, err
	}
	var once sync.Once
	var rollbackErr error
	return result, func() error {
		once.Do(func() {
			s.ioMu.Lock()
			defer s.ioMu.Unlock()
			rollbackErr = s.rollbackRestore(plans)
		})
		return rollbackErr
	}, nil
}

func (s *Store) restore(messageID string) (RestoreResult, []restorePlan, error) {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	s.mu.Lock()
	result := RestoreResult{MessageID: messageID}
	var target *Snapshot
	for index := len(s.snapshots) - 1; index >= 0; index-- {
		if s.snapshots[index].MessageID == messageID {
			copy := cloneSnapshot(s.snapshots[index])
			target = &copy
			break
		}
	}
	if target == nil {
		s.mu.Unlock()
		return result, nil, fmt.Errorf("filehistory: snapshot for user %s is unavailable", messageID)
	}
	paths := make([]string, 0, len(s.trackedFiles))
	for path := range s.trackedFiles {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	snapshots := cloneSnapshots(s.snapshots)
	s.mu.Unlock()
	plans := make([]restorePlan, 0, len(paths))
	var errs []error
	for _, path := range paths {
		backup, ok := target.TrackedFileBackups[path]
		if !ok {
			backup, ok = firstBackup(snapshots, path)
		}
		if !ok {
			result.FilesFailed = append(result.FilesFailed, path)
			errs = append(errs, fmt.Errorf("missing backup for %s", path))
			continue
		}
		plan, err := s.prepareRestore(path, backup)
		if err != nil {
			result.FilesFailed = append(result.FilesFailed, path)
			errs = append(errs, err)
			continue
		}
		plans = append(plans, plan)
	}
	if len(errs) > 0 {
		return result, nil, errors.Join(errs...)
	}
	applied := make([]restorePlan, 0, len(plans))
	for _, plan := range plans {
		changed, err := s.applyRestore(plan)
		if changed || err != nil {
			applied = append(applied, plan)
		}
		if err != nil {
			result.FilesFailed = append(result.FilesFailed, plan.path)
			rollbackErr := s.rollbackRestore(applied)
			if rollbackErr == nil {
				result.FilesChanged = nil
				result.FilesSkipped = nil
				return result, nil, err
			}
			return result, nil, errors.Join(err, rollbackErr)
		}
		if changed {
			result.FilesChanged = append(result.FilesChanged, plan.path)
		} else {
			result.FilesSkipped = append(result.FilesSkipped, plan.path)
		}
	}
	return result, applied, errors.Join(errs...)
}

// PruneActiveBranch 丢弃不在当前 user 活跃路径上的运行态快照。
func (s *Store) PruneActiveBranch(activeIDs map[string]bool) {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.filterActive(activeIDs)
}

// Snapshots 返回防御性快照副本，供 session 测试和诊断使用。
func (s *Store) Snapshots() []Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Snapshot, len(s.snapshots))
	for i := range s.snapshots {
		out[i] = cloneSnapshot(s.snapshots[i])
	}
	return out
}

func (s *Store) applyRecord(record Record) {
	switch record := record.(type) {
	case SnapshotRecord:
		snapshot := cloneSnapshot(record.Snapshot)
		if snapshot.MessageID == "" {
			snapshot.MessageID = record.MessageID
		}
		snapshot.PreCheckpoint = snapshot.PreCheckpoint || record.PreCheckpoint
		if snapshot.PreCheckpoint {
			kept := s.snapshots[:0]
			for _, old := range s.snapshots {
				if old.MessageID != snapshot.MessageID || !old.PreCheckpoint {
					kept = append(kept, old)
				}
			}
			s.snapshots = kept
		}
		s.snapshots = append(s.snapshots, snapshot)
		if len(s.snapshots) > MaxSnapshots {
			s.snapshots = append([]Snapshot(nil), s.snapshots[len(s.snapshots)-MaxSnapshots:]...)
		}
		for path := range snapshot.TrackedFileBackups {
			s.trackedFiles[path] = struct{}{}
		}
	case DeltaRecord:
		for index := len(s.snapshots) - 1; index >= 0; index-- {
			if s.snapshots[index].MessageID != record.SnapshotMessageID {
				continue
			}
			if s.snapshots[index].TrackedFileBackups == nil {
				s.snapshots[index].TrackedFileBackups = make(map[string]Backup)
			}
			s.snapshots[index].TrackedFileBackups[record.TrackingPath] = record.Backup
			s.trackedFiles[record.TrackingPath] = struct{}{}
			break
		}
	}
}

func (s *Store) filterActive(activeIDs map[string]bool) {
	if len(activeIDs) == 0 {
		s.snapshots = nil
		s.trackedFiles = make(map[string]struct{})
		return
	}
	kept := make([]Snapshot, 0, len(s.snapshots))
	for _, snapshot := range s.snapshots {
		if activeIDs[snapshot.MessageID] {
			kept = append(kept, snapshot)
		}
	}
	s.snapshots = kept
	s.trackedFiles = make(map[string]struct{})
	for _, snapshot := range s.snapshots {
		for path := range snapshot.TrackedFileBackups {
			s.trackedFiles[path] = struct{}{}
		}
	}
}

func firstBackup(snapshots []Snapshot, path string) (Backup, bool) {
	for _, snapshot := range snapshots {
		if backup, ok := snapshot.TrackedFileBackups[path]; ok {
			return backup, true
		}
	}
	return Backup{}, false
}

func (s *Store) createBackup(path string, version int) (Backup, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Backup{Version: version, BackupTime: time.Now().UTC(), RealParentDir: resolveRealParentDir(path)}, false, nil
	}
	if err != nil {
		return Backup{}, false, err
	}
	if err := validateBackupSource(info, path); err != nil {
		return Backup{}, false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Backup{}, false, err
	}
	return s.createBackupFromData(path, version, data, info.Mode().Perm())
}

func (s *Store) createBackupFromData(path string, version int, data []byte, mode os.FileMode) (Backup, bool, error) {
	if info, err := os.Lstat(path); err == nil {
		if err := validateBackupSource(info, path); err != nil {
			return Backup{}, false, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Backup{}, false, err
	}
	if mode == 0 {
		mode = 0o600
	}
	if err := s.ensureBackupDir(); err != nil {
		return Backup{}, false, err
	}
	name := backupName(path, version)
	destination := filepath.Join(s.backupDir(), name)
	realParentDir := resolveRealParentDir(path)
	temporary, err := os.CreateTemp(s.backupDir(), ".rivo-file-history-*")
	if err != nil {
		return Backup{}, false, err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode.Perm()); err != nil {
		_ = temporary.Close()
		return Backup{}, false, err
	}
	n, writeErr := temporary.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = io.ErrShortWrite
	}
	closeTemporaryErr := temporary.Close()
	if writeErr != nil {
		return Backup{}, false, writeErr
	}
	if closeTemporaryErr != nil {
		return Backup{}, false, closeTemporaryErr
	}
	if err := os.Rename(temporaryName, destination); err != nil {
		if same, compareErr := s.backupMatchesData(destination, data, mode); compareErr == nil && same {
			nameCopy := name
			return Backup{BackupFileName: &nameCopy, Version: version, BackupTime: time.Now().UTC(), Mode: uint32(mode.Perm()), RealParentDir: realParentDir}, false, nil
		} else if compareErr != nil {
			return Backup{}, false, errors.Join(err, compareErr)
		}
		return Backup{}, false, err
	}
	nameCopy := name
	return Backup{BackupFileName: &nameCopy, Version: version, BackupTime: time.Now().UTC(), Mode: uint32(mode.Perm()), RealParentDir: realParentDir}, true, nil
}

func validateBackupSource(info os.FileInfo, path string) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("filehistory: refusing to back up non-regular path %s", path)
	}
	return validateLinkCount(info)
}

func resolveRealParentDir(path string) string {
	parent := filepath.Dir(path)
	if resolved, err := filepath.EvalSymlinks(parent); err == nil {
		return resolved
	}
	return filepath.Clean(parent)
}

func (s *Store) ensureBackupDir() error {
	dir := s.backupDir()
	if err := s.validateBackupDir(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return s.validateBackupDir()
}

func (s *Store) validateBackupDir() error {
	return validateDirectoryChain(s.backupDir(), s.root)
}

func validateDirectoryChain(path, boundary string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	absBoundary, err := filepath.Abs(boundary)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(absBoundary, absPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("filehistory: backup directory escapes root")
	}
	for current := absPath; ; current = filepath.Dir(current) {
		info, statErr := os.Lstat(current)
		if statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("filehistory: refusing symlink directory %s", current)
			}
			if !info.IsDir() {
				return fmt.Errorf("filehistory: backup path is not a directory %s", current)
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		if current == absBoundary {
			return nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return fmt.Errorf("filehistory: backup directory is outside root")
		}
	}
}

func (s *Store) removeBackup(backup Backup) {
	if backup.BackupFileName == nil {
		return
	}
	if err := s.validateBackupDir(); err != nil {
		return
	}
	_ = os.Remove(filepath.Join(s.backupDir(), *backup.BackupFileName))
}

func (s *Store) backupMatchesFile(path string, backup Backup) (bool, error) {
	if backup.BackupFileName == nil {
		return false, nil
	}
	currentInfo, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if err := s.validateBackupDir(); err != nil {
		return false, err
	}
	backupPath := filepath.Join(s.backupDir(), *backup.BackupFileName)
	info, err := os.Lstat(backupPath)
	if err != nil {
		return false, err
	}
	if err := validateBackupSource(info, backupPath); err != nil {
		return false, err
	}
	if currentInfo.Mode().Perm() != info.Mode().Perm() {
		return false, nil
	}
	return backupMatchesDataFromInfo(backupPath, info, current)
}

func (s *Store) backupMatchesData(path string, data []byte, mode os.FileMode) (bool, error) {
	if err := s.validateBackupDir(); err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if err := validateBackupSource(info, path); err != nil {
		return false, err
	}
	if mode.Perm() != 0 && info.Mode().Perm() != mode.Perm() {
		return false, nil
	}
	return backupMatchesDataFromInfo(path, info, data)
}

func backupMatchesDataFromInfo(path string, _ os.FileInfo, data []byte) (bool, error) {
	stored, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return bytes.Equal(data, stored), nil
}

type restorePlan struct {
	path          string
	remove        bool
	data          []byte
	mode          os.FileMode
	realParentDir string
	changed       bool
	rollbackData  []byte
	rollbackMode  os.FileMode
	rollbackExist bool
}

func (s *Store) prepareRestore(path string, backup Backup) (restorePlan, error) {
	plan := restorePlan{path: path, realParentDir: backup.RealParentDir}
	if err := validatePath(path, true); err != nil {
		return plan, err
	}
	if err := validateRestoreParent(path, backup.RealParentDir); err != nil {
		return plan, err
	}
	if backup.BackupFileName == nil {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return plan, nil
		}
		if err != nil {
			return plan, err
		}
		if info.IsDir() {
			return plan, fmt.Errorf("filehistory: refusing to remove directory %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return plan, err
		}
		plan.rollbackData = data
		plan.rollbackMode = info.Mode().Perm()
		plan.rollbackExist = true
		plan.remove = true
		plan.changed = true
		return plan, nil
	}
	backupPath := filepath.Join(s.backupDir(), *backup.BackupFileName)
	backupInfo, err := os.Lstat(backupPath)
	if err != nil {
		return plan, err
	}
	if backupInfo.Mode()&os.ModeSymlink != 0 || !backupInfo.Mode().IsRegular() {
		return plan, fmt.Errorf("filehistory: backup is not a regular file: %s", backupPath)
	}
	if err := validateLinkCount(backupInfo); err != nil {
		return plan, err
	}
	mode := os.FileMode(backup.Mode & uint32(os.ModePerm))
	if mode == 0 {
		mode = backupInfo.Mode().Perm()
	}
	currentInfo, currentInfoErr := os.Lstat(path)
	current, currentErr := os.ReadFile(path)
	stored, storedErr := os.ReadFile(backupPath)
	if storedErr != nil {
		return plan, storedErr
	}
	if currentInfoErr == nil {
		if currentErr != nil {
			return plan, currentErr
		}
		plan.rollbackData = current
		plan.rollbackMode = currentInfo.Mode().Perm()
		plan.rollbackExist = true
	} else if !errors.Is(currentInfoErr, os.ErrNotExist) {
		return plan, currentInfoErr
	}
	modeMatches := currentInfoErr == nil && currentInfo.Mode().Perm() == mode
	plan.data = stored
	plan.mode = mode
	plan.changed = !(currentErr == nil && modeMatches && bytes.Equal(current, stored))
	return plan, nil
}

func (s *Store) applyRestore(plan restorePlan) (bool, error) {
	if !plan.changed {
		return false, nil
	}
	if err := ensureRestoreParent(plan.path, plan.realParentDir, !plan.remove); err != nil {
		return false, err
	}
	if plan.remove {
		if err := os.Remove(plan.path); err != nil {
			return false, err
		}
		return true, nil
	}
	file, err := os.OpenFile(plan.path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, plan.mode)
	if err != nil {
		return false, err
	}
	n, writeErr := file.Write(plan.data)
	if writeErr == nil && n != len(plan.data) {
		// File.Write is allowed to return a short write without an error.
		// Rewriting a backup must never silently truncate the restored file.
		writeErr = io.ErrShortWrite
	}
	chmodErr := file.Chmod(plan.mode)
	closeErr := file.Close()
	return true, errors.Join(writeErr, chmodErr, closeErr)
}

func (s *Store) rollbackRestore(plans []restorePlan) error {
	var errs []error
	for index := len(plans) - 1; index >= 0; index-- {
		plan := plans[index]
		if err := ensureRestoreParent(plan.path, plan.realParentDir, plan.rollbackExist); err != nil {
			errs = append(errs, err)
			continue
		}
		if !plan.rollbackExist {
			if err := os.Remove(plan.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
			continue
		}
		file, err := os.OpenFile(plan.path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, plan.rollbackMode)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		n, writeErr := file.Write(plan.rollbackData)
		if writeErr == nil && n != len(plan.rollbackData) {
			writeErr = io.ErrShortWrite
		}
		chmodErr := file.Chmod(plan.rollbackMode)
		closeErr := file.Close()
		if err := errors.Join(writeErr, chmodErr, closeErr); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func validateRestoreParent(path, expected string) error {
	parent := filepath.Dir(path)
	resolved, err := filepath.EvalSymlinks(parent)
	if err == nil {
		resolved = filepath.Clean(resolved)
		if expected != "" {
			if resolved != filepath.Clean(expected) {
				return fmt.Errorf("filehistory: refusing changed parent directory for %s", path)
			}
		} else if resolved != filepath.Clean(parent) {
			return fmt.Errorf("filehistory: refusing symlink parent directory for %s", path)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return validateDirectoryChain(parent, filepath.Dir(parent))
}

func ensureRestoreParent(path, expected string, create bool) error {
	if err := validateRestoreParent(path, expected); err != nil {
		return err
	}
	if !create {
		return nil
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	return validateRestoreParent(path, expected)
}

func (s *Store) restoreFile(path string, backup Backup) (bool, error) {
	plan, err := s.prepareRestore(path, backup)
	if err != nil {
		return false, err
	}
	return s.applyRestore(plan)
}

func (s *Store) backupDir() string { return filepath.Join(s.root, "file-history", s.sessionID) }

func backupName(path string, version int) string {
	hash := sha256.Sum256([]byte(path))
	return hex.EncodeToString(hash[:])[:16] + fmt.Sprintf("@v%d", version)
}

func normalizePath(path string) (string, error) {
	if path == "" {
		return "", errors.New("filehistory: empty path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func validatePath(path string, allowMissing bool) error {
	abs, err := normalizePath(path)
	if err != nil {
		return err
	}
	info, statErr := os.Lstat(abs)
	if statErr != nil {
		if allowMissing && errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		return statErr
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("filehistory: refusing symlink path %s", abs)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("filehistory: target is not a regular file %s", abs)
	}
	if info, statErr := os.Lstat(abs); statErr == nil {
		if err := validateLinkCount(info); err != nil {
			return err
		}
	}
	return nil
}

func validateLinkCount(info os.FileInfo) error {
	sys := info.Sys()
	if sys == nil {
		return nil
	}
	value := reflect.ValueOf(sys)
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if value.IsValid() && value.Kind() == reflect.Struct {
		field := value.FieldByName("Nlink")
		if field.IsValid() && field.Kind() >= reflect.Uint && field.Kind() <= reflect.Uint64 && field.Uint() > 1 {
			return fmt.Errorf("filehistory: refusing hard-linked file %s", info.Name())
		}
	}
	return nil
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	copy := snapshot
	copy.TrackedFileBackups = make(map[string]Backup, len(snapshot.TrackedFileBackups))
	for path, backup := range snapshot.TrackedFileBackups {
		copy.TrackedFileBackups[path] = backup
	}
	return copy
}

func cloneSnapshots(snapshots []Snapshot) []Snapshot {
	if snapshots == nil {
		return nil
	}
	out := make([]Snapshot, len(snapshots))
	for index, snapshot := range snapshots {
		out[index] = cloneSnapshot(snapshot)
	}
	return out
}
