# h3 studio

**A local web studio for [h3.c](https://github.com/janishar/h3.c)** — native
MiniMax-H3 video and audio generation on Apple Silicon.

h3 studio is a Go server (no JS build step, one Go dependency) that drives the
`h3` binary: it builds the CLI arguments, runs one-shot or interactive sessions,
manages references and anchors, queues renders, chains shots and charts
profiling — in a browser tab, nothing sent off your machine. It runs as a
[helmstudio][helmstudio] studio and only that way: helmstudio installs it,
launches it, gives it the directory it keeps sessions in, and takes every
finished take into the library it shares with the other studios.

[![Go](https://img.shields.io/badge/Go-1.27%2B-00ADD8?logo=go&logoColor=white)](go.mod)
[![Platform](https://img.shields.io/badge/platform-macOS%20%28Apple%20Silicon%29-lightgrey?logo=apple)](#requirements)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)

<p align="center">
  <img src="docs/assets/screenshot-1.png" alt="h3 studio web UI: a render in progress with live preview, terminal output, take history, and timeline panels" width="100%">
</p>

## Motivation

Video diffusion on Apple Silicon is underserved. ComfyUI has no first-class MLX
support, so it runs these models through PyTorch's `mps` backend — slow for this
workload, and holding far more unified memory than the model needs. h3.c is a
native Metal implementation for MiniMax-H3 with no PyTorch or MLX in the loop;
h3 studio makes it a tool you can use, without a Python stack or a node-graph
app.

## Table of contents

- [Requirements](#requirements) · [Installation](#installation) —
  [Path A: the launcher](#path-a-install-from-the-launcher-recommended) ·
  [Path B: a checkout](#path-b-run-from-a-checkout) ·
  [`scripts/run.sh`](#what-scriptsrunsh-does) ·
  [Troubleshooting](#troubleshooting) · [Debugging](#debugging-and-vs-code)
- [Downloading the weights](#downloading-the-weights-deduplicated)
- [Using the studio](#using-the-studio) — [Flags](#command-line-reference) ·
  [What helmstudio adds](#what-helmstudio-adds)
- [Features](#features) · [Sessions and state](#sessions-and-state) ·
  [External drives](#notes-for-an-external-drive)
- [Security](#security) · [Limits](#limits) ·
  [Contributing](#contributing) · [License](#license) ·
  [Acknowledgments](#acknowledgments)

## Requirements

### Hardware and OS

- **Apple Silicon Mac**, on macOS recent enough for the Metal 4/TensorOps
  frameworks (26.x during development). h3.c uses Metal,
  MetalPerformanceShaders, MetalPerformanceShadersGraph and Accelerate — no
  Intel, no non-Apple GPUs. M3- and M5-class chips are tested; M5 also gets
  native Metal 4/TensorOps fast paths (int8 MLP, quantized attention) that M3
  falls back from automatically.
- **Memory:** validated on a 64 GB MacBook Pro (M5). Smaller Macs can run
  smaller canvases and `--ssd-streaming`, at the cost of tuning the flags in
  [`h3c/README.md`](h3c/README.md).
- **Disk:** ~196 GB for the full checkpoint — `FL2VA` (~62 GB) for prompt and
  first/last-frame generation, `Ref2VA` (~134 GB) for reference-conditioned.
  They duplicate most of their weights, so
  [deduplicating](#downloading-the-weights-deduplicated) brings it to
  **~66 GB**. Internal NVMe is recommended; otherwise see
  [Notes for an external drive](#notes-for-an-external-drive).

### Toolchain

Path A needs only the launcher, which installs the rest and checks this list
itself (the manifest's `requires.tools`). Path B needs all of it:

| Tool | Why | Install |
| --- | --- | --- |
| **the launcher** *(Path A)* | installs, builds and runs the studio; keeps the shared gallery | [Path A](#path-a-install-from-the-launcher-recommended) |
| **`helm`** *(Path B)* | the studio author's CLI; `helm dev` runs this checkout from its manifest | [Path B](#path-b-run-from-a-checkout) |
| **Go 1.27+** | builds the server; its one module, helmstudio's runtime SDK, comes from the Go module proxy | `brew install go` |
| **Command Line Tools** | builds `h3` (Metal, MPS, MPSGraph, Accelerate); full Xcode is not needed | `xcode-select --install` |
| **FFmpeg + FFprobe** | decoding references, probing media, thumbnails, MP4 encoding (`H3_FFMPEG` / `H3_FFPROBE` override the lookup) | `brew install ffmpeg` |
| **git** with submodules | h3.c is vendored as the `h3c` submodule | with the Command Line Tools |

### Model

h3 studio neither downloads nor converts the model; it is pointed at a local
checkpoint prepared for h3.c —
[`MiniMaxAI/MiniMax-H3`](https://huggingface.co/MiniMaxAI/MiniMax-H3), as
`FL2VA/` and `Ref2VA/` pipeline directories (each with `text_encoder/`,
`tokenizer/`, `processor/`, `transformer/`, `video_vae/`, `audio_vae/` and a
`model_index.json`). Review its license first; it is not covered by this
repository's (see [License](#license)).

## Installation

Two ways in — **h3 studio never starts on its own**, since whatever launches it
also gives it a session directory and the library it records takes into.

| | **A — Install it** | **B — Run from a checkout** |
| --- | --- | --- |
| For | using the studio | changing the studio |
| Needs | the **launcher**, helmstudio's daemon and web UI | the **`helm` CLI**; `helm dev` runs one studio, no daemon |
| Install | one click in its library | `git clone`, `make`, `bash scripts/run.sh` |
| Weights | it downloads or links them | you point `H3_MODEL` at a checkpoint |

### Path A: Install from the launcher (recommended)

The launcher clones this repository, runs the build steps in
[`helmstudio.yaml`](helmstudio.yaml) and fetches the weights.

**1. Install the launcher** — helmstudio itself, a daemon and a web UI. Either
**the Mac app** from [its releases][helm-releases], which bundles the daemon and
is **unsigned**, so macOS calls it damaged until you clear the quarantine flag
once (the release notes give the line); or a clone:

```bash
git clone https://github.com/janishar/helmstudio && cd helmstudio && make build && ./bin/helmstudio
```

Then open **http://127.0.0.1:8700**; everything lives in `~/.helmstudio`. Not
the `helm` installer — that is the studio author's CLI, with no library and no
gallery, and is what [Path B](#path-b-run-from-a-checkout) wants.

**2. Install h3 studio from its library.** It is in helmstudio's registry, so it
is already listed. Install it and it first shows every command it will run and
weight it will fetch: the engine (`make -j8` in the submodule), the server
(`go build`) and the checkpoint — or a link to one you have. `Ref2VA` is
optional, so the install can be `FL2VA` alone, with References mode added later.

**3. Start it.** The launcher gives the studio a port, the `FL2VA` path and a
data directory of its own, then opens its page →
[Using the studio](#using-the-studio).

[helmstudio]: https://github.com/janishar/helmstudio
[helm-releases]: https://github.com/janishar/helmstudio/releases
[helm-install]: https://helmstudio.in/docs/install-helm/

### Path B: Run from a checkout

The developer's path: a checkout run against helmstudio's platform API, with
nothing installed into helmstudio. [`scripts/run.sh`](scripts/run.sh) builds the
server, links your checkpoint and starts everything under `helm dev`, so the
engine is the only thing you build by hand.

```bash
# 1. helm, the CLI that runs this checkout, and the rest of the toolchain
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/janishar/helmstudio/main/installer/install.sh)"
xcode-select --install; brew install go ffmpeg

# 2. the checkout, with h3.c as the h3c submodule
git clone --recurse-submodules https://github.com/janishar/h3c-studio.git
cd h3c-studio      # cloned without it: git submodule update --init --recursive

# 3. the engine → h3c/h3, the one thing the script does not build
make -j8 -C h3c

# 4. the weights: ~196 GB, or ~66 GB deduplicated — read the next section first
hf download MiniMaxAI/MiniMax-H3 --local-dir /path/to/MiniMax-H3

# 5. start, or restart; then `stop` to end it
H3_MODEL=/path/to/MiniMax-H3 bash scripts/run.sh
bash scripts/run.sh stop
```

`helm` lands in `~/.local/bin` — no `sudo`, no shell-profile edits, no
helmstudio clone — and the installer prints the `PATH` line if that directory is
not on it; [Install helm][helm-install] covers versions and upgrading. The
launcher is **not** needed, since `helm dev` serves the platform API itself.
The engine's own CLI reference, sampler tuning and performance env vars are in
[h3c/README.md](h3c/README.md). Any download method works (`hf`,
`git lfs clone`, …) as long as the `FL2VA/` and `Ref2VA/` layout survives, and
an existing checkpoint is used as it is — nothing is ever written into it.

The studio prints the URL it came up on: **http://127.0.0.1:8710**, unless that
port is taken and `helm dev` allocates another. Then →
[Using the studio](#using-the-studio).

### What `scripts/run.sh` does

1. **Checks** that `helm` is on `PATH` and `h3c/h3` is built, naming the fix.
2. **Builds `dist/h3studio`,** every run, because `helm dev` runs no build
   steps of its own and would otherwise launch whatever was there.
3. **Links the checkpoint** as the `fl2va` weight when `H3_MODEL` is set, per
   file: nothing is downloaded and the directory is never written to.
4. **Runs `helm dev -f helmstudio.yaml`,** which supplies the platform, a data
   directory and the `/helm/` proxy. Extra arguments go to `helm dev`.

| Variable | What it does |
| --- | --- |
| `H3_MODEL` | Checkpoint to link as `fl2va`. Needed on the first run only — `helm dev` records where the weight was linked. |
| `HELM` | A particular `helm` instead of the one on `PATH`. |
| `H3_DLV` | Port to listen for a debugger on — see [below](#debugging-and-vs-code). |

Sessions and everything else the studio keeps go to `.helm/`, the pid file and
the debugger's shim to `.cache/h3-studio` and `dist/`; all are gitignored. The
manifest's command carries no `--dev`, so a front-end edit means re-running the
script, not refreshing the browser.

### Troubleshooting

| What you see | What it means |
| --- | --- |
| `helm is not installed.` | Run the installer in [Path B](#path-b-run-from-a-checkout), or set `HELM`. |
| `h3c/h3 is not built` | `git submodule update --init --recursive`, then `make -j8 -C h3c`. |
| `weights: no MiniMax-H3 checkpoint at …` | `H3_MODEL` must name the directory holding `FL2VA/` and `Ref2VA/`. |
| `HELM_API is not set…` | The binary was started by hand. Use `bash scripts/run.sh`, or the launcher. |
| `/helm/` assets 404 | A stale `dist/h3studio` — re-run the script. |
| `dlv is not installed` | `go install github.com/go-delve/delve/cmd/dlv@latest` |

### Debugging, and VS Code

```bash
H3_MODEL=/path/to/MiniMax-H3 H3_DLV=2345 bash scripts/run.sh
```

`helm dev` hands the studio a restricted environment and the manifest names
`./dist/h3studio`, not a debugger — so the binary moves aside and that name
becomes a shim running it under [Delve][dlv] on the port, built `-N -l` so
stepping follows the source and given `--continue` so the studio starts instead
of waiting for a client. A run without `H3_DLV` builds over the shim.

VS Code has one launch configuration, because there is one way to run the
studio: **h3 studio** (`F5`) runs **debug: h3 studio** — the script with
`H3_DLV=2345` — and attaches the Go debugger; ending it runs **stop: h3
studio**. `.vscode/tasks.json` holds those and four more (**run: h3 studio**
without the debugger, **build: h3studio (dist)**, **test: go (race)**, **test:
canvas.js (node)**) and the `H3_MODEL` each run uses.

[dlv]: https://github.com/go-delve/delve

## Downloading the weights (deduplicated)

`Ref2VA` and `FL2VA` share everything but the transformer: `video_vae`,
`audio_vae`, `tokenizer`, `processor` and `text_encoder` are byte-identical by
SHA256, and only the transformer shards differ (same sizes, different hashes — a
consistent sharding config, not shared weights). Fetching the shared parts once
and symlinking them costs **~66 GB** instead of ~196 GB.

```bash
# 1. Ref2VA in full
hf download MiniMaxAI/MiniMax-H3 --local-dir ./MiniMax-H3 --include "Ref2VA/*"

# 2. symlink the shared components into FL2VA
cd MiniMax-H3 && mkdir -p FL2VA
ln -s ../Ref2VA/text_encoder FL2VA/text_encoder
ln -s ../Ref2VA/video_vae    FL2VA/video_vae
ln -s ../Ref2VA/audio_vae    FL2VA/audio_vae
ln -s ../Ref2VA/tokenizer    FL2VA/tokenizer
ln -s ../Ref2VA/processor    FL2VA/processor
cd ..

# 3. only the FL2VA transformer
hf download MiniMaxAI/MiniMax-H3 --local-dir ./MiniMax-H3 \
  --include "FL2VA/transformer/*" --include "FL2VA/model_index.json"

# 4. verify
ls -la MiniMax-H3/FL2VA/     # five symlinks → ../Ref2VA/...
du -sh MiniMax-H3            # ~66 GB
./h3 --info -d ./MiniMax-H3  # h3.c accepts the tree
```

> **Notes:** `hf download` can overwrite symlinks when writing into a directory
> that has them — if step 3 replaces them, download the transformer to a scratch
> directory, move it into place and recreate them. The weights also need a
> filesystem with symlinks: APFS and ext4 yes, exFAT no.

## Using the studio

Choose **One-shot** or **Interactive** in the render bar at the bottom of the
left pane. Interactive h3.c starts on **Load h3.c** or the first interactive
render, so starting the studio never loads the model. **⌘/Ctrl+Enter** renders,
**⇧⌘/Ctrl+Enter** queues three seeds. What each panel does is
[Features](#features); where the work is kept is
[Sessions and state](#sessions-and-state).

### Command-line reference

You never type this — the launcher and `scripts/run.sh` both build it from
`helmstudio.yaml`'s `processes[0].cmd` — but the flags are worth knowing:

```bash
./dist/h3studio --h3 ./h3c/h3 --model <FL2VA> --port <port> --root <data>
```

The paths are remembered in `<data>/sessions/h3.json` and `model.json`, so a
later launch can omit them; a flag or environment variable always wins.
**There is no authentication** — see [Security](#security) before binding to
anything but `127.0.0.1`.

| Flag | Default | Description |
| --- | --- | --- |
| `--root` | *(required)* | Directory holding `sessions/` — the data directory helmstudio gives this studio. It creates none of its own and will not start without one. |
| `--h3` | `$H3STUDIO_H3`, else last used | Path to the built `h3` binary. |
| `--model` | `$H3STUDIO_MODEL`, else last used | Path to the checkpoint directory. |
| `--host` | `127.0.0.1` | Bind address; anything but loopback warns. |
| `--port` | `8710` | Bind port; helmstudio passes the one it allocated. |
| `--dev` | `false` | Serve `static/` from disk with `Cache-Control: no-store`, looked up where the studio was launched from, then beside the binary — not under `--root`, which holds no source. |
| `--allow-shell` | `false` | Enable the `$` shell terminal and changing the h3 binary from the browser. |
| `--allow-host` | *(none)* | Extra `Host` names to accept (comma-separated); IPs and `localhost` always are. |

### What helmstudio adds

h3 studio draws its own page — form, takes rail, viewer, terminal — and
helmstudio adds four things around it:

| | |
| --- | --- |
| **Gallery** | helmstudio's own grid over this studio's takes, live: a take that finishes appears without a reload. Scoped to this studio; its library is where these sit beside other studios' work. |
| **Timeline** | **Create Timeline** opens a helmstudio sequence — clips trimmed and dissolved, exported as a job it runs. Clips come from this studio's takes, but the sequence is helmstudio's and can hold any studio's. |
| **Render log** | A third Terminal tab streaming the render as helmstudio sees it; it reconnects after a dropped stream and says what it missed. Output's **follow** and **Clear** hide while it shows — helm-terminal brings its own. |
| **The launcher** | A render appears there as a job with its progress, so what this studio is doing is visible from outside it. |

All of it arrives through the same-origin `/helm/` proxy the server mounts, as
do the theme and this studio's colour, so the page holds no token of
helmstudio's. If those components cannot load the page still works: it falls
back to its vendored helm-css and own theme switch, Gallery and Render log stay
hidden, and **Create Timeline** opens h3 studio's own combine-videos editor.

## Features

**Reference ordering is explicit.** References are numbered `Picture 1`,
`Picture 2` in list order; drag to reorder. The model reads position, not
filenames, so getting it wrong silently produces the wrong shot.

**Three conditioning modes.** **Prompt** (text only), **Anchors** (first/last
frame, FL2VA) and **References** (ordered Ref2VA images, clips, audio). Each
keeps its own inputs, only the active one is sent, and References is disabled
with a reason when the model has no `Ref2VA/`.

**Illegal settings are caught before launch.** The server validates every render
— canvas on the 32-pixel grid and within 768×1344, the 5+17n frame grid,
reference counts and durations, inputs that exist in the session — and the same
errors show in the render bar as you edit. **Command** shows the exact argv (or
REPL commands), built by the code that runs it.

**Canvas by aspect ratio.** 16:9, 9:16, 1:1, 4:3, 3:4, 3:2, 2:3, 21:9, **Match
input** (the first anchor or image reference) or Custom; drag **Megapixels** and
the studio solves the closest legal size, shows the latent size and warns when
an anchor's aspect would stretch.

**One-shot and interactive rendering.** One-shot spawns a fresh `h3` per render;
Interactive keeps it resident, so repeated **Send to h3.c** renders skip the
model load and pay only for re-encoding what changed. You can also type at the
`h3>` console: a queued studio render waits for a manual prompt to finish, and
each render's output is tracked in its own directory so the two never mix.

**Continue generation from any take:**

- **Chain →** extracts the last frame, switches to anchor mode and sets it as
  the *first* frame of the next shot, clearing any last-frame anchor.
- **Use frame** extracts the last frame without switching mode: in anchor mode
  it fills whichever of first/last is empty, in Reference mode it appends as the
  next `Picture N`.
- **Use ref** copies the whole output video in as a `Video N` reference (max 3),
  for motion or subject continuity.
- The **⋮** menu adds **First frame → input**, **Use audio**, **Previews (N)**,
  **Add to compare**, **Download** and **Delete**.

Each copies the source into the session's `inputs/` first, since renders only
ever read references from there.

**Timeline.** Combine takes from a session into one video — pick clips, order
them, export. **Create Timeline** opens helmstudio's sequence editor instead
(see [What helmstudio adds](#what-helmstudio-adds)) and this is its fallback;
the panel lists what either made. Below, two FL2VA takes from one session
combined into a continuous shot:

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

**Part 2** — first frame: Part 1's last frame, pulled in with **Use frame**
*"She smiles, waves, and says 'Hi!' continuing the same shot."*
480×864 · 124f · steps 8 · seed `989663098` (same seed kept for continuity)

Both takes: FL2VA, one-shot mode, combined with the Timeline feature.

</td>
</tr>
</table>

**Reproducibility.** Every render writes a `.json` sidecar beside the MP4: the
full parameter set, the exact argv, an ffprobe summary, the profile and the
saved previews. **Reuse** restores a take into the form; **History** brings back
earlier prompts.

**Queue and seed comparison.** One render at a time — one GPU. **Queue 3 seeds**
submits the same setup with three random seeds and opens a synced grid with a
**Keep** (star) on each; any two takes compare with an **A/B wipe**, and
**★ starred only** filters the list.

**Progress you can read.** The running card shows a Load → Encode → Denoise →
Decode → MP4 stepper, elapsed time and an ETA — measured per denoise step, or
estimated from earlier takes with the same settings, which is also the estimate
shown before you start. The tab title tracks progress and 🔔 raises a browser
notification. Failures pop up with the error and, for known ones (OOM, the macOS
GPU watchdog, missing ffmpeg or model files), a hint.

**Live preview on disk.** Each decoded preview frame is written as a PNG under
`previews/<job>/` and streamed to the viewer by URL, then pruned afterwards
(every frame of the last step, one of each earlier step); **Previews (N)**
scrubs them.

**Live profile.** The Timing tab charts `--profile` wall time per component
across recent takes (model load separate from compute) and lists the latest
render's rows.

**Model check.** The **Model** button shows whether `FL2VA/` and `Ref2VA/` are
present, whether symlinks resolve, the checkpoint size, and whether `h3`,
`ffmpeg` and `ffprobe` run; the dot beside it turns amber or red.

**Keyboard.** ⌘/Ctrl+Enter render · ⇧⌘/Ctrl+Enter queue 3 seeds · Esc close
dialogs · Space play/pause · ←/→ step one frame · J/K next/previous take.

## Sessions and state

A session is a directory under the data directory helmstudio gives as `--root`,
holding one line of work: inputs, outputs and the UI state that produced them.
Nothing is shared between sessions, so switching is instant.

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

`.thumbs/` folders beside videos hold cached posters.

- **`setting.json`** is written atomically on a debounced auto-save as you edit,
  so a session reopens exactly where you left it — prompt, canvas, quality,
  every reference and anchor, and the mode you were in.
- **`inputs/`** is the *only* place renders read references from. Uploads,
  frames from **Chain →** or **Use frame**, and takes pulled back with **Use
  ref** all land here first, even though the take came from `outputs/`.
- **`outputs/`** holds only what `h3` produced: the `.mp4` and its sidecar. The
  sidecar is the only record of a take, so deleting the video deletes sidecar,
  thumbnail and previews. Takes are played and timelined from here, never read
  into a render directly.
- `sessions/last_session.json` tracks the last active session and is restored on
  start, creating `session-1` if empty. Every API call names its session, so two
  tabs can work in different ones; New, duplicate and delete are in the **⋯**
  menu.

A finished take becomes helmstudio's too, without changing any of the above: it
is written to `outputs/` as always, and helmstudio adopts it **by hardlink** —
the same bytes under a second name, one inode, counted once. The gallery item
carries the sidecar's parameters plus the session name as `h3_session`, since
helmstudio checks session ids against its own and these are not those. Deleting
a take here does not undo that: the hardlink keeps the bytes and the gallery
item stays, so a take you want gone must go there too.

## Notes for an external drive

"Copy weights into memory" is on by default and sets `H3_ZERO_COPY_WEIGHTS=0`;
turn it off on internal storage, where zero-copy mapping is faster. The Qwen
prefetch fields set `H3_QWEN_PREFETCH_DEPTH` and `H3_QWEN_PREFETCH` — the
defaults assume a 128 GiB machine, and raising depth can hide slow reads.

## Security

h3 studio has no authentication, so it defends the one thing a local tool must:
other websites and other machines driving it.

- Binds to `127.0.0.1` by default and warns for anything else. Anyone who can
  reach the port can run renders.
- Requests whose `Host` isn't an IP, `localhost`, the `--host` name or an
  `--allow-host` name are refused, which blocks DNS rebinding.
- State-changing requests must come from the studio's own origin with a JSON
  content type, so a page you visit can't forge them.
- The `$` shell terminal and changing the h3 binary from the browser need
  `--allow-shell`.
- Only `H3_*` variables reach h3, **Extra arguments** accepts only `--use-*`
  switches and `--ref-image-size`, and render inputs must be plain file names
  inside the session's `inputs/`.
- Everything the page uses of helmstudio's comes through the same-origin
  `/helm/` proxy, so the browser never holds helmstudio's token. The proxy
  forwards the studio API and the theme stream and nothing else — a launcher
  path (install, launch, stop) 404s and never reaches the daemon.

## Limits

- One render at a time, deliberately.
- Stop sends `SIGTERM` to h3's process group, then `SIGKILL` after 3 seconds;
  stopping an interactive render unloads h3.c.
- Interactive h3.c accepts image references only — use One-shot for video or
  audio references — and Extra arguments apply when it loads, not per render.
- Recording happens as a take finishes and nothing is reconciled afterwards: a
  take deleted here stays in helmstudio's gallery. Combined timeline videos and
  preview PNGs are h3 studio's own and are not adopted at all.
- `/api/queue` does not answer helmstudio's busy contract yet, so its
  switch-studio dialog reads h3 studio as unknown rather than busy or idle.

## Contributing

Bug reports, feature requests and pull requests are all welcome — read
[CONTRIBUTING.md](CONTRIBUTING.md) first. It covers what you need running
locally, the conventions (no Go dependencies beyond helmstudio's runtime SDK, no
frontend build step), the test commands, and why engine issues belong in
[h3.c's own repository](https://github.com/janishar/h3.c).

## License

h3 studio's own source — the Go server and the static web UI — is
[MIT](LICENSE), © Janishar Ali.

h3.c is vendored as a git submodule rather than copied in. It is separately
MIT-licensed (© Salvatore Sanfilippo, [`h3c/LICENSE`](h3c/LICENSE)) and carries
an additional BSD-3-Clause notice for adapted shader code
([`h3c/THIRD_PARTY_NOTICES.md`](h3c/THIRD_PARTY_NOTICES.md)); both must be
preserved if you redistribute `h3c` itself.

The MiniMax-H3 weights are **not** part of this repository and are distributed
by MiniMaxAI under their own license — review
[the model card](https://huggingface.co/MiniMaxAI/MiniMax-H3) before use.

## Acknowledgments

- [Salvatore Sanfilippo](https://github.com/janishar/h3.c) for h3.c, the native
  Metal engine this is a control surface for.
- [MiniMaxAI](https://huggingface.co/MiniMaxAI/MiniMax-H3) for MiniMax-H3.
