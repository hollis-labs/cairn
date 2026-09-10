# Cairn — MVP plan

**Status:** authoritative. Written 2026-08-25 after an audit of the prior
implementation and of the prior art in `~/dev/hollis-labs`.

This is where design decisions land. Earlier documents — the spec under
`docs/spec/`, the ADRs, the READMEs, and the doc comments of the prior
implementation — are superseded and are not in the git history of this repo.
The prior tree is archived at `~/dev/projects/cairn-prior-20260825/`.

---

## 1. What Cairn is

**Cairn assembles files and writes them into a directory.**

It reads a profile out of a profile bundle, resolves it through an `extends`
cascade, and materializes a boot directory a CLI coding agent can be launched
from. It renders the installed layer (`~/.claude`, `~/.codex`) from the same
source.

That is the whole job.

### What Cairn is not

Cairn does not launch, monitor, track, control, create, harness, or steer
agents. It has no opinion about how agents work, relate to each other, or
behave.

**File contents are a black box.** Cairn composes and writes `AGENTS.md`; it
does not parse it, reason about it, validate its prose, or know what any of it
means. The same holds for every file it plants. Whoever owns the profiles owns
the semantics.

Cairn provides the shape and validates the shape. It ships no profiles, no
skills, and no defaults.

### Single user, by design

Cairn is built for one operator who also builds the system it configures. It
is not guarding a stranger from a footgun; it is not guarding its author from
a system its author has full access to anyway. Validation that exists only to
prevent the operator from doing something the operator meant to do is out.

---

## 2. The core decision: adopt, don't rebuild

The prior implementation was 25,677 lines. Roughly 8,000 of them reimplemented
problems already solved in `~/dev/hollis-labs/libs`, and about 4,700 more
implemented concerns that are not Cairn's. Tether, Torque, and Nanite each grew
a near-identical context-assembly pipeline before `agentcontext` was extracted
to end exactly that; Cairn was becoming the fourth.

**Cairn adopts the portfolio libraries.** What is left is genuinely Cairn's.

### Adopted

| Library | What Cairn takes |
|---|---|
| `agentkit/agentcontext` + `.../resolvers` | The entire slot mechanism. 8 slot kinds (`static_file`, `static_dir`, `inline`, `cmd`, `http_text`, `http_json`, `role_summary`, `skill_index`), 7 shipped resolvers, per-slot timeouts and headers, byte/token budgets, per-slot provenance, a determinism contract, and non-required-slot failure that records the error instead of blocking. Entry point: `DefaultProvider.Assemble(ctx, ContextRequest) (*ContextResult, error)`. Cairn renders each `SlotResult` on its own, so a template can place them; `ContextResult.Rendered` is discarded. |
| `agentkit/agentlaunch` | Vocabulary, not pipeline: `MCPServerSpec{Name, Command, Args, Env}` for MCP server definitions, `NativeFile{Kind: raw, RelPath, Content, Mode}` for arbitrary planted files, and `ValidateBootDirRelPath` as a second path-safety opinion. |
| `go-agent-wrapper/plant` | `plant.Planter` / `plant.Spec` as the write boundary, so Cairn speaks the same planting contract as Nanite. |
| `go-providers/provider` | Per-provider layout convention via `BootDirSpec`: which files, at which relative paths, cwd preference, the `--add-dir` argument pattern, and per-file modes (codex `auth.json` is 0600). This is also how codex and opencode arrive without inventing their layouts. |

Verified: all three modules resolve and build together (`agentkit v0.5.1`,
`go-agent-wrapper v0.8.1`, `go-providers v0.24.0`), indirect graph is
`creack/pty`, `go-llm-contracts`, `go-llm-types`.

`go-sqlite` was a fourth. It went with the store: the catalog is a directory of
files, and there is no database left to open.

`yaml.v3` is the one direct dependency outside those three, and it is load
bearing at both ends now: a profile is authored as YAML frontmatter and read
through it (§3), and a subagent definition's frontmatter is written back out as
YAML while the manifest it comes from is JSON (§5). Hand-rolling a YAML
encoder for an opaque map is how quoting bugs plant an unparseable frontmatter,
which a harness reads as no frontmatter at all.

### Kept from the prior tree

Ported from `~/dev/projects/cairn-prior-20260825/`, not rewritten:

| Source | Why it survives |
|---|---|
| `install/` (~4,650 lines) | Rendering the installed layer from the same profile source, with drift detection. Nothing in the portfolio does this. Port with the MCP-audit refusal removed (see §6). |
| `bootdir.writeTree` + `cleanArtifactPath` | Renders every file first, stages into a sibling temp dir, then one `rename`. The whole boot directory is all-or-nothing. Stronger than the per-file atomic writers in the libs, and worth keeping. |
| `bootdir/skills.go` (directory-tree copier) | See §5 — deliberately does not use `NativeFileSkill`. |
| `scope`'s containment check | The boot dir must never land inside the scope directory. ~40 lines guarding a write into a repo under work. |
| `.github/workflows/check.yml`, `Makefile`, `.golangci.yml` | Already in place in this tree. |

### Built here

- The catalog reader and the `extends` cascade. Nothing in the portfolio does
  profile inheritance; `agentlaunch/catalog` is file-backed and flat.
- The composition root and the CLI.

### Dropped outright

The dispatch **authority model** (`bootdir/subagents.go`, 1,626 lines) — a
subagent's tools computed as its own allow-list intersected with the
dispatcher's ceiling, a depth budget enforced by withholding the `Agent` tool
under every spelling, and a conflict error for one profile reached through two
dispatchers. How agents relate to each other is not Cairn's concern, and Cairn
has no `tools` concept to intersect with. What survives is the artifact and
nothing else: a profile names other profiles and each is rendered from its own
declaration — see §5. The four-block instruction file — a fixed pipeline of title, description,
cascaded body and a `## Profile` block of scalars, with no way to reorder it,
omit a block, or put anything resolved-at-boot inside it. It forecloses the
thing Cairn exists for: a profile naming Torque, Tesseract and `hollis-labs`
paths is right for one slice of the operator's work and wrong for a client
repository, and there was no way to get a different shape without a different
Cairn. It also silently answered questions that are the operator's — whether a
`## Profile` block exists at all, and whether an ancestor's prose lands before a
role's or after it. See §5: prose is a template now, and `body` is one value
among several rather than a privileged column. Instance identity and liveness
(`internal/identity`, 885 lines) — that is a registry function. Override
discipline (`overrides/`, 2,235 lines) — judging whether a correction is
well-formed is content review; if scoped content needs planting it rides the
`files` key like any other file. Cross-session messaging intent. Authority
resolution as policy (see §6).

---

## 3. Catalog

A profile bundle is a directory, and the directory is the whole store.

