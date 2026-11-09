#!/usr/bin/env python3
"""Run Rivo on SWE-bench Verified tasks and grade the resulting patches."""

from __future__ import annotations

import argparse
import json
import os
import platform
import re
import shutil
import subprocess
import sys
import tempfile
import time
import uuid
from pathlib import Path
from typing import Any


DATASET = "SWE-bench/SWE-bench_Verified"
DEFAULT_TIMEOUT = 1800
COST_RE = re.compile(r"^Cost \(USD\): \$([0-9]+(?:\.[0-9]+)?)$")
UNKNOWN_RE = re.compile(r"^Unknown cost entries: ([0-9]+)$")


class BenchError(RuntimeError):
    """An expected benchmark setup or execution failure."""


# 执行外部命令并把可预期的进程错误统一转换为评测错误
def run_command(
    args: list[str],
    *,
    cwd: Path | None = None,
    env: dict[str, str] | None = None,
    input_text: str | None = None,
    timeout: int | None = None,
    capture: bool = True,
) -> subprocess.CompletedProcess[str]:
    try:
        return subprocess.run(
            args,
            cwd=cwd,
            env=env,
            input=input_text,
            text=True,
            stdout=subprocess.PIPE if capture else None,
            stderr=subprocess.PIPE if capture else None,
            timeout=timeout,
            check=False,
        )
    except FileNotFoundError as exc:
        raise BenchError(f"command not found: {args[0]}") from exc
    except subprocess.TimeoutExpired as exc:
        detail = exc.stderr or exc.stdout or ""
        if isinstance(detail, bytes):
            detail = detail.decode("utf-8", errors="replace")
        detail = detail.strip()
        if len(detail) > 4000:
            detail = detail[-4000:]
        suffix = f"\n{detail}" if detail else ""
        raise BenchError(f"command timed out after {timeout}s: {args[0]}{suffix}") from exc


def checked_command(args: list[str], **kwargs: Any) -> str:
    # 统一检查命令退出码，保留 stdout/stderr 作为诊断信息
    result = run_command(args, **kwargs)
    if result.returncode != 0:
        detail = (result.stderr or result.stdout or "").strip()
        command = " ".join(args)
        raise BenchError(f"command failed ({result.returncode}): {command}\n{detail}")
    return result.stdout or ""


def load_instances(instance_ids: list[str], limit: int | None) -> list[dict[str, Any]]:
    # 1.读取 Verified test split
    # 2.按显式实例筛选，再应用数量限制
    try:
        from datasets import load_dataset
    except ImportError as exc:
        raise BenchError("infer requires the 'datasets' package; install SWE-bench first") from exc

    rows = [dict(row) for row in load_dataset(DATASET, split="test")]
    by_id = {str(row["instance_id"]): row for row in rows}
    if instance_ids:
        missing = [instance_id for instance_id in instance_ids if instance_id not in by_id]
        if missing:
            raise BenchError("unknown instance id(s): " + ", ".join(missing))
        rows = [by_id[instance_id] for instance_id in instance_ids]
    if limit is not None:
        rows = rows[:limit]
    if not rows:
        raise BenchError("no SWE-bench instances selected")
    return rows


def safe_name(value: str) -> str:
    # 把实例标识转换成可用于目录名的稳定字符串
    return re.sub(r"[^A-Za-z0-9_.-]+", "_", value)


def repo_url(repo: str) -> str:
    return f"https://github.com/{repo}.git"


def ensure_repo(cache_dir: Path, repo: str, base_commit: str) -> Path:
    # 复用仓库裸缓存，并确保目标 base commit 已经可从缓存访问
    cache_dir.mkdir(parents=True, exist_ok=True)
    bare = cache_dir / (safe_name(repo) + ".git")
    if not bare.exists():
        checked_command(
            [
                "git",
                "clone",
                "--bare",
                "--filter=blob:none",
                "--no-tags",
                repo_url(repo),
                str(bare),
            ],
            timeout=DEFAULT_TIMEOUT,
        )
    have_commit = run_command(
        ["git", "--git-dir", str(bare), "cat-file", "-e", f"{base_commit}^{{commit}}"]
    )
    if have_commit.returncode != 0:
        checked_command(
            ["git", "--git-dir", str(bare), "fetch", "--quiet", "origin", base_commit],
            timeout=DEFAULT_TIMEOUT,
        )
    # 清理上次异常退出留下的 worktree 管理记录
    run_command(["git", "--git-dir", str(bare), "worktree", "prune"])
    return bare


