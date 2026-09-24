"""python3 -W error::ResourceWarning -m unittest extension/cc-statusline/test_cc_statusline.py"""

import contextlib
import datetime
import importlib.util
import io
import json
import os
import tempfile
import time
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "cc_statusline.py")

_spec = importlib.util.spec_from_file_location("cc_statusline", SCRIPT)
cs = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(cs)

DAY = "2026-09-24"
NOW = datetime.datetime(2026, 9, 24, 12, 0, tzinfo=datetime.timezone.utc)
STATS = {"grain": "day", "tz": "UTC", "source": "rollup", "daily": [{
    "date": DAY, "inputTokens": 200_000, "outputTokens": 50_000,
    "cacheCreationTokens": 150_000, "cacheReadTokens": 800_000,
    "reasoningTokens": 9_999_999, "costUSDMicro": 0, "costAPIEquivMicro": 14_200_000}]}
LIMITS = {"providers": {
    "codex": {"fetchedAt": 1, "windows": [{"label": "5h", "usedPercent": 9, "resetAt": 1}]},
    "claude": {"fetchedAt": 1, "windows": [
        {"label": "5h", "usedPercent": 63.2, "resetAt": 1},
        {"label": "7d", "usedPercent": 57, "resetAt": 1},
        {"label": "Fable 7d", "usedPercent": 70.6, "resetAt": 1}]}}}
LEFT = "today 1.2M tok · $14.20 api-eq"
RIGHT = "claude 5h 63% · 7d 57% · Fable 7d 71%"


class FakeHub:
    """Stands in for get_json: answers by path, raises for a down path,
    sleeps for a slow one; records every request."""

    def __init__(self, stats=STATS, limits=LIMITS, down=(), slow=()):
        self.docs = {"stats": stats, "limits": limits}
        self.down, self.slow = down, slow
        self.paths = []

    def __call__(self, origin, path):
        self.paths.append((origin, path))
        name = "limits" if path == cs.LIMITS_PATH else "stats"
        if name in self.slow:
            time.sleep(0.3)
        if name in self.down:
            raise ConnectionRefusedError(111, "Connection refused")
        return self.docs[name]


def line(hub, budget=1.0):
    return cs.status_line(get=hub, origin=("127.0.0.1", 8284), zone="UTC", now=NOW,
                          stdin=io.BytesIO(b"{}"), deadline=time.monotonic() + budget)


class LineTests(unittest.TestCase):
    def test_both_parts(self):
        hub = FakeHub()
        self.assertEqual(line(hub), LEFT + " | " + RIGHT)
        self.assertEqual(sorted(p for _, p in hub.paths),
                         ["/api/v1/limits", "/api/v1/stats/daily?from=%s&to=%s&timezone=UTC" % (DAY, DAY)])

    def test_one_part_missing(self):
        self.assertEqual(line(FakeHub(down=("limits",))), LEFT)
        self.assertEqual(line(FakeHub(down=("stats",))), RIGHT)

    def test_no_claude_windows_is_a_missing_part(self):
        for limits in ({"providers": {}}, {"providers": {"claude": {"fetchedAt": 1, "windows": None}}},
                       {"error": {"code": "x"}}):
            with self.subTest(limits=limits):
                self.assertEqual(line(FakeHub(limits=limits)), LEFT)

    def test_hub_down(self):
        self.assertEqual(line(FakeHub(down=("stats", "limits"))), cs.HUB_DOWN)

    def test_over_budget(self):
        t = time.monotonic()
        self.assertEqual(line(FakeHub(slow=("stats", "limits")), budget=0.05), cs.HUB_DOWN)
        self.assertLess(time.monotonic() - t, 0.2)
        self.assertEqual(line(FakeHub(slow=("limits",)), budget=0.05), LEFT)

    def test_zero_day(self):
        self.assertEqual(cs.verified_part({"daily": []}), "today 0 tok · $0.00 api-eq")

    def test_compact_tokens(self):
        for n, want in ((999, "999"), (1_500, "1.5k"), (1_234_567, "1.2M"), (2_050_000_000, "2.0B")):
            self.assertEqual(cs.compact_tokens(n), want)

    def test_hub_origin_is_loopback_only(self):
        self.assertEqual(cs.hub_origin(""), ("127.0.0.1", 8284))
        self.assertEqual(cs.hub_origin("http://localhost:9000"), ("localhost", 9000))
        for bad in ("http://10.0.0.1:8284", "https://127.0.0.1:8284", "http://127.0.0.1:8284/x"):
            self.assertEqual(cs.hub_origin(bad), ("127.0.0.1", 8284))

    def test_run_prints_one_line_and_exits_0(self):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.assertEqual(cs.run(get=FakeHub(), origin=("127.0.0.1", 8284), zone="UTC", now=NOW,
                                    stdin=io.BytesIO(b"{}"), deadline=time.monotonic() + 1), 0)
        self.assertEqual(out.getvalue(), LEFT + " | " + RIGHT + "\n")

    def test_stdin_is_read_to_eof(self):
        payload = b'{"session_id": "abc", "model": {"id": "x"}}'
        r, w = os.pipe()
        try:
            os.write(w, payload)
            os.close(w)
            w = None
            data, n = cs.read_bounded(r, deadline_s=cs.elapsed() + 1)
            self.assertEqual(n, len(payload))
            self.assertEqual(json.loads(data)["session_id"], "abc")
        finally:
            os.close(r)
            if w is not None:
                os.close(w)


class ZoneTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.link = os.path.join(self.tmp.name, "localtime")

    def tearDown(self):
        self.tmp.cleanup()

    def test_zone_from_tz(self):
        os.symlink("/usr/share/zoneinfo/Europe/Istanbul", self.link)
        self.assertEqual(cs.local_zone(env={"TZ": "Asia/Tokyo"}, localtime=self.link), "Asia/Tokyo")

    def test_zone_from_the_localtime_symlink(self):
        for target in ("/usr/share/zoneinfo/Europe/Istanbul", "../usr/share/zoneinfo/Europe/Istanbul"):
            with self.subTest(target=target):
                os.symlink(target, self.link)
                self.assertEqual(cs.local_zone(env={}, localtime=self.link), "Europe/Istanbul")
                self.assertEqual(cs.local_zone(env={"TZ": ""}, localtime=self.link), "Europe/Istanbul")
                os.unlink(self.link)

    def test_fallback_to_utc(self):
        # no TZ and no symlink (missing, or a regular file) -> UTC
        self.assertEqual(cs.local_zone(env={}, localtime=self.link), "UTC")
        with open(self.link, "w"):
            pass
        self.assertEqual(cs.local_zone(env={}, localtime=self.link), "UTC")
        # an unknown zone name -> UTC, with one stderr line
        err = io.StringIO()
        with contextlib.redirect_stderr(err):
            self.assertEqual(cs.today_in("Not/AZone", NOW), ("UTC", DAY))
        self.assertEqual(err.getvalue().count("\n"), 1)
        self.assertIn("Not/AZone", err.getvalue())

    def test_local_date_at_the_utc_boundary(self):
        # 01:00 in Europe/Istanbul (UTC+3) is 22:00 the previous UTC day
        now = datetime.datetime(2026, 9, 23, 22, 0, tzinfo=datetime.timezone.utc)
        hub = FakeHub()
        cs.status_line(get=hub, origin=("127.0.0.1", 8284), zone="Europe/Istanbul", now=now,
                       stdin=io.BytesIO(b"{}"), deadline=time.monotonic() + 1)
        self.assertIn((("127.0.0.1", 8284),
                       "/api/v1/stats/daily?from=2026-09-24&to=2026-09-24&timezone=Europe%2FIstanbul"),
                      hub.paths)


SETTINGS = ('{\n  "model": "opus",\n  "env": {\n    "K": "caf\\u00e9"\n  },\n'
            '  "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "x \\"}{\\""}]}]}\n}\n')


class InstallTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.path = os.path.join(self.tmp.name, "settings.json")
        self.backup = self.path + cs.BACKUP_SUFFIX
        self.script = "/opt/tatitok/extension/cc-statusline/cc_statusline.py"
        self.write(SETTINGS)
        self.quiet = contextlib.ExitStack()
        self.quiet.enter_context(contextlib.redirect_stdout(io.StringIO()))

    def tearDown(self):
        self.quiet.close()
        self.tmp.cleanup()

    def write(self, text):
        with open(self.path, "w", encoding="utf-8") as fh:
            fh.write(text)

    def read(self, path=None):
        with open(path or self.path, encoding="utf-8") as fh:
            return fh.read()

    def install(self):
        return cs.install(settings_path=self.path, script=self.script)

    def uninstall(self):
        return cs.uninstall(settings_path=self.path, script=self.script)

    def test_install_adds_only_the_statusline_key(self):
        os.chmod(self.path, 0o640)
        self.assertEqual(self.install(), 0)
        text = self.read()
        want = {"type": "command", "command": "python3 " + self.script}
        self.assertEqual(json.loads(text), dict(json.loads(SETTINGS), statusLine=want))
        # Every other byte stays: the file is the original with one member spliced in.
        cut = SETTINGS.rindex("}\n}\n") + 1
        self.assertTrue(text.startswith(SETTINGS[:cut]))
        self.assertTrue(text.endswith(SETTINGS[cut:]))
        self.assertIn('\n  "statusLine": {\n    "type": "command",', text)
        self.assertEqual(self.read(self.backup), SETTINGS)
        self.assertEqual(os.stat(self.path).st_mode & 0o777, 0o640)
        self.assertEqual(os.stat(self.backup).st_mode & 0o777, 0o600)

    def test_install_then_uninstall_restores_the_bytes(self):
        for original in (SETTINGS, '{"a":1}', "{}", '{\r\n\t"a": [1, {"b": "}"}]\r\n}\r\n'):
            with self.subTest(original=original):
                self.write(original)
                if os.path.exists(self.backup):
                    os.unlink(self.backup)
                self.assertEqual(self.install(), 0)
                self.assertEqual(json.loads(self.read())["statusLine"]["command"],
                                 "python3 " + self.script)
                self.assertEqual(self.uninstall(), 0)
                with open(self.path, "rb") as fh:
                    self.assertEqual(fh.read(), original.encode())

    def test_install_refuses_a_foreign_statusline(self):
        foreign = SETTINGS.replace('"model": "opus"', '"statusLine": {"type": "command", "command": "~/sl.sh"}')
        self.write(foreign)
        with self.assertRaises(SystemExit) as cm:
            self.install()
        self.assertIn("already has a statusLine", str(cm.exception.code))
        self.assertEqual(self.read(), foreign)
        self.assertFalse(os.path.exists(self.backup))

    def test_install_twice_is_a_no_op(self):
        self.assertEqual(self.install(), 0)
        once = self.read()
        self.assertEqual(self.install(), 0)
        self.assertEqual(self.read(), once)

    def test_install_refuses_an_existing_backup(self):
        self.write(SETTINGS)
        with open(self.backup, "w", encoding="utf-8") as fh:
            fh.write("older backup")
        with self.assertRaises(SystemExit):
            self.install()
        self.assertEqual(self.read(), SETTINGS)
        self.assertEqual(self.read(self.backup), "older backup")

    def test_uninstall_exact_match_only(self):
        for cmd in ("python3 " + self.script + " ", "python3 /other/cc_statusline.py", "~/sl.sh"):
            with self.subTest(cmd=cmd):
                other = SETTINGS.replace('"model": "opus"',
                                         '"statusLine": {"type": "command", "command": %s}' % json.dumps(cmd))
                self.write(other)
                self.assertEqual(self.uninstall(), 1)
                self.assertEqual(self.read(), other)
        self.write(SETTINGS)
        self.assertEqual(self.uninstall(), 0)
        self.assertEqual(self.read(), SETTINGS)

    def test_uninstall_removes_a_first_or_middle_member(self):
        ours = json.dumps({"type": "command", "command": "python3 " + self.script})
        for text, want in (('{"statusLine": %s, "a": 1}' % ours, '{"a": 1}'),
                           ('{\n  "a": 1,\n  "statusLine": %s,\n  "b": 2\n}' % ours, '{\n  "a": 1,\n  "b": 2\n}')):
            with self.subTest(text=text):
                self.write(text)
                self.assertEqual(self.uninstall(), 0)
                self.assertEqual(self.read(), want)


if __name__ == "__main__":
    unittest.main()
