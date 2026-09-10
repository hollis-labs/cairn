#!/usr/bin/env bash
#
# capture.sh — render every Cairn profile into a byte-reproducible tree.
#
# The tree under testdata/goldens/trees is a golden fixture: it is what the
# nine profiles render to, byte for byte, at the commit that captured it. A
# change that alters rendering shows up as a diff in that tree, which is the
# point — verify.sh re-captures into a scratch root and diffs.
#
# Everything the render can reach is staged into one throwaway fixture root and
# every non-deterministic input is pinned:
#
#   $FIX/home    an EMPTY fixture HOME. The bundle names $CAIRN_PROFILE_ROOT
#                for its templates, skills and prompts, so nothing is read out
#                of home — and an empty one is what proves it.
#   $FIX/scope   a git repo built from literals, so the git slots print the
#                same two blocks on every machine
#   $FIX/bin     the cairn built from this working tree, first on PATH, because
#                the conductor's `fleet` slot shells out to `cairn list`
#   $FIX/boot    the boot root the eight boots are planted under, moved into
#                $OUT once they are rendered. It cannot be $OUT/boot: the
#                default --out is inside this checkout, and `cairn boot`
#                refuses a boot root inside a repository that is not the scope
#                (bootdir.CheckRoot) — correctly, since a boot directory is the
#                agent's working directory. Staging it here makes the capture
#                independent of where its output lands, which verify.sh always
#                was and capture.sh only accidentally was
#
# and the session segment is pinned to "golden", which removes the only
# non-determinism inside cairn itself (bootdir.NewSession's timestamp + random
# suffix, used only when --session is empty).
#
# usage:
#   capture.sh [--out <dir>] [--force]
#
#   --out <dir>   where the tree goes; defaults to <repo>/testdata/goldens/trees.
#                 The capture record is written BESIDE it, as
#                 <dir>/../agent-setup.commit — see "the capture record" below
#                 for why it cannot live inside the tree. Give --out a
#                 directory of its own rather than a shared one.
#   --force       clear a non-empty --out first
#
# environment:
#   AGENT_SETUP   the agent-setup checkout; defaults to ~/dev/projects/agent-setup
#   CAIRN         the cairn binary; built from this repo when unset
#   PROVIDER      the harness to render; defaults to claude. agent-setup names
#                 no provider, so cairn requires one — see PROVIDER below.

set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
REPO=$(cd "$SCRIPT_DIR/../.." && pwd -P)

# The real HOME, read before anything shadows it. AGENT_SETUP defaults off it.
REAL_HOME=$HOME
AGENT_SETUP=${AGENT_SETUP:-$REAL_HOME/dev/projects/agent-setup}

OUT=$SCRIPT_DIR/trees
FORCE=0
while [ $# -gt 0 ]; do
	case $1 in
	--out)
		[ $# -ge 2 ] || { echo "capture.sh: --out needs a directory" >&2; exit 2; }
		OUT=$2
		shift 2
		;;
	--out=*)
		OUT=${1#--out=}
		shift
		;;
	--force)
		FORCE=1
		shift
		;;
	-h | --help)
		# Everything from line 2 to the end of the header block, found rather
		# than hardcoded: a line range silently truncates the moment the header
		# grows, and it did exactly that once.
		awk 'NR>1 && /^#/ { sub(/^# ?/, ""); print; next } NR>1 { exit }' \
			"${BASH_SOURCE[0]}"
		exit 0
		;;
	*)
		echo "capture.sh: unknown argument $1" >&2
		exit 2
		;;
	esac
done

for tool in git python3; do
	command -v "$tool" >/dev/null 2>&1 || { echo "capture.sh: $tool is not on PATH" >&2; exit 1; }
done
[ -d "$AGENT_SETUP/profiles" ] || {
	echo "capture.sh: no profiles under $AGENT_SETUP — set AGENT_SETUP" >&2
	exit 1
}
# prompts/ is checked where templates/ and skills/ are not, because it is the
# one an AGENT_SETUP checkout can legitimately predate: the profiles began
# declaring spec.prompts after the directory existed here. Without this the
# render fails deep in prompt resolution rather than saying which directory is
# missing and why.
[ -d "$AGENT_SETUP/prompts" ] || {
	echo "capture.sh: no prompts/ under $AGENT_SETUP — the profiles declare" >&2
	echo "  spec.prompts, so this checkout predates them. Update it." >&2
	exit 1
}

