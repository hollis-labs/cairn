# Using Cairn

Cairn assembles files and writes them into a directory. It prints the path —
or, with `--json`, one object describing the boot — and exits. **You launch.**
Nothing here starts, watches, or supervises an agent.

Four commands and one workflow:

```
cairn boot <binding|profile> [--scope <path>]         materialize a boot dir, print its path
cairn boot <binding|profile> --json                   ... and describe it for a launcher
cairn boot <binding|profile> --save-as <name>         ... and save the composition as a binding
cairn show <binding|profile>                          print what the profile resolves to
cairn show <binding|profile> --json                   ... and describe it for a launcher
cairn install <binding|profile> [--check]             render ~/.claude from the same source
cairn list                                            enumerate the catalog
```

`boot` and `show` also take the composition flags — `--with`, `--skill`,
`--prompt` and `--set`, all repeatable — which say what this one launch
carries beyond the profile it names. See **Composing one launch** under §3. A
composition worth keeping becomes a **binding**: see **Saving one:
`--save-as`** in the same section.

---

## 1. Build

```bash
cd ~/dev/projects/cairn
make build            # → bin/cairn
```

## 2. Point at a bundle

Cairn ships no profiles. It reads them out of a **bundle** — a directory of
files, which is the whole store:

```
<bundle>/
  profiles/    one markdown file per profile, YAML frontmatter and prose
    parts/     the same, for the small reusable ones — one flat namespace
  bindings/    one YAML file per binding, named after the binding
  templates/   the documents your profiles name
  skills/      one directory per skill
  prompts/     one flat .md file per prompt
```

`--profile <dir>` names it; without the flag cairn reads `$CAIRN_PROFILE_ROOT`,
then `$XDG_CONFIG_HOME/agents`, then `~/.config/agents`. There is nothing to
seed and nothing to import: edit a file, and the next command reads it.

`examples/bundle/` is the worked example — a small, real bundle you can boot:

```bash
./bin/cairn list --profile examples/bundle
./bin/cairn boot eng --profile examples/bundle --scope ~/dev/projects/cairn
```

It holds an abstract `base`, a concrete `engineer` that extends it, a
`reviewer` the engineer dispatches and that also boots on its own, a
`docs-only` part, three bindings — one of which composes a
part, which is what `--save-as` writes — and the templates, the skill and the
prompt `engineer` names — plus a second prompt no profile names, which is
there to be added with `--prompt`. Read it — it is the profile-authoring
documentation.

Four things it demonstrates that are easy to get wrong:

- **`extends` merges keyed collections by key and replaces everything else.**
  A child declaring one slot, one template or one skill keeps the ancestor's
  rest — it does not restate them. Every other key is taken whole from the
  nearest profile that declares it, including every key Cairn has never heard
  of. The prose below the frontmatter is the exception to both and concatenates
  ancestor-first.
- **Frontmatter is YAML and `spec` becomes JSON.** So a slot's kind key is
  written `kind:`, and it lands as `"kind"` — which is the `type:`/`"kind"`
  trap that catches everybody copying a slot out of a YAML boot profile
  somewhere else. Cairn will tell you.
- **A duration is written the way Go writes one** — `5s`, `300ms`, `1m30s`.
  Cairn translates it into the nanoseconds a `time.Duration` unmarshals from as
  it reads the catalog, so you never write the number.
- **`key: null` clears an ancestor's key.** Presence wins, and an explicit null
  is presence.

A profile's id is its file name, and the two are held to agreeing. A binding's
name is its file name. Cairn refuses a bundle where either disagrees, and a
binding naming a profile that has no file, rather than discovering it whenever
somebody happens to boot that one name.

## 3. Boot

```bash
cairn boot eng
# /Users/chrispian/.local/state/cairn/boot/eng/20260826T014133Z-9f2a1c
```

The path goes to **stdout**; diagnostics go to **stderr**. A slot that fails to
resolve reports on stderr and the boot still succeeds — a missing memory
service should not stop you working.

Pass `--json` and stdout is one JSON object describing the boot instead of the
bare path — the scope, the settings file to promote, and how the harness wants
to be opened. That is what a launcher reads; see §5.

What lands:

```
<boot-dir>/
  <spec.templates>       your text, markers substituted; any path, any number
  .mcp.json              spec.mcp
  .claude/settings.json  spec.settings for this provider, laid out, plus the grant
  .claude/skills/        spec.skills, whole directory trees
  .claude/commands/boot/ spec.prompts, one substituted file per prompt
  .claude/agents/<id>.md spec.subagents, one per named profile
  <spec.trees>           source directories, copied whole
  <spec.files>           arbitrary paths, literal or resolved
```

**Cairn names none of those files.** `AGENTS.md`, `CLAUDE.md` and `boot.md` are
destinations the example profiles happen to declare. Declare none and you get no
prose; declare fifty and you get fifty. Cairn composes no heading, no section
and no ordering of its own.

### Choosing the target: `--provider`

**A provider is a materialization target, not a property of the content.** One
profile's `access`, `slots`, `templates`, `skills` and `prompts` are neutral and
serve any harness. The one key written in a harness's own vocabulary is
`settings`, so that key — and only that key — is written per provider:

```yaml
spec:
  settings:
    claude:
      permissions: { defaultMode: acceptEdits }
      tui: fullscreen
    codex:
      approval_policy: on-request
```

Cairn selects the document for the harness it is materializing into, merges it
by key at every depth across the chain, and **reads none of it** — selecting is
not interpreting, and neither is merging.

`--provider <name>` names that target on `boot`, `install` and `show`, and
defaults to the profile's own `provider:`. So the flag changes nothing until
you pass it:

```bash
cairn boot eng --provider claude   # what `cairn boot eng` already did
cairn boot eng --provider codex    # render Codex-native AGENTS.md/config.toml
```

Claude Code and Codex layouts are implemented. `opencode` is a name cairn knows
and cannot yet write, and it is refused rather than rendered as another
harness's layout. A word that is no harness at all is a different refusal, and
says so.

