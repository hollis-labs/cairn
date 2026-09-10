# Golden fixtures

`trees/` is what Cairn's nine profiles render to, byte for byte. A change that
alters rendering shows up as a diff there — which is the point.

    testdata/goldens/verify.sh        # re-render, diff against trees/, exit 1 on drift
    testdata/goldens/capture.sh       # re-render into trees/ (accept the new bytes)
    testdata/goldens/agent-setup.commit  # which agent-setup commit trees/ came from

`testdata` is the directory name on purpose: the Go toolchain ignores it, so
none of this reaches a build or a test binary.

## These goldens are not hermetic

**They prove byte-identity on this machine, against one working tree, and they
prove nothing anywhere else.** `capture.sh` reads `~/dev/projects/agent-setup`
live — the checkout as it stands, not a pinned revision — and points cairn at
it with `--profile`, so the profiles under review, and the templates, skills
and prompts they name under `$CAIRN_PROFILE_ROOT`, are all read where they
live. `verify.sh` re-runs `capture.sh`, so it inherits all of it. `AGENT_SETUP`
moves the path; nothing moves the dependency.

The consequences are worth naming, because this is the proof the whole
migration rests on:

- On a machine without `agent-setup`, `verify.sh` cannot run at all — it exits
  with `no profiles under …`, not with a pass.
- In CI, likewise. **These goldens are not a CI check** and adding them to one
  would need the profiles vendored or pinned first.
- When `agent-setup` moves on, `verify.sh` reports a diff that is about the
  profiles rather than about cairn. It says so rather than leaving you to work
  it out — see below.

## Whose change is this diff?

`capture.sh` records the `agent-setup` commit it captured from, in
`agent-setup.commit` beside `trees/`. `verify.sh` compares that against
`$AGENT_SETUP`'s HEAD and names the case before printing the diff:

- **upstream moved** — a stale fixture, not your change. Re-baseline. Read the
  diff first: additions mean upstream gained something, and a removal should be
  paired with an addition at the same key. An **unpaired removal** means cairn
  stopped rendering something, which is yours and is a regression.
- **upstream unchanged** — the diff is your change.
- **upstream dirty** — HEAD does not describe what was captured, so the diff is
  attributable to no commit at all. This is not hypothetical: on 2026-09-03
  cairn's gate was for a real window a function of another session's
  uncommitted edits in the shared checkout.

The record sits beside `trees/` rather than inside it, because a record inside
the tree would itself be a diff every time upstream moved — reporting the drift
as drift — and because `verify.sh` re-runs `capture.sh` into a scratch
directory, which would clobber a record kept at a fixed path in the repo.

None of this pins anything. `capture.sh` still reads live `agent-setup`;
the record only makes the resulting diff legible.

Everything cairn controls *is* pinned — see the table below. The profiles,
templates and skills are the one input that lives somewhere else, and the
goldens are as strong as that checkout is stable.

## What is captured

Eight boots — `architect`, `conductor`, `director`, `engineer`, `orchestrator`,
`planner`, `reviewer`, `writer` — each planted at `boot/<id>/golden`, plus one
installed layer for `base` at `install/base`. `base` is `abstract: true` and
`cairn boot` refuses an abstract profile by design, so the installed layer is
the only way to render it.

`stdout/<id>.txt` and `stderr/<id>.txt` are captured alongside. Capturing
stdout is a deliberate extension beyond "render the tree": stdout is the
planted path, so pinning it pins the plant location too — a change to where a
boot lands, or to the file list an install prints, is then a diff like any
other. Today every `stderr` file is empty: against a fully staged fixture home
no slot goes unfilled and no marker stands for nothing.

## A degraded render is never captured

`capture.sh` refuses to write a tree when any `stderr` file carries a slot that
failed to resolve or a declared slot that filled nothing.

The reason is that a golden is a poor witness against absence. A section missing
because its slot failed looks, in a captured tree, exactly like a section the
profile never declared — and a re-baseline review comparing the diff against a
list of expected changes will tick it off as one of them. The failure mode is
not that something breaks; it is that something breaks and the record of it
looks correct.

The `conductor` profile's `fleet` slot is the case that motivated the guard. It
shells out to `cairn list`, and `cairn boot --profile <dir>` does not reach a
slot's shell — so two things in the render environment point it at this run:
`CAIRN_PROFILE_ROOT`, which is how it finds the same bundle, and `$FIX/bin`
first on `PATH`, which is how it finds the cairn this run built. Lose either
and the slot reads somewhere else or runs something else.