def create_checkout(bare: Path, path: Path, base_commit: str) -> None:
    # 每个实例从裸缓存的 base commit 建立独立 worktree；缺失对象由裸缓存按需补齐
    path.parent.mkdir(parents=True, exist_ok=True)
    checked_command(
        ["git", "--git-dir", str(bare), "worktree", "add", "--detach", str(path), base_commit],
        timeout=DEFAULT_TIMEOUT,
    )
    # 缺失对象被静默跳过时 status 会出现删除行，这里直接判为 checkout 失败
    status = checked_command(
        ["git", "-C", str(path), "status", "--porcelain", "--ignore-submodules=all"],
        timeout=DEFAULT_TIMEOUT,
    )
    if status.strip():
        raise BenchError(f"checkout at {base_commit} is incomplete: {status.splitlines()[0]}")


def remove_checkout(bare: Path, path: Path) -> None:
    # 先从裸缓存注销 worktree，再删目录，避免残留管理记录
    run_command(["git", "--git-dir", str(bare), "worktree", "remove", "--force", str(path)])
    shutil.rmtree(path, ignore_errors=True)
    run_command(["git", "--git-dir", str(bare), "worktree", "prune"])


def session_dir(cwd: Path, home: Path) -> Path:
    # 按 Rivo 的 cwd 编码规则定位该实例隔离 HOME 下的 session 目录
    value = str(cwd.resolve())
    if value.startswith(("/", "\\")):
        value = value[1:]
    value = value.replace("/", "-").replace("\\", "-").replace(":", "-")
    return home / ".rivo" / "sessions" / f"--{value}--"


def session_path(cwd: Path, home: Path, session_id: str) -> Path:
    # 显式 session ID 让费用统计直接绑定到本次 Rivo 执行
    directory = session_dir(cwd, home)
    matches = sorted(directory.glob(f"*_{session_id}.jsonl"))
    if len(matches) != 1:
        raise BenchError(f"Rivo did not create exactly one session for {session_id}")
    return matches[0]


def parse_cost(output: str, instance_id: str) -> dict[str, Any]:
    # 解析 Rivo --cost 的稳定文本格式，并把未知费用视为硬错误
    cost: float | None = None
    unknown: int | None = None
    values: dict[str, Any] = {"instance_id": instance_id}
    for line in output.splitlines():
        match = COST_RE.match(line.strip())
        if match:
            cost = float(match.group(1))
        match = UNKNOWN_RE.match(line.strip())
        if match:
            unknown = int(match.group(1))
        if ": " in line:
            key, value = line.split(": ", 1)
            if key in {
                "Input tokens",
                "Output tokens",
                "Cache read tokens",
                "Cache write tokens",
                "Total tokens",
            }:
                values[key.lower().replace(" ", "_")] = int(value)
    if cost is None or unknown is None:
        raise BenchError(f"could not parse cost output for {instance_id}")
    if unknown != 0:
        raise BenchError(f"unknown cost entries for {instance_id}: {unknown}")
    values["cost_usd"] = cost
    values["unknown_cost_entries"] = unknown
    return values


def cost_for_session(
    rivo: Path, session: Path, env: dict[str, str], instance_id: str
) -> dict[str, Any]:
    # 调用 Rivo --cost 读取单个 session 的费用
    result = run_command([str(rivo), "--cost", str(session)], env=env)
    if result.returncode != 0:
        detail = (result.stderr or result.stdout or "").strip()
        raise BenchError(f"rivo --cost failed for {instance_id}: {detail}")
    return parse_cost(result.stdout or "", instance_id)


def extract_patch(checkout: Path, base_commit: str) -> str:
    # 使用临时 Git index，把 tracked、deleted 和 untracked 文件统一转换成 patch
    with tempfile.NamedTemporaryFile(prefix="rivo-swebench-index-", delete=False) as handle:
        index = Path(handle.name)
    index.unlink()
    try:
        env = os.environ.copy()
        env["GIT_INDEX_FILE"] = str(index)
        checked_command(["git", "read-tree", base_commit], cwd=checkout, env=env)
        checked_command(["git", "add", "--all"], cwd=checkout, env=env)
        return checked_command(
            ["git", "diff", "--cached", "--binary", "--full-index", base_commit, "--"],
            cwd=checkout,
            env=env,
        )
    finally:
        index.unlink(missing_ok=True)


