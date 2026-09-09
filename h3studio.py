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
        self.inputs = (self.workdir / "input").resolve()
        self.outputs = (self.workdir / "outputs").resolve()
        self.inputs.mkdir(parents=True, exist_ok=True)
        self.outputs.mkdir(parents=True, exist_ok=True)
        self.ffmpeg = shutil.which("ffmpeg") or "ffmpeg"


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
            try:
                self.proc.send_signal(signal.SIGINT)
            except Exception:
                pass
            return True
        return False

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
                self.emit("job", job.summary())
                self.emit("queue", self.queue_state())
                self.emit("outputs", list_outputs())

    def _run(self, job):
        p = job.params
        stem = safe_stem(p.get("label") or "take")
        name = f"{stem}-{datetime.now().strftime('%m%d-%H%M%S')}.mp4"
        out_path = CFG.outputs / name

        cmd = [str(CFG.h3), "--profile", "-d", str(CFG.model)]
        cmd += ["-p", p["prompt"]]

        for img in p.get("ref_images", []):
            cmd += ["--ref-image", str(CFG.inputs / img)]
        for clip in p.get("ref_videos", []):
            flag = "--ref-silent-video" if clip.get("silent") else "--ref-video"
            cmd += [flag, str(CFG.inputs / clip["name"])]
        for aud in p.get("ref_audio", []):
            cmd += ["--ref-audio", str(CFG.inputs / aud)]
        if p.get("first_frame"):
            cmd += ["--first-frame", str(CFG.inputs / p["first_frame"])]
        if p.get("last_frame"):
            cmd += ["--last-frame", str(CFG.inputs / p["last_frame"])]

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
            bufsize=0,
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
                self.emit("line", {"id": job.id, "line": line})
            else:
                buf += chunk


RUNNER: Runner = None


# ---------------------------------------------------------------- helpers

def safe_stem(text):
    stem = re.sub(r"[^a-zA-Z0-9._-]+", "-", text).strip("-").lower()
    return (stem or "take")[:48]


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


def list_inputs():
    exts = {".png", ".jpg", ".jpeg", ".webp", ".mp4", ".mov", ".wav", ".mp3", ".m4a"}
    items = []
    for p in sorted(CFG.inputs.iterdir()):
        if p.is_file() and p.suffix.lower() in exts:
            kind = ("image" if p.suffix.lower() in {".png", ".jpg", ".jpeg", ".webp"}
                    else "video" if p.suffix.lower() in {".mp4", ".mov"} else "audio")
            items.append({"name": p.name, "kind": kind, "size": p.stat().st_size})
    return items


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
    refs = params.get("ref_images") or params.get("ref_videos") or params.get("ref_audio")
    anchors = params.get("first_frame") or params.get("last_frame")
    if refs and anchors:
        errs.append("Ref2VA references can't be combined with first/last frame anchors.")
    if params.get("ref_audio") and not (params.get("ref_images") or params.get("ref_videos")):
        errs.append("A standalone audio reference must accompany an image or video.")
    if len(params.get("ref_images", [])) > 9:
        errs.append("At most 9 image references.")
    return errs


# ---------------------------------------------------------------- http

class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
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
                "legal_frames": LEGAL_FRAMES,
                "max_pixels": MAX_PIXELS,
            })
        if p == "/api/inputs":
            return self._json(list_inputs())
        if p == "/api/outputs":
            return self._json(list_outputs())
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

        if p == "/api/render":
            data["frames"] = snap_frames(int(data.get("frames", 22)))
            errs = validate(data)
            if errs:
                return self._json({"errors": errs}, 400)
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
            for f in (CFG.outputs / name, (CFG.outputs / name).with_suffix(".json")):
                if f.exists():
                    f.unlink()
            return self._json({"outputs": list_outputs()})

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
        return self._json({"name": dst.name, "inputs": list_inputs()})

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


if __name__ == "__main__":
    main()
