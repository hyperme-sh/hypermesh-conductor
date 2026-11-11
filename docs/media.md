# Media operations (#15)

`POST /v1/ocr` is the native Mistral OCR adapter. A tier advertises `ocr_outputs`
(`text`, `tables`, `bounding_boxes`) and a revision-bound verified `ocr` capability.
`GET /v1/operations` reports configured OCR models, output support and locality.
Choose the configured tier with `X-Conductor-Tier` (default cheap).

Input is an inline base64 PDF (`document.type=document_url`) or PNG/JPEG
(`document.type=image_url`) in the matching native URL field. Remote document URLs
are rejected. `required_outputs` names mandatory fields; unsupported fields fail
before the document reaches either review or the model. `local_only:true` requires
local model and review endpoints. AIuditor reviews the document operation and any
replacement is validated again without dropping the caller's output requirements.

The response preserves native pages in zero-based order. Every page has Markdown
text. Required tables are separate HTML objects with IDs; render them as untrusted
content. Required block boxes use top-left and bottom-right integer coordinates
within the page's reported pixel dimensions, origin at the top left, x rightward
and y downward. Empty tables/blocks are valid for a page without such content;
missing requested fields, reordered pages, and invalid coordinates fail the
output contract. OCR 4 block output and table extraction follow the official
[Mistral OCR contract](https://docs.mistral.ai/studio/document-processing/basic_ocr)
and [API schema](https://docs.mistral.ai/openapi.yaml).

Requests and responses share the proxy's bounded buffers and timeout. Redirects
are refused. Backend failure status and redacted JSON are returned to the caller;
documents and extracted text are never journaled. Journal usage remains uncertain
because page-based OCR usage is not an inference-token count. Tiers with hard
inference-token budgets reject OCR rather than claim an unsupported guarantee.

The recording-stub contract covers two pages, a table, bounding boxes, and a
local-only refusal. It does not establish extraction quality on a real model.

Future transcription uses typed audio input and timestamped segments in seconds;
image generation uses typed image artifacts with MIME, dimensions, and a content
hash. Long-running backends need an operation ID, explicit pending/completed/failed
states, scoped artifact retrieval, cancellation and expiry. Those operations are
not advertised until implemented and qualified. No binary artifact enters chat
history or the metrics journal by default.
