#!/usr/bin/env python3
"""
h3 studio - a local web control surface for antirez/h3.c

Stdlib only. No pip installs.

    python3 h3studio.py --h3 /path/to/h3.c/h3 --model /path/to/MiniMax-H3

Then open http://127.0.0.1:8710
"""

import argparse
import json
import mimetypes
import os
import queue
import re
import shutil
import signal
import subprocess
import threading
import time
import uuid
from datetime import datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlparse, unquote

HERE = Path(__file__).resolve().parent
STATIC = HERE / "static"

# H3 aligns frame requests upward to 5 + 17n at 24 fps.
LEGAL_FRAMES = [5 + 17 * n for n in range(0, 22)]  # 5 .. 362
MAX_PIXELS = 768 * 1344


class Config:
    def __init__(self, args):
        self.h3 = Path(args.h3).resolve()
        self.model = Path(args.model).resolve()
        self.workdir = self.h3.parent
        # Keep web UI state with the studio, independent of the h3 binary path.
        sessions = HERE / "sessions"
        self.sessions = sessions
        self.model_file = sessions / "model.json"
        self.h3_file = sessions / "h3.json"
        if self.h3_file.exists():
            try:
                saved_h3 = json.loads(self.h3_file.read_text()).get("h3")
                if saved_h3:
                    self.h3 = Path(saved_h3).expanduser().resolve()
                    self.workdir = self.h3.parent
            except (OSError, json.JSONDecodeError, AttributeError):
                pass
        if self.model_file.exists():
            try:
                saved_model = json.loads(self.model_file.read_text()).get("model")
                if saved_model:
                    self.model = Path(saved_model).expanduser().resolve()
            except (OSError, json.JSONDecodeError, AttributeError):
                pass
        self.setting_file = sessions / "last_session.json"
        self.active_session = self.load_last_session()
        self.inputs, self.outputs = self.activate_session(self.active_session)
        setting = self.session_setting(self.active_session)
        current = {}
        if setting.exists():
            try:
                current = json.loads(setting.read_text())
            except (OSError, json.JSONDecodeError):
                current = {}
        defaults = default_session_settings(self.active_session)
        defaults.update(current)
        if not isinstance(defaults.get("takes"), list):
            defaults["takes"] = []
        setting.write_text(json.dumps(defaults, indent=2) + "\n")
        self.ffmpeg = shutil.which("ffmpeg") or "ffmpeg"

    def load_last_session(self):
        source = self.setting_file
        legacy = self.sessions / "setting.json"
        if not source.exists() and legacy.exists():
            source = legacy
        legacy = self.sessions / "setting.cnf"
        if not source.exists() and legacy.exists():
            source = legacy
        if source.exists():
            try:
                data = json.loads(source.read_text())
                raw_name = str(data.get("last_session") or "").strip()
                name = safe_stem(raw_name) if raw_name else ""
                if name and (self.sessions / name).is_dir():
                    return name
            except (OSError, json.JSONDecodeError, AttributeError):
                pass
        existing = sorted(
            p.name for p in self.sessions.iterdir()
            if p.is_dir()
            and (p / "input").is_dir() and (p / "outputs").is_dir()
        )
        return existing[0] if existing else "session-1"

    def activate_session(self, name):
        session = safe_stem(name or "session-1")
        root = self.sessions / session
        inputs = root / "input"
        outputs = root / "outputs"
        inputs.mkdir(parents=True, exist_ok=True)
        outputs.mkdir(parents=True, exist_ok=True)
        self.active_session = session
        self.inputs, self.outputs = inputs, outputs
        self.setting_file.write_text(json.dumps({"last_session": session}, indent=2) + "\n")
        setting = self.session_setting(session)
        if not setting.exists():
            setting.write_text(
                json.dumps(default_session_settings(session), indent=2) + "\n"
            )
        return inputs, outputs

    def session_setting(self, name):
        session = safe_stem(name or "session-1")
        root = self.sessions / session
        root.mkdir(parents=True, exist_ok=True)
        return root / "setting.json"

    def session_dirs(self, name):
        session = safe_stem(name or "default")
        root = self.sessions / session
        inputs, outputs = root / "input", root / "outputs"
        inputs.mkdir(parents=True, exist_ok=True)
        outputs.mkdir(parents=True, exist_ok=True)
        return inputs, outputs

    def terminal_log(self, name=None):
        session = safe_stem(name or self.active_session)
        path = self.sessions / session / "terminal.log"
        path.parent.mkdir(parents=True, exist_ok=True)
        return path


CFG: Config = None  # set in main


# ---------------------------------------------------------------- job model

