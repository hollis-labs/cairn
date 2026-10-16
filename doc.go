// Package cairn is the module root. It holds no code.
//
// Cairn assembles files and writes them into a directory: it reads a profile
// out of a bundle directory, resolves it through an extends cascade, and
// materializes a boot directory a CLI coding agent can be launched from. It
// renders the installed layer from the same source.
//
// It does not launch, monitor, track, control, create, harness, or steer
// agents, and it has no opinion about how agents work or behave. File contents
// are a black box: Cairn provides the shape and validates the shape, and
// whoever owns the profiles owns the meaning.
//
// The command is [github.com/hollis-labs/cairn/cmd/cairn].
package cairn
