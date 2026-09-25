# Ask Jev

`jevkit ask` is the human-facing interface to Jev’s three typed questions:
Noul (yes/no), Choice (a closed set of labels), and Score (ordered levels).
It uses the same key resolution and local redaction safeguards as the agent
integrations. Every state, instruction, and criterion is redacted before it is
sent.

## Single questions

```bash
# Noul: a yes/no assessment returned as a value from 0 to 1.
jevkit ask noul --state "42 tests passed; 0 failed" --question "Did the build succeed?"

# Choice: --options is one comma-separated, closed set of answer labels.
jevkit ask choice --state "HTTP status: 503" --question "What should happen next?" --options "retry,fail"

# Score: levels are ordered from low to high and must contain 2–10 values.
jevkit ask score --state "This change affects authentication" --question "How risky is this?" --levels "low,medium,high"

# JSON is useful for scripts or another tool.
jevkit ask choice --state "lint: 0 issues" --question "What is the outcome?" --options "pass,fail" --format json
```

The CLI translates a Choice list to the API’s `criteria` map with null
descriptions. For the underlying request and answer types, see the [TypeSafe
API reference](https://docs.typesafe.ai/api).

## Input forms

Each `ask noul|choice|score` field has three forms; use exactly one form per
field:

| Field | Plain shorthand | JSON argument | File or stdin |
| --- | --- | --- | --- |
| state | `--state` | `--state-json` | `--state-file` |
| instructions | `--question` | `--instructions-json` | `--instructions-file` |
| criteria | `--options`, `--levels`, or Noul criteria flags | `--criteria-json` | `--criteria-file` |

Plain flags are the ergonomic default: comma-separated values for Choice and
Score, or free text for state, question, and Noul criteria. The JSON flags take
literal JSON text, quoted as one shell argument. Use the file flags for the same
JSON or plain text from a file, or from stdin with `-`.

Use `--criteria-json` or `--criteria-file` when an option needs a rubric,
examples, or other structured fields:

```bash
jevkit ask choice --state "Customer says the invoice total looks wrong" \
  --question "Route to which team?" \
  --criteria-json '{"billing":"Payment, invoice or refund issues","technical":"Product defects or errors","sales":"Pricing or new purchase questions"}'
```

Run `jevkit ask noul --help`, `jevkit ask choice --help`, or
`jevkit ask score --help` for the complete flag set and examples.

## Multi-question requests

`jevkit ask request --file <path|->` sends a full API-shaped request with
several named questions and an optional model override:

The model defaults to the selection shown by `jevkit model status`. A `model`
in the request file overrides that selection, and `--model` overrides the file.

```bash
jevkit ask request --file request.json
cat request.json | jevkit ask request --file - --model jev-1.13.0
```

The file mirrors the wire shape: an optional `model`, a `state`, and a
`questions` object whose entries contain `type`, `instructions`, and
`criteria`. Values are validated against the local typed schema and redacted
before sending. With `--format json`, output includes each answer’s type,
confidence, probabilities, and Score legend.
