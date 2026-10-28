# AGENTS.md

本文件中是「必须遵守」的部分，未在此声明的细节按现有代码就近模仿

判断依据优先级：本文件 > 同目录既有代码惯例 > Go 通用实践；三者冲突时以前者为准，并在改动说明里指出冲突

---

## 1. 分层与依赖方向

### 1.1 依赖层号

**层号 = 1 + 它所依赖的最高层号；零内部依赖的包为第 0 层**；层号相同即同层，可互相 import；**只允许从低层指向高层**

```text
0  internal/llm  internal/tui  internal/filehistory  internal/imageutil
   internal/shell
   internal/tool/args
1  internal/agent  internal/config  internal/ctxview  internal/prompt
   internal/task  internal/testutil
   internal/tool
   internal/llm/api/openai/cache
2  internal/compaction  internal/session  internal/subagent
   internal/llm/api/openai/completions  internal/llm/api/openai/responses
   internal/tool/backgroundbash  internal/tool/taskstoptool
3  internal/llm/provider/openai
   internal/tool/agenttool  internal/tool/sendmessagetool
4  internal/llm/provider/deepseek
   internal/tool/registry
5  internal/provider
6  internal/app
7  internal/tui/adapter
8  internal/cli
9  cmd/rivo
```

放新包前先写出它的 import 列表，用这张表算一遍层号：算出来不高于所有依赖方，就没有反向依赖

三条最容易被破坏的硬约束：

- `internal/llm` **根包**不 import 任何其他 `internal/` 包；协议实现放在 `internal/llm/api/<协议>` 与 `internal/llm/provider/<厂商>`，只向下依赖 `llm` 根包
- `internal/tui` 本体零 `internal/` 依赖，只认识自己定义的类型
- `internal/ctxview` 只依赖 `internal/llm`；它被 `session`(生产投影)与 `compaction`(消费投影)共同引用，因此**不得**反过来依赖这两者中的任何一个

本节只约束 `internal/` 包之间的依赖方向，不限制各包使用哪些第三方库；选第三方库按功能需要判断，不因分层而否决

### 1.2 不可变的职责边界

只列**改了就破坏依赖结构**的边界；表中未列出的能力属于该包的正常演进范围，不受约束

| 包 | 不可让渡的职责 | 不得承担 |
|---|---|---|
| `internal/llm` | 领域消息、内容块、事件、模型与用量类型、**工具契约**(`Tool`/`ToolResult`/`ToolDetails`)、流式原语 | I/O 编排、具体 provider 装配 |
| `internal/ctxview` | 会话投影的共享值类型与纯函数(消息展开、摘要筛选与包装) | 从 entry 生成投影、执行压缩、落盘 |
| `internal/session` | 会话持久化：条目落盘、编解码、树与投影重建 | 驱动 agent、渲染、TUI 状态 |
| `internal/tool` 根包 | 无状态内置工具实现、参数解码、备份追踪、文件变更队列 | 任何 `agent`/`session`/`subagent`/`task` 依赖 |
| `internal/tool/<name>` | 单一工具实现 | 装配多个工具 |
| `internal/tool/registry` | 唯一的工具装配点：工具集合、顺序、平台差异、子任务执行内核 | 业务规则、生命周期 |
| `internal/agent` | 单次运行循环：模型流、工具批次 | 会话落盘细节、子任务管理、UI |
| `internal/app` | 跨领域装配与编排：进程内唯一把各领域包接成可运行对象的地方 | 协议实现、终端渲染 |
| `internal/tui`(本体) | 纯视图与交互状态机 | 任何领域类型接线 |
| `internal/tui/adapter` | 领域 ↔ TUI 的唯一边界：请求路由、事件投影、快照 | 业务规则 |

`internal/tool/<name>` 若要 import `agent`/`session`/`subagent`/`task`，属于架构变更，须在改动说明中论证——否则工具层的扇出会从子包重新长回来

### 1.3 新代码放哪里

**职责归属决定包位置，依赖关系只界定允许的上限**；按顺序判断，命中即停：

1. 已存在承担该职责的包 → 放进去；上表是判断依据，表外的能力按包的现有代码就近归属
2. 属于模型协议 → `internal/llm/api/<协议>`；属于厂商装配 → `internal/llm/provider/<厂商>`，并在 `internal/provider` 的内置表登记
3. 一组同构实现的**选择与装配**(无状态、键 → 构造函数) → 该领域包下的 `registry` 子包，由上层引用；**有状态、有生命周期的编排** → `internal/app`
4. 需要认识领域类型又要产出 TUI 结构 → `internal/tui/adapter`，不要污染 `internal/tui`
5. 单一工具实现(含其参数、结果、详情类型) → `internal/tool/<name>`
6. 跨 ≥2 个包、且不依赖任何包内未导出符号的测试假实现 → `internal/testutil`(不含生产逻辑)；包内假实现留在原包的 `_test.go`

以上都不命中时，才在 `internal/<domain>` 下新建包

### 1.4 禁止事项

- 不得为图方便让低层包 import 高层包(例如让 `session` import `app`)
- 同一类状态出现 3 个以上字段时抽子结构；给已有类型加字段前，先确认能否并入已有的子结构(如 `composer`/`panel`/`transcript`/`queues`)
- 不得让 `internal/tui` 本体 import 领域包，即使是「只用一个常量」

---

## 2. 测试

只有两类：单元测试与集成测试；区分依据是**被测代码是否需要真实外部依赖**，与代码属于哪个包无关；同一个包里可以两类都有

### 2.1 两类

| 类型 | 判据 |
|---|---|---|
| 单元测试 | 聚焦单个函数等最小代码单元，用模拟手段隔离外部依赖，仅验证内部独立逻辑是否正确
| 集成测试 | 聚焦多个模块或外部服务的组合，联通数据库与网络等真实依赖，重点验证各组件间的接口交互与协同是否正常

