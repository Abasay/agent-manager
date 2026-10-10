# AMP Instrumentation Matrix

Per-PR + nightly compatibility-test suite for AMP instrumentation: it checks
that each `(provider × version × framework × version × python)` combination
emits spans the observer can parse. Three docs:

- [`DESIGN.md`](./DESIGN.md) — architecture of the test system (the *why*).
- [`RUNBOOK.md`](./RUNBOOK.md) — operational how-to: adding cells, triaging
  reds, onboarding a Traceloop release, adding a provider, the heavy tier.
- [`FINDINGS.md`](./FINDINGS.md) — living `F-NNN` log of upstream gaps and
  schema concessions.

This README is just the quickstart — everything beyond running a cell lives
in `RUNBOOK.md`.

## Setup (once)

`nox` drives the suite; each cell then gets its own auto-managed venv. You
only need `nox` + `pyyaml` in an outer environment:

```bash
cd test/instrumentation-matrix
python3.11 -m venv .venv
source .venv/bin/activate
pip install nox pyyaml
```

(`.venv/` is gitignored. Python 3.11 is the local default; CI fans out
across 3.10–3.13.)

## Run

```bash
# one cell (cassette replay — no real API key needed)
nox -s emission -- --cell-id=traceloop-0.61.0-langchain-0.3.27-py3.11

# all cells for one framework
nox -s emission -k langchain

# all cells for one Python version (how CI shards the matrix)
nox -s emission -- --python-version=3.11

# the full emission matrix
nox -s emission

# aggregate per-cell reports into reports/summary.md + triage diffs
nox -s report
```

Results land in `reports/` (gitignored): `reports/cells/<id>.json` per cell,
`reports/summary.md`, and `reports/diffs/<id>.diff.md` for failures. See
RUNBOOK §4 for how to read them.

## Re-record cassettes

Only needed when you change a sample's prompt/model/tools (not when bumping
versions). Requires real keys + the framework installed in your venv:

```bash
OPENAI_API_KEY=sk-... ANTHROPIC_API_KEY=sk-ant-... \
    python scripts/record_cassette.py <framework> <scenario>
```

## Run the suite's own unit tests

```bash
pip install pytest jsonschema requests responses   # one-time, into .venv
python -m pytest tests/
```