```
<bundle>/
  profiles/    one markdown file per profile — YAML frontmatter, then prose
    parts/     the same, for the small reusable ones — one global id namespace,
               an organizational convention rather than a second kind
  templates/   the documents profiles name
    layouts/   the documents a profile's `{{ extends }}` names
  skills/      one directory per skill
  prompts/     one flat .md file per prompt
```

`--profile <dir>` names it; without the flag Cairn reads
`$CAIRN_PROFILE_ROOT`, then `$XDG_CONFIG_HOME/agents`, then `~/.config/agents`.
The whole bundle is read into memory at the start of a command and nothing is
read after it, so a cascade, a subagent's profile and a layout all come out of
one snapshot.

**A bundle that is not there is named, not created**, and that is the reverse
of the rule the database it replaces had. A database was conjured on every
open because an empty one is a usable starting state; a directory of files is
not. An absent bundle is a sign Cairn was pointed somewhere wrong, so a read
that finds nothing writes nothing and says which bundle it was reading. `show`
and `install --check` promise that in their own help, and now keep it.

There is nothing to seed and nothing to import. The files are the source, git
is the review surface, and the next command reads whatever is on disk.

A profile's frontmatter is a **closed** set of keys — `id`, `extends`,
`abstract`, `name`, `description`, `provider`, `model`, `spec` — and one Cairn
has never heard of is refused by name. That is the opposite of the rule for
`spec` below, deliberately: a manifest key is somebody else's vocabulary, while
a frontmatter key is a typo, and the file is now the only copy. Nothing
downstream notices that a misspelled `descripton` left a profile with no
description, or that a misspelled `spec` left it with no manifest at all.

A profile's id is its file name and the two are held to agreeing. Two files
claiming one id are refused when the bundle is read, rather than one of them
quietly winning.

Deliberately small. The cautionary example is Nanite's `agents` table: 40-plus
columns across 134 migrations, several deprecated but inert, entangled with
plugins, consumers, roles, and tenancy. That is what happens when the schema
becomes the model — and a frontmatter key set is a schema wherever it is kept.

**Durations are translated as the catalog is read.** Go unmarshals a
`time.Duration` from a number of nanoseconds and not from `"5s"`, and every
duration in the portfolio's YAML is written `5s`. So a `timeout` under
`spec.slots`, `spec.files` or `spec.templates` — the keys whose values reach a
library struct that carries one — is parsed by `time.ParseDuration` and carried
as that number, and the operator keeps writing `5s`. It is those keys and not
every field named `timeout`: `spec.settings`, `spec.subagent` and every key
Cairn does not render are documents somebody else reads, and rewriting a string
into a number inside one of them would hand a harness a value its own schema
rejects — silently, because a wrong duration is a wrong number rather than an
error.

`spec` is the rendering manifest: authored as YAML and converted key by key
into the JSON every shape below decodes from, of which Cairn interprets only
the keys it renders. Anything else is converted like the rest and carried.

```jsonc
{
  "templates": {
    "AGENTS.md": { "kind": "static_file", "static_file": { "path": "~/.config/agents/templates/base.md" } },
    "CLAUDE.md": "@AGENTS.md\n"
  },
  "slots":     [ /* agentcontext.SlotSpec — addressed by name from a template */ ],
  "mcp":       [ /* agentlaunch.MCPServerSpec */ ],
  "skills":    [ "code-review", "capture-decision" ],
  "settings":  { /* into .claude/settings.json, laid out and not otherwise touched */ },
  "subagents": [ "reviewer", "worker" ],   // profile ids; each renders a definition
  "subagent":  { /* this profile's own definition, when another names it */ },
  "trees":     { "docs/engineering": "~/.config/agents/docs/engineering" },
  "files":    {
    "rel/path.md":             "literal content",
    "tasks/current/task.md":   { "kind": "cmd", "cmd": { "run": "torque task get $TASK --format md" } },
    "process/implement.md":    { "kind": "static_file", "static_file": { "path": "~/.config/agents/process/implement.md" } }
  }
}
```

A `templates` value takes the same two shapes a `files` value does, and for the
same reason: a template is text, and text a profile already knows is a literal
while text that lives on disk is a source. What separates the two keys is what
happens afterwards — a template's markers are substituted, a file's bytes are
not.

A `trees` value is a source **directory**, copied whole. Deliberately not a
`static_dir` slot source: that resolver concatenates the files it finds into one
string, which is right for a slot and destroys a directory. A single file rides
`files` with a `static_file` source.

A `files` value is **either a literal string or an `agentcontext.SlotSource`**,
resolved by the same resolvers `slots` uses. This is what gives parity with
Torque, which plants a task bundle — `tasks/<id>/task.md`, `task.json`, and a
per-task `process.md` — all rendered from live state, not from static profile
content. Slots are addressed by name from a template; `files` renders the same
sources to arbitrary paths, unsubstituted. A value that is neither of those two shapes is refused, by
path — a silent coercion would plant bytes nobody wrote at a path a profile
promised.

**A file source that fails fails the boot**, which is deliberately the opposite
of a slot. A slot that does not resolve leaves a section out of the template
that named it and the agent asks its tools instead; a file that does not resolve leaves a hole at
a path the profile promised, and whatever reads that path cannot tell "never
declared" from "the command that fills it fell over". Sources resolve before
any file is rendered, so a refusal writes nothing at all rather than half a
directory.

A source that **resolves empty** is not a failure, and plants an empty file.
The resolver was reached and it answered; that the answer was empty is content,
and content is a black box (§1). Concretely, this is why the request declares
its slots non-required: `agentcontext`'s `Required` flag fails an assembly on
an empty result as well as on a failed one, and a task list that is
legitimately empty is not a boot that should refuse to start.

### Cascade

`extends` composes ancestor-first. **Keyed collections merge by key. Everything
else replaces, closest-wins.** `body` is the exception to both: it concatenates
ancestor-first, because the persona is additive by nature.

A descendant's member of a keyed collection replaces the ancestor's member **at
that key** and leaves the rest standing. A role profile that changes one slot
declares that slot and nothing else.

This supersedes the prior rule — *uniform closest-wins for every field,
including every top-level key of `spec`* — which was chosen for readability and
paid for in restatement: every one of the eight role profiles opened by
spelling out `base`'s `standing` and `context` slots again, and the four that
declare templates spelled out its `AGENTS.md` entry too. Five lines each in
`director.md`, `engineer.md`, `conductor.md` and `orchestrator.md`; four in the
rest.

