"""python3 -W error::ResourceWarning -m unittest extension/litellm-refresh/test_refresh.py"""

import contextlib
import datetime as _dt
import hashlib
import importlib.util
import io
import json
import os
import stat
import tempfile
import unittest
import urllib.error

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "tatitok-litellm-refresh.py")

_spec = importlib.util.spec_from_file_location("litellm_refresh", SCRIPT)
lr = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(lr)

NOW = _dt.datetime(2026, 9, 24, 3, 4, 5, tzinfo=_dt.timezone.utc)


def price_body(n=1200, rate=3e-06):
    obj = {"sample_spec": {"input_cost_per_token": 0}}
    for i in range(n - 1):
        obj["model-%04d" % i] = {"input_cost_per_token": rate, "output_cost_per_token": rate * 5}
    return json.dumps(obj).encode()


class FakeResponse(io.BytesIO):
    def __init__(self, body, status=200):
        super().__init__(body)
        self.status = status


class FakeURLOpen:
    """Stands in for urllib.request.urlopen: returns the body, or raises."""

    def __init__(self, body=None, exc=None, status=200):
        self.body, self.exc, self.status = body, exc, status
        self.calls = []

    def __call__(self, req, timeout=None):
        self.calls.append((req.full_url, timeout))
        if self.exc is not None:
            raise self.exc
        return FakeResponse(self.body, self.status)


