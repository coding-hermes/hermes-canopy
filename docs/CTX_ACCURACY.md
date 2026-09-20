# Context-selection accuracy (GAP-098)

`canopyd context-accuracy` measures whether the context compiler selected the expected live ancestry nodes and topics for a sample of target nodes. It is an operational accuracy harness for the `>90% Context-Selection Accuracy` vision claim; it does not change compiler behavior.

## Formula

For every sampled target, the harness compares the graph-derived golden set with the compiled manifest. `NodeHit` counts expected ancestry node IDs present in `Manifest.Ancestry`; `NodeMiss` counts expected node IDs absent there; `TopicHit` and `TopicMiss` do the same for expected topic IDs and topic manifest references. The reported accuracy is pooled across all entries:

`Accuracy = (NodeHit + TopicHit) / (TotalExpectedNodes + TotalExpectedTopics)`

The denominator is not an average of per-entry ratios. `NodeExtra` counts manifested ancestry IDs outside the golden ancestry set and is reported as precision-loss detail. Per-entry miss records identify missing and extra IDs. Retrieved-tier relevance and card selection are outside this metric.

## Independence

The compiler's ancestry contract is `PGNodeRepo.GetAncestors` in `internal/db/node_repo.go:135-161`: its recursive CTE joins `n.id = chain.parent_id`, so it follows the `nodes.parent_id` chain. The golden expectation follows that same parent-id mechanism through `GraphReader.ParentIDParent`. This is intentional: a parent-id chain is a contract check, not an independent graph-mechanism check.

The separate `GraphReader.EdgeParents` walk reads active `reply`, `fork`, and `synthesis` edges for diagnostics only. `PGNodeRepo.GetSubtree` in `internal/db/node_repo.go:164-198` is the repository method that walks `edges` and handles GAP-073 multi-parent paths. Any node reachable through edges but absent from the parent-id chain is reported as `edgeOnlyAncestors`; it is not counted as a compiler miss because `GetAncestors` does not promise to include it. Topic membership/reference rows are read separately.

## CLI

The command uses the live PostgreSQL database configured by `CANOPY_DB_URL` or the `DB_*` environment variables. It prefers active non-root targets with live parent-id ancestry, round-robins target selection across trees, and fills a short sample with roots only when fewer eligible non-roots exist. It samples 25 targets by default:

    canopyd context-accuracy --sample 25

Human and JSON output include `rootsSampled`, `nonrootSampled`, and `depthScored`; the last is the number of entries whose expected parent-id ancestry has more than one node. `edgeOnlyAncestors` is diagnostic detail, not part of the miss denominator. If no entry exercises multi-node ancestry and no topics are covered, the score carries and the human path prints a warning that it is not meaningful. A `rootFallback` value identifies a sample filled with roots.

A human-readable run prints a line such as `SELECTION_ACCURACY=93.75%` followed by per-entry misses. JSON output is available for automation:

    canopyd context-accuracy --sample 100 --json

Use `--min-accuracy` as a ratio threshold to make a run fail when the measured score is below it:

    canopyd context-accuracy --sample 25 --min-accuracy 0.90

`--sample` must be at least 1. Exit status 0 means a score was measured and met any threshold; status 1 means the database/measurement failed or the threshold was missed; status 2 means command-line usage was invalid.

## Scope and limitations

This harness measures ancestry/topic selection recall plus ancestry precision detail. It does not measure whether the model understood the context, whether retrieved-tier results were relevant, whether card content was useful, token-budget quality, or end-to-end response quality. Multi-reference blocks score only their ancestry contribution; their source/reference content is not treated as topic membership. A sample is a measurement of the selected targets and should not be presented as a statistically representative deployment-wide score without an appropriate sampling plan.
