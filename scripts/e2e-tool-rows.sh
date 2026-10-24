#!/usr/bin/env bash
# 工具行式的端到端验收：真实 API 驱动一次工具链，再用 tui-test 断言终端画面
#
# 用法：scripts/e2e-tool-rows.sh
# 前置：go、tui-test；~/.rivo/agent 下已配置可用的 provider（脚本会复制到临时目录）
set -uo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/rivo-tool-rows.XXXXXX")"
agent_dir="$work/agent"
project="$work/project"
binary="$work/rivo"
session="rivo-tool-rows-$$"
failures=0
assert_timeout=90000

cleanup() {
  tui-test close --session "$session" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

report() { printf '%-4s %s\n' "$1" "$2"; }

# 1.准备项目与独立的 agent 配置，避免污染本机会话与设置
mkdir -p "$project/demo" "$agent_dir"
cat >"$project/demo/greet.ts" <<'EOF'
export function greet(name: string): string {
  return `Hello, ${name}!`;
}
EOF
cat >"$project/demo/config.json" <<'EOF'
{
  "language": "zh",
  "greeting": "Hello"
}
EOF
cp "${HOME}/.rivo/agent/models.json" "$agent_dir/models.json"
cp "${HOME}/.rivo/agent/settings.json" "$agent_dir/settings.json" 2>/dev/null || true
# 凭据可能只写在 .env 里，带上它但不提交
[ -f "$root/.env" ] && cp "$root/.env" "$work/.env"
if [ -f "$work/.env" ]; then
  set -a
  # shellcheck disable=SC1091
  . "$work/.env"
  set +a
fi

# 2.构建被测程序
(cd "$root" && go build -o "$binary" ./cmd/rivo) || exit 1

# 备用屏幕没有 scrollback，断言只看当前视口；脚本因此先把终端拉高，容纳整段回合
expect_text() {
  local pattern="$1" what="$2"
  if tui-test --session "$session" expect text "$pattern" --regex --timeout "$assert_timeout" >/dev/null 2>&1; then
    report ok "$what"
  else
    report FAIL "$what ($pattern)"
    failures=$((failures + 1))
  fi
}

# 每轮的思考排在该轮调用之前，因此工具块的最后一个工具行必须是调用行。
# 旧实现把所有思考堆在末尾，这条断言能区分两者
expect_thinking_before_calls() {
  local what="$1" text last
  text="$(tui-test --session "$session" text 2>/dev/null)"
  last="$(printf '%s' "$text" | grep -E '^ (Thinking\.\.\.|⦁ )' | tail -1)"
  case "$last" in
    *"⦁ "*) report ok "$what" ;;
    "") report FAIL "$what (no tool block found)"; failures=$((failures + 1)) ;;
    *) report FAIL "$what (last tool line is thinking: $last)"; failures=$((failures + 1)) ;;
  esac
}

# find 命中与否都要看 matches 数组，退出码不区分空结果
expect_absent() {
  local pattern="$1" what="$2"
  local matches
  matches="$(tui-test --session "$session" find text "$pattern" --regex 2>/dev/null | grep -c '"row"')"
  if [ "$matches" -gt 0 ]; then
    report FAIL "$what ($pattern)"
    failures=$((failures + 1))
  else
    report ok "$what"
  fi
}

# 3.启动 TUI，等输入框就绪后再提交，避免 prompt 落在启动过程中
cd "$project" || exit 1
RIVO_AGENT_DIR="$agent_dir" tui-test run --session "$session" --restart -- "$binary" >/dev/null
if ! tui-test --session "$session" expect text '❯' --timeout 30000 >/dev/null 2>&1; then
  report FAIL "TUI did not start"
  tui-test --session "$session" text --full | tail -20
  exit 1
fi
# 长回合的工具行要留在视口内，先把终端拉高
tui-test --session "$session" resize 120 70 >/dev/null
tui-test --session "$session" submit 'Run these steps with tools, one after another, without asking questions: 1) read demo/greet.ts 2) read demo/config.json 3) run bash: node -e "console.log(1)" 4) write demo/notes.md with three lines 5) edit demo/greet.ts replacing Hello with Welcome. Then stop.' >/dev/null
if ! tui-test --session "$session" expect text 'Worked for' --regex --timeout 180000 >/dev/null 2>&1; then
  report FAIL "turn did not finish"
  tui-test --session "$session" text --full | tail -40
  exit 1
fi

# 4.断言调用行与结果行的形状
expect_text '⦁ (Read|Search|Bash|Write|Update)\(' "call lines use the tool-row labels"
expect_text 'Worked for [0-9]+[smhd]' "completion line reports the worked duration"
expect_absent '── *Worked for' "completion line is plain text, not a rule"
expect_text 'Searched for [0-9]+ pattern|Read [0-9]+ file' "read-only calls fold into one summary"
expect_text '⎿  (Read|Found|Listed|Wrote|Added) ' "result sentences hang under the bracket"
expect_text '[0-9]+ [+-]' "edit diff shows numbered changed rows"
# diff 版式：上下文行的行号后用 │ 分隔，变更行用 -/+，且整行底色连续
expect_text '[0-9]+ │' "context rows separate the number with │"
expect_text '[0-9]+ \+ ' "added rows use a + sign"
expect_text 'Welcome' "edit diff shows the changed text"
expect_absent 'tools done|… \+[0-9]+ more' "no compact group header or fold marker"
expect_absent '⦁[[:space:]]*$' "no nameless call row"
# 每轮的思考排在该轮调用之前
expect_thinking_before_calls "each round's thinking precedes its calls"
expect_absent 'tool error:' "no raw tool error prefix"

# 5.失败的调用给出一行原因：读一个不存在的文件必然失败，不依赖模型怎么包装命令
tui-test --session "$session" submit 'Use the read tool on demo/does-not-exist.ts and report what happened. Do not create the file.' >/dev/null
expect_text '⎿  Error: ' "failed call reports a one-line reason"
expect_text 'does-not-exist' "failure names the file it could not read"
expect_absent 'tool error:' "tool error prefix is not shown"

# 6.展开后仍是 ⎿ 结构，且不再有折叠提示
tui-test --session "$session" key press ctrl+o >/dev/null
expect_text '⎿' "expanded rows keep the result prefix"
expect_absent 'ctrl\+o to expand' "expanded rows show full output"

if tui-test --session "$session" screenshot "$work/screenshot.png" >/dev/null 2>&1 && [ -f "$work/screenshot.png" ]; then
  cp "$work/screenshot.png" "${TMPDIR:-/tmp}/rivo-tool-rows.png"
  report ok "screenshot saved to ${TMPDIR:-/tmp}/rivo-tool-rows.png"
fi

if [ "$failures" -gt 0 ]; then
  printf '\n%d assertion(s) failed; full transcript:\n' "$failures"
  tui-test --session "$session" text --full | tail -120
  exit 1
fi
printf '\nall tool-row assertions passed\n'
