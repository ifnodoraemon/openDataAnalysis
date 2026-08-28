"""
Python Executor MCP Server
提供安全的 Python 代码执行环境，作为数据分析智能体的通用计算扩展。
通过 HTTP API 接收代码，在受限环境中执行，返回 stdout/stderr/文件输出。

安全策略（纵深防御）：
1. 令牌认证：/execute 和 /files 端点均需 PROXY_TOKEN 认证
2. 进程级隔离：每个请求在独立子进程中执行
3. AST 预检：在执行前静态拒绝危险语法（importlib/ctypes/dunder属性链等）
4. Import 白名单：仅允许预声明安全的模块
5. 内建函数拦截：移除 eval/exec/compile/globals/locals/type/breakpoint 等
6. 属性访问拦截：阻止 __class__/__bases__/__subclasses__/__globals__ 等逃逸链
7. 资源限制：超时上限、进程数限制、内存限制、SIGKILL 后备
8. 文件系统隔离：open() 限制在工作目录内，路径使用 is_relative_to 校验
"""

import hmac
import base64
import io
import logging
import os
import queue
import re
import time
import traceback
import contextlib
import resource
import shutil
import uuid
import multiprocessing
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Any, Literal

from sandbox import (
    SecurityViolation,
    check_code,
    create_sandboxed_builtins,
    sanitize_error,
)

for _thread_env in (
    "OMP_NUM_THREADS",
    "OPENBLAS_NUM_THREADS",
    "MKL_NUM_THREADS",
    "NUMEXPR_NUM_THREADS",
):
    os.environ.setdefault(_thread_env, "1")

from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import FileResponse
from pydantic import BaseModel, ConfigDict, Field, field_validator

MAX_CONCURRENT_EXECUTIONS = 4

logger = logging.getLogger("python-executor")
logging.basicConfig(
    level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s"
)

import asyncio

_concurrency_semaphore = asyncio.Semaphore(MAX_CONCURRENT_EXECUTIONS)

WORK_DIR = Path(
    os.environ.get("WORK_DIR") or (Path(__file__).resolve().parent / "workspace")
).resolve()
MAX_TIMEOUT = int(os.environ.get("PYTHON_MAX_TIMEOUT_SECONDS", "120"))
MAX_CODE_SIZE = int(os.environ.get("MAX_CODE_SIZE", "65536"))
MEMORY_LIMIT_MB = int(os.environ.get("MEMORY_LIMIT_MB", "512"))
FILE_SIZE_LIMIT_MB = int(os.environ.get("FILE_SIZE_LIMIT_MB", "50"))
PROCESS_LIMIT = int(os.environ.get("PROCESS_LIMIT", "64"))
STDOUT_LIMIT = int(os.environ.get("STDOUT_LIMIT", "10000"))
STDERR_LIMIT = int(os.environ.get("STDERR_LIMIT", "5000"))

if MAX_TIMEOUT < 5:
    raise RuntimeError("PYTHON_MAX_TIMEOUT_SECONDS must be at least 5")
for _name, _value in {
    "MAX_CODE_SIZE": MAX_CODE_SIZE,
    "MEMORY_LIMIT_MB": MEMORY_LIMIT_MB,
    "FILE_SIZE_LIMIT_MB": FILE_SIZE_LIMIT_MB,
    "STDOUT_LIMIT": STDOUT_LIMIT,
    "STDERR_LIMIT": STDERR_LIMIT,
}.items():
    if _value <= 0:
        raise RuntimeError(f"{_name} must be greater than zero")
if PROCESS_LIMIT < 0:
    raise RuntimeError("PROCESS_LIMIT must be zero or greater")



def _is_path_within(child: Path, parent: Path) -> bool:
    return child.resolve().is_relative_to(parent.resolve())


def init_namespace(ns: dict, work_dir: str) -> None:
    imports = """
import pandas as pd
import numpy as np
import json
import csv
import math
import statistics
import re
from datetime import datetime, timedelta
from collections import Counter, defaultdict
from numbers import Number
from decimal import Decimal
from fractions import Fraction
import itertools
import functools
import copy
import enum
import string
import textwrap
import hashlib
import base64
import random
import warnings
import contextlib

import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
plt.rcParams['font.sans-serif'] = ['SimHei', 'DejaVu Sans']
plt.rcParams['axes.unicode_minus'] = False
"""
    exec(imports, ns)
    ns["WORK_DIR"] = work_dir


class InputFile(BaseModel):
    model_config = ConfigDict(extra="forbid")

    filename: str
    content_base64: str

    @field_validator("filename")
    @classmethod
    def validate_filename(cls, value: str) -> str:
        if not re.fullmatch(r"[a-zA-Z0-9_.-]+", value or ""):
            raise ValueError("input filename must be a plain safe filename")
        return value


class ExecuteRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")

    code: str
    timeout: int
    session_id: str
    workspace_id: str
    inputs: list[InputFile] = Field(default_factory=list)

    @field_validator("timeout")
    @classmethod
    def validate_timeout(cls, v: int) -> int:
        if v < 5 or v > MAX_TIMEOUT:
            raise ValueError(f"timeout must be between 5 and {MAX_TIMEOUT} seconds")
        return v

    @field_validator("code")
    @classmethod
    def validate_code_size(cls, v: str) -> str:
        if not v:
            raise ValueError("code must not be empty")
        if len(v.encode("utf-8")) > MAX_CODE_SIZE:
            raise ValueError(f"Code exceeds maximum size of {MAX_CODE_SIZE} bytes")
        return v

    @field_validator("session_id", "workspace_id")
    @classmethod
    def validate_identity(cls, value: str) -> str:
        if not re.fullmatch(r"[a-zA-Z0-9_-]{1,128}", value or ""):
            raise ValueError("execution identity must be an exact safe identifier")
        return value


class ToolExecuteRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")

    tool_name: Literal["code_run_python"]
    args: ExecuteRequest


class ExecuteResponse(BaseModel):
    success: bool
    stdout: str
    stderr: str
    error: str | None = None
    files: list[str] = Field(default_factory=list)
    duration_ms: int = 0
    truncated: bool = False


def _verify_proxy_token(request: Request) -> None:
    expected_token = os.environ.get("PROXY_TOKEN")
    if not expected_token:
        raise HTTPException(503, "PROXY_TOKEN not configured; access disabled")
    token = request.headers.get("X-Proxy-Token", "")
    if not hmac.compare_digest(token, expected_token):
        raise HTTPException(403, "Forbidden: Missing or invalid proxy token")


@asynccontextmanager
async def lifespan(application):
    try:
        multiprocessing.set_start_method("spawn", force=True)
    except RuntimeError as exc:
        logger.warning("multiprocessing start method was already initialized: %s", exc)
    WORK_DIR.mkdir(parents=True, exist_ok=True)
    _cleanup_old_files()
    yield


app = FastAPI(title="Python Executor MCP", version="2.1.0", lifespan=lifespan)


@app.middleware("http")
async def quiet_health_logs(request: Request, call_next):
    start = time.time()
    response = await call_next(request)
    if request.url.path not in {"/health", "/tools"}:
        logger.info(
            "http method=%s path=%s status=%s duration_ms=%d",
            request.method,
            request.url.path,
            response.status_code,
            int((time.time() - start) * 1000),
        )
    return response


@app.get("/health")
def health() -> dict:
    return {"status": "ok", "tools": ["code_run_python"]}


@app.get("/tools")
def list_tools() -> dict:
    return {
        "tools": [
            {
                "name": "code_run_python",
                "description": (
                    "Execute Python code in a sandboxed environment. Pre-installed: pandas, numpy, matplotlib, scipy. "
                    "Can perform data processing, statistical analysis, ML, and charting. "
                    "Generated chart files are saved to the workspace directory."
                ),
                "parameters": {
                    "type": "object",
                    "additionalProperties": False,
                    "properties": {
                        "code": {
                            "type": "string",
                            "description": "Python code to execute",
                        },
                        "timeout": {
                            "type": "integer",
                            "description": f"Explicit timeout in seconds, min 5, max {MAX_TIMEOUT}",
                            "minimum": 5,
                            "maximum": MAX_TIMEOUT,
                        },
                        "session_id": {
                            "type": "string",
                            "description": "Exact runtime session identifier",
                            "pattern": "^[a-zA-Z0-9_-]{1,128}$",
                        },
                        "workspace_id": {
                            "type": "string",
                            "description": "Exact runtime workspace identifier",
                            "pattern": "^[a-zA-Z0-9_-]{1,128}$",
                        },
                    },
                    "required": ["code", "timeout", "session_id", "workspace_id"],
                },
            }
        ]
    }


@app.post("/execute-tool")
async def execute_tool(request: Request) -> dict:
    """MCP-compatible tool execution endpoint.

    Accepts a JSON body with `tool_name` and `args` fields,
    dispatches to the appropriate internal handler.
    This allows the python-executor to be used as an MCP tool server.
    """
    _verify_proxy_token(request)

    body = ToolExecuteRequest.model_validate(await request.json())
    req = body.args
    trace_id = request.headers.get("X-Request-ID", "-")
    async with _concurrency_semaphore:
        return await asyncio.get_event_loop().run_in_executor(None, _execute_sync, req, trace_id)


