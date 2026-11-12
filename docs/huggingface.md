# Hugging Face compatibility contract (#46)

Compatibility targets wire protocols, not a dependency on hosted HF services.
Model downloads and deployment remain host/catalog/plane responsibilities.

| Surface | Decision | Owning path / official reference |
| --- | --- | --- |
| Inference Providers chat | Map to existing direct OpenAI-compatible chat | [Router API](https://huggingface.co/docs/inference-providers/index); HF token stays bound to the router origin |
| TGI Messages API | Support the existing chat protocol | [Messages API](https://huggingface.co/docs/text-generation-inference/messages_api); streaming and tools require separate verified capabilities |
| TEI embeddings | Map to existing `/v1/embeddings` | [TEI quick tour](https://huggingface.co/docs/text-embeddings-inference/quick_tour); native `/embed`, rerank and classification adapters are deferred |
| Hub identity/download | Map to host/catalog artifact identity | [Hub download](https://huggingface.co/docs/huggingface_hub/guides/download); resolve requested revision to commit, verify manifest digests before serving |
| safetensors / GGUF | Map to declared runtime format | [TGI safetensors](https://huggingface.co/docs/text-generation-inference/conceptual/safetensors), [Hub GGUF](https://huggingface.co/docs/hub/gguf); no implicit conversion |
| Fast Hub download | Optional host implementation | [Download guide](https://huggingface.co/docs/huggingface_hub/guides/download); current docs deprecate `hf_transfer` in favor of `hf_xet` |
| Endpoints-style deployment API | Defer | [Inference Endpoints](https://huggingface.co/docs/inference-endpoints/index); use existing catalog, enrollment and lease APIs rather than duplicate a managed control plane |

HF's OpenAI-compatible router is documented as chat-only. Native image, speech
and other task APIs are separate operation adapters; chat support does not imply
support for those payloads. HF's Responses beta is not a verified Conductor
capability merely because a generic Responses translator exists.

For a direct HF route, preserve the configured Hub model name and a supported
provider suffix such as `:fastest`, `:cheapest` or `:preferred`. They are upstream
selection hints, not permission to widen local spend/locality policy. For a
Hypermesh gateway route, map an explicitly configured Hub identity to a catalog
ID; do not forward an HF policy suffix as a catalog ID or send an HF token to the
gateway. Dynamic upstream provider selection cannot satisfy an exact-provider or
local-only requirement unless its resolved destination has been verified.
`GET /v1/models` metadata from HF is advisory discovery, not an automatic grant.

Artifact identity includes Hub repository, requested revision, resolved commit,
file digest manifest, tokenizer revision, format and quantization. Route capability
records also bind model identity, runtime revision and protocol. A tag must resolve
to a concrete commit before download verification. Record exact artifacts while
running TEST services continue tracking their moving deployment channels.
A safetensors artifact does not establish TGI/vLLM/TEI compatibility without the
matching architecture/runtime capability; GGUF likewise requires a compatible
llama.cpp-class runtime. Unknown capabilities fail before dispatch.

Contract cases cover chat, SSE, tool calls and embeddings independently, including
wrong feature/model/runtime, missing usage, malformed streams and zero dispatch on
capability denial. They use recording stubs in PRs. Native host-runtime qualification
belongs to the existing nightly batch. HF-hosted accounts are not required for
Hypermesh use, builds or these contract tests. This note specifies compatibility;
it does not claim every Hub checkpoint or remote provider has been qualified.
