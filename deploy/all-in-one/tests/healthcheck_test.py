#!/usr/bin/env python3
"""Exercise the installed healthcheck's observable failure semantics."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).resolve().parents[1] / "healthcheck.sh"


class HealthcheckTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="lanqin-health-test-")
        self.addCleanup(self.tmp.cleanup)
        self.bin = Path(self.tmp.name)
        self.stub("curl", """#!/bin/sh
is_api=0
write_code=0
for arg in "$@"; do
  case "$arg" in *:8080/*) is_api=1;; esac
  case "$arg" in *http_code*) write_code=1;; esac
done
if [ "$is_api" = 1 ]; then
  code=${TEST_API_STATUS:-200}
  rc=${TEST_API_EXIT:-0}
else
  code=${TEST_PROXY_STATUS:-200}
  rc=${TEST_PROXY_EXIT:-0}
fi
[ "$write_code" = 0 ] || printf '%s' "$code"
exit "$rc"
""")
        self.stub("cat", "#!/bin/sh\nprintf '%s\\n' \"$TEST_LIVE_PID\"\n")
        for cmd in ("doveadm", "rspamadm"):
            self.stub(cmd, "#!/bin/sh\nexit 0\n")
        self.stub("redis-cli", "#!/bin/sh\nprintf '%s\\n' \"${TEST_REDIS_REPLY:-PONG}\"\n")

    def stub(self, name, content):
        p = self.bin / name
        p.write_text(content)
        p.chmod(0o755)

    def check(self, **overrides):
        env = {**os.environ, "PATH": f"{self.bin}:/usr/bin:/bin", "TEST_LIVE_PID": str(os.getpid()), **overrides}
        return subprocess.run(["/bin/sh", str(SCRIPT)], env=env, capture_output=True, text=True, timeout=15)

    def test_healthy_api_and_http_redirect(self):
        self.assertEqual(self.check(TEST_PROXY_STATUS="301").returncode, 0)

    def test_proxy_redirect_must_not_hide_unready_api(self):
        self.assertNotEqual(self.check(TEST_API_STATUS="503", TEST_PROXY_STATUS="301").returncode, 0)

    def test_proxy_redirect_must_not_hide_dead_api(self):
        self.assertNotEqual(self.check(TEST_API_EXIT="7", TEST_PROXY_STATUS="301").returncode, 0)

    def test_api_redirect_is_not_readiness(self):
        self.assertNotEqual(self.check(TEST_API_STATUS="301").returncode, 0)

    def test_dead_proxy_is_unhealthy(self):
        self.assertNotEqual(self.check(TEST_PROXY_EXIT="7").returncode, 0)

    def test_redis_error_is_unhealthy(self):
        self.assertNotEqual(self.check(TEST_REDIS_REPLY="LOADING").returncode, 0)


if __name__ == "__main__":
    unittest.main()