The ADR that decided this quotes a larger figure — `director.md` 14 of 20 spec
lines, `conductor.md` 34 of 42 — and that count is wrong, established when the
restatements were actually removed. It measured every spec line beyond a
minimal profile, which sweeps in each profile's own `CLAUDE.md` and `boot.md`
entries and conductor's 26-line `fleet` slot. Those are not inherited from
anywhere. The case for composing parts never rested on the size of the number,
so the decision stands as made; the number does not.

What is lost is real and is not mitigated by prose: a profile can no longer be
read without walking its chain. The answer is a tool that prints a resolved
profile, not a smaller rule — `cairn show`, in §7, which lands with this rule
rather than after it.

**What is keyed, and by what:**

| `spec` key | Shape | Key |
|---|---|---|
| `templates` | map | destination path |
| `slots` | list of objects | `name` |
| `files` | map | boot-relative path |
| `trees` | map | destination path |
| `skills` | list of ids | the id |
| `subagents` | list of ids | the id |
| `install` | object | member name |
| `install.skills` | list of ids | the id |
| `access` | object | member name |
| `access.directories` | list of paths | the path |
| `settings` | object | every key at every depth |
| `mcp` | list of objects | the `name` field |

`install` is itself a keyed collection and it is the row easiest to miss: a
leaf declaring `install: {"skills": [...]}` keeps an ancestor's `install`
members it did not mention. `skills` is the one member of it with a rule of its
own — the nested row above — and every other member replaces whole, so an
install-only key added later behaves predictably without a decision here.
`access` is the same shape for the same reason, and its `directories` union
because a leaf extending a profile to reach one more directory must keep the
ones its ancestor reached — replacing would make extending take access away.

Everything else replaces whole: `provider`, `model`, `skills_dir`, `abstract`,
`subagent`, every scalar column — and every key `spec` carries that Cairn has
never heard of. An unknown key is carried through the cascade and rendered by
the provider renderer if it knows it, and ignored otherwise. It is never an
error.

That last one is why the rule is a **table and not an inference**. A rule like
"any list of scalars unions" cannot tell `subagents` from the `tools` inside an
opaque `subagent` declaration, or from an unknown key's list, and would reach
inside both — which would end the promise that a key this repository has never
heard of survives the cascade byte for byte. A key earns a merge by being named
in the table above, which is `profile.specMergers`' ten top-level entries plus
the two rules that table nests, one under `install` and one under `access`.
Nothing else does.

**A deviation worth recording, because a document elsewhere said otherwise.**
The keyed-merge architecture document's §1.2 table originally had `mcp` as *an
object keyed by top-level server name*. It is not one. `Spec.MCP()` decodes
`[]agentlaunch.MCPServerSpec`, and that type carries a `name` JSON field — so
`mcp` is **a list of objects, keyed by each member's `name` field**, which makes
it structurally identical to `slots`, and one mechanism composes both. That
document has since been corrected to key on the `name` field; the table above
and the manifest example earlier in this section are the statement this
repository holds to. Nothing declares `mcp` today, so the correction cost
nothing here but would have been expensive to find later.

**Ordering is deterministic and nothing may depend on it.** A merged collection
is emitted in key order. Declaration order carries no meaning: a template owns
document order through its marker positions and addresses a slot by name, and
every other keyed collection is a set or a map. Sorting is what makes two
renders of one profile identical; it is not a contract about sequence.

**Clearing:**

- `"key": null` **at the collection** clears the whole collection. Presence
  wins, and an explicit null is presence — it wins with an empty value rather
  than falling through to the ancestor.
- `"key": null` **at a member** removes that member from the merged collection
  — but only where an ancestor also declared the collection. Where exactly one
  profile declares the key its bytes are carried unread (see the byte-identity
  rule below), so a member-level null in it is never interpreted as clearing:
  it reaches the accessor, which refuses it. `templates` and `files` report a
  value that is "neither a string nor a source object"; `trees` reports
  `ErrTreeSource`. Loudly, so nothing is mis-rendered — but a refusal, not a
  removal, and the difference shows up the moment a second profile joins the
  chain.
- `[]` and `{}` mean *"I add nothing"* and no longer clear. Under the prior
  uniform-replace rule an empty collection cleared; under the merge it composes
  with the ancestor's and contributes no members. Clearing is null and only
  null.

**Member-clearing has an honest gap.** It has a natural spelling only where a
member sits under a key: `templates`, `files`, `trees` and `settings`. The
list-shaped collections — `slots`, `skills`, `subagents`, `install.skills` —
identify a member by a field inside it rather than by a key above it, so there
is nowhere to write the null. **A single member of those cannot be removed.**
The whole collection still clears with `"key": null` — but one manifest holds
one value per key, so no profile can both clear a collection and redeclare it.
Replacing a set therefore takes **three profiles**: the ancestor, an
intermediate whose only job is the null, and the leaf that declares the new
set. That is the real cost, stated rather than dressed up as an escape hatch.
No syntax is invented for the missing case: an honest gap recorded is worth
more than a spelling nobody asked for.

Two consequences worth stating, because both are edges a merge has and a
replace did not:

- A **descendant cannot restate a literal null in order to keep it**. Null at a
  member is the clearing spelling, so `"model": null` written in the closer
  profile removes the key rather than setting it. This is narrower than "a
  merged document cannot carry a null": an ancestor's literal null stands as
  long as the descendant never mentions that key, and `{"model": null, "a": 1}`
  merged with `{"b": 2}` keeps all three. What has no spelling is the closer
  profile saying *null, and I mean it*.
- A key **exactly one profile in the chain declares is carried byte for byte**
  — spelling, whitespace and key order included. A merge needs two declared
  values, so one never reaches a merger. This is load-bearing rather than
  incidental: a manifest value is the operator's own, and re-serializing one
  only `base` declares would HTML-escape the `<`, `>` and `&` inside it. What a
  renderer does with a value afterwards is the renderer's, and the JSON
  artifacts are laid out on the way out — see §6.

`$VAR` and `${VAR}` are expanded in **every manifest value that names somewhere
to read from**: a slot source's static path and HTTP URL, a `trees` source, and
`skills_dir`. A profile can then say where something lives without hardcoding a
path that differs between machines and without Cairn growing a second
configuration file to hold one. A leading `~/` expands too, and after the
variables rather than before — a variable holding `~/agents` gets its tilde
expanded as well, where the other order would leave one in the middle of the
result.

A `cmd`'s command line is deliberately **not** expanded: letting the environment
rewrite what runs is a larger promise than "say where to read from", and a
command already runs through a shell that does its own. An unset name expands to
nothing, as in every shell — so a diagnostic about a path names **what the
operator wrote and what it expanded to**, because `$ROOT/docs` with `ROOT` unset
becomes `/docs`, which is absolute and passes every check but the last.

