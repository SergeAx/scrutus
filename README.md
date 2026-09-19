# scrutus

A linter for source-code comments. scrutus finds comments that are wrong or
that say nothing the code does not, reports them like any other linter, and
deletes the useless ones.

Each comment is paired with the code it describes and scored by
[TypeSafe AI](https://typesafe.ai)'s Jev model for accuracy and usefulness.
The model only scores; fixed local thresholds decide what is reported and what
is deleted.

> **Status: v0.1, pre-release.** Go and PHP sources are supported. The
> TypeScript, JavaScript and Python extractors are not written yet; see the
> [implementation status](docs/design_spec.0.1.md#124-implementation-status).

## Quickstart

Install scrutus (Go 1.26 or newer):

```sh
go install github.com/SergeAx/scrutus/cmd/scrutus@latest
```

Set `TYPESAFE_API_KEY` in your environment, or put it in a `.env` file in the
working directory or next to the executable. Then preview what scrutus would
delete, without writing anything:

```sh
scrutus fix --diff ./...
```

Review everything it flags:

```sh
scrutus check ./...
```

```text
payments.go:23:1: ERROR flag [wrong-comment]
    accuracy   [----------]   3%  Fundamentally wrong
    usefulness [#---------]  13%  No information beyond the code
    comment    // Total multiplies the amount by three.
    code       func Total(c Charge, quantity int64) int64 { return c.AmountCents * quantity }

payments.go:58:2: WARNING delete [useless-comment]
    accuracy   [##########] 100%  Accurate without material errors
    usefulness [----------]   2%  No information beyond the code
    comment    // Increment the attempt counter.
    code       attempt++ delay := time.Duration(attempt) * 250 * time.Millisecond

[…]

16 comments in 2 files: 1 error, 4 warning, 2 info
0 cached, 12 assessed in 12 request(s), 2 suppressed, 0 baselined
cost $0.00020 (4800 input tokens), 11ms
```

Then `scrutus fix ./...` deletes the comments marked `delete`. Wrong comments
are flagged and left for a person to fix.

## Usage

| Task | Command |
| --- | --- |
| Lint locally | `scrutus check ./...` |
| Pre-commit hook | `scrutus fix --staged` |
| CI annotations | `scrutus check --changed --format sarif -o scrutus.sarif` |
| Adopt on a legacy codebase | `scrutus baseline ./...`, then commit `baseline.json` |

`--staged` and `--changed` score only comments inside the changed hunks, so an
old comment elsewhere in the file never blocks a commit. `fix` exits 1 when it
has deleted something, so a pre-commit hook fails and you restage. In CI,
`fix` refuses to write files unless you pass `--allow-ci-write`.

Report formats: `text` (the default in a terminal), `json` (the default when
piped), `sarif`, `github`, `rdjson` and `checkstyle`. Run `scrutus help check`
for every flag.

## Configuration

scrutus reads `.scrutus.toml` from the working directory or the nearest
directory above it. Flags override the file.

```toml
version = 1
profile = "default"              # strict | default | lenient
languages = ["go", "php"]
ignore = ["vendor/**", "**/*_test.go"]

[jev]
model = "jev-latest"             # pin a version in CI for stable verdicts
```

List only the languages you want scored. When a file in scope belongs to a
configured language this build cannot parse, scrutus exits 2 instead of
silently skipping the file. The default list includes the three languages that
are not supported yet.

These comments are never scored: `TODO`, `FIXME`, `HACK`, `XXX`, tool
directives such as `//go:`, `nolint` or `@phpstan-`, and anything shorter than
12 characters. To silence a single comment, put `scrutus:ignore` in it or
`scrutus:ignore-next` on the line above. See the
[config file reference](docs/design_spec.0.1.md#42-config-file) for every
setting.

## How comments are judged

A doc comment is judged against the whole declaration it documents. An inline
comment is judged against the paragraph of code below it, up to the next blank
line. A trailing comment is judged against the statement on its line. Jev
answers three questions about each pair:

- **Accuracy**: is it fundamentally wrong, materially misleading, roughly
  correct, or accurate?
- **Usefulness**: does it add no information, a marginal hint, a useful why or
  edge case, or context without which the code would be misread?
- **Overreach**: does it describe code outside its pair, such as the whole
  function?

The first matching rule wins, in this order:

| Rule | Severity | Action | When, under the default profile |
| --- | --- | --- | --- |
| `low-confidence` | info | flag | Model confidence below 0.5 |
| `wide-scope` | info | flag | Overreach 0.8 or higher, except on doc comments |
| `wrong-comment` | error | flag | Accuracy 30% or lower |
| `useless-comment` | warning | delete | Usefulness 15% or lower |
| `redundant-annotation` | warning | flag | A useless `@param`, `@var` or similar tag |
| `weak-comment` | warning | flag | Accuracy 60% or lower, or usefulness 35% or lower |

Wrong comments are never deleted automatically, because one may be the only
sign that the code next to it is wrong too. A useless doc comment on an
exported Go identifier is reported as `weak-comment` instead of being deleted.
If deleting comments leaves a file that no longer parses, scrutus keeps the
original file and reports `fix-aborted`. The
[design spec](docs/design_spec.0.1.md#6-classification-and-policy) explains the
reasoning behind each rule.

## Cost and caching

Jev charges $0.042 per million input tokens and nothing for output. A comment
costs about 630 input tokens, so 100,000 comments cost about $2.65. Each run
stops at `--budget` cents, 50 by default. Verdicts are cached, keyed on the
comment, its code, the rubric version and the model, so unchanged comments are
not scored again.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | No findings at or above `--fail-on`, which defaults to `error` |
| 1 | Findings at or above `--fail-on`, or `fix` deleted something |
| 2 | Tool error: bad config, API failure, or a language this build cannot parse |
| 3 | Budget exceeded, before or during the run |

An API outage is exit 2, never a finding. `--soft-fail` turns connection
failures into exit 0.

## Development

```sh
go build ./...
go vet ./...
go test ./...      # offline: recorded answers through internal/jevstub
```

Read [AGENTS.md](AGENTS.md) for the project's invariants and conventions and
[the design spec](docs/design_spec.0.1.md) for the full design.

## License

[MIT](LICENSE)
