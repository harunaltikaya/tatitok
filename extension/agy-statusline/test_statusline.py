"""python3 -m unittest extension/agy-statusline/test_statusline.py"""

import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "tatitok-agy-statusline.py")

_spec = importlib.util.spec_from_file_location("statusline", SCRIPT)
sl = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(sl)

NOW = 1_788_432_000_000  # epoch ms


def status(conv="conv-1", tin=100, tout=20, email="someone@example.com"):
    return {
        "conversation_id": conv,
        "email": email,
        "model": {"id": "gemini-3.8-flash", "display_name": "Gemini 3.8 Flash (Low)", "effort": "low"},
        "context_window": {
            "total_input_tokens": tin, "total_output_tokens": tout,
            "current_usage": {"input_tokens": tin, "output_tokens": tout,
                              "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
        },
        "quota": {
            "gemini-5h": {"remaining_fraction": 0.75, "reset_time": "2026-09-03T12:00:00Z", "reset_in_seconds": 3600},
            "gemini-weekly": {"remaining_fraction": 0.5, "reset_time": "2026-09-05T00:00:00.500Z", "reset_in_seconds": 100},
            "3p-5h": {"remaining_fraction": 1.0, "reset_time": "", "reset_in_seconds": 60},
            "3p-weekly": {"remaining_fraction": 0.0, "reset_time": "2026-09-06T00:00:00+02:00", "reset_in_seconds": 5},
        },
        "plan_tier": "pro", "cwd": "/home/user/project", "version": "1.1.25", "agent_state": "idle",
    }


class HookTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.ddir = os.path.join(self.tmp.name, "agy")
        self.posts = []

    def tearDown(self):
        self.tmp.cleanup()

    def poster(self, payload):
        self.posts.append(payload)
        return True

    def lines(self):
        p = os.path.join(self.ddir, "statusline.jsonl")
        if not os.path.exists(p):
            return []
        with open(p, encoding="utf-8") as fh:
            return [json.loads(l) for l in fh.read().splitlines() if l]

    def test_email_never_written(self):
        sl.process(status(email="secret@example.com"), self.ddir, now=NOW, poster=self.poster)
        raw = open(os.path.join(self.ddir, "statusline.jsonl"), encoding="utf-8").read()
        self.assertNotIn("secret@example.com", raw)
        self.assertNotIn("email", raw)
        self.assertNotIn("secret@example.com", json.dumps(self.posts))
        self.assertIn("logged_at", self.lines()[0])
        self.assertEqual(self.lines()[0]["logged_at"], "2026-09-03T10:40:00.000Z")
        mode = os.stat(os.path.join(self.ddir, "statusline.jsonl")).st_mode & 0o777
        self.assertEqual(mode, 0o600)
        self.assertEqual(os.stat(self.ddir).st_mode & 0o777, 0o700)

    def test_dedup_on_totals(self):
        a, _ = sl.process(status(), self.ddir, now=NOW, poster=self.poster)
        b, _ = sl.process(status(), self.ddir, now=NOW + 1000, poster=self.poster)
        self.assertTrue(a)
        self.assertFalse(b)
        self.assertEqual(len(self.lines()), 1)
        c, _ = sl.process(status(tout=21), self.ddir, now=NOW + 2000, poster=self.poster)
        self.assertTrue(c)
        self.assertEqual(len(self.lines()), 2)
        # another conversation with the same totals is its own first line
        d, _ = sl.process(status(conv="conv-2"), self.ddir, now=NOW + 3000, poster=self.poster)
        self.assertTrue(d)
        self.assertEqual(len(self.lines()), 3)
        # empty conversation_id never logs
        e, _ = sl.process(status(conv=""), self.ddir, now=NOW + 4000, poster=self.poster)
        self.assertFalse(e)
        self.assertEqual(len(self.lines()), 3)

    def test_quota_payload_shape(self):
        p = sl.quota_payload(status()["quota"], NOW)
        self.assertEqual(set(p), {"agy"})
        self.assertEqual(p["agy"]["fetchedAt"], NOW)
        by = {w["label"]: w for w in p["agy"]["windows"]}
        self.assertEqual(list(by), ["gemini-5h", "gemini-weekly", "3p-5h", "3p-weekly"])
        self.assertAlmostEqual(by["gemini-5h"]["usedPercent"], 25.0)
        self.assertAlmostEqual(by["gemini-weekly"]["usedPercent"], 50.0)
        self.assertAlmostEqual(by["3p-5h"]["usedPercent"], 0.0)
        self.assertAlmostEqual(by["3p-weekly"]["usedPercent"], 100.0)
        self.assertEqual(by["gemini-5h"]["resetAt"], 1_788_436_800_000)      # 2026-09-03T12:00:00Z
        self.assertEqual(by["gemini-weekly"]["resetAt"], 1_788_566_400_500)  # fractional seconds kept
        self.assertEqual(by["3p-5h"]["resetAt"], NOW + 60_000)               # fallback: reset_in_seconds
        self.assertEqual(by["3p-weekly"]["resetAt"], 1_788_645_600_000)      # +02:00 offset honoured
        self.assertIsNone(sl.quota_payload(None, NOW))
        self.assertIsNone(sl.quota_payload({}, NOW))

    def test_post_rate_limited_and_state_on_success_only(self):
        _, p1 = sl.process(status(), self.ddir, now=NOW, poster=self.poster)
        _, p2 = sl.process(status(tout=21), self.ddir, now=NOW + 89_000, poster=self.poster)
        _, p3 = sl.process(status(tout=22), self.ddir, now=NOW + 90_000, poster=self.poster)
        self.assertEqual((p1, p2, p3), (True, False, True))
        self.assertEqual(len(self.posts), 2)
        # a failed POST does not advance last_post_at
        _, p4 = sl.process(status(tout=23), self.ddir, now=NOW + 200_000, poster=lambda _p: False)
        self.assertFalse(p4)
        st = json.load(open(os.path.join(self.ddir, "state.json")))
        self.assertEqual(st["last_post_at"], NOW + 90_000)
        self.assertEqual(st["last_totals"]["conv-1"], [100, 23])

    def test_malformed_stdin(self):
        env = dict(os.environ, XDG_DATA_HOME=self.tmp.name)
        for bad in (b"", b"{not json", b"[1,2]", b"null"):
            r = subprocess.run([sys.executable, SCRIPT], input=bad, capture_output=True, env=env)
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertEqual(r.stdout, b"tatitok")
        self.assertFalse(os.path.exists(os.path.join(self.tmp.name, "tatitok", "agy", "statusline.jsonl")))
        self.assertFalse(os.path.exists(os.path.join(self.tmp.name, "tatitok", "agy", "state.json")))

    def test_end_to_end_stdin(self):
        env = dict(os.environ, XDG_DATA_HOME=self.tmp.name)
        # quota removed so the subprocess makes no network call
        obj = status()
        del obj["quota"]
        r = subprocess.run([sys.executable, SCRIPT], input=json.dumps(obj).encode(),
                           capture_output=True, env=env)
        self.assertEqual((r.returncode, r.stdout), (0, b"tatitok"))
        lines = open(os.path.join(self.tmp.name, "tatitok", "agy", "statusline.jsonl")).read().splitlines()
        self.assertEqual(len(lines), 1)
        self.assertNotIn("email", lines[0])

    def test_install_uninstall(self):
        sp = os.path.join(self.tmp.name, "settings.json")
        with open(sp, "w") as fh:
            json.dump({"model": "x"}, fh)
        sl.install(settings_path=sp, script="/abs/hook.py")
        self.assertTrue(os.path.exists(sp + ".pre-tatitok"))
        self.assertEqual(json.load(open(sp))["statusLine"], {"command": "/abs/hook.py"})
        with self.assertRaises(SystemExit):  # backup exists → refuse
            sl.install(settings_path=sp, script="/abs/hook.py")
        os.remove(sp + ".pre-tatitok")
        with self.assertRaises(SystemExit):  # key exists → refuse
            sl.install(settings_path=sp, script="/abs/hook.py")
        sl.uninstall(settings_path=sp)
        self.assertNotIn("statusLine", json.load(open(sp)))
        self.assertEqual(json.load(open(sp))["model"], "x")


if __name__ == "__main__":
    unittest.main()