Codex boots render `AGENTS.md` and `config.toml`, and `cairn boot --json`
reports the adapter's cwd, project-dir argument, environment amendments and
operator-owned provider-home resources.
Cairn does not plant `auth.json` into disposable boot directories: a launcher
that chooses to set `CODEX_HOME={{.BootDir}}` must have an explicit credential
strategy, and a launcher that preserves the user's existing Codex credentials
should rely on the installed layer or its own config/profile routing. Codex user
skills are installed under `~/.agents/skills`, not under `~/.codex`.

The ordinary manual Codex path keeps the boot directory as the loaded project
so Codex sees its `AGENTS.md`, `config.toml` and `.agents/skills`, while the
real scope is granted with the provider's project-dir argument:

```bash
report="$(cairn boot codex-coord-cairn \
  --profile /Users/chrispian/dev/projects/agent-setup \
  --provider codex \
  --json)"
boot="$(printf '%s\n' "$report" | jq -r '.boot_dir')"
scope="$(printf '%s\n' "$report" | jq -r '.scope')"

ln -s "$HOME/.codex/auth.json" "$boot/auth.json"
ln -s "$HOME/.codex/hooks.json" "$boot/hooks.json"
ln -s "$HOME/.codex/hooks" "$boot/hooks"

cd "$boot"
CODEX_HOME="$boot" codex --add-dir "$scope"
```

Those three links are an explicit operator preparation step, matching the
`home_resource_paths` in the boot report. They expose the
operator's live Codex credential and hook registration to that one boot-local
home without copying their contents and without making Cairn claim or sweep
them. If Codex asks to trust a hook on first launch, inspect the linked
`hooks.json` and `hooks/` target before accepting. The interactive CLI does not
accept `--skip-git-repo-check`; that flag belongs to `codex exec`, where it is
needed for a non-git disposable boot directory. For a non-interactive probe use
`CODEX_HOME="$boot" codex exec --skip-git-repo-check --add-dir "$scope" "<prompt>"`.
Scope access is granted separately by `--add-dir`. Do not make
`--dangerously-bypass-hook-trust` the default manual recipe; it is only for
automation that vets hook sources outside Codex's trust prompt.

It is deliberately **not** one of the four flags below. Those add content to one
launch, which is why `install` takes none of them; this says where the content
is being written, which every command that materializes anything has to answer.

**A flat `settings:` block is refused**, naming the member it found and the
shape to move it to. Accepting it would select nothing for every target and
plant a boot directory silently missing the settings the profile plainly
declares.

### Composing one launch: `--with`, `--skill`, `--prompt`, `--set`

A profile is what a role always is. These four are what one launch is, and none
of them changes a file in the bundle.

This runs against the bundle in this directory. `docs-only` is a part that
ships in it; the second one is a file you write, which is the case the path form
exists for:

```bash
printf -- '---\nspec:\n  skills: ["capture-decision"]\n---\n' > /tmp/session-part.md

cairn boot eng --with docs-only --with /tmp/session-part.md \
               --skill capture-decision \
               --prompt reset-scope \
               --set direction="user-facing docs only, no API reference"
```

The direction lands where `engineer.md` puts `<!-- cairn:slot direction -->`,
and a plain `cairn boot eng` renders nothing there — a slot that stands for
nothing leaves nothing behind, heading included.

**`--with <part>`** merges a profile after the `extends` chain resolves,
closest-wins and in the order given. A part is an *ordinary profile* — there is
no separate fragment kind — so anything composable is also bootable and
inspectable on its own, and its own `extends` resolves against the catalog like
any other profile's.

A part is named by catalog id **or by path**, because generated,
instance-scoped composition content otherwise has nowhere to live: a file
outside the bundle has no name. **A value holding a path separator, or
beginning with `.`, `~` or `$`, is a path; anything else is a catalog id.** So
a part in the current directory is `./part.md` — a bare `part.md` is a *name*,
and cairn says so when no profile has it. `$VAR` and `~/` expand as they do in
every other value that names somewhere to read from.

#### `profiles/parts/`

A bundle accumulates small profiles that exist to be composed, and a flat
`profiles/` puts them in the same list as the roles. So one subdirectory is
also read:

```
profiles/
  engineer.md          →  engineer
  parts/docs-only.md   →  docs-only
```

**It is an organizational convention and nothing else.** Both files are
ordinary profiles in one global id namespace — same parser, same frontmatter,
same merge rules — and the directory is where the file sits, not part of what
the profile is called:

```console
$ cairn show docs-only            # not "parts/docs-only"
$ cairn boot engineer --with docs-only
$ cairn boot docs-only            # a part is bootable on its own, as ever
```

A binding stores `parts: [docs-only]`, and `--save-as` writes that — a nested
part is catalogued content reachable by name, so it is saved by id like any
other and none of the path rules below apply to it.

Writing `--with parts/docs-only` holds a separator, so it is read as a **path**
and fails. Cairn checks whether the stem names a profile and says so rather
than leaving you with "no such file":

```console
$ cairn boot engineer --with parts/docs-only
cairn: --with "parts/docs-only": read the profile parts/docs-only: open
parts/docs-only: no such file or directory; ~/.config/agents holds a profile
named "docs-only", and a profile is named by its id wherever its file sits —
write "docs-only"
```

**One directory, one level.** `profiles/parts/deeper/x.md` is not read, and
neither is `profiles/roles/x.md` — reading every subdirectory would make the
layout of the bundle part of its meaning, so a folder made for tidiness would
join the catalog and a folder of notes would be read as profiles. The cost is
that the others stay ignored *silently*, exactly as a README beside the
profiles is: they are not broken profiles and cairn does not report them as
any kind of profile at all.

A `parts` that is a **symlink** is passed over for the same reason — a bundle
is a directory of files git reviews, and a link out of the tree is content
nobody reviewing the bundle can see.

**Two files claiming one id is refused**, naming both, when the bundle is read:

```console
$ cairn list
cairn: two profiles claim one id: "docs-only" is declared by
~/.config/agents/profiles/docs-only.md and by
~/.config/agents/profiles/parts/docs-only.md — rename one, since a profile is
named by its id wherever the file sits
```

