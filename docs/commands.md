<!--
SPDX-FileCopyrightText: 2026 goatest contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Command reference

The command surface is:

```text
goatest verify [packages...] [flags] [-- test-binary-args...]
goatest plan [packages...]
goatest doctor
goatest init
goatest explain ID
goatest replay ID
goatest accept ID --reason=TEXT --expires=RFC3339 [--owner=NAME] [--ticket=ID]
goatest fix [ID...] [--apply]
goatest report [--latest-full|--run=ID]
goatest cache status|gc|flush
goatest trace summary [RUN]
goatest trace diff RUN-A RUN-B
goatest help [command]
```

Use `goatest help COMMAND` for flags and [report-v1.md](report-v1.md) for verdicts and exit codes.
