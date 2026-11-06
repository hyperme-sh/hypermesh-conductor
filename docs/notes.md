# Linked notes

Markdown files in `notes.dir` are the source of truth. The SQLite index is derived from them and can be deleted and rebuilt. Wikilinks use `[[id]]` or `[[title]]`. Frontmatter may set `id`, `type`, `status`, `order` or `step_order`, and `tags`.

Types are the note verbs: goal, steps, done, drop, fact, note, find, ask, show. A goal marked done or closed stays open in the session primer while a linked step is still open. `conductor index status` prints that violation. The index does not rewrite the file.

```bash
conductor index rebuild --notes notes
conductor index status --notes notes
conductor primer --notes notes
```

`conductor serve` watches the directory by content hash and reindexes a file only when the hash changes. Retrieval, for a chat or messages request, seeds notes from full text and from task or step ids, then walks links and backlinks up to `notes.hops` within `notes.token_budget`. The neighborhood, including the session primer, is inserted after the system prompt. Tool definitions and the system message bytes stay the prefix, so the prefix hash does not change. Retrieved text is untrusted and cannot grant permissions.

An in-memory LRU keeps neighborhoods and compaction summaries. Neighborhood keys include the index generation, so an edit misses. Summary keys mix the prefix hash with the tail text. `conductor summary` reports `memory.lookups`, `memory.hits`, and `memory.hit_rate`.

`notes.embed` is off by default. The index has an embedding column for a cheap-tier vector. Nothing in the notes directory is sent anywhere except the retrieved neighborhood inside a model request the proxy was already going to make. The metrics journal does not store note bodies.

`conductor dream --notes <dir>` writes a markdown diff of proposed links and merges next to the inactive JSON proposal. The notes on disk are left as they are.
