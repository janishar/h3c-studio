# h3 studio

A local web control surface for [h3.c](https://github.com/janishar/h3.c) — native
MiniMax-H3 video/audio inference on Apple Silicon. h3 studio is a Go stdlib-only
web server (no third-party JS build step, no third-party Go dependency besides
`fsnotify`) that drives the `h3` binary: it builds the CLI arguments, runs
one-shot or interactive sessions, manages references and anchors, queues
renders, and shows profiling output.

## Requirements

### Hardware and OS

- **Apple Silicon Mac.** h3.c uses native Metal, MetalPerformanceShaders,
  MetalPerformanceShadersGraph, and Accelerate — it does not run on Intel or
  on non-Apple GPUs. M3-class and M5-class chips are the tested targets; M5
  additionally gets native Metal 4/TensorOps fast paths (int8 MLP, quantized
  attention) that M3 falls back from automatically.
- **macOS** recent enough for the Metal 4/TensorOps frameworks (macOS 26.x was
  used for development). Command Line Tools (`clang`) are sufficient to build
  h3.c — a full Xcode install is not required.
- **Unified memory:** 128 GB is the assumed/validated machine size (prefetch
  and residency defaults in h3.c target it). Lower-memory Macs can still run
  smaller canvases and `--ssd-streaming`, but should expect to tune the model
  flags described in `h3c/README.md`.
- **Disk:** the full MiniMax-H3 checkpoint (both pipelines) is about **196 GB**
  — `FL2VA` (~62 GB) for prompt/first-last-frame generation and `Ref2VA`
  (~134 GB) for reference-conditioned generation. Fast local storage (internal
  NVMe) is recommended; see "Notes for an external drive" below if the
  checkpoint lives on external storage.

### Toolchain

- **Go 1.27+** to build h3 studio itself (`go.mod` pins `go 1.27`).
- **Command Line Tools / clang** to build the `h3` binary (h3.c's `Makefile`
  links `Foundation`, `Metal`, `MetalPerformanceShaders`,
  `MetalPerformanceShadersGraph`, and `Accelerate`).
- **FFmpeg and FFprobe on `PATH`** — required by h3.c for decoding reference
  media and encoding MP4 output (`H3_FFMPEG` / `H3_FFPROBE` env vars can point
  at explicit executables instead). Install with `brew install ffmpeg`.
- **git** with submodule support — this repo vendors h3.c as the `h3c`
  submodule.

### Model

h3 studio does not download or convert the model itself; point it at a local
MiniMax-H3 checkpoint directory prepared for h3.c. The published weights are
[`MiniMaxAI/MiniMax-H3`](https://huggingface.co/MiniMaxAI/MiniMax-H3) on
Hugging Face, and are laid out as an `FL2VA/` and a `Ref2VA/` pipeline
directory (each with `text_encoder/`, `tokenizer/`, `processor/`,
`transformer/`, `video_vae/`, `audio_vae/`, and a `model_index.json`).

## Installation

### 1. Clone with the h3.c submodule

```bash
git clone --recurse-submodules https://github.com/janishar/h3c-studio.git
cd h3c-studio
# if already cloned without --recurse-submodules:
git submodule update --init --recursive
```

### 2. Build the h3 binary (h3.c)

```bash
cd h3c
make -j8
cd ..
```

This produces `h3c/h3`. See [h3c/README.md](h3c/README.md) for the full CLI
reference, sampler/preset tuning, and the environment variables used for
performance diagnosis.

### 3. Download the model

```bash
hf download MiniMaxAI/MiniMax-H3 --local-dir /path/to/MiniMax-H3
```

Budget ~196 GB of free disk space. You can substitute any Hugging Face
download method (`hf` CLI, `git lfs clone`, etc.) as long as the resulting
directory keeps the `FL2VA/` and `Ref2VA/` layout above.

### 4. Build h3 studio

```bash
GOCACHE=$(pwd)/.gocache go build -o ./dist/h3studio .
```

## Run - Development (with hot reload)

```bash
./dist/h3studio \
  --h3 ./h3c/h3 \
  --model /path/to/MiniMax-H3 \
  --host 127.0.0.1 \
  --port 8710 \
  --dev
```

Open http://127.0.0.1:8710

Hot reload is **enabled** - static files (CSS/JS) auto-reload on changes.

**Flag:** Use `--dev` to enable hot reload.

## Run - Production

```bash
./dist/h3studio \
  --h3 ./h3c/h3 \
  --model /path/to/MiniMax-H3 \
  --host 0.0.0.0 \
  --port 8710
```

Hot reload is **disabled** for security (no authentication).

Each named session gets its own `sessions/<name>/input/` and
`sessions/<name>/outputs/` directories inside the studio directory. Nothing is
copied or duplicated between sessions.
Each session stores its full UI state in `sessions/<name>/setting.json`.
The last active session is tracked in `sessions/last_session.json` and restored
when the web UI starts. If no session exists yet, `session-1` is created.
Entering an existing session name restores its settings from that session's
`setting.json`.

Choose One-shot or Interactive mode in the web UI. Interactive h3.c is started
only after clicking Load h3.c, so starting the studio itself never loads the
model.

## What it does

**Reference ordering is explicit.** References are numbered `Picture 1`, `Picture 2`
in list order, and you drag to reorder. Since filenames mean nothing to the model
and position is what it reads, getting this wrong silently produces the wrong shot.

**Illegal settings are caught before launch.** Canvas dimensions must be multiples
of 32 and stay under 768×1344; the duration slider only offers the 5+17n frame grid
and shows real seconds; Ref2VA references and first/last anchors are mutually
exclusive and the mode switch enforces it.

**Shot chaining.** "Chain →" on any take extracts its final frame into the
active session's `input/`,
switches to anchor mode, and sets it as the next shot's first frame. That's the
multi-shot continuity loop in two clicks.

**Reproducibility.** Every render writes a `.json` sidecar next to the MP4 with the
full parameter set and the exact argv used. "Reuse settings" restores a past take
into the form. Nothing depends on you remembering what you did.

**Queue.** One render at a time — one GPU. "Queue 3 seeds" submits the same setup
with three random seeds, which is the cheapest way to judge a prompt.

**Live profile.** The Timing tab parses `--profile` output into per-phase wall times,
so you can see load cost against denoise cost directly.

## Notes for an external drive

"Copy weights into memory" is on by default and sets `H3_ZERO_COPY_WEIGHTS=0`.
Turn it off if you move the checkpoint to internal storage — zero-copy mapping is
the faster path on NVMe.

The Qwen prefetch fields set `H3_QWEN_PREFETCH_DEPTH` and `H3_QWEN_PREFETCH`.
Defaults assume a 128 GiB machine; raising depth can help hide slow reads.

## Limits

- One render at a time, deliberately.
- Stop sends SIGTERM, then SIGKILL after a short timeout; h3 may take a moment to unwind.
- Uploads are held in memory before writing, so very large reference videos will
  be slow to attach.
- Bound to 127.0.0.1. There is no authentication — don't expose it.
