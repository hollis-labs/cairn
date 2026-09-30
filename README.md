# Cairn

Materialize a CLI coding agent from a declarative profile: Cairn resolves a
profile out of a bundle of plain files and writes a provider-native boot
directory an agent can be launched from.

> **Stable and in daily use, still pre-1.0.** Cairn has no tagged release and
> no outside consumers yet, but it materializes every agent session on its
> author's machine and its gate runs on Linux and macOS in CI. It's being
> built in the open: the code, the docs, and this README describe what exists
> today, not a pitch for what's planned. The CLI and the `--json` reports are
> the interfaces a launcher depends on and change deliberately; the exported
> Go packages may still change without notice until `v0.1.0`.

## What Cairn is

Cairn assembles files and writes them into a directory. It:

- reads **profiles** out of a **bundle** — a directory of Markdown files with
  YAML frontmatter, plus the templates, skills and prompts they name;
- resolves a profile through its `extends` chain, and through any **parts**
  composed onto one launch with `--with`, `--skill`, `--prompt` and `--set`;
- renders the result as a **boot directory** laid out the way a harness
  expects it — `AGENTS.md` or `CLAUDE.md`, `.claude/settings.json` or
  `config.toml`, skills, commands, subagent definitions, `.mcp.json` — and
  prints its path (or, with `--json`, a report a launcher reads);
- renders the **installed layer** (`~/.claude`, `~/.codex`) from the same
  source, claiming only the keys it owns and reporting drift with `--check`.

Cairn does not launch, monitor, or supervise agents, and it ships no
profiles. File contents are a black box: Cairn provides and validates the
shape, and whoever owns the bundle owns the meaning. **You launch.**

Providers: `claude` (Claude Code), `codex` (Codex CLI) and `opencode` (OpenCode)
have boot layouts. OpenCode's covers the boot directory only: there is no
installed layer, and it does not render `spec.mcp` or `spec.settings` yet
(OpenCode keeps both in `opencode.json`; the library renderer is tracked as
CW-20260930-0136), so a boot names what it dropped. Harnesses reached only over
ACP, such as Copilot and Pi, have no native boot directory, and Cairn renders
nothing for them. Antigravity waits on a go-providers upgrade.

## Install

Cairn needs Go 1.26.7 or later (see `go.mod`).

```bash
git clone https://github.com/hollis-labs/cairn.git
cd cairn
make build                 # → bin/cairn
# or put it on your PATH:
go install ./cmd/cairn
```

The Go module path is still `github.com/chrispian/cairn`; the repository
moved to the `hollis-labs` organization and the old URL redirects.

## Quick start

The repository carries a small, complete bundle under `examples/bundle/`.
`list` and `show` read it and write nothing:

```bash
# What the bundle holds: bootable profiles, abstract ones, layouts
./bin/cairn list --profile examples/bundle

# What a profile resolves to, and which profile in the chain declared each key
./bin/cairn show engineer --profile examples/bundle
./bin/cairn show engineer --profile examples/bundle --json
```

`boot` writes a boot directory (by default under
`~/.local/state/cairn/boot`, or `$CAIRN_BOOT_ROOT`) and prints its path:

```bash
./bin/cairn boot engineer --profile examples/bundle --scope ~/src/my-project

# Compose one launch: fold a part onto the profile for this boot only
./bin/cairn boot documentarian --profile examples/bundle \
  --scope ~/src/my-project --with docs-only

# Render for Codex instead of the profile's declared provider,
# and describe the result for a launcher
./bin/cairn boot engineer --profile examples/bundle \
  --scope ~/src/my-project --provider codex --json
```

Then start your harness from the printed directory. `--json` reports the
boot directory, the scope, the settings file to promote and how the harness
wants the scope passed (for example `--add-dir`), so a launcher never parses
prose.

`install` renders your home configuration from a profile. Preview it first:

```bash
cairn install base --check             # diff against disk, write nothing
cairn install base --root /tmp/preview # render somewhere harmless
```