# --- what upstream was, at capture time ------------------------------------
#
# These trees are a function of two repositories and only one of them is this
# one. Recording which agent-setup commit they came from is what lets verify.sh
# tell an operator whose change a diff is, rather than leaving them to work it
# out from the diff itself.
#
# A checkout that is not a git repository is not an error: AGENT_SETUP can
# legitimately be an rsynced copy, and such a capture is still valid. It just
# cannot be attributed, and the record says so rather than inventing a sha.
#
# Which is why the toplevel is checked rather than HEAD read directly. `git -C
# <dir> rev-parse HEAD` walks UP to an enclosing repository, so an rsynced copy
# living anywhere inside another checkout would record THAT repository's HEAD —
# a sha with nothing to do with the profiles, written down as though it were
# one. That is the sha this comment promises not to invent.
if AGENT_SETUP_TOP=$(git -C "$AGENT_SETUP" rev-parse --show-toplevel 2>/dev/null) &&
	[ "$(cd "$AGENT_SETUP_TOP" 2>/dev/null && pwd -P)" = "$(cd "$AGENT_SETUP" && pwd -P)" ]; then
	AGENT_SETUP_COMMIT=$(git -C "$AGENT_SETUP" rev-parse HEAD)
	if [ -n "$(git -C "$AGENT_SETUP" status --porcelain 2>/dev/null)" ]; then
		AGENT_SETUP_COMMIT="$AGENT_SETUP_COMMIT-dirty"
	fi
else
	AGENT_SETUP_COMMIT=unknown
fi

# --- the output root -------------------------------------------------------
#
# `cairn boot` refuses to plant over an existing directory (bootdir.ErrExists),
# so the out dir has to be fresh. Refuse to clear one the operator did not
# name for clearing.
if [ -e "$OUT" ]; then
	if [ -n "$(ls -A "$OUT" 2>/dev/null)" ]; then
		if [ "$FORCE" -eq 1 ]; then
			rm -rf "$OUT"
		else
			echo "capture.sh: $OUT exists and is not empty; pass --force to clear it" >&2
			exit 1
		fi
	fi
fi
mkdir -p "$OUT"
# Canonicalized so that the token substitution below sees the same spelling the
# render wrote. On macOS /tmp and $TMPDIR are symlinks, and a path that reaches
# a golden through one form while the token is the other form is a false diff.
OUT=$(cd "$OUT" && pwd -P)

# --- the fixture root ------------------------------------------------------
FIX=$(cd "$(mktemp -d)" && pwd -P)
cleanup() { rm -rf "$FIX"; }
trap cleanup EXIT

# 1. the fixture home, and it is EMPTY on purpose.
#
#    It used to hold .config/agents/{templates,skills,prompts}, rsynced from
#    $AGENT_SETUP with `make install-system`'s own flags, because the profiles
#    named ~/.config/agents by absolute path. They name $CAIRN_PROFILE_ROOT
#    now, install-system is gone, and staging a copy here would be worse than
#    unused: a profile that went back to naming a path under home would find it
#    satisfied, and the golden would certify a render no machine can reproduce.
#    Empty, the harness proves what the change claims — the bundle is
#    self-contained, and home supplies nothing.
#
#    The directory itself still has to exist, because HOME below points at it.
mkdir -p "$FIX/home"