class Job:
    def __init__(self, params):
        self.id = uuid.uuid4().hex[:10]
        self.params = params
        self.label = params.get("label") or ""
        self.state = "queued"          # queued | running | done | failed | cancelled
        self.phase = ""
        self.progress = None           # (n, total)
        self.log = []
        self.command = []
        self.output = None
        self.error = None
        self.started = None
        self.finished = None
        self.profile = []              # parsed profile rows

    def summary(self):
        return {
            "id": self.id,
            "label": self.label,
            "state": self.state,
            "phase": self.phase,
            "progress": self.progress,
            "output": self.output,
            "error": self.error,
            "started": self.started,
            "finished": self.finished,
            "params": self.params,
            "command": self.command,
            "log": self.log[-400:],
            "profile": self.profile,
        }


class Runner:
    """Single worker. One GPU, one render at a time."""

    def __init__(self):
        self.q = queue.Queue()
        self.jobs = {}
        self.order = []
        self.current = None
        self.proc = None
        self.interactive_proc = None
        self.interactive_lines = queue.Queue()
        self.interactive_lock = threading.Lock()
        self.terminal_proc = None
        self.terminal_lock = threading.Lock()
        self.lock = threading.Lock()
        self.listeners = []
        threading.Thread(target=self._loop, daemon=True).start()

    # -- pub/sub for server-sent events

    def subscribe(self):
        q = queue.Queue(maxsize=200)
        with self.lock:
            self.listeners.append(q)
        return q

    def unsubscribe(self, q):
        with self.lock:
            if q in self.listeners:
                self.listeners.remove(q)

    def emit(self, kind, payload):
        if kind == "terminal" and payload.get("line"):
            path = CFG.terminal_log()
            with path.open("a", encoding="utf-8") as stream:
                stream.write(str(payload["line"]) + "\n")
        msg = json.dumps({"kind": kind, "payload": payload})
        with self.lock:
            dead = []
            for q in self.listeners:
                try:
                    q.put_nowait(msg)
                except queue.Full:
                    dead.append(q)
            for q in dead:
                self.listeners.remove(q)

    # -- queue control

    def submit(self, params):
        CFG.terminal_log(params.get("session_name")).write_text("", encoding="utf-8")
        job = Job(params)
        self.jobs[job.id] = job
        self.order.append(job.id)
        self.q.put(job.id)
        self.emit("queue", self.queue_state())
        return job

    def cancel(self, job_id):
        job = self.jobs.get(job_id)
        if not job:
            return False
        if job.state == "queued":
            job.state = "cancelled"
            self.emit("queue", self.queue_state())
            return True
        if job.state == "running" and self.proc:
            job.state = "cancelling"
            proc = self.proc
            threading.Thread(target=self._stop_process, args=(proc,), daemon=True).start()
            return True
        return False

    @staticmethod
    def _stop_process(proc):
        """Stop the whole render process group and force memory release."""
        try:
            os.killpg(proc.pid, signal.SIGTERM)
        except ProcessLookupError:
            return
        try:
            proc.wait(timeout=2)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(proc.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
        finally:
            for stream in (proc.stdout, proc.stderr):
                if stream is not None:
                    stream.close()

    def run_terminal(self, command):
        with self.terminal_lock:
            if self.terminal_proc is not None or self.current is not None:
                return False
            thread = threading.Thread(target=self._run_terminal, args=(command,), daemon=True)
            thread.start()
            return True

    def load_interactive(self, params):
        with self.interactive_lock:
            if self.interactive_proc is not None:
                return False, "interactive h3 is already loaded"
            params["session_name"] = save_session(params)
            CFG.activate_session(params["session_name"])
            self._ensure_interactive(params)
        return True, None

    def send_interactive(self, line):
        text = str(line).strip()
        if not text:
            return False, "input is required"
        with self.interactive_lock:
            if self.current is not None:
                return False, "interactive h3 is busy rendering"
            if self.interactive_proc is None:
                return False, "load interactive h3 first"
            if self.interactive_proc.poll() is not None:
                self.interactive_proc = None
                return False, "interactive h3 has exited; load it again"
            self._interactive_send(text)
        return True, None

    def _run_terminal(self, command):
        self.emit("terminal", {"running": True})
        try:
            self.terminal_proc = subprocess.Popen(
                command, shell=True, executable="/bin/sh", cwd=str(CFG.workdir),
                stdout=subprocess.PIPE, stderr=subprocess.STDOUT, bufsize=1,
            )
            for raw in iter(self.terminal_proc.stdout.readline, b""):
                line = raw.decode("utf-8", "replace").rstrip()
                if line:
                    self.emit("terminal", {"line": line, "running": True})
            code = self.terminal_proc.wait()
            self.emit("terminal", {"line": f"[exit {code}]", "running": False})
        finally:
            with self.terminal_lock:
                self.terminal_proc = None

    def queue_state(self):
        return [self.jobs[i].summary() for i in self.order
                if self.jobs[i].state in ("queued", "running")]

    def history(self, limit=40):
        done = [self.jobs[i].summary() for i in reversed(self.order)
                if self.jobs[i].state in ("done", "failed", "cancelled")]
        return done[:limit]

    # -- worker

    def _loop(self):
        while True:
            job_id = self.q.get()
            job = self.jobs.get(job_id)
            if not job or job.state == "cancelled":
                continue
            self.current = job
            try:
                self._run(job)
            except Exception as exc:                      # noqa: BLE001
                job.state = "failed"
                job.error = str(exc)
            finally:
                job.finished = time.time()
                self.current = None
                self.proc = None
                record_take(job)
                self.emit("job", job.summary())
                self.emit("queue", self.queue_state())
                self.emit("outputs", list_outputs())

    def _run(self, job):
        if job.params.get("run_mode") == "interactive":
            return self._run_interactive(job)
        p = job.params
        inputs, outputs = CFG.session_dirs(p.get("session_name"))
        stem = safe_stem(p.get("label") or "take")
        name = f"{stem}-{datetime.now().strftime('%m%d-%H%M%S')}.mp4"
        out_path = outputs / name

        cmd = [str(CFG.h3), "--profile", "-d", str(CFG.model)]
        cmd += ["-p", p["prompt"]]

        for ref in p.get("refs", []):
            path = str(inputs / ref["name"])
            if ref["kind"] == "image":
                cmd += ["--ref-image", path]
            elif ref["kind"] == "audio":
                cmd += ["--ref-audio", path]
            elif ref.get("mode") == "silent":
                cmd += ["--ref-silent-video", path]
            elif ref.get("mode") == "replace" and ref.get("pairedAudio"):
                cmd += ["--ref-video-audio", path, str(inputs / ref["pairedAudio"])]
            else:
                cmd += ["--ref-video", path]
        if p.get("first_frame"):
            cmd += ["--first-frame", str(inputs / p["first_frame"])]
        if p.get("last_frame"):
            cmd += ["--last-frame", str(inputs / p["last_frame"])]

        cmd += ["--width", str(p["width"]), "--height", str(p["height"])]
        if p.get("render_width") and p.get("render_height"):
            cmd += ["--render-width", str(p["render_width"]),
                    "--render-height", str(p["render_height"])]
        cmd += ["--frames", str(p["frames"]), "--steps", str(p["steps"])]
        cmd += ["--layers", str(p["layers"])]
        if p.get("core_reuse"):
            cmd += ["--core-reuse", str(p["core_reuse"])]
        else:
            cmd += ["--reuse", str(p.get("reuse", 1))]
        if p.get("token_reduction"):
            cmd += ["--token-reduction"]
        if p.get("ssd_streaming"):
            cmd += ["--ssd-streaming"]
        if p.get("int8_row_fc2") and not p.get("ssd_streaming"):
            cmd += ["--use-int8-row-fc2"]
        cmd += ["--seed", str(p["seed"])]
        cmd += ["-o", str(out_path)]

        env = dict(os.environ)
        for k, v in (p.get("env") or {}).items():
            if v not in (None, ""):
                env[str(k)] = str(v)

        job.command = cmd
        job.state = "running"
        job.started = time.time()
        self.emit("job", job.summary())
        self.emit("queue", self.queue_state())

        self.proc = subprocess.Popen(
            cmd, cwd=str(CFG.workdir), env=env,
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            bufsize=0, start_new_session=True,
        )
        self._pump(job, self.proc)
        code = self.proc.wait()

        job.finished = time.time()
        if job.state == "cancelling":
            job.state = "cancelled"
        elif code == 0 and out_path.exists():
            job.state = "done"
            job.output = name
            write_sidecar(out_path, job)
        else:
            job.state = "failed"
            job.error = f"h3 exited with code {code}"

    def _ensure_interactive(self, p):
        if self.interactive_proc is not None:
            return
        cmd = [str(CFG.h3), "--profile", "-d", str(CFG.model),
               "--width", str(p["width"]), "--height", str(p["height"])]
        env = dict(os.environ)
        for key, value in (p.get("env") or {}).items():
            if value not in (None, ""):
                env[str(key)] = str(value)
        self.interactive_proc = subprocess.Popen(
            cmd, cwd=str(CFG.workdir), stdin=subprocess.PIPE,
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, bufsize=0, env=env,
        )
        threading.Thread(target=self._read_interactive, daemon=True).start()
        _, outputs = CFG.session_dirs(p.get("session_name"))
        self._interactive_send(f"!output {outputs}")

    def _read_interactive(self):
        buf = b""
        while self.interactive_proc and self.interactive_proc.stdout:
            chunk = self.interactive_proc.stdout.read(1)
            if not chunk:
                break
            if chunk in (b"\n", b"\r"):
                line = buf.decode("utf-8", "replace").rstrip()
                buf = b""
                if line:
                    self.interactive_lines.put(line)
                    self.emit("terminal", {"line": line, "running": True})
            else:
                buf += chunk
        self.interactive_lines.put(None)

    def stop_interactive(self):
        proc = self.interactive_proc
        if proc is None:
            return
        self.interactive_proc = None
        try:
            if proc.stdin:
                proc.stdin.write(b"!quit\n")
                proc.stdin.flush()
            proc.wait(timeout=5)
        except (BrokenPipeError, OSError, subprocess.TimeoutExpired):
            proc.kill()
            proc.wait()

    def _interactive_send(self, line):
        self.interactive_proc.stdin.write((line + "\n").encode())
        self.interactive_proc.stdin.flush()
        self.emit("terminal", {"line": "h3> " + line, "running": True})

    def _run_interactive(self, job):
        p = job.params
        refs = p.get("refs") or []
        if any(ref.get("kind") != "image" for ref in refs):
            job.state = "failed"
            job.error = "interactive h3 mode currently supports image references only"
            return
        job.state = "running"
        job.started = time.time()
        self._ensure_interactive(p)
        inputs, _ = CFG.session_dirs(p.get("session_name"))
        commands = [
            f"!size {p['width']}x{p['height']}",
            f"!frames {p['frames']}",
            f"!steps {p['steps']}",
            f"!layers {p['layers']}",
            f"!reuse {p.get('reuse', 1)}",
            f"!seed {p['seed']}",
        ]
        if p.get("render_width") and p.get("render_height"):
            commands.append(f"!render-size {p['render_width']}x{p['render_height']}")
        else:
            commands.append("!render-size native")
        commands.extend([
            f"!token-reduction {'on' if p.get('token_reduction') else 'off'}",
            f"!ssd-streaming {'on' if p.get('ssd_streaming') else 'off'}",
            f"!int8-row-fc2 {'on' if p.get('int8_row_fc2') else 'off'}",
            "!refs clear",
            "!first clear",
            "!last clear",
        ])
        commands.extend(f"!ref-image {inputs / ref['name']}" for ref in refs)
        if p.get("first_frame"):
            commands.append(f"!first {inputs / p['first_frame']}")
        if p.get("last_frame"):
            commands.append(f"!last {inputs / p['last_frame']}")
        self.emit("job", job.summary())
        for command in commands:
            self._interactive_send(command)
        self._interactive_send(p["prompt"])
        job.command = commands + [p["prompt"]]
        output = None
        deadline = time.time() + 3600
        while time.time() < deadline:
            line = self.interactive_lines.get()
            if line is None:
                break
            match = re.search(r"Done -> (.+?) \[", line)
            if match:
                output = Path(match.group(1).strip())
                break
            if line.startswith("h3: "):
                job.error = line
                break
        job.finished = time.time()
        if output and output.exists():
            job.state = "done"
            job.output = output.name
            write_sidecar(output, job)
        else:
            job.state = "failed"
            job.error = job.error or "interactive h3 did not produce an output"

    def _pump(self, job, proc):
        """Read h3 output. It rewrites counters with \\r, so split on both."""
        buf = b""
        prof_re = re.compile(r"^h3 profile:\s+(.*?)\s{2,}(\S.*?)\s+wall=\s*([\d.]+)s")
        prog_re = re.compile(r"^(.*?)\s{2,}(\d+)/(\d+)\s*$")
        while True:
            chunk = proc.stdout.read(1)
            if not chunk:
                break
            if chunk in (b"\n", b"\r"):
                line = buf.decode("utf-8", "replace").rstrip()
                buf = b""
                if not line:
                    continue
                job.log.append(line)
                if len(job.log) > 400:
                    del job.log[:100]

                m = prog_re.match(line)
                if m:
                    job.phase = m.group(1).strip()
                    job.progress = [int(m.group(2)), int(m.group(3))]
                elif line.startswith("h3 profile:"):
                    pm = prof_re.match(line)
                    if pm:
                        job.profile.append({
                            "component": pm.group(1).strip(),
                            "stage": pm.group(2).strip(),
                            "wall": float(pm.group(3)),
                        })
                self.emit("job", job.summary())
                self.emit("terminal", {"line": line, "running": True})
            else:
                buf += chunk


RUNNER: Runner = None


# ---------------------------------------------------------------- helpers

def safe_stem(text):
    stem = re.sub(r"[^a-zA-Z0-9._-]+", "-", text).strip("-").lower()
    return (stem or "take")[:48]


def default_session_settings(name):
    return {
        "session_name": safe_stem(name or "session-1"),
        "label": "",
        "prompt": "",
        "prompt_doc": [{"type": "text", "value": ""}],
        "width": 512,
        "height": 512,
        "frames": 22,
        "steps": 4,
        "layers": 50,
        "reuse": 1,
        "seed": 42,
        "run_mode": "oneshot",
        "token_reduction": False,
        "int8_row_fc2": False,
        "ssd_streaming": False,
        "refs": [],
        "env": {"H3_ZERO_COPY_WEIGHTS": "0"},
        "takes": [],
    }


def snap_frames(requested):
    for f in LEGAL_FRAMES:
        if f >= requested:
            return f
    return LEGAL_FRAMES[-1]


def write_sidecar(out_path, job):
    meta = job.summary()
    if job.started and job.finished:
        meta["duration_s"] = round(job.finished - job.started, 2)
    out_path.with_suffix(".json").write_text(json.dumps(meta, indent=2))


def session_path(name):
    return CFG.session_setting(name)


def save_session(params):
    name = safe_stem(params.get("session_name") or params.get("label") or "session-1")
    path = CFG.session_setting(name)
    params = dict(params)
    params["session_name"] = name
    existing = {}
    if path.exists():
        try:
            existing = json.loads(path.read_text())
        except (OSError, json.JSONDecodeError):
            existing = {}
    takes = existing.get("takes", [])
    if not isinstance(takes, list):
        takes = []
    params["takes"] = takes
    path.write_text(json.dumps(params, indent=2) + "\n")
    return name


def record_take(job):
    """Persist each completed render in its session settings."""
    name = safe_stem(job.params.get("session_name") or "session-1")
    path = CFG.session_setting(name)
    try:
        data = json.loads(path.read_text()) if path.exists() else {}
    except (OSError, json.JSONDecodeError):
        data = {}
    takes = data.get("takes", [])
    if not isinstance(takes, list):
        takes = []
    entry = job.summary()
    if job.started and job.finished:
        entry["duration_s"] = round(job.finished - job.started, 2)
    takes.append(entry)
    data["session_name"] = name
    data["takes"] = takes
    path.write_text(json.dumps(data, indent=2) + "\n")


def list_sessions():
    sessions = []
    for root in sorted(CFG.sessions.iterdir()):
        path = root / "setting.json"
        if not root.is_dir() or not path.exists():
            continue
        try:
            data = json.loads(path.read_text())
        except (OSError, json.JSONDecodeError):
            continue
        sessions.append({"name": root.name, "params": data, "mtime": path.stat().st_mtime})
    return sessions


def list_inputs():
    exts = {".png", ".jpg", ".jpeg", ".webp", ".mp4", ".mov", ".wav", ".mp3", ".m4a"}
    items = []
    for p in sorted(CFG.inputs.iterdir()):
        if p.is_file() and p.suffix.lower() in exts:
            kind = ("image" if p.suffix.lower() in {".png", ".jpg", ".jpeg", ".webp"}
                    else "video" if p.suffix.lower() in {".mp4", ".mov"} else "audio")
            item = {"name": p.name, "kind": kind, "size": p.stat().st_size}
            if kind in {"video", "audio"}:
                item["duration"] = probe_duration(p)
            items.append(item)
    return items


def probe_duration(path):
    try:
        result = subprocess.run(
            ["ffprobe", "-v", "error", "-show_entries", "format=duration",
             "-of", "csv=p=0", str(path)],
            check=True, capture_output=True, text=True, timeout=10,
        )
        return round(float(result.stdout.strip()), 2)
    except (OSError, ValueError, subprocess.SubprocessError):
        return None


def list_outputs():
    items = []
    for p in sorted(CFG.outputs.glob("*.mp4"), key=lambda x: x.stat().st_mtime, reverse=True):
        meta = {}
        side = p.with_suffix(".json")
        if side.exists():
            try:
                meta = json.loads(side.read_text())
            except Exception:                              # noqa: BLE001
                meta = {}
        items.append({
            "name": p.name,
            "size": p.stat().st_size,
            "mtime": p.stat().st_mtime,
            "meta": meta,
        })
    return items[:80]


def extract_last_frame(video_name):
    """Pull the final frame of a render into input/ so it can anchor the next shot."""
    src = CFG.outputs / video_name
    if not src.exists():
        raise FileNotFoundError(video_name)
    dst = CFG.inputs / f"{src.stem}-lastframe.png"
    subprocess.run(
        [CFG.ffmpeg, "-y", "-sseof", "-0.2", "-i", str(src),
         "-vsync", "0", "-update", "1", "-q:v", "2", str(dst)],
        check=True, capture_output=True,
    )
    return dst.name


def validate(params):
    errs = []
    w, h = int(params.get("width", 0)), int(params.get("height", 0))
    if w % 32 or h % 32:
        errs.append("Width and height must be multiples of 32.")
    if w < 32 or h < 32:
        errs.append("Width and height must be at least 32.")
    if w * h > MAX_PIXELS:
        errs.append(f"{w}x{h} is {w*h:,} pixels; the ceiling is {MAX_PIXELS:,}.")
    if not params.get("prompt", "").strip():
        errs.append("Write a prompt.")
    refs = params.get("refs") or []
    if not isinstance(refs, list):
        errs.append("References must be an ordered list.")
        refs = []
    for ref in refs:
        if not isinstance(ref, dict) or ref.get("kind") not in {"image", "video", "audio"}:
            errs.append("Each reference must have a valid kind.")
            continue
        if ref.get("kind") == "video" and ref.get("mode") == "replace" and not ref.get("pairedAudio"):
            errs.append("Replacement audio is required for videos in replace mode.")
        try:
            duration = float(ref.get("duration") or 0)
        except (TypeError, ValueError):
            errs.append(f"Invalid duration for {ref.get('name', 'reference')}.")
            duration = 0
        if ref.get("kind") in {"video", "audio"} and duration and not 2 <= duration <= 15:
            errs.append(f"{ref.get('name', 'Reference')} must be between 2 and 15 seconds.")
    anchors = params.get("first_frame") or params.get("last_frame")
    if refs and anchors:
        errs.append("Ref2VA references can't be combined with first/last frame anchors.")
    images = [ref for ref in refs if ref.get("kind") == "image"]
    videos = [ref for ref in refs if ref.get("kind") == "video"]
    audio = [ref for ref in refs if ref.get("kind") == "audio"]
    if audio and not (images or videos):
        errs.append("A standalone audio reference must accompany an image or video.")
    if len(images) > 9:
        errs.append("At most 9 image references.")
    if len(videos) > 3:
        errs.append("At most 3 video references.")
    if len(audio) > 3:
        errs.append("At most 3 audio references.")
    duration = 0
    for ref in refs:
        if isinstance(ref, dict):
            try:
                duration += float(ref.get("duration") or 0)
            except (TypeError, ValueError):
                pass
    if duration > 15:
        errs.append(f"Combined reference duration is {duration:.1f}s; the limit is 15s.")
    return errs


# ---------------------------------------------------------------- http

class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def handle(self):
        try:
            super().handle()
        except (BrokenPipeError, ConnectionResetError):
            pass

    def log_message(self, format, *args):
        pass

    # -- plumbing

    def _send(self, code, body=b"", ctype="application/json", extra=None):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        for k, v in (extra or {}).items():
            self.send_header(k, v)
        self.end_headers()
        if body:
            self.wfile.write(body)

    def _json(self, obj, code=200):
        self._send(code, json.dumps(obj).encode(), "application/json")

    def _body(self):
        n = int(self.headers.get("Content-Length", 0))
        return self.rfile.read(n) if n else b""

    def _file(self, path: Path, download=False):
        if not path.exists() or not path.is_file():
            return self._send(404, b'{"error":"not found"}')
        ctype = mimetypes.guess_type(path.name)[0] or "application/octet-stream"
        data = path.read_bytes()
        extra = {"Accept-Ranges": "bytes"}
        if download:
            extra["Content-Disposition"] = f'attachment; filename="{path.name}"'
        # Range support so <video> can seek
        rng = self.headers.get("Range")
        if rng and rng.startswith("bytes="):
            try:
                start_s, _, end_s = rng[6:].partition("-")
                start = int(start_s) if start_s else 0
                end = int(end_s) if end_s else len(data) - 1
                end = min(end, len(data) - 1)
                part = data[start:end + 1]
                self.send_response(206)
                self.send_header("Content-Type", ctype)
                self.send_header("Content-Range", f"bytes {start}-{end}/{len(data)}")
                self.send_header("Content-Length", str(len(part)))
                self.send_header("Accept-Ranges", "bytes")
                self.end_headers()
                self.wfile.write(part)
                return
            except Exception:                              # noqa: BLE001
                pass
        self._send(200, data, ctype, extra)

    # -- routes

    def do_GET(self):
        u = urlparse(self.path)
        p = unquote(u.path)

        if p == "/":
            return self._file(STATIC / "index.html")
        if p.startswith("/static/"):
            target = (STATIC / p[len("/static/"):]).resolve()
            if STATIC in target.parents or target.parent == STATIC:
                return self._file(target)
            return self._send(403, b'{"error":"forbidden"}')

        if p == "/api/config":
            return self._json({
                "h3": str(CFG.h3),
                "model": str(CFG.model),
                "workdir": str(CFG.workdir),
                "inputs": str(CFG.inputs),
                "outputs": str(CFG.outputs),
                "session": CFG.active_session,
                "setting": str(CFG.setting_file),
                "session_setting": str(CFG.session_setting(CFG.active_session)),
                "interactive": True,
                "legal_frames": LEGAL_FRAMES,
                "max_pixels": MAX_PIXELS,
            })
        if p == "/api/inputs":
            return self._json(list_inputs())
        if p == "/api/outputs":
            return self._json(list_outputs())
        if p == "/api/sessions":
            return self._json(list_sessions())
        if p.startswith("/api/session/"):
            name = Path(p[len("/api/session/"):]).name
            path = session_path(name)
            if not path.exists():
                return self._send(404, b'{"error":"session not found"}')
            CFG.activate_session(name)
            data = json.loads(path.read_text())
            data["session_name"] = safe_stem(name)
            data["input_path"] = str(CFG.inputs)
            data["output_path"] = str(CFG.outputs)
            return self._json(data)
        if p == "/api/queue":
            return self._json({"queue": RUNNER.queue_state(),
                               "history": RUNNER.history()})
        if p.startswith("/media/input/"):
            return self._file(CFG.inputs / Path(p[len("/media/input/"):]).name)
        if p.startswith("/media/output/"):
            return self._file(CFG.outputs / Path(p[len("/media/output/"):]).name)
        if p.startswith("/download/"):
            return self._file(CFG.outputs / Path(p[len("/download/"):]).name, download=True)
        if p == "/api/events":
            return self._events()

        return self._send(404, b'{"error":"not found"}')

    def do_POST(self):
        u = urlparse(self.path)
        p = unquote(u.path)
        if p == "/api/upload":
            return self._upload()
        raw = self._body()
        try:
            data = json.loads(raw or b"{}")
        except json.JSONDecodeError:
            return self._json({"error": "bad json"}, 400)

        if p == "/api/terminal":
            command = str(data.get("command", "")).strip()
            if not command:
                return self._json({"error": "command is required"}, 400)
            if not RUNNER.run_terminal(command):
                return self._json({"error": "terminal is already running a command"}, 409)
            return self._json({"started": True})

        if p == "/api/interactive/load":
            ok, error = RUNNER.load_interactive(data)
            return self._json({"started": ok, "error": error} if error else {"started": True},
                              409 if error else 200)

        if p == "/api/interactive/input":
            ok, error = RUNNER.send_interactive(data.get("line", ""))
            return self._json({"sent": ok, "error": error} if error else {"sent": True},
                              409 if error else 200)

        if p == "/api/model":
            model = Path(str(data.get("model", ""))).expanduser().resolve()
            if not model.is_dir():
                return self._json({"error": "model directory does not exist"}, 400)
            if RUNNER.interactive_proc is not None or RUNNER.current is not None:
                return self._json({"error": "stop the active h3 process before changing the model"}, 409)
            CFG.model = model
            CFG.model_file.write_text(json.dumps({"model": str(model)}, indent=2) + "\n")
            return self._json({"model": str(model)})

        if p == "/api/h3":
            h3 = Path(str(data.get("h3", ""))).expanduser().resolve()
            if not h3.is_file() or not os.access(h3, os.X_OK):
                return self._json({"error": "h3 executable does not exist or is not executable"}, 400)
            if RUNNER.interactive_proc is not None or RUNNER.current is not None:
                return self._json({"error": "stop the active h3 process before changing the binary"}, 409)
            CFG.h3 = h3
            CFG.workdir = h3.parent
            CFG.h3_file.write_text(json.dumps({"h3": str(h3)}, indent=2) + "\n")
            return self._json({"h3": str(h3)})

        if p == "/api/session/activate":
            name = safe_stem(data.get("name") or "default")
            CFG.activate_session(name)
            return self._json({
                "name": name,
                "inputs": str(CFG.inputs),
                "outputs": str(CFG.outputs),
            })

        if p == "/api/session/save":
            name = save_session(data)
            CFG.activate_session(name)
            return self._json({"name": name, "inputs": str(CFG.inputs),
                               "outputs": str(CFG.outputs)})

        if p == "/api/render":
            data["frames"] = snap_frames(int(data.get("frames", 22)))
            errs = validate(data)
            if errs:
                return self._json({"errors": errs}, 400)
            data["session_name"] = save_session(data)
            CFG.activate_session(data["session_name"])
            job = RUNNER.submit(data)
            return self._json(job.summary())

        if p == "/api/cancel":
            ok = RUNNER.cancel(data.get("id", ""))
            return self._json({"ok": ok})

        if p == "/api/chain":
            try:
                name = extract_last_frame(data["name"])
            except subprocess.CalledProcessError as exc:
                return self._json({"error": exc.stderr.decode()[:400]}, 500)
            except Exception as exc:                       # noqa: BLE001
                return self._json({"error": str(exc)}, 400)
            return self._json({"name": name, "inputs": list_inputs()})

        if p == "/api/delete":
            name = Path(data.get("name", "")).name
            kind = data.get("kind", "output")
            if kind not in ("input", "output"):
                return self._json({"error": "invalid delete kind"}, 400)
            root = CFG.inputs if kind == "input" else CFG.outputs
            target = root / name
            for f in ((target,) if kind == "input"
                      else (target, target.with_suffix(".json"))):
                if f.exists():
                    f.unlink()
            if kind == "output":
                path = CFG.session_setting(CFG.active_session)
                try:
                    settings = json.loads(path.read_text())
                    takes = settings.get("takes", [])
                    if isinstance(takes, list):
                        settings["takes"] = [
                            take for take in takes if take.get("output") != name
                        ]
                        path.write_text(json.dumps(settings, indent=2) + "\n")
                except (OSError, json.JSONDecodeError):
                    pass
            RUNNER.emit("inputs" if kind == "input" else "outputs",
                        list_inputs() if kind == "input" else list_outputs())
            return self._json({
                "inputs": list_inputs(),
                "outputs": list_outputs(),
            })

        return self._send(404, b'{"error":"not found"}')

    def _upload(self):
        """Raw body upload; filename comes from the X-Filename header."""
        name = Path(unquote(self.headers.get("X-Filename", "upload.png"))).name
        data = self._body()
        if not data:
            return self._json({"error": "empty upload"}, 400)
        dst = CFG.inputs / name
        i = 1
        while dst.exists():
            dst = CFG.inputs / f"{Path(name).stem}-{i}{Path(name).suffix}"
            i += 1
        dst.write_bytes(data)
        RUNNER.emit("inputs", list_inputs())
        item = next((entry for entry in list_inputs() if entry["name"] == dst.name), None)
        return self._json({"name": dst.name, "kind": item["kind"] if item else None,
                           "duration": item.get("duration") if item else None,
                           "inputs": list_inputs()})

    def _events(self):
        q = RUNNER.subscribe()
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "keep-alive")
        self.end_headers()
        try:
            hello = json.dumps({"kind": "hello", "payload": {
                "queue": RUNNER.queue_state(),
                "history": RUNNER.history(),
                "outputs": list_outputs(),
                "inputs": list_inputs(),
                "terminal_log": CFG.terminal_log().read_text(encoding="utf-8"),
            }})
            self.wfile.write(f"data: {hello}\n\n".encode())
            self.wfile.flush()
            while True:
                try:
                    msg = q.get(timeout=15)
                    self.wfile.write(f"data: {msg}\n\n".encode())
                except queue.Empty:
                    self.wfile.write(b": keepalive\n\n")
                self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            pass
        finally:
            RUNNER.unsubscribe(q)


