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

import ast
import hmac
import io
import logging
import os
import queue
import time
import traceback
import contextlib
import resource
import shutil
import uuid
import multiprocessing
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Any

for _thread_env in (
    "OMP_NUM_THREADS",
    "OPENBLAS_NUM_THREADS",
    "MKL_NUM_THREADS",
    "NUMEXPR_NUM_THREADS",
):
    os.environ.setdefault(_thread_env, "1")

from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import FileResponse
from pydantic import BaseModel, field_validator

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
MAX_TIMEOUT = int(os.environ.get("MAX_TIMEOUT", "120"))
MAX_CODE_SIZE = int(os.environ.get("MAX_CODE_SIZE", "65536"))
MEMORY_LIMIT_MB = int(os.environ.get("MEMORY_LIMIT_MB", "512"))
FILE_SIZE_LIMIT_MB = int(os.environ.get("FILE_SIZE_LIMIT_MB", "50"))
PROCESS_LIMIT = int(os.environ.get("PROCESS_LIMIT", "0"))
STDOUT_LIMIT = int(os.environ.get("STDOUT_LIMIT", "10000"))
STDERR_LIMIT = int(os.environ.get("STDERR_LIMIT", "5000"))

FORBIDDEN_IMPORTS = frozenset(
    {
        "os",
        "sys",
        "subprocess",
        "shutil",
        "socket",
        "urllib",
        "requests",
        "pty",
        "builtins",
        "importlib",
        "ctypes",
        "signal",
        "multiprocessing",
        "threading",
        "pickle",
        "shelve",
        "marshal",
        "code",
        "codeop",
        "compileall",
        "runpy",
        "webbrowser",
        "antigravity",
        "site",
        "pkgutil",
        "xmlrpc",
        "http",
        "ftplib",
        "smtplib",
        "telnetlib",
        "tempfile",
        "glob",
        "io",
        "pathlib",
        "posixpath",
        "ntpath",
        "genericpath",
    }
)

ALLOWED_IMPORTS = frozenset(
    {
        "pandas",
        "numpy",
        "matplotlib",
        "scipy",
        "sklearn",
        "scikit-learn",
        "json",
        "csv",
        "math",
        "statistics",
        "re",
        "datetime",
        "collections",
        "openpyxl",
        "numbers",
        "decimal",
        "fractions",
        "itertools",
        "functools",
        "copy",
        "enum",
        "typing",
        "dataclasses",
        "string",
        "textwrap",
        "difflib",
        "hashlib",
        "base64",
        "random",
        "uuid",
        "pprint",
        "warnings",
        "contextlib",
    }
)

DANGEROUS_ATTR_NAMES = frozenset(
    {
        "__class__",
        "__bases__",
        "__base__",
        "__mro__",
        "__subclasses__",
        "__globals__",
        "__builtins__",
        "__code__",
        "__func__",
        "__self__",
        "__dict__",
        "__closure__",
        "__frame__",
        "__module__",
        "__qualname__",
        "__init__",
        "__new__",
    }
)

REMOVED_BUILTINS = frozenset(
    {
        "eval",
        "exec",
        "compile",
        "type",
        "breakpoint",
        "globals",
        "locals",
        "vars",
        "dir",
        "help",
    }
)

BLOCKED_CALL_NAMES = REMOVED_BUILTINS | {"__import__", "open"}

CRASH_RESULT: dict[str, Any] = {
    "success": False,
    "stdout": "",
    "stderr": "",
    "error": "Execution failed to return a result (possible crash or out of memory).",
}


