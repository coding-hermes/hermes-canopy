# Context-selection accuracy (GAP-098)

`canopyd context-accuracy` measures whether the context compiler selected the expected live ancestry nodes and topics for a sample of target nodes. It is an operational accuracy harness for the `>90% Context-Selection Accuracy` vision claim; it does not change compiler behavior.

## Formula

For every sampled target, the harness compares the graph-derived golden set with the compiled manifest. `NodeHit` counts expected ancestry node IDs present in `Manifest.Ancestry`; `NodeMiss` counts expected node IDs absent there; `TopicHit` and `TopicMiss` do the same for expected topic IDs and topic manifest references. The reported accuracy is pooled across all entries:

`Accuracy = (NodeHit + TopicHit) / (TotalExpectedNodes + TotalExpectedTopics)`

The denominator is not an average of per-entry ratios. `NodeExtra` counts manifested ancestry IDs outside the golden ancestry set and is reported as precision-loss detail. Per-entry miss records identify missing and extra IDs. Retrieved-tier relevance and card selection are outside this metric.

## Independence

The golden set walks active incoming `reply`, `fork`, and `synthesis` edges directly with a breadth-first traversal and reads active topic membership/reference rows separately. The compiler's normal ancestry read path follows `nodes.parent_id` through `NodeReader.GetAncestors`; the harness never calls that method, so it can expose disagreement between the graph and the compiler's selected ancestry instead of comparing a manifest with its own source.

## CLI

The command uses the live PostgreSQL database configured by `CANOPY_DB_URL` or the `DB_*` environment variables. It samples 25 targets by default:

    canopyd context-accuracy --sample 25

A human-readable run prints a line such as `SELECTION_ACCURACY=93.75%` followed by per-entry misses. JSON output is available for automation:

    canopyd context-accuracy --sample 100 --json

Use `--min-accuracy` as a ratio threshold to make a run fail when the measured score is below it:

    canopyd context-accuracy --sample 25 --min-accuracy 0.90

`--sample` must be at least 1. Exit status 0 means a score was measured and met any threshold; status 1 means the database/measurement failed or the threshold was missed; status 2 means command-line usage was invalid.

## Scope and limitations

This harness measures ancestry/topic selection recall plus ancestry precision detail. It does not measure whether the model understood the context, whether retrieved-tier results were relevant, whether card content was useful, token-budget quality, or end-to-end response quality. Multi-reference blocks score only their ancestry contribution; their source/reference content is not treated as topic membership. A sample is a measurement of the selected targets and should not be presented as a statistically representative deployment-wide score without an appropriate sampling plan.
