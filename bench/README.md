# Rivo SWE-bench Verified 评测

英文版见 [README.en.md](README.en.md)。

本目录提供使用 Rivo 运行 [SWE-bench Verified](https://github.com/SWE-bench/SWE-bench) 的脚本。脚本默认支持 Verified 的全部 500 个实例，也可以用 `--instance` 或 `--limit` 先运行小样本。

## 评测原理

SWE-bench 将真实 GitHub Issue 与对应仓库的历史提交配对。每个实例包含仓库地址、`base_commit`、问题描述，以及用于评分的 `FAIL_TO_PASS` 和 `PASS_TO_PASS` 测试信息。Verified 是其中经过工程师确认可解决的 500 个实例。数据字段说明见 [官方数据集文档](https://github.com/SWE-bench/SWE-bench/blob/main/docs/guides/datasets.md)。

一次评测分为两个阶段：

1. **生成 patch**：在 `base_commit` 创建临时源码 checkout，把问题描述交给 Rivo。每个实例使用唯一的 `rivo --session-id` 会话，Rivo 检查代码、修改 checkout 并留下最终实现；脚本将源码变化提取为 `model_patch`。
2. **官方评分**：Docker harness 在固定的任务环境中应用 `model_patch` 并运行测试。目标测试需要从失败变为通过，原本通过的维护测试需要继续通过；满足要求的实例记为 `resolved`。评分依据测试结果，不要求 patch 文本与官方 gold patch 完全相同。

这种设计把“模型是否能完成真实软件修复”和“本机依赖是否恰好可用”分开：模型在临时 checkout 中工作，会话通过显式 ID 隔离，官方 harness 在 Docker 中重建仓库环境。官方评测指南见 [evaluation.md](https://github.com/SWE-bench/SWE-bench/blob/main/docs/guides/evaluation.md)。

## 前置条件

- Go、Python 和 Docker
- 已配置好的 Rivo provider、model 和凭据
- `datasets` Python 包
- SWE-bench CLI
- 在 Apple Silicon 等 ARM 机器上，需要本地的 `swe-bench-tasks` checkout，用于构建任务镜像

安装 SWE-bench CLI 和任务仓库：

```bash
python3 -m venv .venv
source .venv/bin/activate
git clone https://github.com/SWE-bench/SWE-bench.git
cd SWE-bench
pip install -e .
cd ..

git clone --depth 1 https://github.com/SWE-bench/swe-bench-tasks.git
swebench dataset check swe-bench-tasks
deactivate
```

在仓库根目录构建 Rivo

```bash
go build -o Rivo ./cmd/rivo
```

## 三个命令

脚本路径为 `bench/standard/main.py`。

### `infer`

只运行 Rivo，生成 predictions 和费用摘要，不启动 Docker 评分：

```bash
python3 bench/standard/main.py infer \
  --instance sympy__sympy-20590 \
  --rivo Rivo \
  --swebench-venv .venv \
  --output-dir .swebench/runs/sympy-20590
```

参数说明：

- `--instance`：实例 ID，可重复传入
- `--limit`：只处理筛选结果的前 N 个实例
- `--rivo`：Rivo 可执行文件路径；省略时从当前 checkout 构建
- `--repo-cache`：共享裸仓库缓存目录，默认 `.swebench/repos`
- `--output-dir`：保存 prediction、费用、session 和实例日志的目录
- `--timeout`：单个 Rivo 实例的超时时间，默认 1800 秒
- `--keep-checkouts`：把临时源码 checkout 保留在输出目录便于调试
- `--swebench-venv` / `--venv`：使用该 venv 的 Python 加载 `datasets`

### `eval`

对已有 predictions 调用官方 Docker harness：

```bash
python3 bench/standard/main.py eval \
  --predictions .swebench/runs/sympy-20590/predictions.jsonl \
  --instance sympy__sympy-20590 \
  --task-repo swe-bench-tasks \
  --swebench-venv .venv \
  --run-id rivo-sympy-20590 \
  --workers 1
```

参数说明：

- `--predictions`：`infer` 生成的 `predictions.jsonl`
- `--instance`：只评分指定实例，可重复传入
- `--task-repo`：本地 `swe-bench-tasks` checkout；ARM 机器构建镜像时需要
- `--run-id`：官方评分运行 ID，重新评测不同 patch 时应使用新的 ID
- `--workers`：Docker 评分并发数，默认 1
- `--swebench`：SWE-bench CLI 可执行文件名或路径
- `--swebench-venv` / `--venv`：包含 SWE-bench CLI 的 Python 虚拟环境目录
- `--keep-images`：评测完成后保留本次本地构建的 task image，默认删除
- `--reuse-images`：复用已存在的本地 task image；不能同时传 `--task-repo`

### `all`

先执行 `infer`，再执行 `eval`：

```bash
python3 bench/standard/main.py all \
  --instance sympy__sympy-20590 \
  --rivo Rivo \
  --task-repo swe-bench-tasks \
  --swebench-venv .venv \
  --output-dir .swebench/runs/sympy-20590 \
  --run-id rivo-sympy-20590 \
  --workers 1
```

`all` 同时接受 `infer` 和 `eval` 的参数，先生成 patch 和费用摘要，再调用官方评分。

## 选择实例和模型配置

不提供 `--instance` 或 `--limit` 时，`infer` 和 `all` 默认运行全部 500 个 Verified 实例。完整运行会消耗较多时间、磁盘和 API 费用；建议先使用单个实例验证环境。

可以重复传入 `--instance`：

```bash
--instance sympy__sympy-20590 \
--instance astropy__astropy-14539
```

也可以选择前几个实例：

```bash
--limit 2
```

provider、model 和 effort 默认从现有 Rivo 配置及环境读取，也可以用命令行覆盖：

```bash
--provider openai \
--model gpt-5 \
--effort medium
```

`--provider` 必须与 `--model` 一起使用

官方 harness 会按 `run_id` 和 `instance_id` 缓存结果。同一个 run ID 重复评测同一实例时，可能直接复用旧结果。

## 费用和任务耗时

每个实例完成 Rivo 后，脚本会根据显式 session ID 找到本次会话，并调用 `rivo --cost` 读取该 session 文件。

费用输出无法解析、存在未知费用，或没有找到对应 session 时，脚本会立即失败，避免产生不可信的费用统计。

任务耗时使用单调时钟统计，从启动 Rivo `--print` 前开始，到该 Rivo 进程返回为止。以下操作不计入任务耗时：

- 仓库 clone 和临时 checkout 创建
- `rivo --cost` 费用统计
- patch 提取和结果文件写入
- Docker harness 评分

每个实例的耗时写入 `cost-summary.json` 的 `rows[].implementation_seconds`；总耗时写入 `implementation_seconds_total`。

传入 `--task-repo` 时，SWE-bench 会本地构建 task image。`eval` 或 `all` 完成后，脚本默认删除这些实例镜像，只保留 Docker 基础层和共享缓存；传入 `--keep-images` 可以保留镜像供后续评测复用。

## 输出文件

`infer` 写 `--output-dir` 里的任务产物，`eval` 写同目录的评分日志；`--repo-cache` 默认指向 `repos/`，放共享裸仓库缓存。

```text
.swebench/
├── repos/                         # 共享裸仓库缓存
│   └── sympy_sympy.git
└── runs/                          # 每次运行的输出
    └── sympy-20590/
        ├── predictions.jsonl      # 给 SWE-bench 读的 patch 列表
        ├── cost-summary.json      # 累计费用与耗时
        ├── instances/             # 每个实例的产物
        │   └── sympy__sympy-20590/
        │       ├── agent.stdout
        │       ├── agent.stderr
        │       └── session.jsonl  # 供复核 rivo --cost
        ├── checkouts/             # 仅 --keep-checkouts 时保留
        └── logs/                  # swe-bentch评分日志
            └── evaluation/
                └── rivo-sympy-20590/
                    ├── run.json
                    ├── results.json
                    └── rivo/
                        └── sympy__sympy-20590/
                            ├── report.json
                            ├── run_instance.log
                            ├── test_output.txt
                            └── patch.diff
```

评分详细见 [官方评测指南](https://github.com/SWE-bench/SWE-bench/blob/main/docs/guides/evaluation.md)。