That was impossible while `profiles/` was flat: an id must equal its file's
stem, and one directory holds one `docs-only.md`. Two can. It is refused rather
than resolved by precedence, because a rule like "the root wins" is silent
shadowing — the losing file stays on disk, gets edited and committed, and never
takes effect. Note that this fails at **open**, so every command fails until it
is fixed; a tool writing into the bundle should check the id before writing the
file, not after.

A part named by path is **not** held to being named after the id it declares.
That rule is the catalog's, whose map is keyed by the declared id while the
listing walks file names, so a bundled file disagreeing with itself resolves
under one spelling and lists under the other. A part is in no such map — it is
keyed by its path. A generator may write `/tmp/session-4f2a9b.md` declaring
`id: docs-only`, or declare no id at all and take the file's name.

Cairn reads a path-named part during resolution and needs nothing after `boot`
returns. Cleaning it up belongs to whatever generated it.

**A part contributes what it adds, not what the target already settled.** Parts
and targets usually share an abstract base, and folding that base again would
put it *in front of* the target — so the base's value would beat the very
profile that overrode it. Not only reported fields: `spec.templates` is keyed
by destination, so the base's `AGENTS.md` would replace the target's and the
boot directory would carry the wrong instructions. `files`, `trees`, `slots`,
`mcp` and `settings` are the same shape.

So a profile the fold has already reached is skipped when a later chain names
it again — against the target's chain and against every earlier part alike. It
is not a new rule: resolving a single `extends` chain already refuses to visit
a profile twice, and this is that same guard across the sequence of chains.
What both keep is that no profile is ever folded after one of its own
descendants.

One consequence is worth knowing, and cairn tells you when it happens rather
than leaving the flag quietly doing nothing:

```console
$ cairn show eng --with base
cairn: --with base: already in the chain, contributed nothing
```

`base` is already what `eng` extends, so it stays where it first landed. Exit
status is 0 and the document is unchanged — naming a part the resolution
already covers is a fine way to be explicit about what a composition rests on.

**`--skill <a,b,c>`** adds skills to the ones the profile resolves to. It is
comma-separated *and* repeatable, the two forms equivalent and composing, and
it merges into `spec.skills` last and by id — exactly as a part declaring the
same ids would. `spec.skills` is what one boot directory carries, which is why
it earns a flag where `install.skills` — what every session on the machine
loads — does not.

**It is additive only.** Nothing in cairn removes one member of a collection
keyed by its own id: the null that clears a member of an object has nowhere to
be written in a list. A session that wants *fewer* skills boots a different
profile.

**`--prompt <a,b,c>`** adds prompts, in every way `--skill` adds skills: same
two spellings, same merge into `spec.prompts` last and by id, same
additive-only rule. What differs is what arrives. A skill is a directory the
harness loads on its own; a prompt is one file planted at
`.claude/commands/boot/<name>.md`,
which the operator invokes as `/boot:<name>`.

**A prompt is a template**, so it carries the same `<!-- cairn:slot ... -->` and
`<!-- cairn:value ... -->` markers a `templates:` entry does, is substituted
from the same slots and instance values, and a marker in one that stood for
nothing is named on stderr on the same terms — see *Templates and markers*
below. That last one matters more here than it does for a `templates:` entry: a
template is read, and a prompt is a command you type. There is no second
syntax, no extra source kind, and deliberately nowhere to put a conditional — that is what keeps
a template a substitution target rather than a program. A prompt that must
differ is two prompts, or a slot.

**Cairn plants the file and stops.** Nothing fires a prompt at launch: you type
`/boot:handoff`, or `@<boot-dir>/.claude/commands/boot/handoff.md`. That is why
the file is worth having — it is content, addressed by name, that survives the
launch rather than being typed into it.

**`--set <slot>=<value>`** supplies an inline literal for a named slot, merged
last. It is how a one-off direction reaches a session without authoring a file
for it. The member replaces a declared slot of that name whole — its `section`
included — because that is how `spec.slots` composes for every contributor.
Cairn gains no `direction` concept from this: it is a slot like any other, and
cairn goes on owning shape rather than content. A direction worth reusing
becomes an ordinary part and arrives through `--with`.

**`cairn show` takes all four**, and that is not a convenience. `show` is the
"what will this resolve to" preview; a preview blind to the composition would
be blind to precisely the part that makes it differ from its base. It names
every contributor beside each manifest key — the profiles in the chain, the
parts, and the flag itself:

```
spec.skills   engineer, docs-only, --skill
```

Pass `--json` and that document arrives as one object instead, contributors
included — which is what a launch palette reads rather than scraping the
columns above. See **`cairn show --json`** in §5.

`cairn install` takes none of them. It renders the layer every session on the
machine loads, and a composition is an instance concern by construction — a
binding's parts, skills and prompts are not replayed there either, for the
same reason, and `install` says so when the binding it was given composes
something.

### Saving one: `--save-as`

A composition you find yourself typing twice is a **binding**. `--save-as`
writes the one you just booted into `<bundle>/bindings/<name>.yaml`, and
`cairn boot <name>` replays it:

```console
$ cairn boot engineer --with docs-only --skill capture-decision \
                      --scope cairn --save-as eng-docs
cairn: --save-as eng-docs: wrote /Users/you/.config/agents/bindings/eng-docs.yaml

$ cat ~/.config/agents/bindings/eng-docs.yaml
profile: engineer
parts:
  - docs-only
skills:
  - capture-decision
scope: cairn
```

That is the whole format. Five keys, all but `profile` optional, in the order a
composition resolves: the base profile, the parts merged onto its chain, the
skills and prompts merged after those, and the scope — which is not part of the
composition at all but a fact about the instance. Cairn writes what a person
would type, so a saved binding and a hand-authored one are the same kind of
file; add a comment above it and nothing will take it away.

**Those five are the whole set, and a key that is not one of them is refused**
naming the line and what could have been written instead. YAML would otherwise
discard it in silence, and `part:` for `parts:` — or `skill:` for `skills:`,
one character from the flag that fills it — would give you a binding that
composes nothing, boots cleanly and never mentions it.