举例，不作为划分依据：

- 单元测试：`internal/compaction` 的切点计算、`internal/session` 的编解码(`codec_test.go`)、`internal/tool` 的参数解析
- 集成测试：`internal/filehistory` 的真实文件快照、`internal/shell` 的真实子进程、`internal/llm/api/openai/responses` 的 `httptest` SSE、`internal/app` 把 agent / subagent / task / session / tool 接起来跑一轮、`internal/tui/adapter` 用真实 `app` 事件驱动投影

### 2.2 硬性约束

- **测试与被测包共置**；不要建 `test/integration/`、`test/unit/` 之类的平行目录，Go 的默认做法就是同包
- **新增测试默认同包**(`package foo`)；只有在刻意验证「外部消费者视角」时才用 `package foo_test`，不要为了「更纯粹」而普遍外置

### 2.3 该测什么、不该测什么

优先覆盖：状态转移、边界条件、错误路径、并发与取消、跨模块契约、用户可见行为

不写：转发型包装(A 调 B 就断言 B 被调)、mock 回显、源码文本断言、恒真断言、仅断言「非空/长度变大」、同一路径的重复用例

已废弃路径的测试随代码一并删除，不要保留「以防万一」

### 2.4 覆盖率

**全仓语句覆盖率**要求 **≥ 80%**；

不得为凑数写无行为意义的测试

---

## 4. 代码约定

### 4.1 错误

- 不吞错误；签名允许返回 `error` 就必须处理或向上返回，不得 `_ =` 丢弃
- 不用「永远返回 nil 的 error 签名」；函数不会失败就不要返回 `error`
- 不写不可达的防御性回退分支；构造点保证非 nil，就不要在下游反复判空
- 失败要可见：要么返回错误，要么进入工具结果/警告，不要静默降级

### 4.2 类型

- 业务对象不用裸 `any`；需要开放容器时用显式接口 + 注册表(见 `llm.ToolDetails`)，并保证 JSON 往返
- 类型断言必须带 `ok`；单值断言在类型不符时会 panic
- 「未提供」与「零值」语义不同时用指针区分，不要靠 0/"" 猜
- 工具参数优先用 Params struct + `internal/tool/args.Decode`；取单个必填字段用 `args.RequiredString`；shell 类共用的 `command`/`timeout` 走 `internal/shell.ParseArgs`；不要另写第三套解析，也不要在 `Execute` 里手写不带 `ok` 的断言

### 4.3 性能

- 热路径(流式事件、每帧渲染)不做 O(n²) 字符串拼接，用 `strings.Builder` 或旁挂缓冲
- 每帧重建之前先判断是否真的变了，无变化直接跳过
- 深拷贝只在「对外发布」边界做一次，内部变更原地修改
- 不写「先全量重建再丢弃」的代码

### 4.4 资源

- 长生命周期 goroutine 必须有明确退出路径，由所属类型的 `Close`/`stop` 驱动
- 对象被替换时(如换会话)必须关闭旧对象，否则 goroutine 与其引用图会常驻
- 定时器在无工作时要停表，不空转

### 4.5 注释

中文，简练，不啰嗦；写「做什么」与「为什么」，不复述代码

- 关键结构体的关键字段右侧注明用途
- 函数上方说明做什么，不加「作用」二字
- 函数内部按执行链路给关键步骤编号(`// 1.…` `// 2.…`)，只标关键动作，不逐行注释
- 句末不加句号
- 旧注释与新实现矛盾时删旧留新，不要叠加

### 4.6 函数与结构

- 单函数超过约 130 行就按「每轮做的一件事」拆开，主函数只做分发
- switch 分发保持 switch，不要改成 `map[type]handler` 表驱动——那只是多一层间接且丢编译期检查
- 一个类型承担多类互不相关职责时按职责拆子结构并组合，不要继续加字段
- bool 参数若切换两条近乎独立的路径，拆成两个入口

---

## 5. 提交与协作

- 未经明确授权，不执行 `git commit` / `git push`(含 amend、rebase 内 commit、`--force` 及等价的 GUI/MCP/子代理操作)
- `*.md` 默认不入库(`.gitignore` 已忽略)，文档改动不需要 commit
- 一次只新建一个 issue/PR，一个 PR 对应一个 issue
- 提交信息与分支命名遵循 Conventional Commits 与仓库既有风格(`refactor:`、`feat(scope):`、`fix:`)
- issuse描述规范：描述尽可能简练
- PR描述规范：描述要有大局观，不要只顾最近几次commit的改动，要注意整个分支的改动，改动说明里不写「模仿某某实现」这类描述，只写做了什么、为什么

---

## 6. 改动前先看

动手前先确认三件事，避免重复已有工作或制造第二套惯例：

1. **有没有同类实现**；`llm.TextOf`、`llm.DisplayText`、`llm.CloneMessage`、`internal/tool/args`、`internal/testutil` 都是收敛后的唯一实现，不要再写一份
2. **这个函数/类型属于哪一层**(第 1 节)，会不会制造反向依赖
3. **现有测试是否已经断言了要改的行为**，改动后哪些测试需要跟着改或删除

导出的符号要改签名时，先用 LSP 查全部引用，一次迁移所有调用点，不留兼容 shim

---

## 7. 明确不管的部分

以下不属于本文件约束范围，按各自既有做法处理：具体业务逻辑、UI 视觉细节、prompt 文案、依赖版本选择、发布流程

本文件若与代码现状不一致，以「代码是对的、文档过时」为默认假设，并修正本文件；若确实是代码违反了这里写明的硬约束，则修代码