`cairn install` without `--root` writes to your home directory. Run it
yourself, not from inside an agent session — a session running under
`~/.claude` or `~/.codex` would rewrite its own live configuration.

Without `--profile`, Cairn reads the bundle from `$CAIRN_PROFILE_ROOT`, then
`$XDG_CONFIG_HOME/agents`, then `~/.config/agents`. `cairn --help` documents
every flag.

## Concepts

- **Bundle** — the whole store: `profiles/` (with `profiles/parts/`),
  `templates/`, `skills/`, `prompts/`. Edit a file and the next command reads
  it; there is nothing to seed or import.
- **Profile** — one Markdown file. Frontmatter carries `extends`, `provider`,
  and a free-form `spec` (templates, slots, skills, prompts, settings, MCP
  servers, access grants, subagents, files, trees). The prose below the
  frontmatter is the profile's own body. A profile can be `abstract`: extended,
  shown and installed, but never booted.
- **Cascade** — `extends` merges keyed collections by key and replaces
  everything else, closest profile wins; `key: null` clears an ancestor's
  value; bodies concatenate ancestor-first.
- **Part** — an ordinary profile composed onto one launch with `--with`,
  merged after the chain resolves. Cairn writes no composition down: a launch's
  composition belongs to the launcher.
- **Scope** — the project directory the agent works in. It is granted in the
  rendered settings; the boot directory is never planted inside it.
- **Slot** — a named block of context a template places with
  `<!-- cairn:slot name -->` (or, in a profile that is its own template, an
  inline tag), filled at boot from a source such as a file or a command's
  output. A slot that fails reports on stderr and renders nothing; the boot
  still succeeds. `<!-- cairn:value scope -->` and its siblings fill Cairn's
  own values: model, profile, provider, scope, session.
- **Layout** — a document per harness (`bootdir/layouts/*.yaml`) listing every
  path that harness reads and which renderers its tree carries. A new harness
  is a new document, not a new code path.
- **Installed layer** — the provider's home configuration, rendered from a
  profile. Cairn rewrites the keys it owns and preserves the rest.

[`examples/README.md`](examples/README.md) is the full guide: profile
authoring, composition, templates and markers, the access grant, the settings
tier, and the `boot --json` / `show --json` contracts a launcher builds on.

## How it fits the Hollis Labs stack

```
agent-setup (bundle: profiles, templates, skills)
      │
      ▼
   cairn boot / cairn install
      │
      ▼
 boot directory  ──►  launcher (agent-launcher / Tachyon)  ──►  claude / codex
 installed layer (~/.claude, ~/.codex)
```

A content repository — for Hollis Labs, `agent-setup` — is the bundle. Cairn
turns it into a boot directory, and a launcher reads `cairn boot --json` to
open the harness in it with the right scope and settings. Cairn also
implements the `go-agent-wrapper` planting contract that Nanite uses, so the
two speak the same interface for writing a boot tree, though nothing calls
Cairn through it yet.

## Where it's headed

- **Planned:** audit the exported Go surface and tag `v0.1.0`, the first
  release with a compatibility promise.
- **Planned:** move the harness layout documents out of the binary and into
  the bundle, so supporting a harness becomes a content change.
- **Proposed:** a `slots` array in `boot --json`, so a launcher can show a
  failed slot as a warning instead of relaying stderr.
- **Proposed:** converge on one resolved-agent contract shared with Nanite,
  Torque and Tether, with Cairn as the provider of the resolved definition.

## Development

```bash
make check      # gofmt, go vet, golangci-lint, go test -race, govulncheck — what CI runs
go test ./...   # the quick loop
```

`make check` needs `golangci-lint` and `govulncheck` on your PATH.
`testdata/goldens/` holds rendered trees that pin Cairn's output against a real
bundle; `verify.sh` there re-renders against a local `agent-setup` checkout
and is not part of CI.

## License

MIT License — see [`LICENSE`](./LICENSE). © 2026 Chrispian Burks / Hollis Labs.