That holds for a slot as well as for the values Cairn checks itself, and there
it is the only way the declared form survives at all: expansion runs before the
request is built, so a resolver is handed `/process.md` and reports
`/process.md` — correct, and all it can say. Cairn adds what was written ahead
of the resolver's own message and leaves that message untouched.

Nothing below the composition root reads the environment. The lookup is carried
on the instance the way the operator's home is, so a renderer has no hidden
input and a caller that supplies none expands nothing rather than silently
reaching for the process's.

That is what makes `--profile <dir>` a flag and not a feature. A **profile
bundle** is a directory holding `profiles/`, `templates/` and `skills/`, and
`--profile` seeds `$CAIRN_PROFILE_ROOT` with its root — so a
profile says `$CAIRN_PROFILE_ROOT/templates/agents.md` and the bundle relocates
without a value being edited. Expansion did not change to allow it: the flag
wraps the lookup the composition root already hands down, and everything below
sees one more variable. The bundle holds **handles, not content** — a profile
names where a template lives and the file stays the source of truth — and Cairn
opens no part of the bundle itself, only the paths a profile names inside it.

It reaches every value the lookup does, not only the sources a resolver reads:
`spec.skills_dir`, `spec.trees` and `spec.access.directories` expand it too,
through the lookup carried on the rendered instance. The third of those is worth
saying out loud — a granted directory is written into the harness's settings
document, so the bundle root decides part of what a launched agent may read.

The root is made absolute where the operator typed it, because a relative one is
not the same path to everything that reads it: a slot, template or files source
re-resolves it against the scope, while `spec.trees` and `spec.skills_dir`
refuse it outright as not absolute. It must name an existing directory, for the
reason the install root must, and nothing is required inside it — the check is
on the flag only, since a variable Cairn passes through untouched is not
Cairn's to vet. Symlinks are left alone, unlike a scope's: nothing compares this
path for identity, and resolving it would put a spelling into diagnostics that
the operator never wrote. It loses to nothing and wins over
`$CAIRN_PROFILE_ROOT` itself, matching `--boot-root`. The bundle is also where
the profiles come from — the catalog is the store (§3), so the
directory a profile is read out of and the directory its values expand against
are one value, resolved once.

`spec` is JSON, so slot entries use `agentcontext.SlotSpec`'s **JSON** tags.
`SlotSource.Kind` is `json:"kind"` and `yaml:"type"` — a slot copied out of a
Tether boot profile will say `type:` and fail with "unknown slot kind". Write
`"kind"`.

---

## 4. Scope

Scope is one directory path, supplied at boot, optional. It is the materialized
instance's working directory.

**One validation, and it guards a write:** the boot directory must never land
at or inside the scope directory. Checked, with a test.

The rest of the prior scope validation — rejecting `/etc`, `/usr`, `/var`,
`$HOME`, the filesystem root — is dropped as premature. See §1, single user.

**The scope is also granted to the harness**, which is the second thing it now
is and the reason `scope.Parse` insisting the directory exist earns its keep
twice. A boot directory's settings document carries the resolved scope under
the access grant of §6, so a session launched in one reaches its scope without
a flag. Nothing about the working directory changes; what changed is that the
value now leaves cairn in a file as well as in a printed path.

`spec.access.directories` adds to it — see §6 — and the installed layer takes
those and never the scope, because a scope is true of one session and that
layer is read by all of them.

---

## 5. Output contract

```
<boot-dir>/
  <spec.templates>       ← text with markers, substituted; any path, any number
  .mcp.json              ← spec.mcp
  .claude/settings.json  ← spec.settings, laid out, + the access grant
  .claude/skills/        ← spec.skills, directory trees
  .claude/commands/boot/ ← spec.prompts, one substituted file per prompt
  .claude/agents/<id>.md ← spec.subagents, one per named profile
  <spec.trees>           ← source directories, copied whole
  <spec.files>           ← arbitrary paths
```

**No file in that list is one Cairn names.** `AGENTS.md`, `CLAUDE.md` and
`boot.md` are template destinations a profile happens to declare, and a profile
is free to declare none of them, all three, or fifty others. One template with a
single inline slot holding a whole document is valid; so is a template per
section. Whether one of them is a shared base is the operator's convention and
invisible to Cairn.

Location: `~/.local/state/cairn/boot/<profile>/<session>/`, or
`$CAIRN_BOOT_ROOT` when that is set.
Outside any repository, so there is nothing to ignore it in and nothing
tracking it. Retention is the caller's. `cairn boot` prints the path — or, with
`--json`, one object describing the boot, which is what a launcher reads
instead of scraping the rendered prose — and exits; a human launches.

Rendering is deterministic: same inputs, byte-identical output. Slots resolve
at materialization and may therefore vary between two runs — that is a property
of the resolver, and `agentcontext` hashes the request rather than the result
for exactly this reason.

**A slot that resolves empty, and a slot that fails, both render nothing at
all** — no heading, no marker. `agentcontext`'s `DefaultRenderer` emits a bare
heading for both; Cairn's renderer omits the section entirely, matching Tether's
template behaviour.

That rule is why **a slot's heading belongs to the slot, not to the template**.
`SlotSpec.Section` already carries it, and a marker substitutes the heading and
the content together or nothing at all. A template holding `## Memory` above the
marker would keep that heading when the slot failed — a section with nothing
under it, which is the artifact this rule exists to prevent.

**A slot that declares no `section` renders bare content.** The library falls
back to the slot's name as the heading, which was right when the assembled
output was a list of sections and every slot wanted one; in a template the
template supplies the structure, and most slots carry prose whose own headings
are already inside them. A bare `role` line above such a block is Cairn writing
a heading nobody declared. An absent `section` and an empty one are the same
thing and have to be — `SlotSpec.Section` is a plain string, so the two are
indistinguishable once the manifest is decoded. Tether hit exactly
this and paid for it in `{{- if hasSlot "recap" }}` around every section of its
default template; keeping the heading on the slot is what buys the same
behaviour with no conditionals and therefore no template language.

An earlier revision had a failed slot render `**Unavailable.**` plus the error,
to distinguish it from an empty one. That was wrong twice over: it is Cairn
authoring prose into the agent's context, which §1 forbids, and it is
unnecessary now that agents pull current truth from tools rather than from a
planted snapshot. An absent section is correct — an agent that needs the data queries
the tool. **The failure still reports on stderr, to the operator.**

### Templates

A **template** is text with markers. A **slot** is a marker plus a declared
source. Cairn finds markers, substitutes, and writes. It never reads what came
back.

Resolving a marker is not interpreting content: deciding what an instruction
*means* is out of scope, and finding a marker and substituting a declared source
is mechanical. Cairn already did the second when `boot.md` was the only place a
slot could land.

Two verbs, and nothing else:

```markdown
<!-- cairn:slot memory -->     one of spec.slots, by name
<!-- cairn:value scope -->     one of: model, profile, provider, scope, session
```

The syntax is an HTML comment for four reasons. It is invisible in rendered
markdown and unmistakable in source. It is already this repository's comment
idiom — `install`'s generated-file marker is one. A harness resolving `@name`
imports strips HTML comments before it looks for them, so a marker can never be
read as an import — which matters, because `@` is the one syntax a template must
be able to pass through untouched: `CLAUDE.md` holding `@AGENTS.md` is how the
harness is told where to look. And there is nowhere in it to put a conditional,
which is the property that keeps a template a substitution target rather than a
program.

**Instance values are `inline` slots the composition root supplies**, under a
verb of their own rather than a reserved prefix inside the slot namespace. The
two categories genuinely differ — one has a source the operator declared, the
other does not — and a separate verb keeps the slot namespace entirely the
operator's and makes a seventh value later a non-event. The boot directory's own
path and the operator's home are deliberately absent: both are absolute paths
into one machine rather than facts about a profile, and the first is the
directory the file is being written into.

**No default template.** Cairn ships no profiles and no skills; a default
template is content by the same rule. A destination with no template declared is
not rendered.

**This reverses a documented rule.** The instruction file used to be rendered
always, empty if a profile declared nothing, on the reasoning that a boot
directory missing one looks like a render that stopped halfway. That was safe
only while Cairn composed the file itself. It no longer does, so a profile that
declares no template gets no prose — and the pointer file goes with it, because
a hardcoded `@AGENTS.md` aimed at a file that is not rendered resolves to
nothing, silently, with no diagnostic from the harness at all. Both are
templates now, and a pointer a profile declares is one a profile can keep true.

**A marker whose slot was declared and then filled nothing is omitted, and
reported on stderr** — whether the slot failed to resolve or resolved empty. A
marker naming a slot the manifest never declared is omitted too, and says
nothing: the warning exists to debug a block that was supposed to fill, and one
template can carry every marker any profile might fill, so a line per marker a
profile does not use would bury the one line that matters. The report exists so
an operator can diagnose a missing block, not so Cairn can judge the result. A
marker in Cairn's own namespace that Cairn cannot act on — an unknown verb, or a
body that is not exactly a verb and one name — is a **refusal**, because leaving
it would plant the marker's own text in a file an agent reads.

**A `cairn:value` naming something Cairn does not fill is not one of those.** It
renders nothing and is reported, the way a declared slot that filled nothing is.
The value names are Cairn's own, which is why an unknown one earns a line on
stderr where an undeclared slot earns silence — but refusing it would let one
word in a document every profile shares decide whether a boot directory is
written at all.

Substitution does not look at where in a document a marker sits: one inside a
fenced code block is substituted like any other, so a template documenting this
syntax has to avoid writing a live marker.

### Subagent definitions are rendered, not computed

`spec.subagents` is a list of profile ids. Each is looked up, resolved through
its own cascade, and written to `.claude/agents/<id>.md`. The content is that
profile's own `spec.subagent`: an opaque map, transcribed into the
definition's frontmatter the way `spec.settings` is transcribed into the
settings document.

**A parent may not narrow or expand a child.** There is no intersection of tool
lists, because Cairn has no `tools` concept and is not getting one. If a child
lacks a tool it needs, that is a change to the child. Depth is 1 structurally:
Cairn renders the ids a profile named and stops — it does not read a named
profile's own `spec.subagents`, and a subagent gets no boot directory of its
own. Nothing is withheld to enforce that, because there is nothing to withhold.

Three things Cairn does decide, each because the alternative fails silently:

- **`name` is forced to the profile id.** The harness resolves a definition by
  its `name` field rather than by its filename, and a definition carrying no
  name is dropped with no diagnostic at all — verified against Claude Code
  2.1.246, whose loader returns before it logs. A declaration whose `name`
  disagrees with its own id is **refused** rather than overwritten: overwriting
  discards a line the operator wrote. Every other key, known or not, is carried.
- **An id with no profile, an abstract profile, or a profile declaring no
  `spec.subagent`** each refuse the boot, naming the id and the profile that
  named it. Abstract matches `boot`: an abstract profile exists to be extended,
  and a definition is something a harness runs.
- **A duplicate path is still a duplicate path.** A `spec.files` entry at
  `.claude/agents/<id>.md` collides with the definition and refuses, because
  the definitions arrive as their own renderer rather than merged into the
  files map, where one would have silently won.

**The body comes from the declaration's own `body` key**, lifted out of the
frontmatter, and deliberately not from the named profile's cascaded body. The
reason is measured rather than assumed: a subagent's query carries the project
instruction block unless its definition sets `omitClaudeMd`, which only Claude
Code's built-in definitions do — so a subagent dispatched inside a boot
directory already receives that directory's `CLAUDE.md`, and through it
`AGENTS.md`. Rendering the named profile's cascade into the definition would
repeat every ancestor body the parent already supplies, and would put an
ancestor's persona — "you implement one task end to end" — into a definition
for a profile that reviews. Taking the named profile's own prose instead
would mean reading a field that had skipped the cascade, which §3 says no field
does. The declaration is where the definition is declared, so the body is
declared there too, and a descendant restating `spec.subagent` restates it
whole.

Transcription rather than a byte copy is forced by the destination: the
manifest is JSON and frontmatter is YAML. Values are carried by value — a
string that reads as a number stays a string, a number keeps the text the
manifest spelled it with, and the operator's key order is kept.

### The installed layer

`cairn install <profile>` renders a different, shorter set into
`~/.claude`:

```
~/.claude/
  AGENTS.md              ← the template declared for AGENTS.md, + a provenance comment
  CLAUDE.md              ← the template declared for CLAUDE.md, no marker
  settings.json          ← spec.settings + spec.access.directories, laid out,
                           no marker (JSON has no comments); no scope, which
                           belongs to one session and this file is read by all;
                           merged by key, so a key Cairn does not declare stays
  skills/                ← spec.skills, directory trees
```

**Templates are rendered here, but only those two destinations.** A template
free to name any path in the operator's home would cost `--check` its whole
point: the claim set comes from the renderer registration rather than from a
render, which is what lets it report a file left behind by a profile that
stopped declaring one, and a claim set derived from the profile being checked
cannot see that case at all. A template declared for any other destination is a
boot-directory artifact and is not rendered here. `boot` has no such constraint:
it creates the directory fresh and refuses to plant if it already exists.

**This layer resolves the slot kinds whose answer changes only when the operator
changes something** — `static_file`, `static_dir`, `inline`, `role_summary` —
and no others. That is what makes an installed template worth writing at all:
shared prose composes from static sources. A `cmd` or `http_*` slot renders
nothing here and is named once on stderr.