def prompt_for(row: dict[str, Any]) -> str:
    # 只向模型提供任务描述和仓库定位信息，不暴露 gold patch 或测试补丁
    return (
        "You are solving one SWE-bench task in the current repository checkout.\n"
        f"Instance: {row['instance_id']}\n"
        f"Repository: {row['repo']}\n"
        f"Base commit: {row['base_commit']}\n\n"
        "Implement the requested fix directly in the working tree. Inspect the code, "
        "make the smallest correct change, and run focused tests when practical. "
        "Do not only describe a patch and do not commit changes; leave the working tree "
        "containing the final solution.\n\n"
        "Issue:\n"
        f"{row['problem_statement']}\n"
    )


def model_label(args: argparse.Namespace) -> str:
    # 生成 SWE-bench prediction 中用于标识本次模型配置的名称
    if args.provider and args.model:
        return f"{args.provider}/{args.model}"
    if args.model:
        return args.model
    return "rivo"


def build_rivo(root: Path, configured: str | None) -> tuple[Path, Path | None]:
    # 优先使用用户指定的可执行文件，否则构建到临时目录；返回需要清理的目录
    if configured:
        path = Path(configured).expanduser().resolve()
        if not path.is_file() or not os.access(path, os.X_OK):
            raise BenchError(f"Rivo executable is not executable: {path}")
        return path, None
    binary_dir = Path(tempfile.mkdtemp(prefix="rivo-swebench-bin-"))
    binary = binary_dir / "rivo"
    try:
        checked_command(["go", "build", "-o", str(binary), "./cmd/rivo"], cwd=root, timeout=DEFAULT_TIMEOUT)
    except BaseException:
        shutil.rmtree(binary_dir, ignore_errors=True)
        raise
    return binary, binary_dir


