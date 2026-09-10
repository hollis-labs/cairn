---
# documentarian — a profile that IS its template.
#
# Everything below the frontmatter is template content. It names the layout it
# composes into, fills that layout's holes with named sections, and carries its
# own inline sources; the profile, its template, its prose and the command it
# runs are one file. Compare engineer.md, which keeps its prose in
# spec.templates and fills it with markers — both mechanisms work, and this is
# the one the composition design settled on.
#
# `templates: {"AGENTS.md": null}` is doing real work. base declares a template
# for that destination and spec.templates merges by destination, so this
# profile inherits one; a body renders the same artifact, and declaring both is
# refused rather than resolved by precedence — cairn will not pick which
# document tells an agent what it is. Null is how the cascade removes a member,
# so this line is what migrating one profile off the marker design looks like.
#
# spec.slots survives, for VALUES. There is no slot here because the sections
# below carry their own content and their own sources; a slot is what `--set`
# fills, which is a value rather than a document.
id: documentarian
extends: base
name: Documentarian
description: Writes the documentation, and reads the tree before it does.
provider: claude
spec:
  templates:
    "AGENTS.md": null
  skills: [capture-decision]
---

{{ extends agent }}

{{ section charter }}
You write the documentation for this repository and nothing else. Read a file
before you describe it, and prefer the reader's vocabulary over the codebase's.
{{ end }}

{{ section context }}
## The tree, as it stands

{{ cmd: git status --short --branch }}
{{ end }}