The line is drawn by what `--check` needs, not by what is safe to run. A check
re-renders and diffs against disk, so a source whose value can differ between
two renders of one profile reports drift on **every** run — a gate configured
not to gate, the same disease the orphan sweep is scoped to avoid. A static file
can change too, and reporting *that* as drift is the point: the operator edited
the source and has not installed it. It is the answer `--check` already gives
for an edited skill. The other half is that a check is documented to write
nothing and go nowhere near a live configuration; running a profile's commands
on every invocation is a larger promise than that.

**A template shared between the layers therefore renders its `cmd` and `http_*`
sections in a boot directory and not here.**

Four things are deliberately absent:

- **No slot sections**, per the paragraph above.
- **No `.mcp.json`.** §6 drops the audit, and user-level MCP configuration is
  not a file in that directory.
- **No `spec.subagents`.** Same reason as the arbitrary paths below, plus one of
  its own: `~/.claude/agents/` is a directory the operator fills by hand, and
  claiming it would put every definition Cairn did not render into the orphan
  report.
- **No `spec.files` and no `spec.trees`.** `boot` writes into a directory Cairn
  creates fresh and refuses if it already exists; `install` writes into a
  directory that already exists and is full of the operator's live state.
  Arbitrary path→content planting is safe in the first and not the second — and
  rendering it here would make Cairn claim ownership of paths in the operator's
  home for the orphan report below.

Both layers run the **same renderers**. A second renderer per artifact is two
renderings that drift; the prior tree carried them and they did.

### In a home the harness also owns, Cairn claims what it declares and nothing else

`~/.claude` is not Cairn's directory. The harness keeps session state,
credentials, caches and one directory per project in it; the operator keeps
skills and subagent definitions they wrote by hand; Cairn plants three files
and fills the skill directories its profile names. So:

> **Cairn writes what it declares, reports on what it declares, and leaves
> everything else alone — including the parts of an artifact it shares.**

That is one rule, and it has been arrived at three times from three
directions. Stated once, with its three instances under it:

| artifact | claim |
| --- | --- |
| `~/.claude/agents/` | **Never claimed.** Claiming the directory would report every hand-written definition as an orphan, so `spec.subagents` is not rendered into this layer at all. |
| `~/.claude/skills/` | **Claimed by name.** The directories `spec.install.skills` names are swept whole; a skill the operator wrote sits beside them and is reported as unclaimed rather than as drift. (`install.Renderer.Fills`) |
| `~/.claude/settings.json` | **Claimed by key.** Every key the render declares is Cairn's, at every depth; every key it does not is carried through — its name and every token of its value byte for byte, neither re-encoded — and never reported. (`install.Renderer.Merge`) |

The failure avoided is the same one each time, and it has two edges. A claim
wider than the declaration turns `--check` into a gate configured not to gate:
`settings.local.json`, `.credentials.json`, `projects/`, `todos/` and
`history.jsonl` all live in that directory, and a sweep that called them
orphans would exit non-zero forever. Narrowing the claim to *what one render
produced* loses the orphan worth finding — a `settings.json` left by a profile
that stopped declaring one is a path Cairn claims, so Cairn reports it. The
claim therefore comes from the renderer registration and the profile's
declarations, never from a render's output.

**Claiming `settings.json` by key changes `install` as well as `--check`, and
it has to change both.** Render-and-overwrite deletes whatever the harness or
the operator put in that file, and it did: the live document held a `model`
key that `agent-setup/profiles/base.md` declines to declare on purpose, and an
install ate it. Nothing renders that key, so nothing would ever put it back —
the check was quiet afterwards, which is worse than noisy. So `install` merges
its render into what is there rather than replacing it, and the question
`--check` answers becomes *would an install change this file?* A key Cairn
never declared is then neither written nor reported.

What that costs is stated rather than hidden. Cairn can no longer promise the
file's exact contents — it already could not, since the harness rewrites them
— and it can no longer remove a key it stopped declaring, because nothing
distinguishes one from a key the harness wrote. Two documents differing only in
where a declared key sits now read as the same, because Cairn does not reorder
a file it only partly owns. And "carried through" is about members, not about
the file: the whole document is laid out again on the way back to disk, so
whitespace *inside* an undeclared value moves even though none of its
characters do.

A document the merge cannot read member by member — not JSON, not an object, or
declaring one key twice — is not merged at all: the render overwrites it and
`--check` reports it. A file Cairn cannot parse is not a file it can claim part
of, and silently collapsing a duplicate key in the document that carries the
permission mode is not a repair.

**The fence is the installed layer.** A boot directory is created fresh and
refuses to plant over anything, so Cairn owns that whole file there and nothing
about it changes — which is why the golden trees do not move.

`--check` reports and repairs nothing. It exits non-zero when out of sync.

An occupied destination — a directory or a symlink where a file renders — is
refused, and refused for the whole layer before anything moves. Half an
installed layer is not recoverable; a refusal is.

### Directory modes follow the umask

Files are chmod'd past the umask, because a skill's exec bit is load-bearing.
Directories are not, so `~/.claude` lands 0700 or 0755 depending on the shell
`install` ran from. Left alone deliberately per §1 — single user, own home. If
it is ever made explicit, 0700 is the right value, not 0755: that directory
sits beside `.credentials.json`.

### Prompts are skills for content

`spec.prompts` names flat `.md` files under `spec.prompts_dir`, planted at
`.claude/commands/boot/<name>.md`. Everything about the declaration is
`spec.skills`: it cascades and composes by id through the same table of keyed
collections, it resolves against a directory the manifest names because Cairn
ships no content of its own, and `--prompt <a,b,c>` adds to it exactly as
`--skill` does. Nothing here is a second mechanism.

**A prompt is a template**, and that is the one difference from a skill: its
text goes through the same `Substitute` `spec.templates` uses, so it carries
`cairn:slot` and `cairn:value` markers and reaches the boot directory knowing
its scope, its session and its profile. There is no new source kind and no
second parser. There are deliberately **no conditionals**: having nowhere to put
one is what keeps a template a substitution target rather than a program, and a
prompt that must differ is two prompts, or a slot.

**The `boot/` namespace is required, not incidental.** A subdirectory of the
harness's commands directory genuinely namespaces the command rather than
flattening it — verified by experiment, with the bare name answering "Unknown
command" as the control — so a prompt Cairn planted is addressed as Cairn's and
cannot collide with a command written by hand beside it.

**Cairn plants the file and stops.** There is no delivery mechanism, no
auto-fire and no `prompt_path` in `--json`: the operator types `/boot:<name>`,
and Cairn's seam stays "write the files, print the path". A prompt therefore
appears in no installed layer either — `install` renders what every session on
the machine loads, and a per-launch prompt has no meaning there, which is the
same reason `install` takes none of the composition flags.

