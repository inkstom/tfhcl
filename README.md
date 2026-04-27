# tfhcl

A CLI for bulk-editing Terraform `.tf` files: sort, remove, move, or list
top-level blocks across a directory tree, with an interactive TUI picker and a
markdown plan report.

Built on [`hashicorp/hcl/v2`](https://github.com/hashicorp/hcl) so edits
preserve formatting, comments, and whitespace, only the blocks you target are
rewritten.

## Install

```sh
go install github.com/you/tfhcl@latest
```

Or build from source:

```sh
go build -o tfhcl .
```

## Quick start

```sh
# Sort top-level blocks alphabetically (dry-run)
tfhcl --root ./infra --op sort

# Remove every google_project_iam_member resource
tfhcl --root ./infra --op remove --select 'resource.google_project_iam_member.*'

# Move all data sources into a dedicated file
tfhcl --root ./infra --op move --select 'data.*' --out ./infra/data.tf

# Interactively pick blocks to move
tfhcl --root ./infra --op move --out moved.tf -i

# List matching blocks (pipe-friendly, tab-separated)
tfhcl --root ./infra --op list --select 'module.*'

# Generate a markdown change report
tfhcl --root ./infra --op move --select 'data.*' --out ./infra/data.tf --plan
```

By default tfhcl runs in **dry-run** mode and writes nothing. Pass
`--dry-run=false --in-place=true` to actually modify files.

## Scope: what tfhcl edits

tfhcl operates on **top-level blocks**: the outermost `resource`, `module`,
`data`, `variable`, `output`, `locals`, `provider`, `terraform`, etc. blocks of
each `.tf` file. It does **not** touch:

- Attributes inside a block (`name = "foo"`)
- Nested sub-blocks (`lifecycle {}`, `dynamic {}`, `binding {}`)
- Heredocs, expressions, or any block bodies

If you need attribute-level edits, use
[`minamijoyo/hcledit`](https://github.com/minamijoyo/hcledit): it composes
cleanly with tfhcl.

## Operations

| Op       | What it does                                                             |
| -------- | ------------------------------------------------------------------------ |
| `sort`   | Reorder top-level blocks alphabetically by type and labels (file-wide)   |
| `remove` | Delete blocks that match the selector(s)                                 |
| `move`   | Cut matching blocks and write them to the file given by `--out`          |
| `list`   | Print matching blocks (read-only), for piping into shell tools          |

### `sort` notes

`sort` ignores all selectors, it always reorders every top-level block in
each scanned file. Top-level attributes (rare in practice) are kept above the
sorted block sequence. `--interactive` cannot be combined with `sort`.

### `move` notes

- The destination file given by `--out` is **overwritten** with the sorted set
  of moved blocks.
- If the destination file already exists and `--backup=true` (the default),
  tfhcl copies the existing file to `<out>.bak` before overwriting.
- `--out` can point inside or outside the `--root` tree; if it's inside,
  tfhcl will not re-process the just-written file in the same run.
- Moved blocks are sorted by type + labels in the output file for stable
  diffs.

### `list` notes

- Prints `path<TAB>type.label1.label2` to **stdout**, one match per line.
- Prints a totals line (`N matches (block_type=count, …)`) to **stderr**, so
  stdout stays clean for piping.
- Never modifies anything; `--in-place` and `--dry-run` are no-ops here.

## Selectors

The `--select` flag (repeatable) targets blocks by their HCL header:

```
<block-type>.<label1>.<label2>
```

- The first segment is the block type (`resource`, `module`, `data`,
  `variable`, …).
- Subsequent segments map to the block's labels in order. For `resource` and
  `data` that's `<type>.<name>`. For `module`, `variable`, `output`,
  `provider`, etc. it's just `<name>` (one label).
- Blocks with no labels (`locals`, `terraform`) are matched by type alone.
- Each segment supports glob patterns (`*`, `?`, `[abc]`) via Go's
  [`filepath.Match`](https://pkg.go.dev/path/filepath#Match).
- A bare `*` segment matches anything; an omitted trailing segment is treated
  as "any value".

Examples:

| Selector                                  | Matches                                     |
| ----------------------------------------- | ------------------------------------------- |
| `resource.google_project_iam_member.*`    | every IAM-member resource                   |
| `resource.google_storage_*.assets`        | any storage resource named `assets`         |
| `module.network`                          | the `network` module block                  |
| `data.google_iam_policy.admin`            | one specific data source                    |
| `data.*`                                  | every data block                            |
| `variable.*`                              | every input variable                        |
| `output.*`                                | every output                                |
| `locals`                                  | every `locals` block                        |
| `terraform`                               | every `terraform` block                     |

Multiple `--select` flags **OR** together, a block matches if any selector
matches.

### Legacy match flags

These predate `--select` and are still supported, mainly for the existing
`resource`-focused workflow:

| Flag                | Effect                                                          |
| ------------------- | --------------------------------------------------------------- |
| `--block`           | Match only blocks of this type (e.g. `resource`, `module`)      |
| `--resource-type`   | Glob against the first label (resource/data type)               |
| `--resource-name`   | Glob against the second label (resource/data name)              |

These compose with **AND** semantics: a block must satisfy all three to match.
For non-resource block types (`module`, `variable`, …), only `--block` applies.

```sh
# Equivalent to --select 'resource.google_storage_bucket.assets'
tfhcl --root . --op remove \
      --block resource \
      --resource-type 'google_storage_bucket' \
      --resource-name 'assets'
```

If `--op remove` or `--op move` is used without `--select`, `--block`, or
`--interactive`, tfhcl defaults `--block` to `resource` to avoid accidentally
nuking everything.

### Matching precedence

When more than one matching mechanism could apply, tfhcl picks exactly one,
in this order:

1. **Interactive selection** (`--interactive` / `-i`) — if used, the explicit
   set of blocks the user picked is the only thing that matches. `--select`
   and the legacy flags are ignored.
2. **`--select` flags** — if any are present, only selectors are evaluated.
   Legacy flags are ignored.
3. **Legacy flags** — `--block` + `--resource-type` + `--resource-name`.

This means you can't combine `--select` with `--resource-type` and have them
intersect; pick one mechanism per run.

## Interactive mode (`-i`)

Launches a TUI that lists every top-level block in the discovered files with a
live-filtering search box and a side-by-side preview of the highlighted block.
Selections feed straight into the chosen operation.

```sh
tfhcl --root ./infra --op move --out moved.tf -i
tfhcl --root ./infra --op remove -i
tfhcl --root ./infra --op list -i        # browse + search without committing
```

**Keys**

| Key                          | Action                                          |
| ---------------------------- | ----------------------------------------------- |
| _typing_                     | Filter (multi-token AND across type/label/path) |
| `↑` `↓` / `Ctrl+P` `Ctrl+N`  | Move cursor                                     |
| `PgUp` / `PgDn`              | Jump 10 rows                                    |
| `Home` / `End`               | Jump to top / bottom                            |
| `Tab`                        | Toggle selection at cursor (advances cursor)    |
| `Ctrl+A`                     | Toggle all currently-filtered rows              |
| `Ctrl+X`                     | Clear all selections                            |
| `Enter`                      | Confirm and run the operation                   |
| `Esc` / `Ctrl+C`             | Cancel without changes                          |

The filter takes multiple whitespace-separated tokens that all have to match —
`iam admin` finds blocks whose `type.label1.label2` string or file path
contains both `iam` and `admin`.

Notes:

- `--interactive` does not apply to `--op sort` (sort runs file-wide, not per
  block), tfhcl will exit with an error.
- Confirming with no selections is treated as a cancel; tfhcl prints
  `No blocks selected.` and exits 0 without modifying anything.
- Selection precision survives duplicate names across files: tfhcl tracks
  picks by `(file path, block signature)`, so picking `resource.foo.bar` from
  `a.tf` will not match `resource.foo.bar` in `b.tf`.

## Plan mode (`--plan`)

Add `--plan` to any operation to swap the terse summary for a markdown change
report, useful in CI, PR comments, or piping into `glow`:

```sh
tfhcl --root ./infra --op remove --select 'resource.aws_*' --plan > plan.md
```

The report includes:

- A metadata table (operation, root, date, destination, selectors, dry-run)
- A `## Files Affected` section, one subsection per file, with a per-block
  table showing block type, labels, and the action taken
- A `## Summary` section with totals

Plan mode is **purely an output format**. It does not imply dry-run on its
own, combine with `--in-place --dry-run=false` to actually apply the changes
while still emitting the markdown report.

## File discovery

When `--root` is a directory, tfhcl walks it and processes every file ending
in `.tf`. When `--root` is a single `.tf` file, only that file is processed.

| Behavior          | Default              | Override                                 |
| ----------------- | -------------------- | ---------------------------------------- |
| Recurse           | yes                  | `--recursive=false` for top-level only   |
| Skip dirs         | `.git`, `.terraform` | `--exclude-dirs a,b,c` (comma-separated) |
| Hidden files/dirs | skipped              | `--include-hidden`                       |

Files are processed in lexicographic order, there is no parallelism and runs
are deterministic and easy to script around.

```sh
# Only files directly under ./infra (no recursion)
tfhcl --root ./infra --recursive=false --op list --select 'resource.*'

# Also process .terraform/modules and any dotfiles
tfhcl --root . --exclude-dirs '' --include-hidden --op sort
```

## Backups & safety

- Default mode is `--dry-run=true --in-place=false`: nothing is written, the
  summary tells you what *would* change.
- To actually apply changes you must pass **both** `--dry-run=false` and
  `--in-place=true`.
- With `--backup=true` (default) tfhcl writes `<file>.bak` next to every
  modified file *before* the new content is written. For `--op move`, the
  destination file (if it already exists) is also backed up to `<out>.bak`.
- `.bak` files are full copies, not diffs. Repeated runs overwrite the same
  `.bak`; if you need rollback history, commit before running tfhcl.
- If any `.tf` file fails to parse, tfhcl aborts the whole run with a non-zero
  exit and writes nothing. Partial edits are never persisted.

## Exit codes

| Code | Meaning                                                  |
| ---- | -------------------------------------------------------- |
| `0`  | Success (including "no matches" and user cancellation)   |
| `1`  | Any error: bad flags, parse failure, I/O error, etc.     |

`--op list` always returns 0, even with zero matches, so it's safe to use
in shell pipelines guarded by `set -e`.

## Output & color

- The default summary, the interactive TUI, and the colorized parts of `list`
  use [`lipgloss`](https://github.com/charmbracelet/lipgloss). ANSI escape
  codes are stripped automatically when stdout is not a terminal.
- Set the `NO_COLOR` environment variable (any value) to disable color even
  in interactive shells.
- `--op list` writes matches to **stdout** and the totals footer to
  **stderr**, so you can pipe stdout cleanly:

  ```sh
  tfhcl --op list --select 'module.*' | awk -F'\t' '{print $2}'
  ```

- `--plan` markdown is plain text (no ANSI), safe to redirect to a file or
  paste into a PR.

## Flags reference

| Flag                  | Default              | Description                                                |
| --------------------- | -------------------- | ---------------------------------------------------------- |
| `--root`              | `.`                  | Directory or single `.tf` file to process                  |
| `--op`                | `sort`               | `sort`, `remove`, `move`, or `list`                        |
| `--select`            | _(none)_             | Selector expression; repeatable, OR-combined               |
| `--out`               | _(none)_             | Destination file for `--op move` (required for that op)    |
| `--interactive`, `-i` | `false`              | Pick blocks via the TUI (not valid with `--op sort`)       |
| `--plan`              | `false`              | Emit a markdown report instead of the terse summary        |
| `--recursive`         | `true`               | Walk subdirectories of `--root`                            |
| `--in-place`          | `false`              | Write changes back to disk (also requires `--dry-run=false`) |
| `--dry-run`           | `true`               | Show what would change without writing                     |
| `--backup`            | `true`               | Write `<file>.bak` before overwriting                      |
| `--exclude-dirs`      | `.git,.terraform`    | Comma-separated directory names to skip                    |
| `--include-hidden`    | `false`              | Include dotfiles and dot-directories                       |
| `--block`             | _(none, legacy)_     | Match block type (e.g. `resource`, `module`)               |
| `--resource-type`     | _(none, legacy)_     | Glob against the first label of resource/data blocks       |
| `--resource-name`     | _(none, legacy)_     | Glob against the second label of resource/data blocks      |

## Examples

**Strip every IAM binding from a module before importing it elsewhere**

```sh
tfhcl --root ./modules/legacy \
      --op remove \
      --select 'resource.google_project_iam_*' \
      --select 'resource.google_*_iam_binding' \
      --plan
```

**Promote all `data` blocks into one file**

```sh
tfhcl --root ./infra \
      --op move \
      --select 'data.*' \
      --out ./infra/data.tf \
      --in-place --dry-run=false
```

**Find every module reference in a repo**

```sh
tfhcl --root . --op list --select 'module.*' | sort -u
```

**Pick a few blocks by hand**

```sh
tfhcl --root . --op move --out cleanup.tf -i
```

**Generate a PR-ready change plan in CI**

```sh
tfhcl --root ./infra --op remove \
      --select 'resource.aws_iam_user.*' \
      --plan > /tmp/plan.md
gh pr comment "$PR" --body-file /tmp/plan.md
```

**Browse a repo without changing anything**

```sh
tfhcl --root . --op list -i
```

## Comparison with hcledit

[`minamijoyo/hcledit`](https://github.com/minamijoyo/hcledit) is the
established tool in this space, if you need attribute-level edits (read,
update, append individual `key = value` lines), use it. tfhcl focuses on
**top-level block operations across many files** with a directory-walking
interface, glob selectors, an interactive picker, and a markdown plan report.
The two tools compose well: use tfhcl to bulk-move blocks, hcledit to tweak
their internals.

## License

MIT.