It used to be worse. The slot was a `sqlite3` query reading
`${CAIRN_DB:-...}`, and sqlite3 opening `immutable=1` on a path that does not
exist gets an **empty database rather than an error** — so the query failed
with `no such table`, the section rendered nothing, and the boot still exited
0. `cairn list` against a bundle that is not there exits non-zero and says so,
which is loud on stderr. That is still a line a capture would happily baseline,
which is what this guard refuses.

Anything else on `stderr` is printed and kept rather than refused — the
installed layer's "renders no section for" line says which slot kinds `install`
resolves, which is a fact and not a fault.

## What is pinned, and why

Everything the render can reach is staged into one throwaway fixture root:

| Pinned | Why |
| --- | --- |
| `--session golden` | `bootdir.NewSession` is the only nondeterminism inside cairn, and it is used only when `--session` is empty: a UTC timestamp plus a random suffix. |
| an **empty** fixture `HOME` | It used to hold `.config/agents/{templates,skills,prompts}`, staged with `make install-system`'s own `rsync` flags, because the profiles named that location by absolute path. They name `$CAIRN_PROFILE_ROOT` now and `install-system` is gone, so home supplies nothing — and staging a copy anyway would be worse than unused: a profile that went back to naming a path under home would find it satisfied, and the golden would certify a render no machine can reproduce. Empty, this pins that the bundle is self-contained. |
| a fixture scope | `director`, `engineer` and `orchestrator` run `git status --short --branch` and `git log --oneline -12` against the scope. The fixture is a git repo built from literals: pinned author and committer identity and dates so the abbreviated hash is stable, `core.abbrev` set rather than left to git's object-count heuristic, and `GIT_CONFIG_GLOBAL`/`GIT_CONFIG_SYSTEM` nulled on every invocation so the operator's own gitconfig cannot leak in. No remote, so `git status` prints just the branch line. |
| `--profile $AGENT_SETUP` | The catalog is the store, so the profiles under review are read out of the checkout rather than seeded into a database first. |
| `--provider claude` (`PROVIDER`) | agent-setup declares no provider at all — a runtime is a launch's to choose — and cairn refuses to render a profile that names one no layout exists for rather than writing one harness's files into another's directory. So the flag is required, and a capture that omitted it stopped being able to run at agent-setup afc6181. It changes nothing about what is rendered: base declared `claude` before it declared nothing. |
| `CAIRN_PROFILE_ROOT` in the environment | The `conductor` profile's `fleet` slot shells out to `cairn list`, and cairn's `--profile` flag never reaches a slot's shell. Without the export the slot reads `~/.config/agents` instead — which under the fixture `HOME` does not exist, so it fails rather than answering quietly. |
| `$FIX/bin` first on `PATH` | The same slot runs `cairn`, by name. Without this it would run whatever cairn is installed on the machine, which is the one thing the harness exists not to test. Losing it fails loudly only for as long as the installed cairn predates `cairn list`; once this one is installed the same loss would render plausible output from the wrong binary. |
| `TZ=UTC`, `LC_ALL=C`, `XDG_CONFIG_HOME` and `CAIRN_BOOT_ROOT` unset | The environment is built from nothing rather than inherited (`env -i` plus `PATH`), so the operator's shell is not an input. |

Two per-run paths are rewritten to tokens after the render, because leaving
either raw would guarantee a false diff:

- the fixture root → `@FIXTURE@`
- the output root → `@OUT@` (it differs between the capture that wrote the
  golden and every verification run after it)

Both roots are canonicalized with `pwd -P` first. On macOS `/tmp` and `$TMPDIR`
are symlinks, and a path that reaches a golden through one spelling while the
token is the other is a diff nobody enjoys chasing.

The `stderr` files are sorted (`LC_ALL=C sort`) after tokenization. Slot
failures are reported in manifest declaration order, which the keyed merge
deliberately makes meaningless — it is not a contract, so pinning it would
freeze a detail the design says is free. `stdout` is left in the order it was
written.

## Re-capturing

    testdata/goldens/capture.sh --force

`capture.sh` builds `cairn` from the working tree unless `CAIRN` names a
binary, which it copies into the fixture instead. That is deliberate: every
change from here on runs `verify.sh` to prove it did not alter rendering, so
the harness must exercise the source in front of it and not whatever stale
`cairn` is on `$PATH` — and the copy is what lets the `fleet` slot's own
`cairn list` be that same binary.

`AGENT_SETUP` overrides where the bundle comes from — the profiles and the
templates, skills and prompts they name; it defaults to
`~/dev/projects/agent-setup`.

Re-capturing rewrites `trees/`, and `agent-setup.commit` beside it. Read the
diff before committing it — a golden that changed without an intended reason is
the finding, not the noise. `verify.sh` will have told you which case you are
in; see "Whose change is this diff?" above.
