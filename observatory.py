"""Parse agent run logs (JSONL) and classify failure events into a small taxonomy."""

import json

# Small, closed taxonomy of failure classes. Extend deliberately.
FAILURE_TAXONOMY = {
    "tool_error": "A tool call returned an error status",
    "loop": "The agent repeated the same action or message without progress",
    "timeout": "A step exceeded its time or token budget",
    "goal_drift": "The agent abandoned the original goal and did not recover",
    "unhandled_exception": "The agent crashed with an unhandled exception",
    "unknown": "Does not match any known class",
}


def parse_jsonl(path):
    """Yield one dict per non-empty JSONL line, skipping malformed lines."""
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                yield json.loads(line)
            except json.JSONDecodeError:
                # Count it as a failure record of its own: the log itself is broken.
                yield {"type": "log_parse_error"}


def classify(record):
    """Return a taxonomy key for a parsed failure record.

    Records carry a 'type' field; when it is missing or unrecognized,
    fall back to 'unknown' rather than raising.
    """
    rtype = record.get("type")
    if rtype in FAILURE_TAXONOMY:
        return rtype
    if record.get("type") == "log_parse_error":
        return "unhandled_exception"
    return "unknown"


def summarize(records):
    """Return {taxonomy_key: count} across an iterable of parsed records."""
    counts = {key: 0 for key in FAILURE_TAXONOMY}
    for record in records:
        counts[classify(record)] += 1
    return counts


def main(path):
    records = parse_jsonl(path)
    counts = summarize(records)
    total = sum(counts.values())
    print(f"Parsed {total} failure records")
    for key, count in sorted(counts.items(), key=lambda kv: -kv[1]):
        print(f"  {key}: {count}  -  {FAILURE_TAXONOMY[key]}")


if __name__ == "__main__":
    import sys

    if len(sys.argv) != 2:
        print(f"usage: {sys.argv[0]} <run-log.jsonl>")
        sys.exit(1)
    main(sys.argv[1])
