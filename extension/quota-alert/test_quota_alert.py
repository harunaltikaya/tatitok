"""python3 -W error::ResourceWarning -m unittest extension/quota-alert/test_quota_alert.py"""

import contextlib
import datetime as _dt
import importlib.util
import io
import json
import os
import subprocess
import tempfile
import unittest
import unittest.mock

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "quota_alert.py")

_spec = importlib.util.spec_from_file_location("quota_alert", SCRIPT)
qa = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(qa)

RESET_A = 1790211600000
RESET_B = RESET_A + 5 * 3600 * 1000


def providers(**windows):
    """providers(claude=[("5h", 85, RESET_A)]) → the hub's providers object."""
    return {p: {"fetchedAt": 1, "windows": [{"label": l, "usedPercent": u, "resetAt": r}
                                           for l, u, r in ws]}
            for p, ws in windows.items()}


class QuietTest(unittest.TestCase):
    """Captures the script's stdout/stderr lines."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.out, self.err = io.StringIO(), io.StringIO()
        self.quiet = contextlib.ExitStack()
        self.quiet.enter_context(contextlib.redirect_stdout(self.out))
        self.quiet.enter_context(contextlib.redirect_stderr(self.err))

    def tearDown(self):
        self.quiet.close()
        self.tmp.cleanup()


class RunTests(QuietTest):
    def setUp(self):
        super().setUp()
        self.cfg_path = os.path.join(self.tmp.name, "alerts.json")
        self.st_path = os.path.join(self.tmp.name, "quota-alert", "state.json")
        self.write_cfg({"hub_url": "http://127.0.0.1:8284", "default_threshold": 80})
        self.sent = []
        self.fetched = []
        self.snap = {}

    def write_cfg(self, cfg):
        with open(self.cfg_path, "w", encoding="utf-8") as fh:
            json.dump(cfg, fh)

    def fetch(self, hub_url):
        self.fetched.append(hub_url)
        return self.snap

    def send(self, body):
        self.sent.append(body)
        return True

    def run_once(self):
        return qa.run_once(cfg_path=self.cfg_path, st_path=self.st_path,
                           fetch=self.fetch, send=self.send)

    def state(self):
        with open(self.st_path, encoding="utf-8") as fh:
            return json.load(fh)

    def test_threshold_crossing_alerts(self):
        self.snap = providers(claude=[("5h", 79.9, RESET_A), ("7d", 80, RESET_A)])
        self.assertEqual(self.run_once(), 0)
        self.assertEqual(len(self.sent), 1)
        self.assertTrue(self.sent[0].startswith("claude 7d 80% — resets "), self.sent[0])
        self.assertEqual(self.state(), {"claude|7d": RESET_A})

    def test_no_repeat_with_same_reset(self):
        self.snap = providers(codex=[("5h", 90, RESET_A)])
        self.run_once()
        self.snap = providers(codex=[("5h", 97, RESET_A)])
        self.run_once()
        self.assertEqual(len(self.sent), 1)

    def test_millisecond_jitter_is_the_same_reset(self):
        self.snap = providers(claude=[("5h", 90, RESET_A + 613)])
        self.run_once()
        self.snap = providers(claude=[("5h", 91, RESET_A + 629)])
        self.run_once()
        self.assertEqual(len(self.sent), 1)
        self.assertEqual(self.state(), {"claude|5h": RESET_A + 613})

    def test_rearm_on_new_reset(self):
        self.snap = providers(codex=[("5h", 90, RESET_A)])
        self.run_once()
        self.snap = providers(codex=[("5h", 85, RESET_B)])
        self.run_once()
        self.assertEqual(len(self.sent), 2)
        self.assertEqual(self.state(), {"codex|5h": RESET_B})

    def test_nothing_on_the_way_down(self):
        self.snap = providers(codex=[("5h", 90, RESET_A)])
        self.run_once()
        self.snap = providers(codex=[("5h", 10, RESET_B)])
        self.run_once()
        self.assertEqual(len(self.sent), 1)
        self.assertEqual(self.state(), {"codex|5h": RESET_A})

    def test_per_window_override(self):
        self.write_cfg({"default_threshold": 80, "thresholds": {"claude": {"Fable 7d": 70}}})
        self.snap = providers(claude=[("Fable 7d", 71, RESET_A), ("7d", 71, RESET_A)],
                              codex=[("Fable 7d", 71, RESET_A)])
        self.run_once()
        self.assertEqual(len(self.sent), 1)
        self.assertTrue(self.sent[0].startswith("claude Fable 7d 71% — "), self.sent[0])

    def test_body_has_local_reset_time(self):
        self.snap = providers(agy=[("gemini-5h", 99.6, RESET_A)])
        self.run_once()
        hhmm = _dt.datetime.fromtimestamp(RESET_A / 1000).strftime("%H:%M")
        self.assertEqual(self.sent, ["agy gemini-5h 100%% — resets %s" % hhmm])

    def test_null_windows_and_bad_windows_are_skipped(self):
        self.snap = {"codex": {"fetchedAt": 1, "windows": None},
                     "claude": {"fetchedAt": 1, "windows": [{"label": "5h"}, "x",
                                                            {"label": "7d", "usedPercent": 99,
                                                             "resetAt": RESET_A}]}}
        self.assertEqual(self.run_once(), 0)
        self.assertEqual(len(self.sent), 1)

    def test_hub_down_exits_0_with_one_line(self):
        def down(hub_url):
            raise qa.HubError("GET http://127.0.0.1:8284/api/v1/limits: [Errno 111] refused")
        self.assertEqual(qa.run_once(cfg_path=self.cfg_path, st_path=self.st_path,
                                     fetch=down, send=self.send), 0)
        self.assertEqual(self.sent, [])
        self.assertEqual(len(self.err.getvalue().splitlines()), 1)
        self.assertFalse(os.path.exists(self.st_path))

    def test_loopback_only_hub_url(self):
        for bad in ("http://192.168.1.5:8284", "https://127.0.0.1:8284", "http://example.com",
                    "http://127.0.0.1:8284/api", "http://localhost.evil:8284", 8284):
            with self.subTest(hub_url=bad):
                self.write_cfg({"hub_url": bad, "default_threshold": 80})
                self.assertEqual(self.run_once(), 1)
        self.assertEqual(self.fetched, [])
        for good in ("http://127.0.0.1:8284", "http://localhost:9000", "http://127.0.0.1"):
            self.write_cfg({"hub_url": good, "default_threshold": 80})
            self.assertEqual(self.run_once(), 0)
        self.assertEqual(self.fetched, ["http://127.0.0.1:8284", "http://localhost:9000",
                                        "http://127.0.0.1"])

    def test_hub_url_defaults_to_loopback(self):
        self.write_cfg({"default_threshold": 80})
        self.run_once()
        self.assertEqual(self.fetched, [qa.DEFAULT_HUB_URL])

    def test_invalid_config_exits_1(self):
        for cfg in ({}, {"default_threshold": 0}, {"default_threshold": True},
                    {"default_threshold": 80, "treshold": 1},
                    {"default_threshold": 80, "thresholds": {"claude": {"5h": "70"}}}, []):
            with self.subTest(cfg=cfg):
                self.write_cfg(cfg)
                self.assertEqual(self.run_once(), 1)
        os.unlink(self.cfg_path)
        self.assertEqual(self.run_once(), 1)
        self.assertEqual(self.fetched, [])

    def test_damaged_state_is_empty(self):
        os.makedirs(os.path.dirname(self.st_path))
        with open(self.st_path, "w", encoding="utf-8") as fh:
            fh.write("{not json")
        self.snap = providers(codex=[("5h", 90, RESET_A)])
        self.assertEqual(self.run_once(), 0)
        self.assertEqual(self.state(), {"codex|5h": RESET_A})


class NotifyTests(QuietTest):
    def test_notify_send_call(self):
        calls = []

        def run(argv, **kw):
            calls.append((argv, kw))
        self.assertTrue(qa.notify("claude 5h 90% — resets 12:00", run=run))
        self.assertEqual(calls[0][0], ["notify-send", "tatitok", "claude 5h 90% — resets 12:00"])
        self.assertTrue(calls[0][1]["check"])
        self.assertEqual(self.err.getvalue(), "")

    def test_notify_send_missing_or_failing_goes_to_stderr(self):
        def missing(argv, **kw):
            raise FileNotFoundError(2, "No such file or directory", "notify-send")

        def failing(argv, **kw):
            raise subprocess.CalledProcessError(1, argv)
        for run in (missing, failing):
            self.assertFalse(qa.notify("codex 5h 90% — resets 12:00", run=run))
        lines = self.err.getvalue().splitlines()
        self.assertEqual(len(lines), 2)
        self.assertTrue(all(l.endswith("codex 5h 90% — resets 12:00") for l in lines))

    def test_run_with_failing_notify_exits_0_and_records(self):
        cfg = os.path.join(self.tmp.name, "alerts.json")
        st = os.path.join(self.tmp.name, "state.json")
        with open(cfg, "w", encoding="utf-8") as fh:
            json.dump({"default_threshold": 80}, fh)

        def missing(argv, **kw):
            raise FileNotFoundError(2, "No such file or directory", "notify-send")
        rc = qa.run_once(cfg_path=cfg, st_path=st,
                         fetch=lambda url: providers(claude=[("5h", 95, RESET_A)]),
                         send=lambda body: qa.notify(body, run=missing))
        self.assertEqual(rc, 0)
        self.assertIn("claude 5h 95%", self.err.getvalue())


class InstallTests(QuietTest):
    def setUp(self):
        super().setUp()
        self.unit_dir = os.path.join(self.tmp.name, "systemd", "user")
        self.script = os.path.join(self.tmp.name, "quota_alert.py")
        with open(self.script, "w", encoding="utf-8") as fh:
            fh.write("#!/usr/bin/env python3\n")
        os.chmod(self.script, 0o755)
        self.calls = []

    def systemctl(self, *args):
        self.calls.append(args)

    def read(self, path):
        with open(path, encoding="utf-8") as fh:
            return fh.read()

    def install(self):
        return qa.install(unit_dir=self.unit_dir, script=self.script, systemctl=self.systemctl)

    def test_install_writes_units_and_enables_timer(self):
        with unittest.mock.patch.dict(os.environ):
            os.environ.pop("XDG_CONFIG_HOME", None)
            os.environ.pop("XDG_DATA_HOME", None)
            self.assertEqual(self.install(), 0)
        service, timer = qa.unit_paths(self.unit_dir)
        svc = self.read(service)
        for line in (qa.MARKER, "Type=oneshot", "ExecStart=%s" % self.script,
                     "Environment=DBUS_SESSION_BUS_ADDRESS=unix:path=%t/bus"):
            self.assertIn(line + "\n", svc)
        tmr = self.read(timer)
        for line in (qa.MARKER, "OnCalendar=*:0/5", "Persistent=true", "WantedBy=timers.target"):
            self.assertIn(line + "\n", tmr)
        self.assertEqual(self.calls, [("daemon-reload",),
                                      ("enable", "--now", "tatitok-quota-alert.timer")])

    def test_install_refuses_foreign_unit(self):
        service, _ = qa.unit_paths(self.unit_dir)
        os.makedirs(self.unit_dir)
        with open(service, "w", encoding="utf-8") as fh:
            fh.write("[Unit]\n")
        with self.assertRaises(SystemExit):
            self.install()
        self.assertEqual(self.read(service), "[Unit]\n")
        self.assertEqual(self.calls, [])

    def test_uninstall_removes_only_marked_units(self):
        self.assertEqual(self.install(), 0)
        self.calls.clear()
        self.assertEqual(qa.uninstall(unit_dir=self.unit_dir, systemctl=self.systemctl), 0)
        self.assertEqual([os.path.exists(p) for p in qa.unit_paths(self.unit_dir)], [False, False])
        self.assertEqual(self.calls, [("disable", "--now", "tatitok-quota-alert.timer"),
                                      ("daemon-reload",)])


if __name__ == "__main__":
    unittest.main()
