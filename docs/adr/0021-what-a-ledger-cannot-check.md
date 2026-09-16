<!--
SPDX-FileCopyrightText: 2026 goatest contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# 0021 — What a ledger cannot check

## Status

Accepted, 2026-09-17. The ledgers it is about are listed in
[CLAUDE.md](../../CLAUDE.md) and live in `internal/devgates`, `internal/trace`,
`internal/cli`, `internal/report`, `internal/assure` and
`internal/devtools/testaudit`.

## Context

This repository gained twelve documentation ledgers in a day. Each pins a set
the code enumerates against a set a page enumerates, in both directions, and
each carries a test proving it can fail — because two agreeing lists is also
what it looks like when one of them was read as empty.

They work. Writing them found a verdict missing from two pages, a `--help` that
described a file it did not write, a flag exclusivity nothing documented, and a
vocabulary written out five times with two of the copies compared to nothing.

They also did not find several things found the same day, and the pattern in
what they missed is worth writing down before it is forgotten. A ledger is a
tool with a shape, and knowing the shape is how you know what else you need.

There are three questions, and a ledger answers one.

**Does the claim match the set?** A ledger answers this, mechanically and in
both directions. This is most of what the twelve do.

**Does the set cover what actually happens?** Nothing mechanical answers this.
It is a choice of granularity, made by whoever writes the ledger, and a ledger
over the wrong set is green while the thing it was built to protect goes wrong.

go-mutants' `docs/trace-v1.md` claimed this module spelled a payload
differently but carried "the same `kind` and `detail` fields". The field names
were right. What was wrong was the implication that the same things flowed
through them, and no comparison of field names can see that: `progress` here
carried three different kinds of line, one of which the dashboard parsed with
`Sscanf` out of prose written for a person. A ledger over field names is green.
A ledger over the kind vocabulary would not have been.

The same shape, closer to home: `dogfood_task_test.go` pinned the contents of
the dogfood task, correctly and completely, for a task no workflow ran. And the
licence ledger checked that every file said under what terms it was offered
while the manifest it read had never been formatted, because `mise run fmt`
would have caught that and CI was reimplementing the task rather than running
it.

**Is the reasoning behind the claim correct?** Nothing answers this at all, and
a wrong reason survives longer than a wrong claim because a wrong claim
eventually fails a test.

Three from one day. `internal/gocmd` in go-mutants bounded an elapsed time at
five times a deadline with a comment worrying that the bound was loose — the
worry was right, and the number chosen did not answer it, and the comment said
the first thing and not the second. The tier scan here evaluated a build
constraint instead of reading it, on the reasoning that evaluation was what a
constraint is for; `//go:build !windows` evaluates true for a tag set holding
only `integration`, because the negation of absent is present. And the licence
gate read `REUSE.toml` as lines rather than decoding it, with a comment
explaining that a decoder "would make the gate depend on the shape of a document
whose shape REUSE owns" — which had the dependency backwards, since reading
lines depends on the formatting, and `taplo fmt` collapsing an array onto one
line was enough to make the gate report every annotated file as unlicensed.

Each of those reasons was written down, reviewed, and wrong. The claims they
supported were mostly right, which is why nothing failed.

## Decision

Ledgers are the default and stay the default. Any page that enumerates a set the
code also enumerates is pinned to the code by a test, in both directions, and
adding such a page means adding its ledger in the same change.

Beyond that, three things follow from what a ledger cannot do.

1. **Count the places a set is written out, before choosing what to pin.** A
   ledger over three of five copies is green while two drift. The prepare
   vocabulary was in the constants, the schema, the page, `reader.go` and
   `internal/devtools/tracesummary`; the answer was not a bigger ledger but
   `internal/trace/vocabulary.go`, so that there are two places and everything
   else derives.

2. **Pin that a thing runs, not only that it is correct.** A task, a gate or a
   guard that nothing invokes is a claim with no verification behind it however
   carefully its contents are checked. `internal/devgates` refuses a job that
   runs a task mise does not declare, and one that runs a task without
   installing mise — the second is a failure only the workflow can find, which
   is precisely why a gate should be asked to know it.

3. **A reason is a claim about why, and it is unchecked.** When a comment
   explains a choice, the explanation is the part most likely to be wrong and
   least likely to fail. Write the alternative that was rejected and what it
   would have cost, because that is the part a reader can evaluate. "Read as
   lines because a decoder would couple us to REUSE's shape" is a reason nobody
   can check; "read as lines rather than decoded, which couples this to taplo's
   line breaks instead" is one anybody can.

### A measurement is a claim about a mechanism

A number borrowed from somewhere else is a reason with its evidence detached,
and it reads as stronger than a reason because it has digits in it.

go-mutants measured a recording flag on its own dogfood at about twenty times
the wall clock and said so. The number was right. Acting on it here was not:
that cost comes from the engine writing every child's output to a file, and this
module's recording keeps a length and a digest and drops the bytes - so the
mechanism producing the twenty was absent here, and the number said nothing
about this repository. A flag was removed from a workflow on the strength of it,
which is a change made with no measurement at all, wearing somebody else's.

The obligation runs both ways, and the sending half is the easier one to miss.
"About twenty times" can be borrowed; "about twenty times, for a baseline that
runs seventeen packages' tests three times, in an implementation that saves each
child's output to a file" cannot. A measurement shared without its mechanism is
a conclusion shared without its reason, which is the same defect as the one
above with more authority behind it.

## Consequences

- The twelve ledgers are cheap to keep and were worth their cost several times
  over on the day they were written. Nothing here argues against more of them.
- Granularity is a design decision with no test behind it. A reviewer asking
  "what does this ledger not see?" is doing work no tool does, and the answer
  belongs in the ledger's own comment.
- The third point has no enforcement and is not going to get one. It is a
  habit, recorded here so that the next person to write a confident sentence
  about why has read a page saying that three such sentences were wrong in one
  day.
- A number from another project, another scope or another day is evidence about
  what it measured and a hypothesis about anything else. Saying which is free at
  the moment of writing and expensive afterwards.
- This record will age. The examples in it are from a single day of work on one
  branch, and their value is as evidence that the categories exist rather than
  as a survey. A later reader should trust the three questions and check the
  examples.