def infer(args: argparse.Namespace, root: Path) -> Path:
    # 1.选择任务并准备 Rivo binary 和仓库缓存
    # 2.逐个实例运行 Rivo、统计费用并提取 patch
    # 3.写出 predictions 和累计费用摘要
    if args.provider and not args.model:
        raise BenchError("--provider requires --model")
    instances = load_instances(args.instance, args.limit)
    output = Path(args.output_dir).expanduser().resolve()
    rivo, binary_dir = build_rivo(root, args.rivo)
    cache = Path(args.repo_cache).expanduser().resolve()
    if args.keep_checkouts:
        checkouts_root = output / "checkouts"
    else:
        checkouts_root = Path(tempfile.mkdtemp(prefix="rivo-swebench-checkouts-"))
    predictions_path = output / "predictions.jsonl"
    cost_rows: list[dict[str, Any]] = []
    predictions: list[dict[str, Any]] = []
    failures: list[dict[str, str]] = []
    env = os.environ.copy()
    configured_dir = env.get("RIVO_AGENT_DIR", "").strip()
    config_dir = Path(configured_dir or (Path.home() / ".rivo")).expanduser().resolve()

    try:
        for number, row in enumerate(instances, start=1):
            # 每个实例使用独立 checkout、临时 HOME 和显式 session ID
            instance_id = str(row["instance_id"])
            checkout = checkouts_root / safe_name(instance_id)
            instance_dir = output / "instances" / safe_name(instance_id)
            task_home = Path(tempfile.mkdtemp(prefix="rivo-swebench-home-"))
            bare: Path | None = None
            try:
                task_env = env.copy()
                task_env["HOME"] = str(task_home)
                task_env["USERPROFILE"] = str(task_home)
                task_env["RIVO_AGENT_DIR"] = str(config_dir)
                bare = ensure_repo(cache, str(row["repo"]), str(row["base_commit"]))
                session_id = f"swebench-{safe_name(instance_id)}-{uuid.uuid4().hex[:12]}"
                create_checkout(bare, checkout, str(row["base_commit"]))
                # 固定 PWD 为解析后的 checkout，保证 Rivo 与脚本算出同一个 session 目录
                task_env["PWD"] = str(checkout.resolve())
                # 保留用户的 Rivo 配置和凭据，用 session ID 绑定本次任务上下文
                command = [str(rivo), "--session-id", session_id, "--print"]
                if args.provider:
                    command.extend(["--provider", args.provider])
                if args.model:
                    command.extend(["--model", args.model])
                if args.effort:
                    command.extend(["--effort", args.effort])
                # 只计 Rivo 进程执行时间，费用统计和 patch 处理均在计时结束后进行
                implementation_started = time.perf_counter()
                result = run_command(command, cwd=checkout, env=task_env, input_text=prompt_for(row), timeout=args.timeout)
                implementation_seconds = time.perf_counter() - implementation_started
                instance_dir.mkdir(parents=True, exist_ok=True)
                (instance_dir / "agent.stdout").write_text(result.stdout or "", encoding="utf-8")
                (instance_dir / "agent.stderr").write_text(result.stderr or "", encoding="utf-8")
                if result.returncode != 0:
                    detail = (result.stderr or result.stdout or "").strip()
                    raise BenchError(f"Rivo failed for {instance_id} ({result.returncode}): {detail}")
                # 会话复制到输出目录，临时 HOME 在 finally 中删除
                session = session_path(checkout, task_home, session_id)
                session_copy = instance_dir / "session.jsonl"
                shutil.copy2(session, session_copy)
                # 费用确认成功后才生成 prediction，未知费用会在这里立即终止评测
                cost = cost_for_session(rivo, session_copy, task_env, instance_id)
                cost["session_id"] = session_id
                cost["implementation_seconds"] = implementation_seconds
                patch = extract_patch(checkout, str(row["base_commit"]))
                prediction = {
                    "instance_id": instance_id,
                    "model_name_or_path": model_label(args),
                    "model_patch": patch,
                }
                predictions.append(prediction)
                cost_rows.append(cost)
                predictions_path.write_text(
                    "".join(json.dumps(item, ensure_ascii=False) + "\n" for item in predictions),
                    encoding="utf-8",
                )
                print(
                    f"[{number}/{len(instances)}] {instance_id}: "
                    f"${cost['cost_usd']:.6f}, {implementation_seconds:.3f}s",
                    flush=True,
                )
            except BenchError as exc:
                instance_dir.mkdir(parents=True, exist_ok=True)
                (instance_dir / "error.txt").write_text(str(exc), encoding="utf-8")
                failures.append({"instance_id": instance_id, "error": str(exc)})
                first_line = str(exc).splitlines()[0]
                print(f"[{number}/{len(instances)}] {instance_id}: FAILED: {first_line}", flush=True)
            finally:
                shutil.rmtree(task_home, ignore_errors=True)
                if not args.keep_checkouts:
                    if bare is None:
                        shutil.rmtree(checkout, ignore_errors=True)
                    else:
                        remove_checkout(bare, checkout)
    finally:
        if not args.keep_checkouts:
            shutil.rmtree(checkouts_root, ignore_errors=True)
        if binary_dir is not None:
            shutil.rmtree(binary_dir, ignore_errors=True)

    total = {
        "instances": len(cost_rows),
        "attempted": len(instances),
        "succeeded": len(predictions),
        "failed": len(failures),
        "failures": failures,
        "cost_usd": sum(row["cost_usd"] for row in cost_rows),
        "unknown_cost_entries": sum(row["unknown_cost_entries"] for row in cost_rows),
        "input_tokens": sum(row.get("input_tokens", 0) for row in cost_rows),
        "output_tokens": sum(row.get("output_tokens", 0) for row in cost_rows),
        "cache_read_tokens": sum(row.get("cache_read_tokens", 0) for row in cost_rows),
        "cache_write_tokens": sum(row.get("cache_write_tokens", 0) for row in cost_rows),
        "total_tokens": sum(row.get("total_tokens", 0) for row in cost_rows),
        "implementation_seconds_total": sum(
            row["implementation_seconds"] for row in cost_rows
        ),
        "rows": cost_rows,
    }
    (output / "cost-summary.json").write_text(json.dumps(total, indent=2) + "\n", encoding="utf-8")
    (output / "failures.json").write_text(
        json.dumps(failures, indent=2, ensure_ascii=False) + "\n", encoding="utf-8"
    )
    print(
        f"summary: {len(predictions)} succeeded, {len(failures)} failed, {len(instances)} attempted",
        flush=True,
    )
    return predictions_path


