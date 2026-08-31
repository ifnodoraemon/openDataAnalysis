"""Security tests for the Python executor sandbox.

Run with: python -m pytest -q test_sandbox.py
"""

import os

import pytest
import requests

from sandbox import SecurityViolation, check_code

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


requires_executor = pytest.mark.skipif(
    not _executor_available() and not os.environ.get("REQUIRE_EXECUTOR"),
    reason="python executor is not running",
)


def _headers():
    return {"X-Proxy-Token": PROXY_TOKEN}


def _execute(code, timeout=5):
    return requests.post(
        f"{BASE_URL}/execute",
        json={
            "code": code,
            "timeout": timeout,
            "session_id": "sess_test",
            "workspace_id": "ws_test",
        },
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
        json={"code": "print('hello')", "timeout": 5, "session_id": "sess_test", "workspace_id": "ws_test"},
    )
    assert resp.status_code in (403, 503)


def test_invalid_proxy_token():
    resp = requests.post(
        f"{BASE_URL}/execute",
        json={"code": "print('hello')", "timeout": 5, "session_id": "sess_test", "workspace_id": "ws_test"},
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


def test_attr_os_escape_rejected():
    with pytest.raises(SecurityViolation) as exc_info:
        check_code("matplotlib.os.system('sh')")
    assert "not allowed" in str(exc_info.value)


def test_attr_sys_modules_escape_rejected():
    with pytest.raises(SecurityViolation) as exc_info:
        check_code("pandas.sys.modules['subprocess'].run(['sh'])")
    assert "not allowed" in str(exc_info.value)


def test_attr_importlib_escape_rejected():
    with pytest.raises(SecurityViolation) as exc_info:
        check_code("numpy.importlib.import_module('os')")
    assert "not allowed" in str(exc_info.value)


def test_getattr_dynamic_access_rejected():
    with pytest.raises(SecurityViolation) as exc_info:
        check_code("f = getattr(matplotlib, 'os')\nf.system('sh')")
    assert "not allowed" in str(exc_info.value)


def test_import_from_dangerous_attr_rejected():
    with pytest.raises(SecurityViolation) as exc_info:
        check_code("from pandas import io\nio.common.urlopen('http://example.com')")
    assert "not allowed" in str(exc_info.value)


def test_dotted_import_dangerous_component_rejected():
    with pytest.raises(SecurityViolation) as exc_info:
        check_code("import pandas.io")
    assert "not allowed" in str(exc_info.value)


def test_license_interactive_rejected():
    with pytest.raises(SecurityViolation):
        check_code("license()")


def test_bytearray_rejected():
    with pytest.raises(SecurityViolation):
        check_code("b = bytearray(b'x')")


def test_re_compile_allowed():
    check_code("import re\npat = re.compile(r'[a-z]+')\npat.match('abc')")


def test_dataframe_eval_allowed():
    check_code("df.eval('a > 1')")


def test_scipy_signal_allowed():
    check_code("import scipy.signal\nscipy.signal.find_peaks([1, 2, 3])")


def test_pandas_numpy_ast_allowed():
    check_code(
        "import pandas as pd\n"
        "import numpy as np\n"
        "df = pd.DataFrame({'a': np.array([1, 2, 3])})\n"
        "df['b'] = df['a'] * 2\n"
        "print(df.describe())"
    )


def test_matplotlib_ast_allowed():
    check_code(
        "import matplotlib\n"
        "matplotlib.use('Agg')\n"
        "import matplotlib.pyplot as plt\n"
        "plt.plot([1, 2], [3, 4])\n"
        "plt.savefig('chart.png')\n"
        "plt.close('all')"
    )


@requires_executor
def test_escape_via_matplotlib_os_blocked():
    resp = _execute("matplotlib.os.system('sh')")
    data = resp.json()
    assert not data["success"]
    assert "not allowed" in data["error"].lower()


@requires_executor
def test_escape_via_pandas_sys_blocked():
    resp = _execute("pandas.sys.modules['subprocess'].run(['id'])")
    data = resp.json()
    assert not data["success"]
    assert "not allowed" in data["error"].lower()


@requires_executor
def test_escape_via_numpy_importlib_blocked():
    resp = _execute("numpy.importlib.import_module('os')")
    data = resp.json()
    assert not data["success"]
    assert "not allowed" in data["error"].lower()


@requires_executor
def test_escape_via_getattr_blocked():
    resp = _execute("f = getattr(matplotlib, 'os')\nf.system('sh')")
    data = resp.json()
    assert not data["success"]


@requires_executor
def test_pandas_numpy_matplotlib_usage_allowed():
    resp = _execute(
        "import pandas as pd\n"
        "import numpy as np\n"
        "import matplotlib\n"
        "matplotlib.use('Agg')\n"
        "import matplotlib.pyplot as plt\n"
        "df = pd.DataFrame({'a': np.arange(5)})\n"
        "df['b'] = np.sqrt(df['a'])\n"
        "plt.plot(df['a'], df['b'])\n"
        "print(df['b'].sum())"
    )
    data = resp.json()
    assert data["success"], f"normal usage should work: {data.get('error', '')}"


if __name__ == "__main__":
    for name, fn in sorted(globals().items()):
        if name.startswith("test_"):
            try:
                fn()
                print(f"通过：{name}")
            except Exception as e:
                print(f"失败：{name}：{e}")
