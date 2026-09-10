# Cairn

Cairn reads an agent bundle, resolves profiles and composition overlays, renders
provider-native boot directories, and renders or checks installed layers. It
does not launch, monitor, track, control or grant authority to agents.

## Start Here

- `doc.go` and `examples/README.md` define the public shape.
- `catalog/` reads bundles, profiles and layouts.
- `profile/resolve.go` and `profile/merge.go` own composition order and merge
  rules.
- `bootdir/layouts/*.yaml` is one document per harness: every path a harness
  reads, in both layers, plus which renderers that tree carries. Read the
  document before the code — a harness's paths are not in Go.
- `bootdir/` renders boot directories.
- `install/` renders/checks installed layers and preserves owned keys.
- `cmd/cairn/bootjson.go` is the launcher-facing JSON contract.
- `cmd/cairn/provider.go` selects the materialization provider.

## Commands

Use the smallest command that exercises the changed boundary:

```bash
go test ./profile ./catalog ./bootdir ./install ./cmd/cairn
go test ./...
make check
testdata/goldens/verify.sh
```

Run broader checks when provider layout, install ownership, persistence or the
public CLI contract changes. `make check` is the landing gate, and is what CI
runs. Anything that can change what a profile renders also needs `verify.sh`,
which no Go test reaches.

## Boundaries

Provider names and provider-keyed settings already exist. That is not the same
as implemented provider materialization: unsupported providers must be refused
or implemented through the existing layout/renderer/install seams.

A harness's paths belong in its layout document, never in Go. A second harness
is a second tree — a document, and a renderer registration if its content
genuinely differs in shape rather than in placement. Do not reintroduce a
constant for a file a harness reads, and do not add a `switch` on the provider
in a render path: the tree says which renderer runs. What stays in Go is what a
document cannot express — install's merge-by-key, which preserves the
operator's own settings while rewriting the keys cairn owns, and the adapter
table that asks go-providers what a harness's own spec currently says.

Never test install behavior against a live home. Use fixture roots and preserve
unrelated config, auth, plugin and skill state.

`testdata/goldens/trees/` holds rendered `.claude/` trees. They are Cairn's
expected output, not this repo's own agent configuration — a sweep that clears
project-level harness directories and reaches them deletes the render gate.
`verify.sh` re-renders against a live `~/dev/projects/agent-setup` checkout, so
it is machine-local, is not a CI check, and says whether a diff is upstream's or
yours. Re-baseline only with `capture.sh --force`, reading the diff first.
