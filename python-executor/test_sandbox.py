"""Security tests for the Python executor sandbox.

Run with: python -m pytest -q test_sandbox.py
"""

import os

import pytest
import requests

BASE_URL = os.environ.get(
    "PYTHON_EXECUTOR_BASE_URL", "http://localhost:8081"
)
PROXY_TOKEN = os.environ.get("PROXY_TOKEN", "test-token")


def _executor_available():
    try:
        resp = requests.get(f"{BASE_URL}/health", timeout=2)
        return resp.status_code == 200
    except requests.RequestException:
        return False


pytestmark = pytest.mark.skipif(
    not _executor_available(),
    reason="python executor is not running",
)


def _headers():
    return {"X-Proxy-Token": PROXY_TOKEN}


def _execute(code, timeout=5):
    return requests.post(
        f"{BASE_URL}/execute",
        json={"code": code, "timeout": timeout},
        headers=_headers(),
    )


def test_os_import_blocked():
    resp = _execute("import os\nprint(os.getcwd())")
    data = resp.json()
    assert not data["success"]
    assert "not allowed" in data["error"].lower()


def test_subprocess_import_blocked():
    resp = _execute("import subprocess\nsubprocess.run(['id'])")
    data = resp.json()
    assert not data["success"]


def test_importlib_import_blocked():
    resp = _execute("import importlib\nimportlib.import_module('os')")
    data = resp.json()
    assert not data["success"]


def test_socket_import_blocked():
    resp = _execute("import socket\nsocket.socket()")
    data = resp.json()
    assert not data["success"]


def test_ctypes_import_blocked():
    resp = _execute("import ctypes\nctypes.CDLL('libc.so.6')")
    data = resp.json()
    assert not data["success"]


def test_dunder_class_access_blocked():
    resp = _execute("x = ().__class__")
    data = resp.json()
    assert not data["success"]
    assert "not allowed" in data["error"].lower()


def test_dunder_subclasses_blocked():
    resp = _execute(
        "x = ().__class__.__bases__[0].__subclasses__()"
    )
    data = resp.json()
    assert not data["success"]


def test_dunder_globals_blocked():
    resp = _execute("x = (1).__class__.__globals__")
    data = resp.json()
    assert not data["success"]


def test_eval_blocked():
    resp = _execute("eval('__import__(\"os\")')")
    data = resp.json()
    assert not data["success"]


def test_exec_blocked():
    resp = _execute("exec('import os')")
    data = resp.json()
    assert not data["success"]


def test_type_blocked():
    resp = _execute("type('X', (object,), {})")
    data = resp.json()
    assert not data["success"]


def test_getattr_blocked():
    resp = _execute("getattr({}, '__class__')")
    data = resp.json()
    assert not data["success"]


def test_file_access_outside_workspace_blocked():
    resp = _execute(
        "with open('/etc/passwd', 'r') as f:\n"
        "    print(f.read()[:50])"
    )
    data = resp.json()
    assert not data["success"]


def test_happy_path():
    resp = _execute("print('hello world')")
    data = resp.json()
    assert data["success"]
    assert "hello world" in data["stdout"]


def test_pandas_available():
    resp = _execute(
        "import pandas as pd\n"
        "df = pd.DataFrame({'a': [1,2,3]})\n"
        "df['b'] = df['a'] * 2\n"
        "print(df.to_string())"
    )
    data = resp.json()
    assert data["success"], f"pandas should work: {data.get('error', '')}"


def test_proxy_token_required():
    resp = requests.post(
        f"{BASE_URL}/execute",
        json={"code": "print('hello')", "timeout": 5},
    )
    assert resp.status_code in (403, 503)


def test_invalid_proxy_token():
    resp = requests.post(
        f"{BASE_URL}/execute",
        json={"code": "print('hello')", "timeout": 5},
        headers={"X-Proxy-Token": "wrong-token"},
    )
    assert resp.status_code == 403


def test_session_workspace_isolation():
    resp = requests.post(
        f"{BASE_URL}/execute",
        json={
            "code": "with open('out.txt', 'w') as f: f.write('isolated')",
            "timeout": 5,
            "session_id": "sess_test123",
            "workspace_id": "ws_test123",
        },
        headers=_headers(),
    )
    data = resp.json()
    assert data["success"]
    assert len(data["files"]) == 1


if __name__ == "__main__":
    for name, fn in sorted(globals().items()):
        if name.startswith("test_"):
            try:
                fn()
                print(f"PASS: {name}")
            except Exception as e:
                print(f"FAIL: {name}: {e}")