def _apply_resource_limits() -> None:
    mem = MEMORY_LIMIT_MB * 1024 * 1024
    fsize = FILE_SIZE_LIMIT_MB * 1024 * 1024
    resource.setrlimit(resource.RLIMIT_AS, (mem, mem))
    if PROCESS_LIMIT > 0:
        nproc = _current_user_task_count() + PROCESS_LIMIT
        _, hard = resource.getrlimit(resource.RLIMIT_NPROC)
        if hard != resource.RLIM_INFINITY:
            nproc = min(nproc, hard)
        resource.setrlimit(resource.RLIMIT_NPROC, (nproc, nproc))
    resource.setrlimit(resource.RLIMIT_FSIZE, (fsize, fsize))


def _current_user_task_count() -> int:
    proc = Path("/proc")
    if not proc.exists():
        return 0
    uid = os.getuid()
    count = 0
    for status_path in proc.glob("[0-9]*/status"):
        try:
            lines = status_path.read_text(errors="ignore").splitlines()
        except OSError:
            continue
        owner_uid = None
        for line in lines:
            if line.startswith("Uid:"):
                parts = line.split()
                if len(parts) >= 2:
                    owner_uid = int(parts[1])
                break
        if owner_uid != uid:
            continue
        task_dir = status_path.parent / "task"
        try:
            count += sum(1 for _ in task_dir.iterdir())
        except OSError:
            count += 1
    return count


CRASH_RESULT: dict[str, Any] = {
    "success": False,
    "stdout": "",
    "stderr": "",
    "error": "Execution failed to return a result (possible crash or out of memory).",
}


def run_in_process(code: str, req_dir_path: str, q: multiprocessing.Queue) -> None:
    req_dir = Path(req_dir_path)
    os.chdir(req_dir)

    try:
        _apply_resource_limits()
    except (ValueError, OSError) as exc:
        q.put({
            "success": False,
            "stdout": "",
            "stderr": "",
            "error": f"Failed to apply execution resource limits: {exc}",
        })
        return

    try:
        check_code(code)
    except SecurityViolation as e:
        q.put({
            "success": False,
            "stdout": "",
            "stderr": "",
            "error": f"Security violation: {e}",
        })
        return

    local_ns: dict = {}
    try:
        init_namespace(local_ns, str(req_dir))
    except Exception as exc:
        q.put({
            "success": False,
            "stdout": "",
            "stderr": "",
            "error": f"Failed to initialize analysis libraries: {exc}",
        })
        return
    local_ns["__builtins__"] = create_sandboxed_builtins(req_dir)

    stdout_buf = io.StringIO()
    stderr_buf = io.StringIO()
    success = True
    error = None

    try:
        with (
            contextlib.redirect_stdout(stdout_buf),
            contextlib.redirect_stderr(stderr_buf),
        ):
            exec(code, local_ns)
    except Exception as e:
        success = False
        error = sanitize_error(
            f"{type(e).__name__}: {e}\n{traceback.format_exc()}"
        )

    try:
        import matplotlib.pyplot as plt

        plt.close("all")
    except Exception as exc:
        logger.warning("failed to close matplotlib figures: %s", exc)

    q.put(
        {
            "success": success,
            "stdout": stdout_buf.getvalue(),
            "stderr": stderr_buf.getvalue(),
            "error": error,
        }
    )


def _collect_output_files(req_dir: Path, input_names: set[str]) -> list[str]:
    if not req_dir.exists():
        return []
    new_files = []
    for f in req_dir.rglob("*"):
        if f.is_file():
            relative_name = str(f.relative_to(req_dir))
            if relative_name in input_names:
                f.unlink()
                continue
            if f.is_symlink() or not _is_path_within(f, req_dir):
                raise RuntimeError(f"output path escapes request directory: {relative_name}")
            new_files.append(str(f.relative_to(WORK_DIR)))
    if not new_files:
        cleanup_error = _remove_request_dir(req_dir)
        if cleanup_error:
            raise RuntimeError(f"failed to clean empty request directory: {cleanup_error}")
    return new_files


def _remove_request_dir(req_dir: Path) -> str | None:
    try:
        shutil.rmtree(req_dir)
    except FileNotFoundError:
        return None
    except OSError as exc:
        logger.error("failed to remove request directory %s: %s", req_dir, exc)
        return str(exc)
    return None


@app.post("/execute", response_model=ExecuteResponse)
async def execute_code(req: ExecuteRequest, request: Request):
    _verify_proxy_token(request)

    if _concurrency_semaphore.locked():
        raise HTTPException(429, "Too many concurrent executions. Please retry later.")

    trace_id = request.headers.get("X-Request-ID", "-")
    async with _concurrency_semaphore:
        return await asyncio.get_event_loop().run_in_executor(None, _execute_sync, req, trace_id)