def eval_predictions(args: argparse.Namespace) -> int:
    # 只负责调用官方 Docker harness，不重新运行 Rivo
    predictions = Path(args.predictions).expanduser().resolve()
    if not predictions.is_file():
        raise BenchError(f"predictions file does not exist: {predictions}")
    run_id = args.run_id or f"rivo-verified-{time.strftime('%Y%m%d-%H%M%S')}-{time.time_ns() % 1_000_000:06d}"
    if args.reuse_images and args.task_repo:
        raise BenchError("--reuse-images cannot be combined with --task-repo")
    if platform.machine().lower() in {"arm64", "aarch64"} and not args.task_repo and not args.reuse_images:
        raise BenchError("ARM evaluation requires --task-repo for local SWE-bench image builds")
    swebench = resolve_swebench(args.swebench, args.swebench_venv)
    command = [swebench, "eval", "verified", "-p", str(predictions), "--run-id", run_id, "-j", str(args.workers)]
    if args.task_repo:
        command.extend(["--task-repo", str(Path(args.task_repo).expanduser().resolve())])
    if args.instance:
        for instance_id in args.instance:
            command.extend(["-i", instance_id])
    result = run_command(command, cwd=predictions.parent, timeout=None, capture=False)
    if args.task_repo and not args.keep_images:
        cleanup_evaluation_images(predictions, run_id)
    return result.returncode


def resolve_swebench(configured: str, venv: str | None) -> str:
    # 直接调用虚拟环境中的入口，避免依赖父 shell 的 activate 状态
    if not venv:
        return configured
    root = Path(venv).expanduser().resolve()
    candidates = [root / "bin" / "swebench", root / "Scripts" / "swebench.exe", root / "Scripts" / "swebench"]
    for candidate in candidates:
        if candidate.is_file() and os.access(candidate, os.X_OK):
            return str(candidate)
    raise BenchError(f"SWE-bench CLI not found in virtual environment: {root}")


def resolve_venv_python(venv: str) -> Path:
    # 解析虚拟环境 Python，确保 datasets 与 swebench 使用同一环境
    root = Path(venv).expanduser().resolve()
    candidates = [root / "bin" / "python", root / "Scripts" / "python.exe", root / "Scripts" / "python"]
    for candidate in candidates:
        if candidate.is_file() and os.access(candidate, os.X_OK):
            return candidate
    raise BenchError(f"Python executable not found in virtual environment: {root}")


def cleanup_evaluation_images(predictions: Path, run_id: str) -> None:
    # 删除本次本地构建的 task image 和构建日志，保留 Docker 基础层和共享缓存
    report = predictions.parent / "logs" / "evaluation" / run_id / "results.json"
    if not report.is_file():
        print(f"swebench: evaluation report not found; images were not removed: {report}", file=sys.stderr)
        return
    try:
        images = json.loads(report.read_text(encoding="utf-8")).get("unremoved_images", [])
    except (OSError, json.JSONDecodeError) as exc:
        print(f"swebench: could not read image list; images were not removed: {exc}", file=sys.stderr)
        return
    for image in images:
        result = run_command(["docker", "image", "rm", "--force", image])
        if result.returncode != 0:
            detail = (result.stderr or result.stdout or "").strip()
            print(f"swebench: failed to remove image {image}: {detail}", file=sys.stderr)
    shutil.rmtree(predictions.parent / "logs" / "image_builder", ignore_errors=True)


def reexec_in_venv(venv: str | None, argv: list[str]) -> None:
    # 传入 venv 时重启当前脚本，使数据集加载也使用 venv 的 Python
    if not venv:
        return
    root = Path(venv).expanduser().resolve()
    python = resolve_venv_python(venv)
    if Path(sys.prefix).resolve() == root:
        return
    env = os.environ.copy()
    env["VIRTUAL_ENV"] = str(root)
    env["PATH"] = str(python.parent) + os.pathsep + env.get("PATH", "")
    os.execve(str(python), [str(python), str(Path(__file__).resolve()), *argv], env)