# 2. the fixture scope: a git repo whose every input is a literal.
#
#    GIT_CONFIG_GLOBAL/SYSTEM are nulled on every invocation so the operator's
#    own gitconfig — hooks path, commit.gpgsign, commit template, core.abbrev,
#    init.defaultBranch — cannot reach the fixture. The author and committer
#    identity and dates are literals so the abbreviated hash `git log --oneline`
#    prints is stable, and core.abbrev is pinned rather than left to git's
#    object-count heuristic. There is no remote, so `git status --short
#    --branch` prints the branch line and nothing else.
mkdir -p "$FIX/scope"
fixgit() {
	env GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_NOSYSTEM=1 \
		GIT_AUTHOR_NAME='Golden Fixture' GIT_AUTHOR_EMAIL='golden@fixture.invalid' \
		GIT_AUTHOR_DATE='2020-01-01T00:00:00+0000' \
		GIT_COMMITTER_NAME='Golden Fixture' GIT_COMMITTER_EMAIL='golden@fixture.invalid' \
		GIT_COMMITTER_DATE='2020-01-01T00:00:00+0000' \
		TZ=UTC LC_ALL=C \
		git -C "$FIX/scope" "$@"
}
fixgit init -q -b main
fixgit config user.name 'Golden Fixture'
fixgit config user.email 'golden@fixture.invalid'
fixgit config core.abbrev 7
fixgit config commit.gpgsign false
printf 'The scope a golden capture renders against.\n' >"$FIX/scope/README.md"
fixgit add README.md
fixgit commit -q -m 'Seed the golden fixture scope'

# There is no third. A seeded store used to be one, built from the profile
# files by agent-setup's bin/seed.py; the catalog is the store now, so the
# profiles are read out of $AGENT_SETUP where they already live.

# --- the binary ------------------------------------------------------------
#
# Built from this working tree unless CAIRN names one. Every task from here on
# runs verify.sh to prove its own change did not alter rendering, so the
# harness has to exercise the source in front of it — not a stale cairn that
# happens to be on $PATH.
#
# It always lands at $FIX/bin/cairn, a CAIRN given by the operator copied there
# rather than run where it lies, because the renders below put that directory
# first on PATH. The conductor's `fleet` slot shells out to `cairn list`, and a
# slot that found some other cairn would be capturing a binary this run never
# built.
#
# Removing that PATH entry today happens to fail loudly, and that is a
# coincidence rather than a property. It fails because the cairn installed on
# this machine predates `cairn list` and answers with a usage banner and a
# non-zero exit. Install this cairn and the same removal goes quiet: the slot
# would resolve, render plausible output, and the capture would baseline a
# listing produced by a binary nobody in this run built. The pin is what makes
# it right; the noise is not.
mkdir -p "$FIX/bin"
CAIRN_BIN=$FIX/bin/cairn
if [ -n "${CAIRN:-}" ]; then
	cp "$CAIRN" "$CAIRN_BIN"
else
	(cd "$REPO" && go build -o "$CAIRN_BIN" ./cmd/cairn)
fi

# --- the environment every render runs under -------------------------------
#
# Built from nothing rather than inherited, so the operator's environment is
# not an input. PATH survives because the git slots need it, with $FIX/bin
# ahead of it for the reason just above.
#
# CAIRN_PROFILE_ROOT is exported as well as passed as --profile, and the two
# are not redundant. The flag tells cairn which bundle to read; the variable is
# the only way the same bundle reaches a slot's shell, because a flag does not.
# The conductor's `fleet` slot runs `cairn list`, which resolves its bundle the
# way every other command does — the flag it was not given, then this variable.
# It is the same seam CAIRN_DB filled before the store was retired.
render_env=(env -i
	PATH="$FIX/bin:$PATH"
	HOME="$FIX/home"
	CAIRN_PROFILE_ROOT="$AGENT_SETUP"
	TZ=UTC
	LC_ALL=C)

# --- the nine renders ------------------------------------------------------
mkdir -p "$OUT/stdout" "$OUT/stderr"

# The staged boot root — see the header. Moved to $OUT/boot below, before the
# tokenizer runs, so what it rewrites is the tree in its final place. `mv` and
# not `cp`, because a mktemp root on another device makes this a copy, and mv's
# cross-device fallback duplicates the mode where cp without -p would not — the
# same executable bit the tokenizer goes out of its way to preserve.
BOOT=$FIX/boot

# Eight concrete profiles, in sorted order.
BOOTS=(architect conductor director engineer orchestrator planner reviewer writer)