**Values are saved as they were written.** `--scope ~/dev/projects/cairn` saves
that spelling and not the expansion of it, for the same reason a part keeps its
declared spelling: a binding that recorded one machine's expansion is a binding
that works on one machine.

**With one exception: a relative `--scope` is saved as the directory it
resolved to**, and cairn says so. A `~/` path and an absolute path both still
name the same place tomorrow; a relative path is anchored to the working
directory of the shell that typed it, and a binding records no working
directory. Saved verbatim it would resolve somewhere else from somewhere else,
silently.

**A `--with` typed onto a binding lands after the binding's own parts**, which
is closest-wins with the terminal closest — the same rule the `extends` chain
follows. So `cairn boot eng-docs --with x --save-as eng-docs-x` grows the
composition rather than replacing it.

Two things `--save-as` does not do, and the difference between them is the
point:

- **A `--set` is dropped, and each one is named on stderr.** This run still
  gets the value; the binding does not. A `--set` carries *content*, and
  content in the catalog is the seam this design keeps clean. A direction worth
  reusing is an ordinary part and arrives through `--with`.
- **A path member is a refusal, not a drop**, and the diagnostic names the
  path. The two look alike and are not: a `--set` can be dropped soundly
  because nothing is lost but reuse, while dropping a path member would
  silently change what the binding composes. Inlining the file's content
  instead would turn a handle into content, which is the thing the bundle's
  shape rests on not doing. Put the part in `profiles/` or `profiles/parts/`
  and name it by id, or boot without `--save-as`. The refusal is about what the composition holds, not
  which flag it arrived on — a binding whose own `parts:` names a path is
  refused too, and the diagnostic says which file to edit.

`--skill` and `--prompt` go the other way and *are* saved, which is the same
distinction read from the other side: both lists are **ids**, the same kind of
thing a binding already holds for its parts, so the reason to drop a `--set`
does not transfer.

An existing binding is never overwritten — its file may hold a comment nothing
else carries — so saving over one is a refusal too.

Saving under a name the bundle already has a *profile* for is allowed, and
reported. A binding outranks a profile of the same name at every lookup, so
`cairn boot <name>` means the binding from then on; that is a fine thing to
want and a bad thing to find out later.

### Templates and markers

A template is your text with markers in it. Two verbs, nothing else:

```markdown
<!-- cairn:slot memory -->     one of spec.slots, by name
<!-- cairn:value scope -->     one of: binding, model, profile, provider, scope, session
```

An HTML comment, so it is invisible in rendered markdown and cannot be mistaken
for the harness's own `@AGENTS.md` import — which a template has to be able to
pass through untouched, since that is how `CLAUDE.md` points at the file beside
it.

A marker Cairn cannot parse is a **refusal**, and nothing is written: a verb
that is not `slot` or `value`, and a body that is not exactly a verb and one
name — no name written in it at all, or more than one. Leaving one in place
would plant the marker's own text where an agent reads it. Everything outside
`cairn:` is left alone.

Neither *name* is checked before rendering, so a marker naming a slot that was
never declared, or a `cairn:value` outside the six, is not a refusal: it renders
nothing. What Cairn *says* about the two differs, because the two sets belong to
different people. The slot set is yours, so a marker for a block this profile
does not declare is ordinary and Cairn says nothing about it. The six values are
Cairn's, so a name outside them is one nothing will ever fill: **`boot` and
`install` both name it on stderr**, with the six beside it. `install --check`
names it too, and needs to: a template emptied by a marker Cairn cannot fill
renders no file at all, and into a root that never held that file there is
nothing on disk for the check to call an orphan, so it reports "In sync" over a
pointer that now includes nothing. (Empty a template that *was* installed and
the check does catch it — the file becomes an orphan and the check fails.) A
`cairn:slot` whose slot *was* declared and then resolved
empty or failed renders nothing too, and is reported the same way, so you can
tell a missing block from one you removed. The warning exists to debug a marker
that was *supposed* to fill, and one shared template can carry every marker any
profile might fill: a line for every marker a profile does not use would be
noise deep enough to bury the one line that matters.

**Slots are where the leverage is.** They resolve at materialization, so they
carry live state rather than a paragraph that went stale a month ago. The example
`engineer` pulls `git status` and `git log`; the same mechanism reaches an HTTP
endpoint or any command, which is how memory and task state get in:

```jsonc
"slots": [
  { "name": "memory", "section": "## Memory",
    "source": { "kind": "http_json",
                "http_json": { "url": "${TESSERACT_URL}/v1/recall?limit=20" } } },
  { "name": "tasks", "section": "## Open tasks",
    "source": { "kind": "cmd",
                "cmd": { "run": "torque tasks list --status in_progress --format brief" } } }
]
```

Kinds: `static_file`, `static_dir`, `inline`, `cmd`, `http_text`, `http_json`,
`role_summary`. Write `"kind"`, not `"type"` — Cairn will say so if you forget.

**A slot that fails renders nothing at all** — no heading, no marker — and so
does one that resolves empty. Cairn writes no sentence of its own into an
agent's context, and a section that says "unavailable" is one nobody declared
and nobody can correct. An agent that needs the data asks the tool, which is
current where a planted file is a snapshot. The failure is not lost: it goes to
**stderr**, where the operator reads, and so does a marker whose slot was
declared and filled nothing.

**Put the heading on the slot, not in the template.** `"section": "## Memory"`
comes back with the content or not at all. Write `## Memory` in the template
above the marker and you keep that heading on the day the slot fails, which is
the one thing this rule exists to prevent.

**Leave `section` out and the slot substitutes bare content** — no heading at
all. That is what you want for a block of prose that carries its own headings,
which is most of what composes an instruction file.

`$VAR` and `${VAR}` are expanded in every manifest value that names somewhere to
read from: a source's **path** and **URL**, a `trees` source, and `skills_dir`.
A leading `~/` expands too, after the variables — so a variable holding
`~/agents` works. A `cmd`'s command line is not expanded; it already runs
through a shell that does its own.