def build_parser() -> argparse.ArgumentParser:
    # 三个入口分别覆盖生成预测、官方评分和两步串联
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)

    def add_infer_options(command: argparse.ArgumentParser) -> None:
        command.add_argument("--instance", action="append", default=[], help="SWE-bench instance ID; repeat to select multiple instances")
        command.add_argument("--limit", type=int, help="process only the first N selected instances")
        command.add_argument("--provider", help="override the configured Rivo provider; requires --model")
        command.add_argument("--model", help="override the configured Rivo model")
        command.add_argument("--effort", choices=["off", "minimal", "low", "medium", "high", "xhigh", "max"], help="override Rivo effort level")
        command.add_argument("--rivo", help="path to an existing Rivo executable; otherwise build one from this checkout")
        command.add_argument("--repo-cache", default=".swebench/repos", help="directory for shared bare repository caches")
        command.add_argument(
            "--output-dir",
            default=f".swebench/runs/{time.strftime('%Y%m%d-%H%M%S')}-{time.time_ns() % 1_000_000:06d}",
            help="directory for predictions, costs, instance artifacts, and evaluation logs",
        )
        command.add_argument("--timeout", type=int, default=DEFAULT_TIMEOUT, help="per-instance Rivo timeout in seconds")
        command.add_argument("--keep-checkouts", action="store_true", help="keep temporary source checkouts in the output directory for debugging")
        command.add_argument("--swebench-venv", "--venv", dest="swebench_venv", help="Python virtual environment containing datasets and the SWE-bench CLI")

    infer_parser = subparsers.add_parser("infer", help="run Rivo and generate predictions")
    add_infer_options(infer_parser)

    eval_parser = subparsers.add_parser("eval", help="grade an existing prediction file")
    eval_parser.add_argument("--predictions", required=True, help="path to predictions.jsonl generated by infer")
    eval_parser.add_argument("--run-id", help="official SWE-bench evaluation run ID")
    eval_parser.add_argument("--workers", type=int, default=1, help="number of Docker grading workers")
    eval_parser.add_argument("--task-repo", help="local swe-bench-tasks checkout for building task images")
    eval_parser.add_argument("--instance", action="append", default=[], help="limit grading to this instance ID; repeatable")
    eval_parser.add_argument("--swebench", default="swebench", help="SWE-bench CLI executable")
    eval_parser.add_argument("--swebench-venv", "--venv", dest="swebench_venv", help="Python virtual environment containing datasets and the SWE-bench CLI")
    eval_parser.add_argument("--keep-images", action="store_true", help="keep locally built task images after evaluation")
    eval_parser.add_argument("--reuse-images", action="store_true", help="reuse existing local task images without rebuilding")

    all_parser = subparsers.add_parser("all", help="generate predictions, then run official evaluation")
    add_infer_options(all_parser)
    all_parser.add_argument("--run-id", help="official SWE-bench evaluation run ID")
    all_parser.add_argument("--workers", type=int, default=1, help="number of Docker grading workers")
    all_parser.add_argument("--task-repo", help="local swe-bench-tasks checkout for building task images")
    all_parser.add_argument("--swebench", default="swebench", help="SWE-bench CLI executable")
    all_parser.add_argument("--keep-images", action="store_true", help="keep locally built task images after evaluation")
    all_parser.add_argument("--reuse-images", action="store_true", help="reuse existing local task images without rebuilding")
    return parser


def main(argv: list[str] | None = None) -> int:
    # 根据子命令分派流程，并把预期错误转换为稳定的非零退出码
    args = build_parser().parse_args(argv)
    try:
        reexec_in_venv(getattr(args, "swebench_venv", None), list(sys.argv[1:] if argv is None else argv))
        root = Path(__file__).resolve().parents[2]
        if args.command == "infer":
            infer(args, root)
            return 0
        if args.command == "eval":
            return eval_predictions(args)
        predictions = infer(args, root)
        eval_args = argparse.Namespace(
            predictions=str(predictions),
            run_id=args.run_id,
            workers=args.workers,
            task_repo=args.task_repo,
            instance=args.instance,
            swebench=args.swebench,
            swebench_venv=args.swebench_venv,
            keep_images=args.keep_images,
            reuse_images=args.reuse_images,
        )
        return eval_predictions(eval_args)
    except BenchError as exc:
        print(f"swebench: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
