# AGENTS.md

scrutus is a Go CLI that grades source-code comments for accuracy and
usefulness through the Jev scoring API, reports them like a linter, and
deletes the useless ones.

## Status

The v0.1 core runs end to end: `check`, `fix`, `baseline`,
`cache` and `version`, every reporter, the cache, profiles, `.env` and the
budget. Go and PHP parse in pure Go; JavaScript, TypeScript and Python parse
through tree-sitter in cgo builds. §12.4 of the spec tracks the gaps.

The spec is [`docs/design_spec.0.1.md`](docs/design_spec.0.1.md); read it
before writing code. When the code has to deviate from it, update the document
in the same commit.

The Jev backend is `serge.ax/go/typesafe-sdk-go`, checked out next door at
`../typesafe-sdk-go` and not yet tagged. Develop across the two with a
gitignored `go.work`, never a `replace` directive in `go.mod`.

## Build and test

```sh
go build ./...
go vet ./...
go test ./...      # no network: recorded answers through internal/jevstub
gofmt -l .         # must print nothing, testdata included
```

`testdata/sample` is the corpus the end-to-end tests run on, and
`testdata/jev/payments.json` holds the verdicts the stub replays for it, keyed
by a fragment of each comment. To drive the real binary the same way, serve the
fixtures and point the SDK at them. Run it from inside the sample, because the
repo's own `.scrutus.toml` ignores `testdata/**`, and without the cache, which
would otherwise keep the stub's verdicts:

```sh
go run ./internal/jevstub/cmd/jevstub testdata/jev/payments.json   # prints a URL
cd testdata/sample
TYPESAFE_API_KEY=stub TYPESAFE_BASE_URL=<url> go run ../../cmd/scrutus check --no-cache .
```

Target the current stable Go release and use its idioms. Windows is both a
release target and a development platform: never assume LF line endings or
`/` path separators.

## Invariants

Each of these is relied upon somewhere downstream; breaking one breaks the
cache, the baseline, CI integrations or users' trust.

- Only `internal/assess/jev` touches the network. Scope, extract and filter
  are pure parsing; classify, report and fix are pure policy.
- No test hits the network: use `FakeAssessor` or the recorded responses in
  `testdata/jev/`.
- The model never chooses an action. Thresholds and rules live in
  `internal/classify`.
- A finding ID hashes comment text, code text, rubric version and model
  version — never file path or line.
- Spans are byte offsets, never line/column.
- Rule ids (`wrong-comment`, `useless-comment`, `redundant-annotation`,
  `weak-comment`, `wide-scope`, `low-confidence`, `commented-out-code`,
  `fix-aborted`) are a public contract consumed by SARIF, baselines and
  `--fail-on`. Never rename them.
- The JSON report is canonical and every other format is a projection of it.
  Schema changes bump `schema_version`; rubric wording changes bump
  `rubricVersion`.
- An API outage is a tool error (exit 2, or 0 with `--soft-fail`), never a
  finding.
- `check` never writes. `fix` only deletes comments and re-parses the result
  before an atomic write.
- `go.mod` stays `go install`-clean: no `replace` directives. Only the
  tree-sitter extractors need cgo, so a `CGO_ENABLED=0` build parses Go and
  PHP only — and must exit 2, never skip files, when the scope needs an
  extractor it does not carry.

## Comments

This tool deletes useless comments; its own code must survive it. Comment only
what the code cannot say: a workaround, a non-obvious ordering constraint, a
deliberate choice that looks like a mistake. No comments restating the code or
the signature, no doc comments on tests. A doc comment on an exported
identifier must say more than its name.

## Commits

- [Conventional Commits](https://www.conventionalcommits.org/):
  `type(scope): subject`, where scope is the package name without `internal/`
  (`extract`, `assess`, `classify`, …) or `docs`. Use type `agents` for
  changes to `AGENTS.md`.
- Small, atomic commits; each one builds, passes tests and carries its own
  documentation changes.
- Subject around 50 columns, body hard-wrapped at 72, explaining why rather
  than what.
- On working branches, fold corrections into the commit they belong to
  (`git commit --fixup` plus autosquash) instead of stacking fix-ups.