An unset name expands to nothing, so `$ROOT/docs` becomes `/docs`. Cairn's
diagnostics name both forms for exactly that reason — a resolver is handed the
expansion and can only report that one, so what you wrote comes from Cairn or
from nowhere:

```
spec.trees copies "docs" from "$ROOT/docs", which expanded to "/docs",
which does not exist

slot "memory" did not resolve: the static_file path is "$AGENT_DOCS/process.md",
which expanded to "/process.md": static_file: read /process.md: no such file
```

A path with no variable in it reads exactly as it always did; the pair appears
only when expansion changed something.

### `files`: the same sources, at arbitrary paths

`spec.files` maps a boot-relative path to **either a literal string or a slot
source**. Literals cover content the profile already knows; sources cover
content that is only true at boot — a task bundle, say, planted where a process
expects to find it:

```jsonc
"files": {
  "README-for-this-session.md": "read task.md first\n",
  "tasks/T-42/task.md":  { "kind": "cmd", "cmd": { "run": "torque task get T-42 --format md" } },
  "tasks/T-42/task.json": { "kind": "cmd", "cmd": { "run": "torque task get T-42 --format json" } },
  "tasks/T-42/process.md": { "kind": "static_file",
                             "static_file": { "path": "~/.config/agents/process/implement.md" } }
}
```

**A file source that fails fails the boot** — the opposite of a slot, on
purpose. A missing section is degraded context and the agent routes around it;
a missing file is a hole at a path the profile promised, and whatever opens
that path cannot tell "never declared" from "the command that fills it fell
over". Sources resolve before anything is written, so a refusal leaves no
directory behind.

A source that resolves *empty* is not a failure and plants an empty file — the
resolver answered, and what it answered is content, which Cairn does not read.

### `trees`: copying a directory whole

```jsonc
"trees": { "docs/engineering": "~/.config/agents/docs/engineering" }
```

Destination on the left, source directory on the right, copied recursively with
executable bits preserved. The source takes `$VAR` and `~/` — it is nothing but
"point at a path", so it is the value most likely to want one. A single file rides `files` with a `static_file`
source instead. **Do not reach for a `static_dir` slot** — that concatenates
every file it finds into one string, which is right for a slot and destroys a
directory.

A symlink to a file is followed and copied by value. A symlink to a directory,
or one that dangles, is refused by name — Cairn does not walk into linked
directories, and saying so beats planting something that is not a file.

### `subagents`: naming other profiles

`spec.subagents` is a list of profile ids. Each is looked up, resolved through
its own cascade, and written to `.claude/agents/<id>.md`. What lands in the
file is **that profile's own `spec.subagent`** — an opaque map, carried into
the frontmatter the way `spec.settings` is carried into the settings document:

```jsonc
// in the profile that boots
"subagents": ["reviewer"]

// in reviewer's own spec
"subagent": {
  "description": "Reviews a diff with no shared context.",
  "tools": ["Read", "Grep", "Glob"],
  "model": "sonnet",
  "body": "Read the diff. Report what you found and nothing else.\n"
}
```

which renders:

```markdown
---
name: reviewer
description: Reviews a diff with no shared context.
tools:
  - Read
  - Grep
  - Glob
model: sonnet
---

Read the diff. Report what you found and nothing else.
```

**Being dispatchable is a capability, not a mode.** `spec.subagent` is what
lets another profile name this one. It takes nothing away: the same `reviewer`
boots into a session of its own with `cairn boot reviewer`, off the same
cascade and the same prose, and the planted definition is that role narrowed to
a dispatch rather than a different role. A profile with no `subagent` block
refuses to be named; `abstract: true` is the only key that rules out both.

**A parent may not narrow or expand a child.** There is no tool intersection,
no ceiling, no depth cap — Cairn has no `tools` concept and reads none of these
keys. If the reviewer needs a tool it does not have, edit the reviewer. Depth is
1 because Cairn renders the ids you named and stops: it never reads a named
profile's own `subagents`, and a subagent gets no boot directory of its own.

Two things are Cairn's, and both exist because the alternative fails quietly:

- **`name` is forced to the profile id**, because the harness resolves a
  definition by that field rather than by the filename, and a definition with
  no `name` at all is dropped with no message. Write a *different* `name` and
  Cairn refuses rather than overwriting what you wrote.
- **A named id with no profile, an `abstract` profile, or a profile with no
  `subagent` key refuses the boot**, naming the id and the profile that named
  it. Nothing is written.

`body` is the definition's prompt, and it is the declaration's own key rather
than the profile's `body` column. A dispatched subagent already receives the
boot directory's `CLAUDE.md`, and through it `AGENTS.md` — so the cascade is in
its context already, and rendering it again would only add an ancestor's
persona to a profile that does something else. Leave `body` out and the
definition is frontmatter and nothing else.

A `files` entry at `.claude/agents/<id>.md` is a duplicate path, and refuses.
Neither wins.

### The access grant

`spec.access.directories` is a list of paths — `$VAR` and `~/` expand — naming
what the agent may reach beyond the directory it works in. **The scope is
granted without being declared**; these add to it.

```json
"access": {"directories": ["~/dev/hollis-labs/apps/nanite"]}
```

It is neutral: it names directories, not one harness's permission key. Cairn
hands the paths to the provider adapter that owns the harness's spelling for
them, so for Claude Code they land at `permissions.additionalDirectories` in
the settings document beside whatever the profile declared there. Two profiles
in a chain that each name directories grant the union.

**Every path is checked before it is granted**, and each of these is a refusal
that names what you wrote and what it expanded to:

```
spec.access.directories declares "..", which is not an absolute path
spec.access.directories declares "$ROOT/docs", which expanded to "/docs", which does not exist
```

Relative is the one that matters. The harness reads this file from inside the
boot directory, so a `..` granted verbatim is the boot root — every other
session's boot directory for that profile. Absolute, existing, and then
symlink-resolved, which is what the scope already gets.