---

### Skills are directories, deliberately

`agentlaunch.NativeFileSkill` resolves to a flat `.claude/skills/<id>.md`.
Claude Code's current convention is a **directory**: `.claude/skills/<slug>/SKILL.md`
alongside `references/` and scripts. Cairn therefore ports the prior tree's
directory-tree copier and uses `NativeFileKind` only for `raw`.

Do not "fix" this back to `NativeFileSkill`. It would silently flatten every
multi-file skill.

---

## 6. Authority is rendered, not decided

`spec.settings` holds one document per provider — `settings: {claude: {...},
codex: {...}}` — and the document for the harness being materialized into is
written into that harness's settings file as the cascade composed it. Cairn
does not model permission modes, does not validate tool names, does not read a
rule the operator wrote, and makes no claim about what the harness does with
what it writes. Selecting a document is not reading one, and neither is merging
it: the choice is made on the provider's name and nothing below it.

**A provider is a materialization target rather than a property of the
content.** `access`, `slots`, `templates` and `skills` are neutral and serve
every target, so one profile materializes into any harness cairn has a layout
for; only `settings` is written in a harness's own vocabulary, so only
`settings` is asked which harness it is for. `--provider` on `boot`, `install`
and `show` names the target, defaulting to the one the profile declares. A
`spec.settings` whose members name no provider is refused
(`profile.ErrSettingsProvider`) rather than read as one target's document,
because accepting it would select nothing for every target and render a boot
directory silently missing what the profile plainly declares.

If a rendered rule turns out not to enforce, that is a finding about the
harness, not a Cairn defect.

**The one key Cairn contributes is the access grant**, and it is a mapping
rather than a judgement. `spec.access.directories` is a neutral declaration —
paths, with the resolved scope granted on top of them without anything having
to declare it — and the harness's spelling for those paths comes from the
provider adapter that owns its schema, not from a table here: cairn builds an
adapter carrying the directories and nothing else and merges the document it
returns. So cairn never names `permissions.additionalDirectories` and holds no
value for any other key in that document.

**A document that declares the granted key by hand is refused, not
overwritten** (`bootdir.ErrGrantConflict`). Declaring the harness's key under
`spec.settings` was the only way to grant a directory before `spec.access`
existed, so a profile still doing it would otherwise lose its grants on
upgrade — silently, since the render succeeds either way. Two sources for one
value is the ambiguity the neutral declaration exists to remove; unioning them
would let a grant reach the file through a channel that gets none of the
checks below. The refusal names the key it found and the key to move it to.
The conflict is detected by walking the *fragment's* own keys against the
declared document, so cairn still names none of them and looks nowhere the
adapter's answer does not already sit.

**A declared directory is checked before it is granted.** Expanded, then
refused unless it is absolute, refused unless it names an existing directory,
then symlink-resolved — the four steps `scope.Parse` performs on the scope,
because the scope is one of these grants and two spellings of one directory
must not arrive as two. The absolute check is the load-bearing one: the harness
reads this file from inside the boot directory, so a relative `..` granted
verbatim is the boot root and every other session's boot directory under it.
A grant that widens by accident is the one outcome this key must never produce.

What it costs is stated in `bootdir.RenderSettings`: a document cairn
contributes to is composed rather than carried, so its keys come back sorted at
the levels the merge touched. A document cairn contributes nothing to is still
the operator's bytes.

The one edit is layout. Every JSON artifact cairn writes — `.mcp.json` and the
settings document — is indented two spaces per level, because the operator
reads these files and diffs them, and because Claude Code rewrites the settings
document it was handed at exactly that width. `json.Indent` moves whitespace
between tokens and changes nothing else, so key order, string spelling and
number spelling all survive it; laying a document out is not re-encoding it,
and a hand-spelled document still comes back out with its own characters. That
is also why `--check` normalizes both sides through the same function before
comparing: a check that reported the harness's own layout as drift on every run
would stop meaning anything, and one that forgave more than whitespace would
stop being a check. Layout is all that normalizing decides. *Which* keys the
comparison answers for is a separate question, settled by the claim in §5 —
the keys Cairn declares, and no others.

This drops the prior tree's `bootdir/settings.go` and its 636 lines of tests,
its closed `PermissionMode` enum, and its `access[] → Read(path/**)` translator.
It also drops `install`'s MCP audit, which refused to install when the
installed layer already configured an MCP server.

---

## 7. Commands

```
cairn boot <profile> [--scope <path>]               materialize a boot dir, print its path
cairn install <profile>                             render the installed layer
cairn install <profile> --check                     re-render, diff against disk, report drift
cairn show <profile> [--scope <path>]               print what the profile resolves to
cairn list                                          enumerate the catalog
```

All four take `--profile <dir>`, naming the profile bundle the catalog is read
from and seeding `$CAIRN_PROFILE_ROOT` with its root — see §3. `show` reports
the root as well, because it expands nothing itself.

`list` prints the profiles with their descriptions, the abstract ones apart
from them, and the layouts a profile's `{{ extends }}` can name — each in its
own block, and an empty block omitted. It
exists because a file-backed catalog with no way to list it is a directory the
operator has to `ls` themselves — the answer is in no one file — and because
the conductor profile's launch menu was a SQL query until the database went
away. It takes no target: one profile is what `show` is for.

It does not print the bundle's own path. The listing is read in a terminal,
where the operator just typed the flag that chose the bundle, and in a boot
file, where a machine-specific absolute path would be the one line of a render
that differs between two checkouts. `show` reports the root.

`install` takes the same argument as `boot`, and there is no default. A
well-known id like `base` would mean Cairn knowing the name of a profile it
does not ship. Unlike
`boot`, `install` may be given an `abstract` profile — the installed layer is
normally rendered from the abstract root of the cascade.

`cairn install` is human-executed, permanently. Every agent working on Cairn
runs under `~/.claude`; an agent running `install` rewrites its own live
configuration mid-session.

