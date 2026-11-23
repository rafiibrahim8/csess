# csess

List, fork and move [Claude Code](https://claude.com/claude-code) sessions between directories.

Claude Code ties every session to the directory it was started in. `claude -c`
and `claude --resume` only see the sessions of the current directory, so
renaming a project directory "loses" all of its sessions. `csess` keeps them
following the directory, lists them, and copies them elsewhere.

```console
$ csess ls
/home/me/work/api  (* = what `claude -c` resumes)
  MODIFIED          SESSION                               TITLE
* 2026-11-23 14:15  9f2c1e7a-4b3d-4e8f-a1c2-7d6e5f4a3b21  Fix flaky auth test
  2026-11-21 09:02  0e8d7c6b-5a49-4382-b716-a5f4e3d2c1b0  Add rate limiting

$ csess mv ~/work/api ~/work/api-v2      # the dir, its sessions, history and settings
$ csess fork -l -s 9f2c ~/scratch/try    # continue a copy of a session somewhere else
$ csess ls --all | grep MISSING          # sessions whose directory is gone
```

## Commands

| Command | What it does |
|---|---|
| `csess ls [<dir>]` | Sessions of a dir (default: current), newest first, with titles. `*` marks what `claude -c` resumes. |
| `csess ls --all` | Every dir that has sessions, with count and last activity. `MISSING` = the dir no longer exists. |
| `csess fork [<srcdir> \| -s <id>] <dir>` | Copy a session (default: the newest of `<srcdir>`, or of the current dir) into `<dir>` (created if needed) under a new id, with its scratchpad and the dir's memory, and print the resume command. `-l` resumes it right away; `--no-memory` skips the memory. |
| `csess mv [-n] <old> <new>` | Rename a dir together with its sessions and those of its subdirs, the up-arrow prompt history, and its `.claude.json` entries (trust, allowed tools, MCP servers). `-n` shows the plan. |
| `csess completion <shell>` | Print the completion script for bash, zsh, fish or powershell. |

Output is colored on a terminal; `--color auto|on|off` overrides that, and `NO_COLOR` is honored.

`mv` refuses when the target exists, when the source is a symlink or contains
Claude's own config, and while a `claude` process is running inside the source.
It keeps transcript modification times, so `claude -c` still resumes the same
session afterwards, and backs up `.claude.json` before changing it.

Details: [`man csess`](docs/csess.1) (or `man ./docs/csess.1` in a checkout).

## Install

**Prebuilt:** download `csess-<version>-<os>-<arch>.tar.gz` from
[Releases](https://github.com/rafiibrahim8/csess/releases), check it against
`SHA256SUMS`, and copy the files into place:

```sh
tar xzf csess-v*-linux-x86_64.tar.gz && cd csess-v*-linux-x86_64
install -Dm755 csess ~/.local/bin/csess
install -Dm644 man/csess.1 ~/.local/share/man/man1/csess.1
install -Dm644 completions/csess.bash ~/.local/share/bash-completion/completions/csess
install -Dm644 completions/_csess ~/.local/share/zsh/site-functions/_csess
install -Dm644 completions/csess.fish ~/.config/fish/completions/csess.fish
```

**With Go** (1.27+):

```sh
go install github.com/rafiibrahim8/csess/cmd/csess@latest
```

Then load completions with e.g. `source <(csess completion bash)`.

Linux and macOS. The binary is static (pure Go, no cgo) and has no runtime
dependencies.

## Configuration

`csess` reads Claude's state where Claude keeps it: `~/.claude`, or
`$CLAUDE_CONFIG_DIR` when that is set (including `.claude.json`, which then
lives inside it instead of in `$HOME`).

## Development

```sh
go test ./...
go build ./cmd/csess
```

```
cmd/csess/        entry point
internal/cli/     the commands (cobra)
internal/claude/  Claude Code's on-disk format: folder names, transcripts, .claude.json
internal/fsutil/  path and file helpers
docs/csess.1      man page
```

Releases: push a `v*` tag; [release.yml](.github/workflows/release.yml) tests,
builds every target and publishes the tarballs with `SHA256SUMS`.

## License

[MIT](LICENSE)
