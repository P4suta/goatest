# 0006 — Every temporary directory has an owner

## Status

Accepted, 2026-09-04. Implemented by `internal/tempowner` (the owner pair and
the sweep), `internal/keptledger` (the record of what a keep left behind), the
run scratch and its lifecycle in `internal/assure` (`run_scratch.go`,
`keep_temp.go`), the `KeepTemp` and sweep passthroughs in
`internal/mutationbridge`, and the temporary-directory evidence of
`goatest cache status|gc` in `internal/app`.

## Context

A run writes far more outside the repository than inside it. go-mutants copies
the whole module into a snapshot, builds a probe tree beside it, and keeps a
scratch directory; goatest adds a build cache layer, a scratch per baseline
round, a tree per validated candidate, and a fuzz cache for an original control.
On this project that is measured in gigabytes per run, and three agents sharing
one 457 GB disk have twice filled it.

Every one of those directories was made with `os.MkdirTemp` directly under the
temporary root, side by side with everybody else's, and every one of them was
removed by a deferred call in the process that made it. That is the whole of the
problem. A SIGKILL, an out-of-memory kill, a closed terminal, a machine that
lost power — each ends the process somewhere between the copy and the removal,
and what is left is a full copy of somebody's module that nothing will ever
delete, wearing a name that says nothing about who made it or whether anybody is
still using it. `--keep-temp` made the same thing on purpose and documented that
nothing would ever remove it.

Two questions have to be answerable about a directory found in a temporary root:
*who made this*, and *is anybody still using it*. Nothing in the old layout
answered either.

## Decision

1. **One scratch directory per run, and everything below it.** A run makes
   `goatest-run-*` under the configured temporary root before it writes
   anything, and everything it would otherwise have made beside it goes below
   instead: `build/` for the build cache layer, `baseline-*` per round,
   `candidate-*` per validated candidate, `control-fuzz-*` for a fuzzing
   original control. One directory is one removal, one owner, and one path a
   developer looks in. The names below it are short because the parent already
   says which tool made them.

   The subdirectories are still released as the run finishes with each of them,
   because that is what keeps the peak footprint small; the removal of the run
   scratch at the end covers whatever is left, including what a killed step
   never got to.

2. **A run that cannot make one still runs.** Every consumer takes the parent
   *and* the prefix from the scratch, and a scratch that could not be made
   answers with the temporary root and the `goatest-` names the sweep knows. The
   same rule covers a validation outside any run (`goatest fix`), which has no
   run scratch at all. No path in this design can produce a directory nothing
   recognizes.

3. **The lock is the liveness signal, not the pid.** A claimed directory holds
   `owner.lock`, an exclusive advisory lock held open for the whole run. A lock
   that can be taken means the process that held it no longer exists, whatever
   it was called and whatever its pid has been reused for since. A pid wraps and
   is reused, so asking whether one is alive on a long-lived machine answers a
   question about some other process — and answering it wrongly means deleting
   the working directory of a running verification. The lock is the operating
   system's own answer and costs one open file.

4. **The marker is for people, and for one bit.** `owner.json` is a
   `goatest-temp-owner-v1` document naming the run, the process, the start time,
   the repository being verified, and `kept`. A person who finds four gigabytes
   in their temporary directory reads it to learn which project and which run
   put it there. The sweep reads exactly one field of it — `kept` — because the
   lock has already answered everything else. A marker that cannot be read at
   all is treated as one that does not say kept: a half-written marker must not
   make a dead directory immortal.

5. **An unowned directory is judged by age, and 24 hours is the number.** A
   directory carrying no marker is either one made before this convention or one
   caught in the moment between its own mkdir and its claim. Age is the only
   evidence available about it, and the two costs are asymmetric: waiting costs
   disk, and being wrong deletes the temporary directory of a run in progress.
   Twenty-four hours is far longer than any run and short enough that a
   developer who fills their disk on Monday has it back on Tuesday. The window
   is permanent rather than transitional, because the mkdir-then-claim gap is.

6. **Sweeping is what a run does before it writes, and what `cache gc` does on
   demand.** A run sweeps the temporary root first, so that a machine holding
   the leftovers of a killed run has the disk back before this one asks for
   hundreds of megabytes of it. `goatest cache gc` runs the same sweep, and
   `goatest cache status` runs the same classification without removing
   anything. The prefix list is exported from the run layer and used by all
   three: two lists that could disagree would be two conventions. go-mutants'
   own directories are not in it — they carry owner files of their own and its
   `Open` sweeps them, which goatest reports rather than duplicates.

   A sweep runs only against a directory somebody named. An empty temporary
   root collects nothing, and `cmd/goatest` is the one layer that may name the
   machine's own — the same rule the executable and the user cache directory
   already follow. Resolving it one layer lower is not a convenience: it made a
   maintenance command in a test, with no temporary directory and a clock a day
   ahead, collect the working directory of a run that was using it.

7. **A keep is recorded where it outlives the run.** `--keep-temp` marks the
   directory kept, so no sweep takes it, and writes it to
   `.goatest/kept-temp-v1.json`: path, run, moment, size. The trace already
   carried an `artifact` event for each kept path, but a successful untraced run
   writes no trace, and that is precisely the run that leaves gigabytes nobody
   can account for. The ledger lives in the repository's own `.goatest`
   directory rather than beside the directories it names, because it has to
   survive the removal of every one of them and because the commands that read
   it are already run from a repository.

8. **`[cache] ttl` bounds a keep, and nothing else does.** `goatest cache gc`
   removes a kept directory once it is older than the TTL and drops the entries
   of directories that are already gone. No byte budget applies: a keep is a
   request somebody made on purpose, and the only bound that respects the
   request is time. A developer who wants one back sooner removes it by hand,
   and the next `gc` drops its entry.

9. **None of this can fail a run.** A sweep that fails, a scratch that cannot be
   made, claimed or removed, and a ledger that cannot be written are progress
   notes — `temp-sweep`, `temp-unavailable`, `kept-temp-unrecorded` — and the
   run reaches the same verdict it would have reached. This is housekeeping done
   on the way to measuring mutants, and housekeeping that could fail a
   verification would be worse than no housekeeping at all. For the same reason
   none of it enters any cache identity: where a run put its directories and
   what its sweep found are facts about the machine, exactly as
   [0002](0002-trace-is-not-evidence.md) says of a trace.

## Consequences

- The lock is released before the directory is removed, everywhere. On Windows
  an open handle inside a directory is what makes the removal fail, so the order
  is not stylistic.
- Two runs on one machine never contend: each holds the lock of its own scratch,
  and a sweep that meets a live one counts it and moves on. A sweep that meets a
  directory it cannot judge records the failure and finishes the others, because
  leaving a gigabyte on the disk over one unrelated permission problem would be
  the wrong trade.
- `internal/tempowner` is deliberately the same convention as go-mutants'
  package of the same name, written separately because they are separate
  modules. The two sweeps therefore leave each other's directories alone by
  prefix and would agree about any directory they both looked at.
- A `--keep-temp` run now keeps the mutation engine's snapshot too, which is the
  one tree that answers what a mutant actually saw. It is also the largest, and
  the ledger is what makes that affordable: it is collectable rather than
  permanent.
- The ledger is one more file in `.goatest` that a strict reader has to
  understand. It is decoded with `DisallowUnknownFields` and a schema check,
  because the one thing its reader goes on to do is remove the directories it
  names.
- A directory a developer removes by hand leaves an entry behind until the next
  `gc`. `cache status` reports it as `missing` rather than pretending it is
  there, which is also how the ledger stays honest about a temporary directory
  the operating system cleared at boot.