# The harness this captures. It is passed rather than left to the profile
# because agent-setup declares no provider at all any more — a runtime is a
# launch's to choose, not an agent's to carry — and cairn refuses to render a
# profile that names none rather than writing one harness's files into
# another's directory. So `--provider` is required, and a capture that omitted
# it stopped being able to run at commit afc6181.
#
# It is claude because that is the tree the goldens under trees/ hold. Naming
# it here changes nothing about what is rendered: base declared `claude`
# before it declared nothing, so the same document comes out either way, which
# is what makes this a harness fix rather than a re-baseline.
PROVIDER=${PROVIDER:-claude}

fail() {
	local id=$1
	echo "capture.sh: rendering $id failed" >&2
	echo "--- stderr ---" >&2
	cat "$OUT/stderr/$id.txt" >&2 || true
	exit 1
}

for id in "${BOOTS[@]}"; do
	"${render_env[@]}" "$CAIRN_BIN" boot "$id" \
		--profile "$AGENT_SETUP" \
		--scope "$FIX/scope" \
		--session golden \
		--provider "$PROVIDER" \
		--boot-root "$BOOT" \
		>"$OUT/stdout/$id.txt" 2>"$OUT/stderr/$id.txt" || fail "$id"
done

mv "$BOOT" "$OUT/boot"

# base is abstract: true, and `cairn boot` refuses an abstract profile by
# design. The installed layer is the only way to render it, and rendering it is
# the point — it is where the templates, the settings and the skill set live.
#
# Unlike a boot, an install wants its root to exist already: install.NewRoot
# validates the shape and Root.Check requires the directory.
mkdir -p "$OUT/install/base"
"${render_env[@]}" "$CAIRN_BIN" install base \
	--profile "$AGENT_SETUP" \
	--provider "$PROVIDER" \
	--root "$OUT/install/base" \
	>"$OUT/stdout/base.txt" 2>"$OUT/stderr/base.txt" || fail base

# --- tokenize --------------------------------------------------------------
#
# All three roots are per-run paths: $FIX is a mktemp directory and $OUT
# differs between the capture that wrote the golden and every verification run
# after it. Left raw, any one of them guarantees a false diff.
#
# $BOOT is inside $FIX but stands for $OUT/boot, because that is where the tree
# it rendered now lives. It has to be substituted BEFORE $FIX — which it is,
# being the longer needle — or the paths a boot directory records about itself
# would come out as @FIXTURE@/boot.
#
# Rewritten in place through the same inode so the mode survives — a planted
# skill's executable bit is load-bearing. The longer path is replaced first, so
# that one being a prefix of the other cannot leave a half-substituted path.
FIX="$FIX" OUT="$OUT" BOOT="$BOOT" python3 - "$OUT" <<'PY'
import os, sys

root = sys.argv[1]
subs = [(os.environ["BOOT"].encode(), b"@OUT@/boot"),
        (os.environ["FIX"].encode(), b"@FIXTURE@"),
        (os.environ["OUT"].encode(), b"@OUT@")]
subs.sort(key=lambda p: len(p[0]), reverse=True)

for dirpath, dirnames, filenames in os.walk(root):
    dirnames.sort()
    for name in sorted(filenames):
        path = os.path.join(dirpath, name)
        if os.path.islink(path) or not os.path.isfile(path):
            continue
        with open(path, "rb") as fh:
            data = fh.read()
        out = data
        for needle, token in subs:
            out = out.replace(needle, token)
        if out == data:
            continue
        # r+b, not a rename: rewriting through the existing inode keeps the
        # file's mode.
        with open(path, "r+b") as fh:
            fh.write(out)
            fh.truncate()
PY

