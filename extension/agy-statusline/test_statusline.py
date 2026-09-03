"""python3 -m unittest extension/agy-statusline/test_statusline.py"""

import fcntl
import importlib.util
import io
import json
import multiprocessing
import os
import subprocess
import sys
import tempfile
import time
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "tatitok-agy-statusline.py")

_spec = importlib.util.spec_from_file_location("statusline", SCRIPT)
sl = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(sl)

NOW = 1_788_432_000_000  # epoch ms (2026-09-03T10:40:00Z)


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


def read_text(path):
    with open(path, encoding="utf-8") as fh:
        return fh.read()


def read_json(path):
    with open(path, encoding="utf-8") as fh:
        return json.load(fh)


def write_json(path, obj):
    with open(path, "w", encoding="utf-8") as fh:
        json.dump(obj, fh)


class HookTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.ddir = os.path.join(self.tmp.name, "agy")
        self.posts = []
        sl.T0 = time.monotonic()  # each test is its own hook invocation

    def tearDown(self):
        self.tmp.cleanup()

    def poster(self, payload):
        self.posts.append(payload)
        return True

    def log_path(self):
        return os.path.join(self.ddir, "statusline.jsonl")

    def lines(self):
        if not os.path.exists(self.log_path()):
            return []
        return [json.loads(l) for l in read_text(self.log_path()).splitlines() if l]

    def run_script(self, stdin, **env):
        e = dict(os.environ, XDG_DATA_HOME=self.tmp.name, **env)
        return subprocess.run([sys.executable, SCRIPT], input=stdin, capture_output=True, env=e)

    # ---- logging --------------------------------------------------------

    def test_email_never_written(self):
        sl.process(status(email="secret@example.com"), self.ddir, now=NOW, poster=self.poster)
        raw = read_text(self.log_path())
        self.assertNotIn("secret@example.com", raw)
        self.assertNotIn("email", raw)
        self.assertNotIn("secret@example.com", json.dumps(self.posts))
        self.assertEqual(self.lines()[0]["logged_at"], "2026-09-03T10:40:00.000Z")
        self.assertEqual(os.stat(self.log_path()).st_mode & 0o777, 0o600)
        self.assertEqual(os.stat(self.ddir).st_mode & 0o777, 0o700)

    def test_dedup_on_totals(self):
        a = sl.process(status(), self.ddir, now=NOW, poster=self.poster)
        b = sl.process(status(), self.ddir, now=NOW + 1000, poster=self.poster)
        self.assertTrue(a.appended)
        self.assertFalse(b.appended)
        self.assertFalse(a.busy or b.busy)
        self.assertEqual(len(self.lines()), 1)
        c = sl.process(status(tout=21), self.ddir, now=NOW + 2000, poster=self.poster)
        self.assertTrue(c.appended)
        self.assertEqual(len(self.lines()), 2)
        d = sl.process(status(conv="conv-2"), self.ddir, now=NOW + 3000, poster=self.poster)
        self.assertTrue(d.appended)
        self.assertEqual(len(self.lines()), 3)
        e = sl.process(status(conv=""), self.ddir, now=NOW + 4000, poster=self.poster)
        self.assertFalse(e.appended)
        self.assertEqual(len(self.lines()), 3)

    # ---- quota payload ----------------------------------------------------

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
        self.assertEqual(by["gemini-5h"]["resetAt"], 1_788_436_800_000)
        self.assertEqual(by["gemini-weekly"]["resetAt"], 1_788_566_400_500)
        self.assertEqual(by["3p-5h"]["resetAt"], NOW + 60_000)
        self.assertEqual(by["3p-weekly"]["resetAt"], 1_788_645_600_000)
        self.assertIsNone(sl.quota_payload(None, NOW))
        self.assertIsNone(sl.quota_payload({}, NOW))

    def test_post_rate_limited_and_state_on_success_only(self):
        p1 = sl.process(status(), self.ddir, now=NOW, poster=self.poster).posted
        p2 = sl.process(status(tout=21), self.ddir, now=NOW + 89_000, poster=self.poster).posted
        p3 = sl.process(status(tout=22), self.ddir, now=NOW + 90_000, poster=self.poster).posted
        self.assertEqual((p1, p2, p3), (True, False, True))
        self.assertEqual(len(self.posts), 2)
        # a failed POST advances only the attempt stamp, never last_post_at
        p4 = sl.process(status(tout=23), self.ddir, now=NOW + 200_000, poster=lambda _p: False).posted
        self.assertFalse(p4)
        st = read_json(os.path.join(self.ddir, "state.json"))
        self.assertEqual(st["last_post_at"], NOW + 90_000)
        self.assertEqual(st["last_attempt_at"], NOW + 200_000)
        self.assertEqual(st["last_totals"]["conv-1"], [100, 23])
        # and the failed attempt is not retried before the interval elapses
        p5 = sl.process(status(tout=24), self.ddir, now=NOW + 250_000, poster=self.poster).posted
        self.assertFalse(p5)
        self.assertEqual(len(self.posts), 2)

    # ---- (b) state validation ---------------------------------------------

    def test_corrupt_state_is_discarded(self):
        os.makedirs(self.ddir, mode=0o700)
        sp = os.path.join(self.ddir, "state.json")
        bad_states = [
            {"last_totals": [["conv-1", [1, 2]]], "last_post_at": NOW},   # list, not dict
            {"last_totals": {"conv-1": "12"}, "last_post_at": NOW},        # value not [int,int]
            {"last_totals": {"conv-1": [1, 2, 3]}},                        # wrong arity
            {"last_totals": {"conv-1": [1, True]}},                        # bool is not int
            {"last_totals": {}, "last_post_at": "yesterday"},              # wrong type
            {"last_totals": {}, "last_attempt_at": None},                  # wrong type
            "just a string", [1, 2], 42,
        ]
        for bad in bad_states:
            write_json(sp, bad)
            self.assertEqual(sl.load_state(sp), {}, bad)
        with open(sp, "w", encoding="utf-8") as fh:
            fh.write("{not json")
        self.assertEqual(sl.load_state(sp), {})
        # A list-shaped last_totals still logs AND posts: fresh start.
        write_json(sp, {"last_totals": [["conv-1", [100, 20]]], "last_post_at": NOW})
        r = sl.process(status(), self.ddir, now=NOW + 1000, poster=self.poster)
        self.assertTrue(r.appended)
        self.assertTrue(r.posted)
        st = read_json(sp)
        self.assertEqual(st["last_totals"], {"conv-1": [100, 20]})
        self.assertEqual(st["last_post_at"], NOW + 1000)
        # A valid state round-trips unchanged.
        self.assertEqual(sl.load_state(sp), st)

    # ---- (c) bounded stdin ------------------------------------------------

    def test_stdin_bound(self):
        limit = sl.STDIN_LIMIT
        self.assertIsNone(sl.read_stdin_bounded(io.BytesIO(b"x" * (limit + 1))))
        self.assertIsNone(sl.read_stdin_bounded(io.BytesIO(b"x" * (limit + 5000))))
        self.assertEqual(len(sl.read_stdin_bounded(io.BytesIO(b"x" * limit))), limit)
        # Over the limit end to end: a VALID object padded past 256 KiB must
        # print "tatitok", exit 0 and write nothing.
        obj = status()
        del obj["quota"]
        obj["pad"] = "p" * (limit + 10)
        r = self.run_script(json.dumps(obj).encode())
        self.assertEqual((r.returncode, r.stdout), (0, b"tatitok"), r.stderr)
        self.assertFalse(os.path.exists(os.path.join(self.tmp.name, "tatitok", "agy", "statusline.jsonl")))
        self.assertFalse(os.path.exists(os.path.join(self.tmp.name, "tatitok", "agy", "state.json")))

    def test_malformed_stdin(self):
        for bad in (b"", b"{not json", b"[1,2]", b"null"):
            r = self.run_script(bad)
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertEqual(r.stdout, b"tatitok")
        self.assertFalse(os.path.exists(os.path.join(self.tmp.name, "tatitok", "agy", "statusline.jsonl")))
        self.assertFalse(os.path.exists(os.path.join(self.tmp.name, "tatitok", "agy", "state.json")))

    def test_end_to_end_stdin(self):
        obj = status()
        del obj["quota"]  # no network call from the subprocess
        r = self.run_script(json.dumps(obj).encode())
        self.assertEqual((r.returncode, r.stdout), (0, b"tatitok"))
        lines = read_text(os.path.join(self.tmp.name, "tatitok", "agy", "statusline.jsonl")).splitlines()
        self.assertEqual(len(lines), 1)
        self.assertNotIn("email", lines[0])

    # ---- state lock -----------------------------------------------------------

    def test_lock_busy_skips_refresh(self):
        os.makedirs(self.ddir, mode=0o700)
        fd = os.open(os.path.join(self.ddir, "state.lock"), os.O_RDWR | os.O_CREAT, 0o600)
        fcntl.flock(fd, fcntl.LOCK_EX)
        try:
            r = sl.process(status(), self.ddir, now=NOW, poster=self.poster)
        finally:
            os.close(fd)
        self.assertTrue(r.busy)
        self.assertEqual(self.lines(), [])
        self.assertEqual(self.posts, [])
        self.assertFalse(os.path.exists(os.path.join(self.ddir, "state.json")))
        # released → the next refresh does the work
        r = sl.process(status(), self.ddir, now=NOW, poster=self.poster)
        self.assertFalse(r.busy)
        self.assertEqual(len(self.lines()), 1)
        self.assertEqual(len(self.posts), 1)

    def test_lock_race_one_attempt(self):
        # Two processes racing on one state dir: the first holds the lock
        # through its (slow) poster call, the second finds it busy and skips.
        # Exactly one attempt is stamped and exactly one poster call happens.
        os.makedirs(self.ddir, mode=0o700)
        calls = os.path.join(self.tmp.name, "poster-calls")
        results = os.path.join(self.tmp.name, "results")
        os.makedirs(calls)
        os.makedirs(results)

        def worker(tag, delay):
            time.sleep(delay)

            def poster(_payload):
                with open(os.path.join(calls, tag), "w") as fh:
                    fh.write("1")
                time.sleep(0.4)  # keep the lock held so the sibling collides
                return True

            r = sl.process(status(), self.ddir, now=NOW, poster=poster)
            with open(os.path.join(results, tag), "w") as fh:
                json.dump({"busy": r.busy, "posted": bool(r.posted), "appended": r.appended}, fh)

        ctx = multiprocessing.get_context("fork")
        a = ctx.Process(target=worker, args=("a", 0.0))
        b = ctx.Process(target=worker, args=("b", 0.15))
        a.start()
        b.start()
        a.join(5)
        b.join(5)
        self.assertEqual((a.exitcode, b.exitcode), (0, 0))
        got = {t: read_json(os.path.join(results, t)) for t in ("a", "b")}
        self.assertEqual(sorted(os.listdir(calls)), ["a"], got)  # one poster call
        self.assertEqual(got["a"], {"busy": False, "posted": True, "appended": True})
        self.assertEqual(got["b"], {"busy": True, "posted": False, "appended": False})
        st = read_json(os.path.join(self.ddir, "state.json"))
        self.assertEqual(st["last_attempt_at"], NOW)
        self.assertEqual(st["last_post_at"], NOW)
        self.assertEqual(len(self.lines()), 1)

    # ---- stdin deadline ---------------------------------------------------------

    def test_stdin_open_pipe_full_object_is_parsed(self):
        obj = status()
        del obj["quota"]
        env = dict(os.environ, XDG_DATA_HOME=self.tmp.name)
        t0 = time.perf_counter()
        p = subprocess.Popen([sys.executable, SCRIPT], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                             stderr=subprocess.PIPE, env=env)
        p.stdin.write(json.dumps(obj).encode())
        p.stdin.flush()  # NOT closed: no EOF ever arrives
        out, err = p.stdout.read(), p.stderr.read()
        p.wait(5)
        elapsed = time.perf_counter() - t0
        p.stdin.close()
        p.stdout.close()
        p.stderr.close()
        self.assertEqual((p.returncode, out), (0, b"tatitok"), err)
        self.assertLess(elapsed, 0.5, "hook did not stop at the stdin deadline")
        lines = read_text(os.path.join(self.tmp.name, "tatitok", "agy", "statusline.jsonl")).splitlines()
        self.assertEqual(len(lines), 1)

    def test_stdin_half_object_stall(self):
        half = json.dumps(status())[:40]
        env = dict(os.environ, XDG_DATA_HOME=self.tmp.name)
        t0 = time.perf_counter()
        p = subprocess.Popen([sys.executable, SCRIPT], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                             stderr=subprocess.PIPE, env=env)
        p.stdin.write(half.encode())
        p.stdin.flush()
        out = p.stdout.read()
        p.wait(5)
        elapsed = time.perf_counter() - t0
        p.stdin.close()
        p.stdout.close()
        p.stderr.close()
        self.assertEqual((p.returncode, out), (0, b"tatitok"))
        self.assertLess(elapsed, 0.5)
        self.assertFalse(os.path.exists(os.path.join(self.tmp.name, "tatitok", "agy", "statusline.jsonl")))
        self.assertFalse(os.path.exists(os.path.join(self.tmp.name, "tatitok", "agy", "state.json")))

    def test_read_stdin_bounded_deadline_and_limit(self):
        r, w = os.pipe()
        try:
            os.write(w, b'{"a":1}')
            sl.T0 = time.monotonic()
            t0 = time.perf_counter()
            got, retained = sl.read_bounded(r, deadline_s=0.1)
            self.assertEqual((got, retained), (b'{"a":1}', 7))
            self.assertLess(time.perf_counter() - t0, 0.3)
        finally:
            os.close(r)
            os.close(w)
        self.assertIsNone(sl.read_stdin_bounded(io.BytesIO(b"x" * (sl.STDIN_LIMIT + 1))))

    def test_read_bounded_retains_exactly_limit_plus_one(self):
        # A writer pushes well past the bound; the reader must reject having
        # held EXACTLY limit + 1 bytes (per-read size = remaining capacity).
        limit = sl.STDIN_LIMIT
        r, w = os.pipe()
        payload = b"x" * (limit + 100_000)

        def writer():
            try:
                view = memoryview(payload)
                while view:
                    n = os.write(w, view)
                    view = view[n:]
            except OSError:
                pass  # reader closed early — expected once it rejects
            finally:
                os.close(w)

        import threading
        th = threading.Thread(target=writer)
        th.start()
        try:
            sl.T0 = time.monotonic()
            got, retained = sl.read_bounded(r, limit, deadline_s=2.0)
        finally:
            os.close(r)
            th.join(5)
        self.assertIsNone(got)
        self.assertEqual(retained, limit + 1)
        # Exactly at the limit is accepted whole.
        r, w = os.pipe()
        exact = b"y" * 70_000  # fits the pipe buffer? no — write in a thread too

        def writer2():
            try:
                view = memoryview(exact)
                while view:
                    n = os.write(w, view)
                    view = view[n:]
            finally:
                os.close(w)

        th = threading.Thread(target=writer2)
        th.start()
        try:
            sl.T0 = time.monotonic()
            got, retained = sl.read_bounded(r, 70_000, deadline_s=2.0)
        finally:
            os.close(r)
            th.join(5)
        self.assertEqual((len(got), retained), (70_000, 70_000))

    # ---- end-to-end budget --------------------------------------------------------

    def test_budget_exhausted_writes_nothing(self):
        sl.T0 = time.monotonic() - 1.0  # the whole budget is already spent
        with self.assertRaises(sl.Budget):
            sl.process(status(), self.ddir, now=NOW, poster=self.poster)
        self.assertEqual(self.lines(), [])
        self.assertEqual(self.posts, [])
        self.assertFalse(os.path.exists(os.path.join(self.ddir, "state.json")))
        # run_hook swallows it and still answers agy
        out = io.StringIO()
        real = sys.stdout
        sys.stdout = out
        try:
            rc = sl.run_hook(json.dumps(status()).encode())
        finally:
            sys.stdout = real
        self.assertEqual((rc, out.getvalue()), (0, "tatitok"))

    def test_append_line_is_one_write(self):
        os.makedirs(self.ddir, mode=0o700)
        p = os.path.join(self.ddir, "statusline.jsonl")
        calls = []
        real_write = os.write

        def spy(fd, data):
            calls.append(len(data))
            return real_write(fd, data)

        sl.os.write = spy
        try:
            sl.append_line(p, b'{"x":1}\n')
        finally:
            sl.os.write = real_write
        self.assertEqual(calls, [8])
        self.assertEqual(read_text(p), '{"x":1}\n')
        self.assertEqual(os.stat(p).st_mode & 0o777, 0o600)

    def test_append_line_short_write_terminates_line(self):
        os.makedirs(self.ddir, mode=0o700)
        p = os.path.join(self.ddir, "statusline.jsonl")
        calls = []
        real_write = os.write

        def short(fd, data):
            calls.append(bytes(data))
            if len(data) > 1:
                return real_write(fd, data[:3])  # fake fd: 3 of 8 bytes land
            return real_write(fd, data)

        sl.os.write = short
        try:
            with self.assertRaises(OSError):
                sl.append_line(p, b'{"x":1}\n')
        finally:
            sl.os.write = real_write
        # the partial line is terminated by exactly one extra "\n" write
        self.assertEqual(calls, [b'{"x":1}\n', b"\n"])
        self.assertEqual(read_text(p), '{"x\n')
        # and the file is still one-line-per-record: the next append is clean
        sl.append_line(p, b'{"y":2}\n')
        self.assertEqual(read_text(p).split("\n"), ['{"x', '{"y":2}', ""])

    def test_short_write_still_answers_agy(self):
        real_write = os.write

        def short(fd, data):
            return real_write(fd, data[:3]) if len(data) > 1 else real_write(fd, data)

        sl.os.write = short
        out = io.StringIO()
        real = sys.stdout
        sys.stdout = out
        saved = os.environ.get("XDG_DATA_HOME")
        os.environ["XDG_DATA_HOME"] = self.tmp.name  # run_hook resolves data_dir() itself
        try:
            rc = sl.run_hook(json.dumps(status()).encode())
        finally:
            sys.stdout = real
            sl.os.write = real_write
            if saved is None:
                del os.environ["XDG_DATA_HOME"]
            else:
                os.environ["XDG_DATA_HOME"] = saved
        self.assertEqual((rc, out.getvalue()), (0, "tatitok"))
        self.assertTrue(read_text(os.path.join(self.tmp.name, "tatitok", "agy", "statusline.jsonl")).endswith("\n"))

    # ---- (e) hub URL validation ------------------------------------------

    def test_hub_url_loopback_only(self):
        for ok in ("http://127.0.0.1:8284", "http://127.0.0.1", "http://localhost:9000", "http://localhost"):
            self.assertEqual(sl.hub_url(ok), ok)
            self.assertEqual(sl.ingest_url(ok), ok + "/api/v1/limits")
        # The same 12 negative cases the Go test pins HUB_URL_RE to.
        for bad in (
            "https://127.0.0.1:8284", "http://127.0.0.1:8284/", "http://127.0.0.1:8284/api",
            "http://127.0.0.1.evil.example", "http://localhost.evil.example", "http://evil.example",
            "http://10.0.0.5:8284", "http://[::1]:8284", "http://127.0.0.1:8284?x=1",
            "http://user@127.0.0.1:8284", "ftp://127.0.0.1", " http://127.0.0.1:8284",
        ):
            self.assertEqual(sl.hub_url(bad), sl.DEFAULT_HUB_URL, bad)
        for bad in ("http://127.1", "http://0x7f000001", "http://127.0.0.1#f", "http://127.0.0.1:8284\n"):
            self.assertEqual(sl.hub_url(bad), sl.DEFAULT_HUB_URL, bad)
        self.assertEqual(sl.hub_url(""), sl.DEFAULT_HUB_URL)
        self.assertEqual(sl.hub_url(None) if "TATITOK_HUB_URL" not in os.environ else sl.DEFAULT_HUB_URL,
                         sl.DEFAULT_HUB_URL)
        # A non-loopback override is never POSTed to: the synchronous poster
        # is handed the validated URL only.
        seen = []

        def fake_urlopen(req, timeout=None):
            seen.append(req.full_url)
            raise OSError("no network in tests")

        orig = sl.urllib.request.urlopen
        sl.urllib.request.urlopen = fake_urlopen
        try:
            self.assertFalse(sl.post_quota({"agy": {}}, url=sl.ingest_url("http://evil.example")))
        finally:
            sl.urllib.request.urlopen = orig
        self.assertEqual(seen, ["http://127.0.0.1:8284/api/v1/limits"])

    # ---- (d) install / uninstall ------------------------------------------

    def test_install_uninstall(self):
        sp = os.path.join(self.tmp.name, "settings.json")
        write_json(sp, {"model": "x"})
        sl.install(settings_path=sp, script="/abs/hook.py")
        self.assertTrue(os.path.exists(sp + ".pre-tatitok"))
        self.assertEqual(read_json(sp)["statusLine"], {"command": "/abs/hook.py"})
        with self.assertRaises(SystemExit):  # backup exists → refuse
            sl.install(settings_path=sp, script="/abs/hook.py")
        os.remove(sp + ".pre-tatitok")
        with self.assertRaises(SystemExit):  # key exists → refuse
            sl.install(settings_path=sp, script="/abs/hook.py")
        self.assertEqual(sl.uninstall(settings_path=sp, script="/abs/hook.py"), 0)
        self.assertNotIn("statusLine", read_json(sp))
        self.assertEqual(read_json(sp)["model"], "x")
        self.assertEqual(sl.uninstall(settings_path=sp, script="/abs/hook.py"), 0)  # nothing to do

    def test_uninstall_refuses_foreign_statusline(self):
        sp = os.path.join(self.tmp.name, "settings.json")
        before = {"model": "x", "statusLine": {"command": "/somewhere/else.sh"}}
        write_json(sp, before)
        mtime = os.stat(sp).st_mtime_ns
        out = io.StringIO()
        real = sys.stdout
        sys.stdout = out
        try:
            rc = sl.uninstall(settings_path=sp, script="/abs/hook.py")
        finally:
            sys.stdout = real
        self.assertEqual(rc, 1)
        self.assertIn("/somewhere/else.sh", out.getvalue())
        self.assertEqual(read_json(sp), before)
        self.assertEqual(os.stat(sp).st_mtime_ns, mtime)


if __name__ == "__main__":
    unittest.main()
