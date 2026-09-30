# Changelog

All notable changes to Cairn are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Cairn has no tagged
release yet; everything below is unreleased.

## [Unreleased]

### Added

- `cairn boot` materializes a boot directory from a profile and prints its
  path; `--json` prints one object a launcher reads instead (boot directory,
  scope, bundle, settings file, and how the harness takes the scope).
- `cairn install` renders the installed layer (`~/.claude`, `~/.codex`) from
  the same profile, claiming `settings.json` and skills by key and preserving
  everything it does not own; `--check` re-renders, diffs and reports drift
  without writing.
- `cairn show` prints what a profile resolves to and which profile declared
  each key; `--json` carries the merged manifest and per-key provenance.
- `cairn list` enumerates a bundle's profiles, abstract profiles and layouts.
- Bundles: `--profile <dir>` names the bundle, read from `$CAIRN_PROFILE_ROOT`,
  `$XDG_CONFIG_HOME/agents` or `~/.config/agents`, and `$CAIRN_PROFILE_ROOT`
  expands in every manifest path so a bundle relocates without edits.
- Composition for one launch: `--with` parts (from `profiles/parts/` or a
  path), `--skill`, `--prompt` and `--set`, all repeatable and additive.
- Prompts, planted as `/boot:<name>` commands the operator invokes.
- Subagents: a profile can name other profiles, rendered as agent definitions.
- `spec.files` and `spec.trees` place arbitrary files and whole directories,
  literal or resolved from a slot source.
- Templates with `cairn:slot` / `cairn:value` markers, and profiles that are
  their own template, with the body rendered where the layout says.
- Native Codex materialization alongside Claude Code; `--provider` picks the
  target, and `opencode` is refused by name.
- Per-harness layout documents (`bootdir/layouts/*.yaml`): a harness is a tree
  described as data, not a code path.
- Access grants: the scope and `spec.access.directories` are granted in the
  rendered settings file.
- Golden render trees that pin output byte for byte, and a CI gate on Linux
  and macOS.

### Changed

- `extends` merges keyed collections by key and replaces everything else,
  replacing the earlier uniform closest-wins cascade.
- The catalog is the bundle on disk; the earlier SQLite store was removed.
- A slot or marker that produces nothing renders nothing (its line included)
  and is reported on stderr instead of leaving a gap in the file.
- Content a harness tree has nowhere to put is dropped and reported instead of
  refusing the boot.

### Removed

- Bindings and `--save-as`: Cairn holds no launch state, and a composition
  belongs to the launcher.
- The scope alias registry; a scope is a path.
- The reference `boot.sh` launcher script.

### Security

- A boot root inside a git repository other than the scope's is refused.
- Values that are not meant to reach instruction files (such as MCP
  environment) cannot be substituted into rendered prose.