class ASTSandboxValidator(ast.NodeVisitor):
    """AST 级别的静态安全检查，在执行前拒绝危险代码模式。"""

    def __init__(self) -> None:
        self.errors: list[str] = []

    def _error(self, node: ast.AST, msg: str) -> None:
        self.errors.append(f"Line {getattr(node, 'lineno', '?')}: {msg}")

    def _check_import(self, node: ast.AST, module_name: str | None) -> None:
        if not module_name:
            return
        base = module_name.split(".")[0]
        if base in FORBIDDEN_IMPORTS:
            self._error(node, f"import '{base}' is not allowed")
        elif base not in ALLOWED_IMPORTS:
            self._error(node, f"import '{base}' is not in the allowed list")

    def visit_Import(self, node: ast.Import) -> None:
        for alias in node.names:
            self._check_import(node, alias.name)
        self.generic_visit(node)

    def visit_ImportFrom(self, node: ast.ImportFrom) -> None:
        self._check_import(node, node.module)
        self.generic_visit(node)

    def visit_Attribute(self, node: ast.Attribute) -> None:
        if node.attr in DANGEROUS_ATTR_NAMES:
            self._error(node, f"access to attribute '{node.attr}' is not allowed")
        self.generic_visit(node)

    def visit_Call(self, node: ast.Call) -> None:
        if (
            isinstance(node.func, ast.Attribute)
            and node.func.attr in BLOCKED_CALL_NAMES
        ):
            self._error(
                node, f"calling '{node.func.attr}' is not allowed in this context"
            )
        if isinstance(node.func, ast.Name) and node.func.id in REMOVED_BUILTINS:
            self._error(node, f"calling '{node.func.id}' is not allowed")
        self.generic_visit(node)

    def visit_Subscript(self, node: ast.Subscript) -> None:
        if isinstance(node.slice, ast.Constant) and isinstance(node.slice.value, str):
            if node.slice.value in DANGEROUS_ATTR_NAMES:
                self._error(
                    node, f"subscript access to '{node.slice.value}' is not allowed"
                )
        self.generic_visit(node)

    def visit_Constant(self, node: ast.Constant) -> None:
        if isinstance(node.value, str) and node.value in DANGEROUS_ATTR_NAMES:
            self._error(
                node,
                f"dangerous string constant '{node.value}' is not allowed",
            )
        self.generic_visit(node)


def validate_code_with_ast(code: str) -> list[str]:
    try:
        tree = ast.parse(code)
    except SyntaxError as e:
        return [f"Syntax error: {e}"]
    validator = ASTSandboxValidator()
    validator.visit(tree)
    return validator.errors


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
    try:
        exec(imports, ns)
    except Exception as e:
        logger.warning("Some libraries not available: %s", e)
    ns["WORK_DIR"] = work_dir


class ExecuteRequest(BaseModel):
    code: str
    timeout: int = 30

    @field_validator("timeout")
    @classmethod
    def clamp_timeout(cls, v: int) -> int:
        return max(5, min(v, MAX_TIMEOUT))

    @field_validator("code")
    @classmethod
    def validate_code_size(cls, v: str) -> str:
        if len(v) > MAX_CODE_SIZE:
            raise ValueError(f"Code exceeds maximum size of {MAX_CODE_SIZE} bytes")
        return v


class ExecuteResponse(BaseModel):
    success: bool
    stdout: str
    stderr: str
    error: str | None = None
    files: list[str] = []
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
    except RuntimeError:
        pass
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
                    "properties": {
                        "code": {
                            "type": "string",
                            "description": "Python code to execute",
                        },
                        "timeout": {
                            "type": "integer",
                            "description": f"Timeout in seconds, default 30, max {MAX_TIMEOUT}",
                            "default": 30,
                        },
                    },
                    "required": ["code"],
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

    body = await request.json()
    tool_name = body.get("tool_name", "")
    args = body.get("args", {})

    if tool_name != "code_run_python":
        raise HTTPException(400, f"Unknown tool: {tool_name}")

    code = args.get("code", "")
    timeout = args.get("timeout", 30)

    req = ExecuteRequest(code=code, timeout=timeout)
    return await execute_code(req, request)


def _apply_resource_limits() -> None:
    try:
        mem = MEMORY_LIMIT_MB * 1024 * 1024
        fsize = FILE_SIZE_LIMIT_MB * 1024 * 1024
        resource.setrlimit(resource.RLIMIT_AS, (mem, mem))
        if PROCESS_LIMIT > 0:
            nproc = max(PROCESS_LIMIT, _current_user_task_count() + 32)
            _, hard = resource.getrlimit(resource.RLIMIT_NPROC)
            if hard != resource.RLIM_INFINITY:
                nproc = min(nproc, hard)
            resource.setrlimit(resource.RLIMIT_NPROC, (nproc, nproc))
        resource.setrlimit(resource.RLIMIT_FSIZE, (fsize, fsize))
    except (ValueError, OSError):
        pass


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


def _build_safe_builtins(req_dir: Path) -> dict:
    import builtins

    safe_builtins = builtins.__dict__.copy()
    original_import = builtins.__import__
    original_getattr = builtins.getattr
    original_open = builtins.open

    def safe_import(name, globals=None, locals=None, fromlist=(), level=0):
        base_name = name.split(".")[0]
        if base_name in FORBIDDEN_IMPORTS:
            raise ImportError(
                f"Importing '{base_name}' is not allowed in this sandbox."
            )
        if base_name not in ALLOWED_IMPORTS:
            raise ImportError(
                "Importing is not allowed. Only pre-approved libraries are available."
            )
        return original_import(name, globals, locals, fromlist, level)

    def safe_open(file, mode="r", *args, **kwargs):
        resolved = Path(file).resolve()
        if not _is_path_within(resolved, req_dir):
            raise PermissionError(
                "Access denied: file operations are restricted to the working directory."
            )
        return original_open(resolved, mode, *args, **kwargs)

    def safe_getattr(obj, name, *args):
        if isinstance(name, str) and name in DANGEROUS_ATTR_NAMES:
            raise AttributeError(
                f"Access to attribute '{name}' is not allowed in this sandbox."
            )
        return original_getattr(obj, name, *args)

    safe_builtins["__import__"] = safe_import
    safe_builtins["open"] = safe_open
    safe_builtins["getattr"] = safe_getattr

    for name in REMOVED_BUILTINS:
        safe_builtins.pop(name, None)

    return safe_builtins


