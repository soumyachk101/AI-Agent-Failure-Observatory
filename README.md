# AI Agent Failure Observatory

A small, dependency-free toolkit for observing and triaging AI agent failures: parse agent run logs (JSONL), classify each failure event into a small taxonomy, and get counts you can track over time.

## Usage

Requires Python 3.8+, no dependencies.

```sh
python observatory.py <run-log.jsonl>
```

Each line of the input file is a JSON object describing one failure event, with a `type` field naming the failure. The tool prints a per-class count and a short explanation of each class.

## Failure taxonomy

| Key | Meaning |
| --- | --- |
| `tool_error` | A tool call returned an error status |
| `loop` | The agent repeated the same action or message without progress |
| `timeout` | A step exceeded its time or token budget |
| `goal_drift` | The agent abandoned the original goal and did not recover |
| `unhandled_exception` | The agent crashed with an unhandled exception (also used for malformed log lines) |
| `unknown` | Does not match any known class |

Malformed JSON lines are not skipped silently — each one is counted as an `unhandled_exception` so a broken log file is visible in the tally.

## Extend the taxonomy

Add a new key to `FAILURE_TAXONOMY` in `observatory.py`. Everything else (parsing, counting, reporting) picks it up automatically.