def _execute_sync(req: ExecuteRequest, trace_id: str) -> ExecuteResponse:
    start = time.time()

    session_dir = WORK_DIR / req.workspace_id / req.session_id
    session_dir.mkdir(parents=True, exist_ok=True)

    req_id = f"req_{uuid.uuid4().hex[:8]}"
    req_dir = session_dir / req_id
    req_dir.mkdir(parents=True, exist_ok=True)

    for input_file in req.inputs:
        try:
            payload = base64.b64decode(input_file.content_base64, validate=True)
        except Exception as exc:
            cleanup_error = _remove_request_dir(req_dir)
            detail = f"Invalid input payload: {exc}"
            if cleanup_error:
                detail += f"; cleanup failed: {cleanup_error}"
            return ExecuteResponse(success=False, stdout="", stderr="", error=detail)
        if len(payload) > FILE_SIZE_LIMIT_MB * 1024 * 1024:
            cleanup_error = _remove_request_dir(req_dir)
            detail = "Input payload exceeds the configured file-size limit"
            if cleanup_error:
                detail += f"; cleanup failed: {cleanup_error}"
            return ExecuteResponse(success=False, stdout="", stderr="", error=detail)
        (req_dir / input_file.filename).write_bytes(payload)

    q: multiprocessing.Queue = multiprocessing.Queue()
    p = multiprocessing.Process(target=run_in_process, args=(req.code, str(req_dir), q))
    p.start()
    p.join(req.timeout)

    if p.is_alive():
        p.terminate()
        p.join(5)
        if p.is_alive():
            p.kill()
            p.join(5)
        cleanup_error = _remove_request_dir(req_dir)
        detail = f"Execution timed out after {req.timeout} seconds."
        if cleanup_error:
            detail += f" Cleanup failed: {cleanup_error}"
        return ExecuteResponse(
            success=False,
            stdout="",
            stderr="",
            error=detail,
            files=[],
            duration_ms=int((time.time() - start) * 1000),
        )

    try:
        result = q.get(timeout=1)
    except queue.Empty:
        result = CRASH_RESULT

    new_files = _collect_output_files(req_dir, {item.filename for item in req.inputs})
    duration_ms = int((time.time() - start) * 1000)
    raw_stdout = result["stdout"]
    raw_stderr = result["stderr"]
    truncated = len(raw_stdout) > STDOUT_LIMIT or len(raw_stderr) > STDERR_LIMIT

    logger.info(
        "execute trace_id=%s req_id=%s success=%s duration_ms=%d files=%d stdout_chars=%d stderr_chars=%d",
        trace_id,
        req_id,
        result["success"],
        duration_ms,
        len(new_files),
        len(raw_stdout),
        len(raw_stderr),
    )

    return ExecuteResponse(
        success=result["success"],
        stdout=raw_stdout[:STDOUT_LIMIT],
        stderr=raw_stderr[:STDERR_LIMIT],
        error=result["error"],
        files=new_files,
        duration_ms=duration_ms,
        truncated=truncated,
    )


@app.get("/files/{file_path:path}")
def get_file(file_path: str, request: Request):
    _verify_proxy_token(request)

    if not re.fullmatch(r"[a-zA-Z0-9_-]+/[a-zA-Z0-9_-]+/req_[a-f0-9]{8}/[a-zA-Z0-9_./-]+", file_path):
        raise HTTPException(400, "Invalid filename format")
    if any(part in {"", ".", ".."} for part in Path(file_path).parts):
        raise HTTPException(400, "Invalid filename path segments")

    filepath = (WORK_DIR / file_path).resolve()
    if not _is_path_within(filepath, WORK_DIR):
        raise HTTPException(400, "Invalid file path")
    if not filepath.exists():
        raise HTTPException(404, "File not found")
    return FileResponse(filepath)


def _cleanup_old_files(max_age_hours: int = 24) -> None:
    cutoff = time.time() - max_age_hours * 3600
    for f in WORK_DIR.rglob("*"):
        if f.is_file() and f.stat().st_mtime < cutoff:
            try:
                f.unlink()
            except OSError as exc:
                logger.error("failed to remove expired executor file %s: %s", f, exc)
    directories = sorted(
        (path for path in WORK_DIR.rglob("*") if path.is_dir()),
        key=lambda path: len(path.parts),
        reverse=True,
    )
    for directory in directories:
        try:
            directory.rmdir()
        except OSError:
            continue


if __name__ == "__main__":
    import uvicorn

    port = int(os.environ.get("PORT", "8081"))
    if port < 1 or port > 65535:
        raise RuntimeError("PORT must be between 1 and 65535")
    uvicorn.run(app, host="0.0.0.0", port=port, access_log=False)
