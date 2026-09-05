# Cairn

Cairn reads an agent bundle, resolves profiles and composition overlays, renders
provider-native boot directories, and renders or checks installed layers. It
does not launch, monitor, track, control or grant authority to agents.

## Start Here

- `doc.go` and `examples/README.md` define the public shape.
- `catalog/` reads bundles, profiles and bindings.
- `profile/resolve.go` and `profile/merge.go` own composition order and merge
  rules.
- `bootdir/` renders boot directories.
- `install/` renders/checks installed layers and preserves owned keys.
- `cmd/cairn/bootjson.go` is the launcher-facing JSON contract.
- `cmd/cairn/provider.go` selects the materialization provider.

## Commands

Use the smallest command that exercises the changed boundary:

```bash
go test ./profile ./catalog ./bootdir ./install ./cmd/cairn
go test ./...
```

Run broader checks when provider layout, install ownership, persistence or the
public CLI contract changes.

## Boundaries

Provider names and provider-keyed settings already exist. That is not the same
as implemented provider materialization: unsupported providers must be refused
or implemented through the existing layout/renderer/install seams.

Never test install behavior against a live home. Use fixture roots and preserve
unrelated config, auth, plugin and skill state.