**Do not declare `permissions.additionalDirectories` under `spec.settings`.**
That was the only way to grant a directory before this key existed; it is now
the key cairn writes the grant into, and a document declaring it by hand is
refused by name rather than quietly overwritten. Move it here — it is the only
spelling that composes with the scope, unions across a chain, and gets the
checks above.

It is the one key cairn contributes to that document. Everything else under
`spec.settings.<provider>` is still the operator's own, unread — see the section
below for the tier that decides whether the harness honours any of it.

`spec.access` needs no provider key of its own, and that is the division of
labour: it names directories, cairn maps them onto whichever key the target
harness reads them from, and one declaration therefore grants the same
directories to every target. `spec.settings` is where you write something only
one harness understands, which is why it is the key that is asked which one.

### The settings tier, and why a launcher passes `--settings`

A `settings.json` that merely sits in the boot directory is read as
**`projectSettings`** — the untrusted tier, because a repo can control it.
Verbatim from Claude Code 2.1.246:

```
settings defaultMode "auto" ignored — only policy/user/flag settings may
grant auto mode (projectSettings and localSettings are repo-controllable)
```

So a profile declaring `defaultMode: auto` validates, renders, plants — and is
then **silently downgraded** at launch with a warning. The same rule governs
`autoMode` classifier rules.

Passing `--settings <boot-dir>/.claude/settings.json` promotes the file to
`flagSettings`, which is trusted. That is what `settings_path` in the `--json`
report is for.

This is deliberately the launcher's job. Cairn writes the file and describes
it; whoever owns the invocation decides what tier it lands in. Cairn taking it
over would mean Cairn owning the launch, which is a different product.

**`--settings` is also the whole access grant, and that has been watched.** A
session opened as `claude --settings <boot-dir>/.claude/settings.json`, with no
`--add-dir`, in a boot directory scoped to a real project, read that project
with no permission prompt and no refusal. So `permissions.additionalDirectories`
in a flag-tier settings file is the grant, and `--add-dir` is redundant for any
launcher that passes `--settings` — which is every launcher, because
`--settings` is what keeps `defaultMode` from being downgraded in the first
place.

A launcher therefore passes `--settings` and nothing else. `cairn boot --json`
still reports `project_dir_arg`, because a provider that grants directories on
the command line rather than in a config file will need it — Codex does — and
that is a fact about the provider for a launcher to act on, not a flag Cairn
has an opinion about.

## 4. The installed layer

Renders `~/.claude` from the same profiles, so the global floor and a boot
directory cannot disagree about what a profile says.

```bash
cairn install base --check     # what would change; writes nothing; exit 1 if out of sync
cairn install base             # write it
```

**Run `install` yourself, never from inside an agent session** — every agent on
this machine runs under `~/.claude`, so an agent that runs it rewrites its own
live configuration mid-session.

Slots resolve here too, but only the kinds whose answer changes when *you*
change something — `static_file`, `static_dir`, `inline`, `role_summary`. A
`cmd` or `http_*` slot renders nothing in this layer and says so on stderr,
because `--check` re-renders and diffs against disk: a `git status` slot would
report drift on every run and the check would stop meaning anything.

Cairn claims four things in that directory — `AGENTS.md`, `CLAUDE.md`,
`settings.json`, and the `skills/` subtree. Not `agents/`: subagent definitions
are a boot-directory artifact, and claiming that directory would report every
definition you wrote by hand as an orphan. Everything else (`settings.local.json`,
`.credentials.json`, `projects/`, `todos/`, `history.jsonl`) is invisible to
`--check`, which is why it does not cry wolf on a real installation.

---

## 5. Calling Cairn from a launcher (Tachyon)

Tachyon's engine seam is already the right shape for this: the Swift app shells
out to `tachyon-engine`, parses JSON on stdout, and spawns the agent itself.
`tachyon-engine launch` **emits** a runnable command rather than exec'ing it —
exactly Cairn's contract.

So Cairn slots in as a step before the spawn:

```
Tachyon.app  ──shell──▶  cairn boot <binding> --json  ──▶  boot dir on disk
     │                                                          │
     └────── spawns `claude --settings <settings_path>` with cwd = boot dir
```

### `cairn boot --json`

Without `--json`, stdout is the bare path and nothing else — that seam has not
moved. With it, stdout is one JSON object and nothing else, so
`$(cairn boot eng --json)` is parseable and diagnostics still go to stderr:

```json
{
  "boot_dir": "/Users/.../boot/eng/20260826T014133Z-9f2a1c",
  "provider": "claude",
  "profile_root": "/Users/chrispian/.config/agents",
  "scope": "/Users/chrispian/dev/projects/cairn",
  "settings_path": "/Users/.../boot/eng/20260826T014133Z-9f2a1c/.claude/settings.json",
  "cwd_preference": "boot_dir",
  "project_dir_arg": ["--add-dir", "{{.ProjectDir}}"],
  "home_resource_paths": null,
  "saved_binding_path": null,
  "saved_dropped_sets": null
}
```

It exists because the alternative was scraping. An earlier launcher pulled the
scope out of the rendered `AGENTS.md` with `sed`, which made a launcher's
access grant depend on a marker's position in a document written to be
re-authored. That worked, and it worked by luck: one template edit moving the
line would have returned an empty scope with no error, which is a launcher
granting nothing and saying so nowhere.

Flat, `snake_case`, and **every key is emitted on every
boot** — the key set is the contract, and a key that came and went would make a
consumer handle two shapes for one meaning.

**A value Cairn does not have is `null`, never `""` and never `[]`.** An empty
string is the shape most likely to be interpolated straight into argv: `claude
$FLAG "$VALUE"` passes an empty argument and the launch is wrong with nothing
reporting it, where `null` forces the consumer to decide. Keys whose values can
be null each say something different:

- `scope` — the binding declared none and no `--scope` was given.
- `settings_path` — the render produced no file at the harness's settings path.
  That is a real case: a profile declaring no `spec.settings` with no directory
  to grant produces none. The path is read off what the render actually
  produced, so it never names a file that is not there.
- `project_dir_arg` — this harness needs no flag to grant a directory. It is
  **not** how "there is no scope" is spelled; `scope` says that.
