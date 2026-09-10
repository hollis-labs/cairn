---
# docs-only — a part.
#
# There is no "fragment" kind. A part is an ordinary profile that happens to be
# small, which is why this file looks like every other one and why
# `cairn show docs-only` and `cairn boot docs-only` both work on it alone.
#
# It sits in profiles/parts/ and that is where the convention ends: the
# directory is where the file lives, not part of what the profile is called.
# The id below is the whole name, so this is `--with docs-only` and never
# `--with parts/docs-only`, it shares one global namespace with engineer.md one
# directory up, and two files claiming one id are refused when the bundle is
# read rather than one of them quietly winning.
#
# It extends base for the same reason engineer does — a part needs a provider
# and a template to be bootable on its own — and that shared ancestor is
# exactly the case composition has to get right: folding base a second time
# behind engineer would put base's AGENTS.md template back in front of
# engineer's. A profile the resolution has already reached is folded once,
# where it first landed.
#
# What it contributes when composed is what it declares below and nothing else.
#
# Its prose is a SECTION and not loose text, and that is what makes it a
# fragment: a document holding nothing but section declarations declares
# content and no shape, so composing it never hands it the shape of the whole
# document. Loose prose here would — a part is closer than the profile being
# booted, and closest-wins would give it the frame.
id: docs-only
extends: base
name: Docs only
description: Narrows a session to user-facing documentation.
spec:
  # An empty slot: it renders nothing until `--set direction=<text>` supplies
  # the content at materialization. A direction worth reusing is written here
  # instead, and then it needs no flag at all.
  #
  # The section below is what a --set does NOT keep. A --set replaces the slot
  # of that name WHOLE, exactly as a part declaring that slot would, so the
  # heading goes with it — spec.slots composes by name and a member is never
  # merged field by field. The heading is here for the case where this file
  # carries the content.
  slots:
    - name: direction
      section: "## Direction"
      source:
        kind: inline
        inline:
          content: ""
  skills:
    - capture-decision
---

{{ section direction }}
## Direction

Write user-facing documentation only. Prefer the reader's vocabulary over the
codebase's, and leave the API reference to the generator that owns it.
{{ end }}