`show` resolves and prints. It renders nothing and writes nothing — no boot
directory, no installed layer, no temporary file, and nothing in the bundle it
read. That is unqualified now and was not always: until the catalog became the
store, opening the database created one, and did it before the target was
looked up, so a `show` that then failed still left a directory and a schema'd
file behind. It is the mitigation §3 owes the operator: a
value the cascade composed from three profiles reads exactly like a value one
profile wrote, so each manifest key is printed beside the profiles in the chain
that declare it, and beside any flag that contributed one.
Per key, not per member — naming the profile behind one merged slot is
provenance the cascade does not keep, and adding it is a change to `Resolve`
rather than to its caller. Like `install` and unlike `boot`, `show` accepts an
`abstract` profile: the abstract root is the profile most worth reading.
`--scope` reports what `boot` would work in rather than changing anything,
because scope is an instance value and no part of the resolved manifest depends
on it; a scope that does not resolve is reported on stderr rather than refused,
since the containment guard that refusal exists for guards a write `show` never
makes. `--profile` is reported the same way and for the same reason — `show`
reads no value, so it expands nothing — but a `--profile` that names no
directory is refused rather than reported, because unlike a scope it is always
typed on the command line being run. `--provider` is reported the same way as
`--scope`: it names the harness a boot of this target would materialize into,
which is now a choice rather than a reading of the profile. `show` looks up no
layout for it — it renders nothing, so a target cairn cannot yet write is still
a profile worth reading, and `boot` and `install` are where such a target is
refused.

**`install --root` requires the directory; `boot --boot-root` creates it.** The
asymmetry is deliberate and is not a thing to tidy. `install` writes into the
operator's real home, and refusing an absent `--root` (`install.ErrRootNotFound`)
is what stops a typo silently building a parallel configuration tree that
nothing will ever read — the failure would be invisible, because a render into
the wrong place succeeds. A boot directory is disposable and creating its root
is the point: `cairn boot` plants a fresh directory per session under a root
that may well not exist yet on a new machine. `--profile` follows `install`'s
half for the same reason, one step earlier: a bundle that is not there is a
sign cairn was pointed somewhere wrong.

Profile authoring is a text editor. A profile is a file under
`<bundle>/profiles` and a layout is a file under `<bundle>/templates/layouts`;
there is no import command and nothing to import into.

---

## 8. Build order

**Nothing else lands until `cairn boot` writes a directory that can be opened
and read.** Until that exists, every question about content is answered by
reasoning instead of by looking.

1. **Module skeleton** — `go.mod` with the adopted modules. `cmd/cairn`
   with flag parsing that errors honestly on every unimplemented path.
2. **Catalog** — read `<bundle>/profiles/*.md`, `<bundle>/profiles/parts/*.md`
   and `<bundle>/templates/layouts/*.md` into memory, convert each profile's YAML manifest into the JSON `spec` the
   rest of the tree decodes, and refuse a bundle that is not there. It was a
   sqlite store until the catalog replaced it; the shape of what is loaded did
   not change.
3. **Profile + cascade** — load by id, walk `extends` ancestor-first, detect
   cycles, closest-wins over the columns, keyed collections in `spec` merged by
   key and every other `spec` key replaced whole, concatenate `body`.
   `Resolve` carries the `abstract` flag rather than acting on it — `install`
   legitimately resolves an abstract profile. `cairn boot` is what refuses one.
4. **Render** — `[]bootdir.File` from a resolved profile: templates with their
   markers substituted, `.mcp.json`, `.claude/settings.json`, skills, subagent
   definitions, trees, `spec.files`. Path validation and duplicate-path
   detection. No filesystem writes.
5. **Slots** — `spec.slots` → `agentcontext.ContextRequest` →
   `DefaultProvider.Assemble` → one rendered section per slot, addressed by
   name from a template. Wire `resolvers.Default()`; add
   `resolvers.WithSkillIndex` only if a profile needs it.
6. **Plant** — `writeTree` ported. Scope containment check. `cairn boot` end
   to end, printing a path. **This is the MVP gate.**
7. **Install** — port `install/`, minus the MCP audit refusal. `cairn install`
   and `cairn install --check`.

Steps 2–5 are independent enough to run in parallel behind the interfaces step
1 fixes. Step 6 is the barrier.

---

## 9. Open, tracked, not blocking

- **Operator review of rendered prose.** The prior tree's `AGENTS.md` section
  renderers and `overrides/render.go` preamble wrote Cairn's own editorial voice
  into agent contracts — "Escalate to your `reports_to`, not sideways", "never
  as instructions addressed to you". Under §1 that content belongs in a profile
  body, not in the binary. **Chrispian reviews and rewrites these before go-live.**
  Until then, render structured sections from declared fields only, and no
  authored paragraphs.
- Codex and opencode planters, via `go-providers` `BootDirSpec`. Claude first.
- `cairn profile import/export`.

### Named at the end of the MVP build, deliberately not chased

None is a defect. Each is recorded because the reason for leaving it is the
useful part, and an unnamed loose end gets rediscovered as a bug.

- **`bootdir.Renderers()` labels are Claude-shaped strings** — `.mcp.json`,
  `.claude/settings.json`, `.claude/skills`. `Renderer.Artifact` is a label and
  never a path, so nothing breaks, but a render error would name a path codex
  does not use. Rewriting them provider-neutral with no second provider to test
  against is speculative; the right moment is when the codex layout arrives.
- **`bootdir.Planter`, the `plant.Planter` adapter, has no caller.** It exists
  because §2 names that contract as the write boundary, so Cairn speaks what
  Nanite speaks. Cairn's own path uses `PlantFiles`, because `plant.Spec`
  carries no file modes and a skill's executable bit is load-bearing. If
  nothing external ever calls it, it is ceremony — decide when something tries.
- **A golden fixture would have to avoid the literal `.claude` name.** Cairn's
  output *is* a `.claude/` tree, so recording one byte-for-byte checks live
  configuration into the repo. `testdata` is inert to the go command and means
  nothing to a harness: reading any file under the project root makes Claude
  Code probe `<dir>/.claude/skills` in every directory between that file and
  the root and register what it finds as project skills, and load a `CLAUDE.md`
  from those same directories as instructions — which then follows its
  `@AGENTS.md` pointer, the exact pair cairn renders. The prior tree solved this
  by storing the segment as `_claude` and mapping it back where the fixture is
  read; it had three such directories and no literal one. This tree keeps no
  copy of its output at all — it asserts structure in code, and
  `bootdir/skills_test.go` builds skill trees under the test's own temporary
  directory for this reason. The module-wide walk that used to enforce the
  naming rule is gone with the goldens: it guarded a shape the design no longer
  produces, and a build that fails on a `.claude/skills` directory is failing on
  a supported feature it has no way to know was a mistake. If goldens return, so
  does `_claude`.
- **`cairn install` prints every path it wrote, unchanged ones included.**
  `--check` before an install already answers "what would change", and
  inventing a diff format nobody has asked for is how a small tool grows.

Bounded by construction, and worth knowing: §6 says `spec.settings` is written
with no validation, but a value that is not valid JSON cannot reach the
manifest at all — the catalog builds each key's JSON from the YAML the operator
wrote rather than accepting JSON text. "Not validated" is bounded by "it is at
least JSON", which is shape rather than meaning. That is the intended
line, and it is what lets the renderer lay a document out without ever needing
a fallback in practice.
