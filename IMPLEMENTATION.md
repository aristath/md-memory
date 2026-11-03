# Standalone engine completion

Scope: Go engine and stdio MCP. Markdown is authoritative. Filename-only IDs,
open metadata and relation names, preservation, and failure isolation remain the
foundations. No Clara integration, live-data import, automatic memory Git commits,
network transport, inference service, or background organization was added.

## Capability audit

- [x] Discovery across all four record directories, damaged-file isolation,
  readable orphan sources, and retained single-record creations. Evidence:
  `catalog.go`, `TestCatalogRebuildsAndPreservesIndependentSources`,
  `TestDiscoveryIncludesReadablePendingSingleCreation`.
- [x] Disposable lexical index, names/aliases/descriptions/prose/type/link/time
  retrieval, byte-revision freshness, explicit rebuild and bounded results.
  Evidence: `TestIndexLossPreservesRetrievalGraphChronologyAndComposition`,
  `TestExplicitRebuildDiscardsCachedInterpretations`.
- [x] Source-owned relationships, derived backlinks, story membership edges,
  bounded traversal and uncertainty. Evidence:
  `TestGraphAndRedirectsPreserveAmbiguity`,
  `TestCanonicalStoryReferencesParticipateInBacklinks`.
- [x] Partial chronology from stated bounds and relative order, cycle/constraint
  diagnostics, and independent healthy constraints alongside damaged dates.
  Evidence: `chronology_story_test.go`, including the 2,000-record fixture.
- [x] Canonical story references, nested composition, Unicode/binary pages,
  missing/cyclic/redirected references and shared-prefix skipping. Evidence:
  `TestStoriesComposeReferencesWithoutCopyingNarratives`,
  `TestStoryCanPageLargeSharedCompositionsWithoutReplayingTheirPrefix`,
  `TestStoryBudgetAlsoBoundsMissingReferences`.
- [x] Explicit merge/split, retained originals, one-to-many redirects, rich
  aliases/relationship qualifiers and retry identity. Evidence:
  `organize_test.go`, `completion_review_test.go`.
- [x] Related batches, local references, revision protection, durable receipts,
  interrupted publication, coherent engine views and deliberate resumable
  withdrawal. Evidence: `batch_test.go`, `batch_abandon_test.go`,
  `batch_process_test.go` (three killed-process publication boundaries and
  concurrent process retries). Damaged coordination retains readable snapshots
  as unconfirmed instead of asserting a committed revision.
- [x] Deterministic preparation, relevant shortlists/duplicates/ambiguities,
  original accounts, and two-call capture. Evidence:
  `TestPreparationReturnsAmbiguityWithoutChoosingAnIdentity`,
  `TestActualStdioCaptureRetrievalOrganizationAndRestart`.
- [x] Explicit import roots, reviewed byte spans, verbatim archives, mappings,
  coverage, source removal and retry identity. Evidence: `import_test.go`,
  `TestImportRetrySurvivesRemovalOfSourceDirectory`,
  `TestStdioImportRequiresUnambiguousReviewedBounds`.
- [x] Official SDK stdio MCP with ten tools, guide/record resources, structured
  bounded responses, source/receipt/proposal paging, restart/retry and lifecycle
  handling. Evidence: actual executable/client tests in `mcpserver/stdio_test.go`,
  including signal/EOF shutdown during a waiting write and resource/error budgets.
- [x] README build/install/client configuration, API/format/recovery contracts,
  limitations, and executable Go capture examples.

## Review and release gates

- [x] Final complete correctness/durability review has no actionable findings.
- [x] Final complete simplification review is clean on the same implementation.
- [x] `go test -race -count=1 ./...` passes on the final implementation.
- [x] `go vet ./...` passes on the final implementation.
- [x] MCP executable build and CLI help smoke check pass.
- [x] Real stdio tests also pass with race instrumentation in the child server:
  `GOFLAGS=-race GORACE=atexit_sleep_ms=0 go test -count=1 ./mcpserver`.

Publication is verified after commit through the local commit and the remote
main ref. The final report supplies that revision and push confirmation.

The final gates passed with Go 1.27.3 on linux/amd64. Both full review passes
were repeated after the last implementation changes, with no remaining known
actionable findings. Verification covers the final implementation, not merely
the earlier successful test runs.

## Deliberate guarantees and limits

Journal acceptance, individually synced publication, and a synced commit marker
are distinct phases. Engine queries coordinate with writers; raw filesystem reads
are not multi-file transactions. Individual read calls and separate pages are not
one persistent snapshot. Uncooperative external editors retain the documented
last-check/rename race; byte revisions cannot distinguish identical restorations.

Original/proposed bytes and raw accounts survive in retained journals. A damaged
journal cannot be guessed back into a valid transaction: intact physical records
remain readable with explicit uncertainty, while unsafe publication is blocked.
Missing or ambiguous data is preserved for deliberate resolution. Ordinary reads
and queries do not rewrite malformed authoritative files.

The index checks every source's current bytes and caches parsing. Its format is
disposable; incompatible or damaged cache data is rebuilt, without migrations of
memory files. Query responses copy their bounded pages rather than retaining an
entire result array or large clipped strings.

Imports operate on temporary fixtures or explicitly configured roots, with one
stable operation per canonical source-root/path pair. Reviewed ranges preserve
exact source bytes; substantive gaps are reported and the full original remains
archived. Repartitioning committed imports uses explicit organization.

Only before/after impose temporal constraints. Narrative order and arbitrary
relationship names do not. Redirect ambiguity stays explicit. Story rendering
uses a depth boundary and a reference-visit budget; extreme virtual offsets report
an actionable range error. No model inference is hidden behind any operation.