class QuietTest(unittest.TestCase):
    """Swallows the script's one-line stdout/stderr reports."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.quiet = contextlib.ExitStack()
        self.quiet.enter_context(contextlib.redirect_stdout(io.StringIO()))
        self.quiet.enter_context(contextlib.redirect_stderr(io.StringIO()))

    def tearDown(self):
        self.quiet.close()
        self.tmp.cleanup()


class RefreshTests(QuietTest):
    def setUp(self):
        super().setUp()
        self.cdir = os.path.join(self.tmp.name, "tatitok")

    def path(self, name):
        return os.path.join(self.cdir, name)

    def run_refresh(self, fake):
        return lr.run_refresh(cdir=self.cdir, urlopen=fake, now=NOW)

    def snapshot(self):
        """(name → (bytes, mtime_ns)) for every file in the config dir."""
        out = {}
        for name in sorted(os.listdir(self.cdir)):
            p = self.path(name)
            with open(p, "rb") as fh:
                out[name] = (fh.read(), os.stat(p).st_mtime_ns)
        return out

    def seed(self):
        """A good first refresh, with mtimes pushed into the past so a
        rewrite would be visible."""
        self.assertEqual(self.run_refresh(FakeURLOpen(price_body(rate=1e-06))), 0)
        for name in (lr.LIVE_FILE, lr.META_FILE):
            os.utime(self.path(name), ns=(1_000_000_000, 1_000_000_000))
        return self.snapshot()

    def test_success_writes_both_files(self):
        body = price_body()
        fake = FakeURLOpen(body)
        self.assertEqual(self.run_refresh(fake), 0)
        self.assertEqual(fake.calls, [(lr.SOURCE_URL, lr.FETCH_TIMEOUT_S)])
        with open(self.path(lr.LIVE_FILE), "rb") as fh:
            self.assertEqual(fh.read(), body)
        with open(self.path(lr.META_FILE), encoding="utf-8") as fh:
            meta = json.load(fh)
        self.assertEqual(meta, {
            "fetched_at": "2026-09-24T03:04:05Z",
            "sha256": hashlib.sha256(body).hexdigest(),
            "bytes": len(body),
            "source_url": lr.SOURCE_URL,
        })
        for name in (lr.LIVE_FILE, lr.META_FILE):
            self.assertEqual(stat.S_IMODE(os.stat(self.path(name)).st_mode), 0o600, name)
        self.assertEqual(stat.S_IMODE(os.stat(self.cdir).st_mode), 0o700)
        self.assertEqual(sorted(os.listdir(self.cdir)), [lr.LIVE_FILE, lr.META_FILE])

    def test_source_url_is_the_snapshot_source(self):
        meta_path = os.path.join(HERE, "..", "..", "internal", "pricing", "snapshot_meta.json")
        with open(meta_path, encoding="utf-8") as fh:
            self.assertEqual(lr.SOURCE_URL, json.load(fh)["source_url"])

    def test_existing_dir_is_tightened_to_0700(self):
        os.makedirs(self.cdir, mode=0o775)
        os.chmod(self.cdir, 0o775)
        self.assertEqual(self.run_refresh(FakeURLOpen(price_body())), 0)
        self.assertEqual(stat.S_IMODE(os.stat(self.cdir).st_mode), 0o700)

    def test_unchanged_sha_writes_nothing(self):
        before = self.seed()
        self.assertEqual(self.run_refresh(FakeURLOpen(price_body(rate=1e-06))), 0)
        self.assertEqual(self.snapshot(), before)  # bytes AND mtimes unchanged

    def test_changed_body_replaces_both(self):
        before = self.seed()
        body = price_body(rate=2e-06)
        self.assertEqual(self.run_refresh(FakeURLOpen(body)), 0)
        after = self.snapshot()
        self.assertEqual(after[lr.LIVE_FILE][0], body)
        self.assertNotEqual(after[lr.LIVE_FILE][1], before[lr.LIVE_FILE][1])
        meta = json.loads(after[lr.META_FILE][0])
        self.assertEqual(meta["sha256"], hashlib.sha256(body).hexdigest())

    def test_damaged_copy_with_matching_meta_is_refreshed(self):
        self.seed()
        with open(self.path(lr.LIVE_FILE), "wb") as fh:
            fh.write(b"{}")
        body = price_body(rate=1e-06)
        self.assertEqual(self.run_refresh(FakeURLOpen(body)), 0)
        with open(self.path(lr.LIVE_FILE), "rb") as fh:
            self.assertEqual(fh.read(), body)

    def assert_failure_leaves_files(self, fake, want_stderr):
        before = self.seed()
        err = io.StringIO()
        orig, lr.sys.stderr = lr.sys.stderr, err
        try:
            self.assertEqual(self.run_refresh(fake), 1)
        finally:
            lr.sys.stderr = orig
        self.assertEqual(self.snapshot(), before)  # untouched, no tmp file
        line = err.getvalue()
        self.assertEqual(line.count("\n"), 1, line)
        self.assertIn(want_stderr, line)
        return line

    def test_bad_json_leaves_files(self):
        line = self.assert_failure_leaves_files(FakeURLOpen(b'{"model": {"x": 1}, oops'),
                                                "not valid JSON")
        self.assertNotIn("oops", line)  # content is never printed

    def test_nan_is_bad_json(self):
        body = price_body().replace(b"3e-06", b"NaN", 1)
        self.assert_failure_leaves_files(FakeURLOpen(body), "not valid JSON")

    def test_not_an_object_leaves_files(self):
        self.assert_failure_leaves_files(FakeURLOpen(b"[1, 2, 3]"), "not a JSON object")

    def test_too_few_keys_leaves_files(self):
        self.assert_failure_leaves_files(FakeURLOpen(price_body(n=999)), "999 keys")

    def test_network_error_leaves_files(self):
        exc = urllib.error.URLError("[Errno -2] Name or service not known")
        self.assert_failure_leaves_files(FakeURLOpen(exc=exc), "Name or service not known")

    def test_http_status_leaves_files(self):
        self.assert_failure_leaves_files(FakeURLOpen(price_body(), status=203), "HTTP 203")

    def test_first_run_failure_creates_nothing(self):
        self.assertEqual(lr.run_refresh(cdir=self.cdir, urlopen=FakeURLOpen(b"nope"), now=NOW), 1)
        self.assertFalse(os.path.exists(self.cdir))

    def test_write_failure_removes_tmp(self):
        before = self.seed()
        real_replace = lr.os.replace

        def failing_replace(src, dst):
            raise OSError(28, "No space left on device")

        lr.os.replace = failing_replace
        err = io.StringIO()
        orig, lr.sys.stderr = lr.sys.stderr, err
        try:
            self.assertEqual(self.run_refresh(FakeURLOpen(price_body(rate=2e-06))), 1)
        finally:
            lr.os.replace = real_replace
            lr.sys.stderr = orig
        self.assertEqual(self.snapshot(), before)  # no tmp file left behind
        self.assertIn("No space left", err.getvalue())


class InstallTests(QuietTest):
    def setUp(self):
        super().setUp()
        self.unit_dir = os.path.join(self.tmp.name, "systemd", "user")
        self.script = os.path.join(self.tmp.name, "tatitok-litellm-refresh.py")
        with open(self.script, "w", encoding="utf-8") as fh:
            fh.write("#!/usr/bin/env python3\n")
        os.chmod(self.script, 0o755)
        self.calls = []

    def systemctl(self, *args):
        self.calls.append(args)

    def units(self):
        return lr.unit_paths(self.unit_dir)

    def read(self, path):
        with open(path, encoding="utf-8") as fh:
            return fh.read()

    def install(self):
        return lr.install(unit_dir=self.unit_dir, script=self.script, systemctl=self.systemctl)

    def test_install_writes_units_and_enables_timer(self):
        self.assertEqual(self.install(), 0)
        service, timer = self.units()
        svc = self.read(service)
        self.assertIn(lr.MARKER + "\n", svc)
        self.assertIn("ExecStart=%s\n" % self.script, svc)
        self.assertIn("Type=oneshot\n", svc)
        tmr = self.read(timer)
        self.assertIn(lr.MARKER + "\n", tmr)
        for line in ("OnCalendar=daily", "Persistent=true", "RandomizedDelaySec=1h",
                     "WantedBy=timers.target"):
            self.assertIn(line + "\n", tmr)
        self.assertEqual(self.calls, [("daemon-reload",),
                                      ("enable", "--now", "tatitok-litellm-refresh.timer")])

    def test_install_is_repeatable_over_its_own_units(self):
        self.assertEqual(self.install(), 0)
        self.assertEqual(self.install(), 0)

    def test_install_refuses_foreign_units(self):
        for foreign in self.units():
            with self.subTest(unit=os.path.basename(foreign)):
                os.makedirs(self.unit_dir, exist_ok=True)
                for p in self.units():
                    if os.path.exists(p):
                        os.unlink(p)
                with open(foreign, "w", encoding="utf-8") as fh:
                    fh.write("[Unit]\nDescription=someone else's\n")
                with self.assertRaises(SystemExit) as cm:
                    self.install()
                self.assertIn("not ours", str(cm.exception.code))
                self.assertEqual(self.read(foreign), "[Unit]\nDescription=someone else's\n")
                self.assertEqual([os.path.exists(p) for p in self.units()].count(True), 1)
                self.assertEqual(self.calls, [])

    def test_install_refuses_non_executable_script(self):
        os.chmod(self.script, 0o644)
        with self.assertRaises(SystemExit):
            self.install()
        self.assertFalse(os.path.exists(self.unit_dir))

    def test_uninstall_removes_only_marked_units(self):
        self.assertEqual(self.install(), 0)
        self.calls.clear()
        self.assertEqual(lr.uninstall(unit_dir=self.unit_dir, systemctl=self.systemctl), 0)
        self.assertEqual([os.path.exists(p) for p in self.units()], [False, False])
        self.assertEqual(self.calls, [("disable", "--now", "tatitok-litellm-refresh.timer"),
                                      ("daemon-reload",)])

    def test_uninstall_leaves_foreign_units(self):
        service, timer = self.units()
        os.makedirs(self.unit_dir)
        for p in (service, timer):
            with open(p, "w", encoding="utf-8") as fh:
                fh.write("[Unit]\n")
        self.assertEqual(lr.uninstall(unit_dir=self.unit_dir, systemctl=self.systemctl), 1)
        self.assertEqual([os.path.exists(p) for p in self.units()], [True, True])
        self.assertEqual(self.calls, [])


if __name__ == "__main__":
    unittest.main()
