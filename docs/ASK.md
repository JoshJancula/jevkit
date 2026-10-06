# Ask Jev from the terminal

`jevkit ask` sends a typed question through the same key resolution and local redaction as agent integrations. Choose the answer shape you need:

```bash
# Yes/no value from 0 to 1
jevkit ask noul --state "42 tests passed; 0 failed" --question "Did the build succeed?"

# One label from a closed set
jevkit ask choice --state "HTTP status: 503" --question "What next?" --options "retry,fail"

# One ordered level
jevkit ask score --state "Authentication changed" --question "How risky?" --levels "low,medium,high"
```

Add `--format json` when another tool will read the result. For a full API-shaped request with several named questions, use `jevkit ask request --file request.json` or `--file -` for stdin. A request file can also choose a `model`; `--model` on the command overrides it.

## Supplying structured input

| Field | Plain text | JSON argument | File or stdin |
| --- | --- | --- | --- |
| State | `--state` | `--state-json` | `--state-file` |
| Question | `--question` | `--instructions-json` | `--instructions-file` |
| Choices or levels | `--options`, `--levels`, or Noul criteria flags | `--criteria-json` | `--criteria-file` |

Use one form per field. Plain options and levels are comma-separated. JSON and file forms support descriptions or other structured criteria:

```bash
jevkit ask choice --state "Invoice total looks wrong" \
  --question "Which team?" \
  --criteria-json '{"billing":"Invoice or refund issues","technical":"Product defects"}'
```

Run `jevkit ask noul --help`, `jevkit ask choice --help`, or `jevkit ask score --help` for the complete flag set. See the [TypeSafe API reference](https://docs.typesafe.ai/api) for underlying request and answer types.
