# Tiered routing

Rules choose the model tier for a step. A suggestion may agree with that choice. The recorded tier is the policy tier.

## Job classes

| Class | Default tier | Call |
| --- | --- | --- |
| parse, extract, index, classify, embed, summarize, status | cheap | chat completions, except embed |
| plan, architecture, tradeoff, code, review | frontier | chat completions |

`embed` uses the embeddings method on the cheap endpoint. `local` is another name for the cheap tier. `strong` is another name for the frontier tier. Labels such as `status update`, `code review`, and `code generation` map onto the classes above.

A profile file may set `class_tiers` for a known class. That map is policy. A suggestion is not.

## Suggestions

The rank is cheap, then frontier.

- A suggestion equal to the policy tier is accepted.
- A suggestion above the policy tier is rejected. The reason starts with `rejected out-of-policy upgrade`. The cheap or local endpoint still receives the step.
- Any other suggestion is rejected and the policy tier still stands.

## Fallback

A failed call is recorded as uncertain usage on the tier that was selected. The other tier is not called, and the step is not retried.

## Preview fields

`conductor route` prints:

- `job_class`
- `tier`
- `model_id`
- `reason`
- `suggestion_tier` and `suggestion_accepted` (null when no suggestion was offered)
- `prefix_sha256`

The preview does not include the endpoint URL or an API key.

## Endpoints

`examples/endpoints.json` names an OpenAI-compatible API root and a model id for each tier. `base_url` is the API root, such as `http://127.0.0.1:8080/v1`. Keys are not written in the file. `api_key_env` names the environment variable that holds the key. Omit `api_key_env` only for an endpoint that does not require a key. A URL with userinfo is rejected.

## Prefix

The system prompt and tool definitions in the config are the cacheable prefix. They are canonicalized (sorted tool names and sorted JSON keys) and hashed. Step text is sent after the system message. A step cannot supply its own system message. The hash is stored on every journal row. Tail compaction is not implemented.

## Commands

```bash
python -m conductor route --config examples/endpoints.json --class parse
python -m conductor route --config examples/endpoints.json --class parse --suggest frontier
python -m conductor run --config examples/endpoints.json --journal runs/journal.jsonl \
  --task task-1 --step step-1 --class summarize --message "Summarize the note."
python -m conductor summary --journal runs/journal.jsonl
```

Exit status is 0 when the command finishes, 1 when the provider call fails after a journal row is written, and 2 when the config, class, or journal is not usable.