# --- sort stderr -----------------------------------------------------------
#
# Slot failures are reported in manifest declaration order, which the keyed
# merge deliberately makes meaningless — it is not a contract, so pinning it
# would freeze a detail the design says is free. Sorted after tokenization, so
# the order does not depend on the fixture's per-run path. stdout is left
# alone: it is the planted path, and its order is the argument order above.
for f in "$OUT"/stderr/*.txt; do
	# Through the same inode, for the reason the tokenizer does it: `sort -o`
	# renames a fresh 0600 temp file over the target and loses the mode.
	LC_ALL=C sort "$f" >"$f.sorted"
	cat "$f.sorted" >"$f"
	rm -f "$f.sorted"
done

# --- refuse a degraded capture ---------------------------------------------
#
# A golden that records a slot which failed to resolve, a declared slot that
# filled nothing, or a value cairn cannot fill, is a golden of a broken render —
# and the damage is that it reads exactly like a golden of a working one. A
# section that is missing because its slot failed is indistinguishable, in a
# captured tree, from a section the profile never declared, and a line whose
# value marker was misspelled is indistinguishable from one whose value was
# legitimately empty.
#
# The conductor's `fleet` slot is the case that motivated this. It is a cmd slot
# that shells out, `cairn boot --profile <dir>` does not reach a slot's shell,
# and the two exports above — CAIRN_PROFILE_ROOT and $FIX/bin on PATH — are what
# point it at this run's bundle and this run's binary. Lose either and the slot
# reads somewhere else or runs something else. It used to be worse: the slot
# queried sqlite, and immutable=1 on an absent path opens an EMPTY database
# rather than erroring, so the section rendered nothing and the boot still
# exited 0. `cairn list` against a bundle that is not there is an error, so the
# failure is loud now — but loud on stderr, which a capture would still
# baseline, and this is what refuses it.
#
# All three patterns are cmd/cairn/main.go's own: reportSlotFailures and
# reportUnfilledMarkers. Anything else on stderr is printed and does not fail —
# the installed layer's "renders no section for" line is a fact about which
# kinds install resolves, not a fault, and neither is the line naming the values
# cairn fills, which only ever follows one of the patterns above.
degraded=$(grep -lE 'slot "[^"]*" did not resolve|slot "[^"]*" filled nothing|value "[^"]*" is not one cairn fills' \
	"$OUT"/stderr/*.txt 2>/dev/null || true)
if [ -n "$degraded" ]; then
	echo "capture.sh: refusing to capture a degraded render" >&2
	for f in $degraded; do
		echo "  $(basename "$f" .txt):" >&2
		grep -E 'did not resolve|filled nothing|is not one cairn fills|the values cairn fills are' \
			"$f" | sed 's/^/    /' >&2
	done
	echo "" >&2
	echo "A golden recording a failed or unfilled slot looks like a golden of a" >&2
	echo "profile that never declared it. Fix the render; do not capture this." >&2
	exit 1
fi

# Whatever else reached stderr is surfaced rather than silently baselined. It is
# still captured — it is part of the golden — but an operator re-capturing sees
# it rather than discovering it in a diff later.
for f in "$OUT"/stderr/*.txt; do
	[ -s "$f" ] || continue
	echo "capture.sh: $(basename "$f" .txt) wrote to stderr:" >&2
	sed 's/^/  /' "$f" >&2
done

# --- the capture record ----------------------------------------------------
#
# Written beside the tree rather than inside it, and the placement is
# load-bearing twice over. verify.sh diffs the tree, so a record inside it
# would itself be a diff every time upstream moved — reporting the drift as
# drift, which is the confusion this exists to end. And verify.sh re-runs this
# script into a scratch directory: a record written to a fixed path in the repo
# would be clobbered by the very run that needs to read it.
#
# Travelling with $OUT means the record always describes the tree beside it —
# and it means --out writes one file outside the directory it names, which is
# why the usage block above says to give --out a directory of its own.
printf '%s\n' \
	'# The agent-setup commit these golden trees were captured from.' \
	'#' \
	'# capture.sh writes this; verify.sh compares it against $AGENT_SETUP HEAD so' \
	'# that a failing gate says whose change the diff is. A "-dirty" suffix means' \
	'# the capture came from a tree with uncommitted edits, so the sha does not' \
	'# fully describe it. "unknown" means AGENT_SETUP is not a git checkout.' \
	'#' \
	'# See README.md, "Whose change is this diff?".' \
	"$AGENT_SETUP_COMMIT" \
	>"$(dirname "$OUT")/agent-setup.commit"

files=$(find "$OUT" -type f | wc -l | tr -d ' ')
bytes=$(find "$OUT" -type f -exec cat {} + | wc -c | tr -d ' ')
echo "captured ${#BOOTS[@]} boots + 1 installed layer into $OUT ($files files, $bytes bytes)"
