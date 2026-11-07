# AIuditor hop

Every proxied request follows one order: classify and route, then the AIuditor hop, then the destination. Conductor does not implement AIuditor. It ships the contract, an HTTP client, and an in-process stub.

AIuditor is the component that loads safety rules, plugins, and RBAC. Conductor sends it the request and the routing decision. AIuditor returns allow, deny, or modify. The routing rules still choose the tier. A modify cannot change the model: Conductor writes the policy model back onto the body before the destination call.

## Modes

| `aiuditor.mode` | Behavior |
| --- | --- |
| `stub` | In-process. An empty rules file allows the request. This is the default. |
| `http` | `POST` the envelope below to `aiuditor.url`. |

`fail_closed` defaults to true. When mode is `http` and the endpoint is unreachable, times out, or returns a non-200 response, Conductor does not call the destination and records uncertain usage. Set `fail_closed` to false only when a missed review should continue to the destination.

The stub rules file is optional:

```json
{
  "deny_job_classes": {"code": "rbac: denied"},
  "replace": [{"job_class": "parse", "from": "SECRET", "to": "[redacted]", "reason": "plugin"}]
}
```

`examples/aiuditor-stub-rules.json` is a sample. The sample is not loaded by `examples/endpoints.json`. The stub is a test double. It does not load plugins and it does not evaluate RBAC.

## Envelope

`POST` body:

```json
{
  "request": {
    "protocol": "openai.chat",
    "method": "POST",
    "path": "/v1/chat/completions",
    "stream": false,
    "headers": {"content-type": "application/json"},
    "body": {}
  },
  "routing": {
    "job_class": "parse",
    "tier": "cheap",
    "model_id": "local-small",
    "reason": "policy tier for parse is cheap",
    "suggestion_tier": null,
    "suggestion_accepted": null,
    "prefix_sha256": ""
  }
}
```

`protocol` is one of `openai.chat`, `openai.responses`, `openai.embeddings`, `anthropic.messages`.

Headers named `authorization`, `proxy-authorization`, `x-api-key`, `cookie`, and `set-cookie` are removed before the hop. Conductor headers are removed too. The body is included so a rule can read the prompt. Conductor does not write that body to the journal or the default log.

Response:

```json
{"action": "allow", "reason": "rbac: member"}
```

`action` is `allow`, `deny`, or `modify`. `modify` includes `body`, a replacement JSON object. Any other action is treated as an unreachable hop.

Deny becomes HTTP 403 to the client. The destination is not called. The journal error is `aiuditor deny: ` plus the reason.
