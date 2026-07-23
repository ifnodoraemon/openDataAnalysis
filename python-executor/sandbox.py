"""
Sandbox security filters for Python code execution.

Defense-in-depth layers:
1. AST pre-check — statically reject dangerous syntax before execution.
2. Import whitelist — only allow pre-declared safe modules.
3. Builtins filtering — remove eval/exec/compile/__import__/type/getattr etc.
4. Restricted open — file access confined to the request working directory.
5. Error sanitization — strip internal paths from tracebacks shown to users.
"""

import ast
import os
import re
from pathlib import Path

ALLOWED_IMPORTS = frozenset({
    "pandas", "numpy", "matplotlib", "scipy", "sklearn",
    "json", "csv", "math", "statistics", "re",
    "datetime", "collections", "numbers", "decimal", "fractions",
    "itertools", "functools", "copy", "enum", "string", "textwrap",
    "hashlib", "base64", "random", "warnings", "contextlib",
    "openpyxl",
})

BLOCKED_DUNDER_ATTRS = frozenset({
    "__class__", "__bases__", "__base__", "__subclasses__",
    "__globals__", "__builtins__", "__import__",
    "__code__", "__module__", "__mro__",
    "__dict__", "__getattribute__", "__getattr__",
    "__setattr__", "__delattr__",
    "__loader__", "__spec__", "__path__",
    "__reduce__", "__reduce_ex__",
})

BLOCKED_BUILTINS = frozenset({
    "eval", "exec", "compile", "__import__", "breakpoint",
    "globals", "locals", "vars",
    "exit", "quit", "help", "input",
    "type",
    "getattr", "setattr", "delattr",
    "memoryview",
})

BLOCKED_CALL_NAMES = frozenset(BLOCKED_BUILTINS | {"open"})


class SecurityViolation(Exception):
    """Raised when user code violates sandbox rules."""
    pass


def check_code(code: str) -> None:
    """AST pre-check: reject dangerous syntax before execution."""
    try:
        tree = ast.parse(code)
    except SyntaxError as e:
        raise SecurityViolation(f"Syntax error: {e.msg} (line {e.lineno})")

    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            for alias in node.names:
                root = alias.name.split(".")[0]
                if root not in ALLOWED_IMPORTS:
                    raise SecurityViolation(
                        f"import of '{alias.name}' is not allowed"
                    )

        elif isinstance(node, ast.ImportFrom):
            module = node.module or ""
            root = module.split(".")[0]
            if root not in ALLOWED_IMPORTS:
                raise SecurityViolation(
                    f"import from '{module}' is not allowed"
                )

        elif isinstance(node, ast.Attribute):
            if node.attr in BLOCKED_DUNDER_ATTRS:
                raise SecurityViolation(
                    f"access to '{node.attr}' is not allowed"
                )

        elif isinstance(node, ast.Call):
            func = node.func
            if isinstance(func, ast.Name) and func.id in BLOCKED_CALL_NAMES:
                raise SecurityViolation(
                    f"call to '{func.id}' is not allowed"
                )

        elif isinstance(node, ast.Name):
            if (
                node.id in BLOCKED_BUILTINS
                and isinstance(node.ctx, ast.Load)
            ):
                raise SecurityViolation(
                    f"use of '{node.id}' is not allowed"
                )


def make_restricted_open(work_dir: Path):
    """Return an open() that confines file access to work_dir."""
    real_open = open

    def restricted_open(
        file,
        mode="r",
        buffering=-1,
        encoding=None,
        errors=None,
        newline=None,
        closefd=True,
        opener=None,
    ):
        if isinstance(file, int):
            raise PermissionError(
                "opening raw file descriptors is not allowed"
            )
        if isinstance(file, (str, bytes, os.PathLike)):
            path = Path(file)
            if not path.is_absolute():
                path = work_dir / path
            resolved = path.resolve()
            if not resolved.is_relative_to(work_dir.resolve()):
                raise PermissionError(
                    f"access to path outside workspace is not allowed: {file}"
                )
        return real_open(
            file,
            mode=mode,
            buffering=buffering,
            encoding=encoding,
            errors=errors,
            newline=newline,
            closefd=closefd,
            opener=opener,
        )

    return restricted_open


def create_sandboxed_builtins(work_dir: Path) -> dict:
    """Create a filtered builtins dict with dangerous functions removed."""
    import builtins
    safe = dict(builtins.__dict__)
    for name in BLOCKED_BUILTINS:
        safe.pop(name, None)
    safe["open"] = make_restricted_open(work_dir)
    return safe


def sanitize_error(error: str) -> str:
    """Strip internal file paths from error messages."""
    error = re.sub(r'File "/[^"]*"', 'File "<internal>"', error)
    return error
