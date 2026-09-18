# h3 studio

**A local web control surface for [h3.c](https://github.com/janishar/h3.c)** — native
MiniMax-H3 video/audio inference on Apple Silicon.

h3 studio is a Go web server (no JS build step, one Go dependency) that drives
the `h3` binary: it builds the CLI arguments, runs one-shot or interactive
sessions, manages references and anchors, queues renders, chains shots
together, and surfaces live profiling output — all from a browser tab, with
nothing sent off your machine.

It runs as a [helmstudio][helmstudio] studio, and only that way: helmstudio
installs it, launches it, hands it the directory it keeps sessions in, and
takes every finished take into the library it shares with the other studios.
The one Go dependency is helmstudio's runtime SDK. Start at
[Set up helmstudio](#set-up-helmstudio).

[![Go](https://img.shields.io/badge/Go-1.27%2B-00ADD8?logo=go&logoColor=white)](go.mod)
[![Platform](https://img.shields.io/badge/platform-macOS%20%28Apple%20Silicon%29-lightgrey?logo=apple)](#requirements)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)

<p align="center">
  <img src="docs/assets/screenshot-1.png" alt="h3 studio web UI: a render in progress with live preview, terminal output, take history, and timeline panels" width="100%">
</p>

## Motivation

Video diffusion on Apple Silicon is underserved. ComfyUI has no first-class
MLX support, so it's stuck running these models through PyTorch's `mps`
backend — which is slow for this workload and holds onto a lot more unified
memory than the model actually needs, on hardware where that memory is
shared with everything else running. h3.c takes a different approach: it's a
native Metal implementation with no PyTorch/MLX in the loop, built
specifically for MiniMax-H3 on Apple GPUs. h3 studio exists to make that
engine usable as a real tool — a browser UI over the CLI — without pulling in
a Python stack or a node-graph app to get there.

## Table of contents

- [Motivation](#motivation)
- [Requirements](#requirements)
- [Installation](#installation)
- [Downloading the weights (deduplicated)](#downloading-the-weights-deduplicated)
- [Usage](#usage)
  - [Set up helmstudio](#set-up-helmstudio)
  - [Running](#running)
  - [What helmstudio adds](#what-helmstudio-adds)
  - [Running from a checkout](#running-from-a-checkout)
  - [Run from VS Code](#run-from-vs-code)
- [Features](#features)
- [Sessions and state](#sessions-and-state)
- [Notes for an external drive](#notes-for-an-external-drive)
- [Security](#security)
- [Limits](#limits)
- [Contributing](#contributing)
- [License](#license)
- [Acknowledgments](#acknowledgments)

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
- **Unified memory:** validated on a 64 GB MacBook Pro (M5). Lower-memory
  Macs can still run smaller canvases and `--ssd-streaming`, but should
  expect to tune the model flags described in [`h3c/README.md`](h3c/README.md).
- **Disk:** the full MiniMax-H3 checkpoint (both pipelines) is about **196 GB**
  — `FL2VA` (~62 GB) for prompt/first-last-frame generation and `Ref2VA`
  (~134 GB) for reference-conditioned generation. Both pipelines duplicate
  most of their weights, so this can be brought down to **~66 GB**; see
  [Downloading the weights (deduplicated)](#downloading-the-weights-deduplicated).
  Fast local storage (internal NVMe) is recommended; see
  [Notes for an external drive](#notes-for-an-external-drive) if the
  checkpoint lives on external storage.

### Toolchain

- **helmstudio's `helm`** — what runs h3 studio, and what installs it; the
  studio does not start on its own. See
  [Set up helmstudio](#set-up-helmstudio). The rest of this list is what its
  manifest asks for before it will install the studio (`requires.tools`).
- **Go 1.27+** to build h3 studio itself (`go.mod` pins `go 1.27.1`). The build
  fetches one module — helmstudio's runtime SDK — so the first build wants the
  Go module proxy; nothing else is vendored, generated or downloaded.
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
Hugging Face, laid out as an `FL2VA/` and a `Ref2VA/` pipeline directory (each
with `text_encoder/`, `tokenizer/`, `processor/`, `transformer/`,
`video_vae/`, `audio_vae/`, and a `model_index.json`). Review that model's own
license and usage terms on Hugging Face before downloading — it is not
covered by this repository's license (see [License](#license)).

## Installation

Installing h3 studio is helmstudio's job: it runs the steps below itself, from
this repository's own manifest. If you want to *use* the studio, that is the
whole of it — see [Set up helmstudio](#set-up-helmstudio).

What follows is the same thing by hand, for working on the studio. It ends at a
built binary, which does not run on its own; starting it is
[Running from a checkout](#running-from-a-checkout).

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
directory keeps the `FL2VA/` and `Ref2VA/` layout above. To download only
~66 GB instead, see [Downloading the weights (deduplicated)](#downloading-the-weights-deduplicated)
below before running this step.

### 4. Build h3 studio

```bash
GOCACHE=$(pwd)/.gocache go build -o ./dist/h3studio .
```

The web UI is embedded in the binary, and the first build downloads
helmstudio's runtime SDK from the Go module proxy. The binary does not start by
itself — it wants a helmstudio to run under, which
[Running from a checkout](#running-from-a-checkout) gives it.

## Downloading the weights (deduplicated)

`MiniMaxAI/MiniMax-H3` ships two variants, `Ref2VA` and `FL2VA`, which share
everything except the transformer. Downloading both in full costs ~144 GB.
Verified by SHA256: `video_vae`, `audio_vae`, `tokenizer`, `processor` and
`text_encoder` are byte-identical across the two; only the transformer shards
differ (same sizes, different hashes — a consistent sharding config, not
shared weights).

Fetching the shared components once and symlinking them brings the download
down to **~66 GB**.

### 1. Download Ref2VA in full

```bash
hf download MiniMaxAI/MiniMax-H3 --local-dir ./MiniMax-H3 \
  --include "Ref2VA/*"
```

### 2. Symlink the shared components into FL2VA

```bash
cd MiniMax-H3
mkdir -p FL2VA
ln -s ../Ref2VA/text_encoder FL2VA/text_encoder
ln -s ../Ref2VA/video_vae    FL2VA/video_vae
ln -s ../Ref2VA/audio_vae    FL2VA/audio_vae
ln -s ../Ref2VA/tokenizer    FL2VA/tokenizer
ln -s ../Ref2VA/processor    FL2VA/processor
cd ..
```

### 3. Download only the FL2VA transformer

```bash
hf download MiniMaxAI/MiniMax-H3 --local-dir ./MiniMax-H3 \
  --include "FL2VA/transformer/*" \
  --include "FL2VA/model_index.json"
```

### 4. Verify

```bash
ls -la MiniMax-H3/FL2VA/          # expect five symlinks → ../Ref2VA/...
du -sh MiniMax-H3                 # expect ~66 GB
./h3 --info -d ./MiniMax-H3       # confirms h3.c accepts the tree
```

> **Note:** `hf download` can overwrite symlinks when writing into a directory
> that already contains them. If step 3 replaces them, download the transformer
> to a scratch directory and move it into place, then recreate the symlinks.

> **Note:** symlinks require the weights to live on a filesystem that supports
> them. APFS and ext4 are fine; exFAT is not.

## Usage

### Set up helmstudio

h3 studio runs under [helmstudio][helmstudio] and does not start without it.
Where it keeps sessions and what it records takes with both come from whatever
launched it, and it invents neither — started by hand, it stops:

```
h3 studio runs under helmstudio. HELM_API is not set, so there is no platform to run under.
  installed:  start it from helmstudio's Studios list
  a checkout: bash scripts/run.sh
```

So installing it is helmstudio's job, and there is nothing to clone:

1. **Install helm**, helmstudio's launcher
   ([other ways to install it][helm-install]):

   ```bash
   /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/janishar/helmstudio/main/installer/install.sh)"
   ```

2. **Install h3 studio from helmstudio's Studios list.** It reads this
   repository's own [`helmstudio.yaml`](helmstudio.yaml) and shows you every
   command it will run and every weight it will fetch before anything executes:
   the h3.c engine (`make -j8` in the submodule), the studio server
   (`go build`), and the MiniMax-H3 checkpoint — or a link to one you already
   have. `Ref2VA` is optional there, so the install can be `FL2VA` alone
   (~62 GB) with References mode added later.

3. **Start it from helmstudio**, which gives it a port, the `FL2VA` path and a
   data directory of its own, and opens its page.

Working *on* h3 studio rather than using it means a checkout and `helm dev`
instead — see [Running from a checkout](#running-from-a-checkout).

[helmstudio]: https://github.com/janishar/helmstudio

[helm-install]: https://github.com/janishar/helmstudio#getting-started

### Running

What helmstudio launches is the same `dist/h3studio` its build steps produced:

```bash
./dist/h3studio --h3 ./h3c/h3 --model <FL2VA> --port <port> --root <data>
```

That is `helmstudio.yaml`'s `processes[0].cmd` with `{port}` and `{data}`
filled in. The paths are remembered in `<data>/sessions/h3.json` and
`<data>/sessions/model.json`, so a later launch can omit them; a flag or
environment variable always wins over the remembered value.

**There is no authentication** — see [Security](#security) before binding to
anything other than `127.0.0.1`.

| Flag | Default | Description |
| --- | --- | --- |
| `--root` | *(required)* | Directory holding `sessions/` — helmstudio's data directory for this studio. The studio creates none of its own and will not start without one. |
| `--h3` | `$H3STUDIO_H3`, else last used | Path to the built `h3` binary. |
| `--model` | `$H3STUDIO_MODEL`, else last used | Path to the MiniMax-H3 checkpoint directory. |
| `--host` | `127.0.0.1` | Bind address. A warning is printed for anything but loopback. |
| `--port` | `8710` | Bind port; helmstudio passes the one it allocated. |
| `--dev` | `false` | Serve `static/` from disk with `Cache-Control: no-store`. It looks where the studio was launched from, then beside the binary — not under `--root`, which holds no source. |
| `--allow-shell` | `false` | Enable the `$` shell terminal and changing the h3 binary from the browser. |
| `--allow-host` | *(none)* | Extra `Host` names to accept (comma-separated). IP addresses and `localhost` are always accepted. |

Choose **One-shot** or **Interactive** in the render bar at the bottom of the
left pane. Interactive h3.c starts when you click **Load h3.c** or send the
first interactive render, so starting the studio itself never loads the model.
**⌘/Ctrl+Enter** renders from anywhere; **⇧⌘/Ctrl+Enter** queues three seeds.

### What helmstudio adds

h3 studio draws its own page — the same form, the same takes rail, the same
viewer, the same terminal — and helmstudio adds four things around it:

| | |
| --- | --- |
| **Gallery** | The **Gallery** button in the top bar opens helmstudio's own grid over the takes this studio recorded, live — a take that finishes appears without a reload. The grid is scoped to this studio; helmstudio's library is where these sit beside what other studios made. |
| **Timeline** | **Create Timeline** opens a helmstudio sequence: clips trimmed and dissolved, exported as a job it runs for you. Clips are picked from this studio's takes, though the sequence is helmstudio's and can hold any studio's. |
| **Render log** | A third tab in the Terminal panel, streaming the render as helmstudio sees it — it reconnects after a dropped stream and tells you what it missed. The pane's **follow** and **Clear** belong to Output and are hidden while this tab is showing — helm-terminal brings its own. |
| **The launcher** | A render appears in helmstudio as a job with its progress, so what this studio is doing is visible from outside it. |

All of it arrives through the same-origin `/helm/` proxy the server mounts, so
the page holds no token of helmstudio's. The theme and this studio's own colour
come the same way, live from whatever is running it.

If those components cannot be loaded while the page is opening, the page still
works: it falls back to its vendored copy of helm-css and its own theme switch,
the Gallery button and the Render log tab stay hidden, and **Create Timeline**
opens h3 studio's own combine-videos editor.

### Running from a checkout

This is the developer's path — a checkout, run against helmstudio's platform
API without installing anything into helmstudio. Someone *using* h3 studio
installs it from the catalogue instead; see
[Set up helmstudio](#set-up-helmstudio).

```bash
H3_MODEL=/path/to/MiniMax-H3 bash scripts/run.sh
bash scripts/run.sh stop
```

The script runs the studio under `helm dev`, which is what gives it the
platform it will not start without, a data directory to keep sessions in, and
the `/helm/` proxy the page reads helm-css, the theme and this studio's hue
from.

`helm` comes from [helmstudio's installer][helm-install] and needs no
helmstudio checkout; `H3_MODEL` points at a MiniMax-H3 directory you already
have, which is linked per file rather than downloaded, so nothing is written
into it. `HELM` names a particular helm if you do not want the one on `PATH`.
Everything the studio keeps — sessions included — goes to `.helm/` at the top
of the checkout, and what the script itself makes (its pid file, the debugger's
shim) to `.cache/h3-studio` and `dist/`. All three are gitignored, so a run
leaves the working tree as it found it.

**`helm dev` runs no build steps** — the checkout is yours — so the script
builds `dist/h3studio` first. A stale binary is the difference between the
`/helm/` proxy answering and returning 404. The manifest's command carries no
`--dev`, so a front-end edit means running the script again rather than
refreshing the browser.

To debug it there, set `H3_DLV` to a port:

```bash
H3_MODEL=/path/to/MiniMax-H3 H3_DLV=2345 bash scripts/run.sh
```

`helm dev` hands the studio a restricted environment, and the manifest names
`./dist/h3studio` rather than a debugger, so the binary moves aside and that
name becomes a shim running it under [Delve][dlv], which listens on the port.
It is built with `-N -l` so stepping follows the source, and Delve is given
`--continue` so the studio starts rather than waiting for a client. A run
without `H3_DLV` builds a normal binary over the shim again.

[dlv]: https://github.com/go-delve/delve

### Run from VS Code

There is one launch configuration, because there is one way to run the studio.
**h3 studio** (`Cmd+Shift+D`, then `F5`) runs the **debug: h3 studio** task —
`scripts/run.sh` with `H3_DLV=2345` — and attaches the Go extension's debugger
to the Delve in front of the binary; ending the session runs
**stop: h3 studio**.

`.vscode/tasks.json` holds that task and five more: **run: h3 studio** and
**stop: h3 studio** for a run without the debugger, plus **build: h3studio
(dist)**, **test: go (race)** and **test: canvas.js (node)**. The checkpoint
each run uses is `H3_MODEL` in that file — edit it there.

## Features

**Reference ordering is explicit.** References are numbered `Picture 1`,
`Picture 2` in list order, and you drag to reorder. Since filenames mean
nothing to the model and position is what it reads, getting this wrong
silently produces the wrong shot.

**Three conditioning modes.** **Prompt** (text only), **Anchors** (first/last
frame, FL2VA) and **References** (ordered Ref2VA images, clips and audio). Each
mode keeps its own inputs, and only the active one is sent to h3. References
are disabled with an explanation when the model has no `Ref2VA/` pipeline.

**Illegal settings are caught before launch.** The server validates every
render — canvas on the 32-pixel grid and under 768×1344, the 5+17n frame grid,
reference counts and durations, inputs that actually exist in the session —
and the same errors show in the render bar as you edit. **Command** shows the
exact argv (or REPL commands) the server will run, built by the same code that
runs it.

**Canvas by aspect ratio.** Pick 16:9, 9:16, 1:1, 4:3, 3:4, 3:2, 2:3, 21:9,
**Match input** (follows the first anchor or image reference) or Custom, then
drag **Megapixels**; the studio solves the closest legal size and shows the
latent size. A warning appears when an anchor's aspect would be stretched.

**One-shot and interactive rendering.** One-shot spawns a fresh `h3` process
per render. Interactive mode keeps `h3` resident, so repeated **Send to h3.c**
renders skip the model load and only pay for re-encoding the changed
prompt/conditioning. You can also type commands and prompts at the `h3>`
console; a queued studio render waits for a manual prompt to finish first, and
each render's output is tracked in its own directory so the two never get
mixed up.

**Continue generation from any take.** Every take carries ways to feed
itself back into the next render, so a shot can grow out of whatever you
already generated instead of starting cold:

- **Chain →** extracts the take's last frame, switches the form to anchor
  mode, and sets that frame as the *first* frame of the next shot (clearing
  any existing last-frame anchor) — the fastest way to keep a sequence moving
  forward, e.g. a stationary shot to a walking shot.
- **Use Frame** extracts the take's last frame and adds it as a reference
  instead of forcing a mode switch: in anchor mode it fills whichever of
  first/last is still empty, in Reference mode it's appended as the next
  `Picture N`. Use this when you want the frame as an Ref2VA reference
  alongside others, not as a hard first/last anchor.
- **Use ref** copies the take's whole output video into the session and adds
  it as a `Video N` reference (max 3), for continuing motion/subject
  continuity from the clip itself rather than a single frame.
- The **⋮** menu adds **First frame → input**, **Use audio** (extracts the
  soundtrack as an audio reference), **Previews (N)**, **Add to compare**,
  **Download** and **Delete**.

All of these copy the source file into the session's `inputs/` directory first,
since renders only ever read references from there — takes themselves live in
`outputs/` and are never read back directly.

**Timeline.** Combine multiple takes from a session into a single output
video from the Timeline panel — pick clips, order them, and export. **Create
Timeline** opens helmstudio's sequence editor instead (see
[What helmstudio adds](#what-helmstudio-adds)), and this one is what that button
falls back to when helmstudio's components cannot be loaded; the panel lists
what it made either way. Below, two FL2VA takes from the same session are
combined with it into one continuous shot:

<table>
<tr>
<td width="260" valign="top">

<video src="docs/assets/timeline-1.mp4" controls width="240">
  Your browser does not support embedded video —
  <a href="docs/assets/timeline-1.mp4">download the clip</a> instead.
</video>

https://github.com/user-attachments/assets/d7991a0b-a7eb-44d0-a7d4-5fdf9392eebe

</td>
<td valign="top">

**Part 1** — first frame: uploaded `image.png` (right)
<img src="docs/assets/Image.png" alt="Uploaded first-frame reference" width="60" align="right">
*"She walks away down a rainy boulevard, then turns to face camera."*
480×864 · 124f · steps 8 · seed `989663098`

**Part 2** — first frame: Part 1's last frame, pulled in with **Use Frame**
*"She smiles, waves, and says 'Hi!' continuing the same shot."*
480×864 · 124f · steps 8 · seed `989663098` (same seed kept for continuity)

Both takes: FL2VA, one-shot mode, combined with the Timeline feature.

</td>
</tr>
</table>

**Reproducibility.** Every render writes a `.json` sidecar next to the MP4
with the full parameter set, the exact argv, an ffprobe summary, the profile
and the saved previews. **Reuse** restores a past take into the form and
**History** in the prompt block brings back earlier prompts. Nothing depends
on you remembering what you did.

**Queue and seed comparison.** One render at a time — one GPU. **Queue 3
seeds** submits the same setup with three random seeds; when they finish, the
viewer opens a synced grid of the three with a **Keep** (star) button on each.
Any two takes can also be compared with an **A/B wipe**, and **★ starred
only** filters the take list.

**Progress you can read.** The running card shows a Load → Encode → Denoise
→ Decode → MP4 stepper, elapsed time and an ETA (measured per denoise step, or
estimated from this session's earlier takes with the same settings — the same
estimate shows in the render bar before you start). The tab title shows
progress, and 🔔 turns on a browser notification when a render finishes.
Failures pop up with the error and, for known problems (out of memory, the
macOS GPU watchdog, missing ffmpeg or model files), a hint about what to do.

**Live preview on disk.** With Live preview on, each decoded preview frame is
written as a PNG under `previews/<job>/` and streamed to the viewer by URL.
After the take finishes the previews are pruned (every frame of the last step,
one frame of each earlier step) and **Previews (N)** scrubs through them.

**Live profile.** The Timing tab charts `--profile` wall time per component
across recent takes (model load separated from compute) and lists the latest
render's rows.

**Model check.** The **Model** button shows whether `FL2VA/` and `Ref2VA/` are
present, whether symlinks resolve, the checkpoint size, and whether `h3`,
`ffmpeg` and `ffprobe` run. The dot next to it turns amber or red when
something needs attention.

**Keyboard.** ⌘/Ctrl+Enter render · ⇧⌘/Ctrl+Enter queue 3 seeds · Esc close
dialogs · Space play/pause · ←/→ step one frame · J/K next/previous take.

## Sessions and state

A session is just a directory: `sessions/<name>/`, inside the data directory
helmstudio gives the studio as `--root`. It holds everything for one line of
work — its inputs, its rendered outputs, and the exact UI state that produced
them. Nothing is copied or duplicated between sessions, so switching sessions
is instant and each one's disk footprint is only what you put in it.

```
sessions/<name>/
├── setting.json     # full UI state: prompt, canvas, quality, refs, anchors, ...
├── terminal.log     # the session's terminal output
├── inputs/          # uploads, extracted frames/audio, takes reused as refs
│                    #   (each with a <name>.json sidecar: original name, probe)
├── outputs/         # rendered .mp4 takes, each with a .json sidecar
├── previews/        # live-preview PNGs, one folder per render
└── timeline/        # combined videos, each with a .json sidecar
```

`.thumbs/` folders next to videos hold cached poster images.

A finished take becomes helmstudio's too, and that changes nothing about the
layout above. It is written to `outputs/` as it always was, and helmstudio
adopts it by hardlink: the same bytes appear in its library under a second
name, at the same inode, counted once, so the footprint above is still the
whole of it. The gallery item carries the sidecar's parameters plus the session
name as `h3_session`, because helmstudio checks session ids against its own
sessions and h3 studio's directories are not those. The directories stay h3
studio's own; only what a take *becomes* — an asset, a gallery item, a clip on
a sequence — is helmstudio's.

Deleting a take here does not undo the adoption: h3 studio removes its own
files, but helmstudio's hardlink keeps the bytes and its gallery item stays, so
a take you want gone entirely has to go there too.

**State** — `setting.json` is written atomically on a debounced auto-save
while you edit the form, so a session reopens exactly where you left
it: prompt text, canvas size, quality settings, every reference and anchor,
and which mode you were in.

**Input** — `inputs/` is the *only* place renders read references from.
Anything the model can see during a render — an uploaded image/video/audio
file, a frame extracted with **Chain →** or **Use Frame**, or a take pulled
back in with **Use ref** — lands here first, even though the original take it
came from lives in `outputs/`.

**Output** — `outputs/` holds only what `h3` produced: the rendered `.mp4`
plus a matching `.json` sidecar with the full parameter set and the exact
argv used for that take (what **Reuse** reads from). The sidecar is the only
record of a take — deleting the video deletes its sidecar, thumbnail and
previews too. Takes are read
from here for playback and for the Timeline, but never read back into a
render directly — continuing from one always goes through `inputs/` first
(see [Continue generation from any take](#features) above).

Session bookkeeping lives one level up: the last active session is tracked in
`sessions/last_session.json` and restored when the web UI starts (creating
`session-1` if nothing exists yet). Every API call names its session, so two
browser tabs can work in different sessions at once. New, duplicate and delete
live in the **⋯** menu next to the session switcher.

## Notes for an external drive

"Copy weights into memory" is on by default and sets `H3_ZERO_COPY_WEIGHTS=0`.
Turn it off if you move the checkpoint to internal storage — zero-copy
mapping is the faster path on NVMe.

The Qwen prefetch fields set `H3_QWEN_PREFETCH_DEPTH` and `H3_QWEN_PREFETCH`.
Defaults assume a 128 GiB machine; raising depth can help hide slow reads.

## Security

h3 studio has no authentication, so it defends the one thing a local tool
must: other websites and other machines driving it.

- It binds to `127.0.0.1` by default and prints a warning for any other
  address. Anyone who can reach the port can run renders.
- Requests whose `Host` header isn't an IP address, `localhost`, the `--host`
  name or an `--allow-host` name are refused, which blocks DNS rebinding.
- State-changing requests must come from the studio's own origin and use a
  JSON content type, so a page you visit can't forge them.
- The `$` shell terminal and changing the h3 binary from the browser are off
  unless you pass `--allow-shell`.
- Only `H3_*` environment variables reach h3, **Extra arguments** only accepts
  `--use-*` switches and `--ref-image-size`, and render inputs must be plain
  file names inside the session's `inputs/`.
- Everything the page uses of helmstudio's — helm-css, the theme stream, this
  studio's hue, the gallery, the render log — comes through the same-origin
  `/helm/` proxy the server mounts, so the browser never holds helmstudio's
  token. The proxy forwards the studio API and the theme stream and nothing
  else — a launcher path (install, launch, stop) is 404 and never reaches the
  daemon.

## Limits

- One render at a time, deliberately.
- Stop sends `SIGTERM` to h3's process group, then `SIGKILL` after 3 seconds.
  Stopping an interactive render unloads h3.c.
- Interactive h3.c accepts image references only; use One-shot for video or
  audio references.
- Extra arguments in Interactive mode apply when h3.c is loaded, not per
  render.
- Recording with helmstudio happens as a take finishes, and nothing is
  reconciled afterwards: a take deleted here stays in helmstudio's gallery.
  Combined timeline videos and preview PNGs are h3 studio's own and are not
  adopted at all.
- `/api/queue` does not answer helmstudio's busy contract yet, so its
  switch-studio dialog reads h3 studio as unknown rather than busy or idle.

## Contributing

Contributions are welcome — bug reports, feature requests, and pull requests
alike. Please read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a PR; it
covers what you need running locally, coding conventions (no Go dependencies
beyond helmstudio's runtime SDK, no frontend build step), the test commands, and
how issues involving the `h3.c` engine itself should be routed to
[its own repository](https://github.com/janishar/h3.c).

## License

h3 studio's own source (the Go server and the static web UI) is licensed
under the [MIT License](LICENSE), © Janishar Ali.

This repository vendors [h3.c](https://github.com/janishar/h3.c) as the `h3c`
git submodule rather than embedding a copy of its source. `h3c` is separately
MIT-licensed (© Salvatore Sanfilippo — see [`h3c/LICENSE`](h3c/LICENSE)) and
carries an additional BSD-3-Clause notice for shader code adapted from a
third-party project (see [`h3c/THIRD_PARTY_NOTICES.md`](h3c/THIRD_PARTY_NOTICES.md)).
Both notices must be preserved if you redistribute `h3c` itself.

The MiniMax-H3 model weights are **not** part of this repository and are
distributed separately by MiniMaxAI under their own license — review
[the model card on Hugging Face](https://huggingface.co/MiniMaxAI/MiniMax-H3)
before use.

## Acknowledgments

- [Salvatore Sanfilippo](https://github.com/janishar/h3.c) for h3.c, the
  native Metal inference engine this project is a control surface for.
- [MiniMaxAI](https://huggingface.co/MiniMaxAI/MiniMax-H3) for the MiniMax-H3
  model.
