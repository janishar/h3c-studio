# h3 studio

A local web control surface for [antirez/h3.c](https://github.com/antirez/h3.c).
Python stdlib only — nothing to install.

## Run

```bash
python3 h3studio.py \
  --h3    /Users/janisharali/GenAI/minimax-h3-mlx/h3.c/h3 \
  --model /Users/janisharali/GenAI/minimax-h3-mlx/MiniMax-H3
```

Open http://127.0.0.1:8710

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

**Reference ordering is explicit.** References are numbered `Picture 1`, `Picture 2``
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
- Stop sends SIGINT; h3 may take a moment to unwind.
- Uploads are held in memory before writing, so very large reference videos will
  be slow to attach.
- Bound to 127.0.0.1. There is no authentication — don't expose it.