def main():
    global CFG, RUNNER
    ap = argparse.ArgumentParser(description="Local web UI for h3.c")
    ap.add_argument("--h3", required=True, help="path to the h3 binary")
    ap.add_argument("--model", required=True, help="path to the MiniMax-H3 directory")
    ap.add_argument("--port", type=int, default=8710)
    ap.add_argument("--host", default="127.0.0.1")
    args = ap.parse_args()

    CFG = Config(args)
    if not CFG.h3.exists():
        raise SystemExit(f"h3 binary not found: {CFG.h3}")
    if not CFG.model.exists():
        raise SystemExit(f"model directory not found: {CFG.model}")
    CFG.model_file.write_text(json.dumps({"model": str(CFG.model)}, indent=2) + "\n")
    CFG.h3_file.write_text(json.dumps({"h3": str(CFG.h3)}, indent=2) + "\n")

    RUNNER = Runner()
    srv = ThreadingHTTPServer((args.host, args.port), Handler)
    srv.daemon_threads = True
    print(f"h3 studio  →  http://{args.host}:{args.port}")
    print(f"  binary   {CFG.h3}")
    print(f"  model    {CFG.model}")
    print(f"  input    {CFG.inputs}")
    print(f"  outputs  {CFG.outputs}")
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        print("\nstopped")
    finally:
        RUNNER.stop_interactive()
        srv.server_close()


if __name__ == "__main__":
    main()