- `env_amendments` — this harness declares no launch environment amendments.
- `home_resource_paths` — Cairn knows of no provider-home resources the launcher
  must deliberately provide when the provider home is pointed at the boot
  directory.
- `saved_binding_path` — no `--save-as` was given.
- `saved_dropped_sets` — no `--set` was dropped from a save, which is one
  meaning covering both "no `--save-as`" and "a save that dropped nothing".
  `saved_binding_path` is what separates those two states.

`profile_root` is the exception: it is **never null**, which is that same rule
applied rather than a break from it. `null` is for a value Cairn does not have,
and a boot that resolved no bundle read no profile and wrote no directory —
there is no document for the null to appear in.

**`scope` and `settings_path` are not equivalent, and only one implication
holds.** The scope is itself one of the granted directories, and one directory
to grant is enough on its own to render the settings document — so a non-null
`scope` guarantees a non-null `settings_path`, which is the invariant a
launcher rests on when it drops `--add-dir`. The reverse is false: a profile
that grants a directory it is not scoped to renders the document with no scope
at all. Read the key; do not infer it.

`cwd_preference` is `"boot_dir"` or `"project_dir"` — the preference, not a
resolved directory, because Cairn launches nothing and choosing between the two
on a launcher's behalf would be Cairn deciding the invocation.

`project_dir_arg` is the provider's own flag, **already split into argv tokens**
and with the placeholder **left standing**. Both halves are deliberate:

- *Split here.* The spec spells it as one string, `"--add-dir {{.ProjectDir}}"`,
  and a consumer handed that must substitute and split itself. Substitute first
  and a scope named `.../scope with space` becomes two arguments instead of one
  — silently, on the one input nobody tests. Nothing in `go-providers` states
  the safe order, and both of that library's own boot-dir examples replace on
  the whole pattern, so a consumer reading this document alongside them lands
  on the unsafe recipe. Split into tokens, no element can contain whitespace
  and the shape is safe by construction.
- *Substituted there.* `go-providers` documents substitution as the app's job
  at spawn time, and doing it here would tie the key to `scope`: a boot with no
  scope would have to report `null`, which already means "this harness needs no
  such flag". Replace `{{.ProjectDir}}` in each token with `scope`.

`profile_root` is the bundle the boot was composed out of, and what
`$CAIRN_PROFILE_ROOT` expanded to in every manifest value that names somewhere
to read from. **The boot directory does not record it, and the bundle resolves
without it** — with no flag and no variable, Cairn falls to
`$XDG_CONFIG_HOME/agents` and then to `~/.config/agents`. So an agent launched
with `--profile` that runs `cairn` again from inside its own boot directory
reads a *different* bundle: silently, and correctly by every rule as written,
which is what makes it hard to see. It is the same class of failure as the
`sed` scrape — something inferred that should have been told.

**Cairn does not export it, and that is the launcher's half.** Cairn writes a
directory and describes it; the process that has a child to put a variable into
is the one that spawns the harness, exactly as with `--settings`. What was
missing was never the export — it was the value.

`saved_binding_path` and `saved_dropped_sets` describe a `--save-as`, which is
the one thing `cairn boot` does that leaves nothing in the boot directory to
read. Both used to be announced on stderr and nowhere else, so a launcher that
composed and saved in one call had to parse a diagnostic to learn what it had
just created.

`saved_dropped_sets` names slots and never carries their values. A launcher
that passed `--set` already holds what it typed; what it cannot otherwise know
is which of those stopped at this run. **A refused save has no key at all**, and
that is a decision rather than an omission: every refusal `--save-as` can raise
is raised *before* the boot, so it exits non-zero, plants no directory and
prints no document. `$(cairn boot x --json)` either parses or the command
failed, and there is one thing to check.

There is no `version` field, and the rule that replaces it is: **new keys are
free; renaming or removing one is breaking and must update every consumer in
the same change.** That does not rest on how many consumers there are to
break, and it holds at none, because the cost of a rename lands on whoever
adopts the contract next and the two directions do not cost the same. Keeping
a key that has already been published costs a line in a struct. Renaming one
hands a consumer still reading the old name a `null` where a path used to be —
not a parse error, but a session opened without `--settings`: no access grant,
no trusted tier, and nothing reporting either. A version number would not have
stopped a rename; the sentence might.

Minimal shell, at a prompt. It needs `jq` — a JSON parser for a document with
named keys, in place of a regular expression over prose:

```bash
BOOT="$(cairn boot eng --json)"   # stdout is the object, stderr still yours
BOOT_DIR="$(jq -r '.boot_dir // empty' <<<"$BOOT")"
SETTINGS="$(jq -r '.settings_path // empty' <<<"$BOOT")"
[ -n "$BOOT_DIR" ] || { echo "cairn reported no boot directory" >&2; exit 70; }

# The export is this script's, not Cairn's. Without it an agent that runs
# `cairn` from inside the boot directory falls back to ~/.config/agents and
# reads a bundle nobody chose.
export CAIRN_PROFILE_ROOT="$(jq -r '.profile_root' <<<"$BOOT")"

set --
[ -n "$SETTINGS" ] && set -- "$@" --settings "$SETTINGS"
cd "$BOOT_DIR" && exec claude "$@"
```

**`// empty` on every read of a key that can be null, and it is not
decoration.** `jq -r` prints the four characters `null` for a null value, so a
read without it hands back `null` as though it were a path — `--settings null`,
which is exactly the argv the null rule above exists to prevent, arriving
through the one reader that turns the null back into a string. Building the
arguments with `set --` rather than interpolating a string is the other half: a
settings path with a space in it stays one argument. `profile_root` is read
without it because it is the one key that is never null, and a `// empty` there
would claim otherwise.

Minimal Go, engine-side:

