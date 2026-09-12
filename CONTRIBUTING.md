# Contributing to h3 studio

Thanks for considering a contribution — bug reports, feature ideas, and pull
requests are all welcome.

## Before you start

h3 studio only makes sense together with two things it doesn't vendor as
buildable source:

- **[h3.c](https://github.com/janishar/h3.c)**, checked out as the `h3c`
  submodule (`git submodule update --init --recursive`).
- **A local MiniMax-H3 checkpoint** to point it at (see the README's
  [Installation](README.md#installation) section).

You'll need both to run the app end to end, but UI-only or server-logic
changes can often be reviewed from a diff without a full model download —
say so in your PR if that's the case.

## Reporting bugs

Open a [GitHub issue](https://github.com/janishar/h3c-studio/issues) with:

- What you did and what you expected vs. what happened.
- Your macOS version and chip (M3-class / M5-class).
- The relevant slice of the **Terminal** panel's output (Timing tab included,
  if it's a performance issue) — most bugs in this app show up there first.

## Making changes

1. Fork the repo and create a branch off `main`.
2. Keep the diff focused — one fix or feature per PR is easier to review than
   several bundled together.
3. Match the existing style: the Go server is intentionally stdlib-only aside
   from `fsnotify` (see `server/`), and the frontend is plain HTML/CSS/JS with
   no build step (see `static/`). Please don't introduce a bundler, framework,
   or new Go dependency without discussing it in an issue first.
4. Run `go build ./...` and `go vet ./...` before opening a PR.
5. For UI changes, actually click through the flow in a browser (`--dev` for
   hot reload) rather than relying on a visual read of the diff.

## Pull requests

- Describe *why* the change is needed, not just what changed.
- Link the issue it fixes, if any.
- Small, incremental PRs get reviewed faster than large ones.

## Scope note on `h3c`

This repository only vendors `h3.c` as a git submodule; it doesn't fork or
patch it. If your change requires editing the inference engine itself (Metal
kernels, the CLI, model loading, etc.), that belongs in the
[h3.c repository](https://github.com/janishar/h3.c), not here.

## License

By contributing, you agree that your contributions will be licensed under the
project's [MIT License](LICENSE).