def run_in_process(code: str, req_dir_path: str, q: multiprocessing.Queue) -> None:
    req_dir = Path(req_dir_path)
    os.chdir(req_dir)

    _apply_resource_limits()

    local_ns: dict = {}
    init_namespace(local_ns, str(req_dir))
    local_ns["__builtins__"] = _build_safe_builtins(req_dir)

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
        error = f"{type(e).__name__}: {e}\n{traceback.format_exc()}"

    try:
        import matplotlib.pyplot as plt

        plt.close("all")
    except Exception:
        pass

    q.put(
        {
            "success": success,
            "stdout": stdout_buf.getvalue(),
            "stderr": stderr_buf.getvalue(),
            "error": error,
        }
    )


def _collect_output_files(req_dir: Path, req_id: str) -> list[str]:
    if not req_dir.exists():
        return []
    new_files = []
    for f in req_dir.glob("*"):
        if f.is_file():
            dest_name = f"{req_id}_{f.name}"
            shutil.move(str(f), str(WORK_DIR / dest_name))
            new_files.append(dest_name)
    shutil.rmtree(req_dir, ignore_errors=True)
    return new_files


@app.post("/execute", response_model=ExecuteResponse)
async def execute_code(req: ExecuteRequest, request: Request):
    _verify_proxy_token(request)

    if _concurrency_semaphore.locked():
        raise HTTPException(429, "Too many concurrent executions. Please retry later.")

    async with _concurrency_semaphore:
        return await asyncio.get_event_loop().run_in_executor(None, _execute_sync, req, request)


def _execute_sync(req: ExecuteRequest, request: Request) -> ExecuteResponse:
    start = time.time()

    ast_errors = validate_code_with_ast(req.code)
    if ast_errors:
        return ExecuteResponse(
            success=False,
            stdout="",
            stderr="",
            error="Code rejected by security policy:\n" + "\n".join(ast_errors),
            files=[],
            duration_ms=int((time.time() - start) * 1000),
        )

    req_id = f"req_{uuid.uuid4().hex[:8]}"
    req_dir = WORK_DIR / req_id
    req_dir.mkdir(parents=True, exist_ok=True)

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
        shutil.rmtree(req_dir, ignore_errors=True)
        return ExecuteResponse(
            success=False,
            stdout="",
            stderr="",
            error=f"Execution timed out after {req.timeout} seconds.",
            files=[],
            duration_ms=int((time.time() - start) * 1000),
        )

    try:
        result = q.get_nowait()
    except queue.Empty:
        result = CRASH_RESULT

    new_files = _collect_output_files(req_dir, req_id)
    duration_ms = int((time.time() - start) * 1000)
    raw_stdout = result["stdout"]
    raw_stderr = result["stderr"]
    truncated = len(raw_stdout) > STDOUT_LIMIT or len(raw_stderr) > STDERR_LIMIT

    logger.info(
        "execute success=%s duration_ms=%d files=%d stdout_chars=%d stderr_chars=%d",
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


@app.get("/files/{filename}")
def get_file(filename: str, request: Request):
    _verify_proxy_token(request)

    if not re.match(r"^req_[a-f0-9]{8}_", filename):
        raise HTTPException(400, "Invalid filename format")

    filepath = (WORK_DIR / filename).resolve()
    if not _is_path_within(filepath, WORK_DIR):
        raise HTTPException(400, "Invalid file path")
    if not filepath.exists():
        raise HTTPException(404, "File not found")
    return FileResponse(filepath)


def _cleanup_old_files(max_age_hours: int = 24) -> None:
    cutoff = time.time() - max_age_hours * 3600
    for f in WORK_DIR.glob("*"):
        if f.is_file() and f.stat().st_mtime < cutoff:
            try:
                f.unlink()
            except OSError:
                pass


if __name__ == "__main__":
    import uvicorn

    try:
        port = int(os.environ.get("PORT", "8081"))
    except ValueError:
        port = 8081
    uvicorn.run(app, host="0.0.0.0", port=port, access_log=False)