```go
// Materialize a boot directory. Cairn writes files and describes them; it
// starts nothing.
cmd := exec.CommandContext(ctx, cairnBin, "boot", binding, "--scope", scope, "--json")
cmd.Stderr = os.Stderr // slot failures and diagnostics surface to the operator
out, err := cmd.Output()
if err != nil {
    return fmt.Errorf("cairn boot %s: %w", binding, err)
}
var boot struct {
    BootDir       string   `json:"boot_dir"`
    Scope         *string  `json:"scope"`
    SettingsPath  *string  `json:"settings_path"`
    ProjectDirArg []string `json:"project_dir_arg"`
}
if err := json.Unmarshal(out, &boot); err != nil {
    return fmt.Errorf("cairn boot %s: %w", binding, err)
}

// Tachyon spawns it. Cairn is done.
args := []string{}
if boot.SettingsPath != nil {
    args = append(args, "--settings", *boot.SettingsPath)
}
// Only for a provider that grants directories on the command line. Claude Code
// does not — the grant is in the settings file above — so this is nil here.
// Substituting per token is what keeps a scope with a space in it one argument.
if boot.Scope != nil {
    for _, tok := range boot.ProjectDirArg {
        args = append(args, strings.ReplaceAll(tok, "{{.ProjectDir}}", *boot.Scope))
    }
}
launch := exec.Command("claude", args...)
launch.Dir = boot.BootDir
```

### `cairn show --json`

The same seam, one command over. Without `--json`, stdout is the document laid
out for reading; with it, stdout is one JSON object and nothing else, and
diagnostics still go to stderr.

It exists for a launch palette. A GUI showing "what this binding already
carries" beside an additive skill picker should source that list from `show`
rather than reimplementing the cascade — that is what `show` is for. But `show`
without this emits prose, so a consumer would be scraping a human-facing
document for structured data: **the same defect as the `sed` scrape above, in
the other command.**

```json
{
  "profile": "engineer",
  "chain": ["base", "engineer"],
  "name": "Engineer",
  "description": "Writes code.",
  "provider": "claude",
  "model": "opus",
  "abstract": false,
  "scope": "/Users/chrispian/dev/projects/cairn",
  "profile_root": "/Users/chrispian/.config/agents",
  "spec": {
    "skills": {
      "value": ["code-review", "writing", "qhealth"],
      "contributors": ["engineer", "docs-only", "binding \"eng\""]
    },
    "settings": {
      "value": {"env": {"CAIRN": "1"}},
      "contributors": ["base"]
    }
  }
}
```

**The contract is `cairn boot --json`'s, in every clause** — `snake_case`, every
key emitted on every call, a value Cairn does not have spelled `null`, and no
`version` field because new keys are free while renaming one is breaking. A
consumer reading both documents in one session should not have to learn two
sets of rules. Where a key means the same thing in both it is spelled the same
way and carries the same value: `scope` is absolute and symlink-resolved in
both, and `null` in both when there is none. A launcher that showed one scope
and booted into another would be worse than one that showed nothing.

**`spec` nests where the boot report is flat**, and the reason the boot report
is flat still holds. It is flat because every value in it is a scalar or a list
of them, so there is nothing for nesting to group — not because flat is the
house style. Here the payload is a map
from manifest key to an arbitrary JSON value, which has nowhere to go but under
a key of its own, and `jq -r '.spec.settings.value'` is the same one-key read.

The pairing inside each entry is structural on purpose. The value and the
names beside it are **one fact** — that pairing is the whole reason `show`
exists — so they are one object rather than two parallel maps a consumer could
iterate out of step.

**`contributors` is per key, and it is not per member.** It says `spec.skills`
came from the profile, a part and the binding; it does **not** say which of
them supplied the skill in front of you. That is a limit of the cascade rather
than of this command — the second answer cannot be assembled without a second
copy of the merge table — and a shape implying otherwise would be worse than
the prose, because a launcher would build a UI on it and the UI would be
confidently wrong.

Not every member is a profile id. `--skill`, `--prompt` and `--set` appear as
they are spelled, and a binding replaying a saved composition appears as
`binding "eng"`. **Do not resolve a member as a profile.** What these are is
what you would have to change to change the value.

For the palette in particular, that answers the question the union raises:
skills reach a boot directory from three contributors — the resolved profile,
any part, and the binding's own skill list — and they compose as a collection
keyed by id. `spec.skills.value` is that union already done, which is the whole
reason to route the list through `show`.

**Two nulls that are not the same null.** An empty manifest is `{}`, not
`null`: `null` is for a value Cairn does not have, and a profile declaring
nothing has an empty manifest rather than no manifest. But a `value` of `null`
inside `spec` is a **declaration** — it is how a profile clears an ancestor's
key — so `"settings": {"value": null, ...}` means *this profile deliberately
has no settings*, which is a different fact from `settings` being absent from
`spec`, which means nothing in the chain ever mentioned it. A consumer that
treats the two as one will render an inherited settings document for a profile
that went to the trouble of saying it has none.

**`--json` does not move what goes to stderr.** A scope that did not resolve
and a `--with` that contributed nothing are facts about the resolution rather
than lines of the document; they are reported the same way in both forms, and a
consumer reading only stdout gets one object either way. A scope that did not
resolve is `null` here and named there.

```bash
SHOW="$(cairn show eng --json)"          # stdout is the object, stderr still yours
jq -r '.spec.skills.value[]?' <<<"$SHOW" # what this binding already carries
jq -r '.scope // empty'       <<<"$SHOW" # empty rather than the string "null"
```

### What Cairn is still missing for this

Which slots failed. A launcher building a UI wants to show a warning rather
than making the operator read stderr, and the report carries no `slots` array
yet. Adding one is additive and therefore free; it is the next key to add when
a launcher wants it.

### On `plant.Planter`

Cairn implements `go-agent-wrapper/plant.Planter` so it speaks the same
planting contract Nanite does. **Nothing calls it**, including Cairn itself,
which uses its own `PlantFiles` because `plant.Spec` carries no file modes and
a skill's executable bit is load-bearing.

For a launcher that shells out — Tachyon, a shell script, you at a prompt — it
is irrelevant: it is a Go interface, and the process boundary is the seam. It
only matters if something imports Cairn as a library and wants to plant
*through* the contract it already speaks. It costs ~40 lines and nothing at
runtime, so it stays until something either uses it or clearly never will.
