# Project priorities

Build this engine like a 1990s Toyota Hilux: simple, durable, and able to keep
working when individual parts are damaged.

- Durability is the top priority. Preserve memories and unfinished writes.
- Keep code simple and readable. Prefer a clear 10-line solution over a 50-line
  abstraction that provides the same behavior.
- Do not add unnecessary checks or strict validation that prevent useful work.
- Accept equivalent inputs and normalize them when their meaning is clear.
  For example, `3` and `"3"` should work where a string representation is needed.
- Memories must remain usable. Damaged metadata must not hide an intact body.
- Isolate failures. One bad field, record, directory, or derived index must not
  disable healthy records or unrelated operations.
- Return errors only for real issues that block the requested operation.
  Report recoverable problems as diagnostics and continue useful work.
- Preserve pending work and recover automatically after interruption. Never
  delete a recoverable write just because publication failed.
- Retain ambiguous or unparsed data rather than guessing or silently discarding
  it. Never claim a write is durable before its data and directory are synced.
- Keep the filesystem protections and coordination needed to prevent data loss.
  Resilience does not mean unsafe writes or pretending a failed write succeeded.
- No backward-compatibility layers and no migrations. Change the implementation
  directly and keep one clear path.
- Test actual recovery and continued operation, not merely successful rejection
  of imperfect inputs.
- Work in small increments within the scope agreed with the user.
