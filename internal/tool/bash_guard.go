package tool

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// BackgroundRunner 把命令交给后台任务执行,返回任务 ID 供后续查询
type BackgroundRunner interface {
	RunBackground(context.Context, map[string]any) (llm.ToolResult, error)
}

// guardShellCommand 拦截灾难性命令。explore/plan 的只读性由工具池与 prompt 保证,
// 这里只挡住模型无论如何都不该执行的操作
func guardShellCommand(command string) error {
	// 1.控制字符会被 bash 静默丢弃,让校验与实际执行产生分歧
	if controlCharPattern.MatchString(command) {
		return fmt.Errorf("bash: command contains non-printable control characters")
	}
	// 2.逐段检查,避免 `ls; rm -rf /` 这类复合命令绕过
	for _, segment := range splitShellSegments(command) {
		if reason := dangerousShellSegment(segment); reason != "" {
			return fmt.Errorf("bash: %s", reason)
		}
	}
	return nil
}

// controlCharPattern 匹配 bash 会静默丢弃的不可打印字符,保留 \t \n \r
var controlCharPattern = regexp.MustCompile(`[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]`)

// splitShellSegments 按 shell 运算符切分命令
func splitShellSegments(command string) []string {
	return strings.FieldsFunc(command, func(r rune) bool {
		return r == ';' || r == '|' || r == '&' || r == '\n' || r == '(' || r == ')'
	})
}

// dangerousShellSegment 返回该片段被拒绝的原因,空字符串表示放行
func dangerousShellSegment(segment string) string {
	fields := strings.Fields(strings.TrimSpace(segment))
	if len(fields) == 0 {
		return ""
	}
	// 3.zsh 模块内建命令在 bash 下不存在,拦掉可避免解析器分歧
	if zshModuleCommands[fields[0]] {
		return fmt.Sprintf("zsh module command %q is not allowed", fields[0])
	}
	// 4.递归删除根目录或家目录
	if fields[0] == "rm" && hasRecursiveForceFlag(fields[1:]) && targetsCriticalPath(fields[1:]) {
		return "refusing to recursively delete a critical path"
	}
	// 5.写块设备会不可逆地破坏磁盘
	if fields[0] == "dd" && writesBlockDevice(fields[1:]) {
		return "refusing to write directly to a block device"
	}
	return ""
}

var zshModuleCommands = map[string]bool{
	"zmodload": true, "emulate": true, "sysopen": true, "sysread": true,
	"syswrite": true, "sysseek": true, "zpty": true, "ztcp": true,
	"zsocket": true, "zf_rm": true, "zf_mv": true, "zf_ln": true,
	"zf_chmod": true, "zf_chown": true, "zf_mkdir": true, "zf_rmdir": true,
	"zf_chgrp": true,
}

// hasRecursiveForceFlag 判断 rm 是否同时带递归与强制
func hasRecursiveForceFlag(args []string) bool {
	recursive, force := false, false
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		if strings.ContainsAny(arg, "rR") {
			recursive = true
		}
		if strings.Contains(arg, "f") {
			force = true
		}
	}
	return recursive && force
}

// targetsCriticalPath 判断 rm 的目标是否包含根目录或家目录本身
func targetsCriticalPath(args []string) bool {
	home, _ := os.UserHomeDir()
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		cleaned := strings.TrimRight(arg, "/")
		if cleaned == "" || cleaned == "/" || cleaned == "." || cleaned == ".." ||
			cleaned == "/*" || cleaned == "~" || (home != "" && cleaned == home) {
			return true
		}
	}
	return false
}

// writesBlockDevice 判断 dd 的 of= 是否指向块设备
func writesBlockDevice(args []string) bool {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "of=") {
			continue
		}
		target := strings.TrimPrefix(arg, "of=")
		if strings.HasPrefix(target, "/dev/") && !strings.HasPrefix(target, "/dev/null") {
			return true
		}
	}
	return false
}
