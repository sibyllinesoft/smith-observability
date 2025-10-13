CREATE DATABASE IF NOT EXISTS otel;

USE otel;

CREATE TABLE IF NOT EXISTS otel_traces (
    Timestamp DateTime64(9) CODEC(Delta, ZSTD(1)),
    TraceId String CODEC(ZSTD(1)),
    SpanId String CODEC(ZSTD(1)),
    ParentSpanId String CODEC(ZSTD(1)),
    TraceState String CODEC(ZSTD(1)),
    SpanName LowCardinality(String) CODEC(ZSTD(1)),
    SpanKind LowCardinality(String) CODEC(ZSTD(1)),
    ServiceName LowCardinality(String) CODEC(ZSTD(1)),
    ResourceAttributes Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    ScopeName String CODEC(ZSTD(1)),
    ScopeVersion String CODEC(ZSTD(1)),
    SpanAttributes Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    Duration UInt64 CODEC(ZSTD(1)),
    StatusCode LowCardinality(String) CODEC(ZSTD(1)),
    StatusMessage String CODEC(ZSTD(1)),
    Events Nested (
        Timestamp DateTime64(9),
        Name LowCardinality(String),
        Attributes Map(LowCardinality(String), String)
    ) CODEC(ZSTD(1)),
    Links Nested (
        TraceId String,
        SpanId String,
        TraceState String,
        Attributes Map(LowCardinality(String), String)
    ) CODEC(ZSTD(1)),
    INDEX idx_trace_id TraceId TYPE bloom_filter(0.001) GRANULARITY 1,
    INDEX idx_res_attr_key mapKeys(ResourceAttributes) TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX idx_res_attr_value mapValues(ResourceAttributes) TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX idx_span_attr_key mapKeys(SpanAttributes) TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX idx_span_attr_value mapValues(SpanAttributes) TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX idx_duration Duration TYPE minmax GRANULARITY 1
) ENGINE = MergeTree()
PARTITION BY toDate(Timestamp)
ORDER BY (ServiceName, SpanName, toDateTime(Timestamp))
TTL toDateTime(Timestamp) + toIntervalDay(14)
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS otel_traces_trace_id_ts (
    TraceId String CODEC(ZSTD(1)),
    Start DateTime CODEC(Delta, ZSTD(1)),
    End DateTime CODEC(Delta, ZSTD(1)),
    INDEX idx_trace_id TraceId TYPE bloom_filter(0.01) GRANULARITY 1
) ENGINE = MergeTree()
PARTITION BY toDate(Start)
ORDER BY (TraceId, Start)
TTL toDateTime(End) + toIntervalDay(14)
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

CREATE MATERIALIZED VIEW IF NOT EXISTS otel_traces_trace_id_ts_mv
TO otel_traces_trace_id_ts
AS
SELECT
    TraceId,
    min(Timestamp) AS Start,
    max(Timestamp) AS End
FROM otel_traces
WHERE TraceId != ''
GROUP BY TraceId;

CREATE OR REPLACE VIEW agent_runs AS
SELECT
    TraceId AS trace_id,
    any(ServiceName) AS service_name,
    min(Timestamp) AS start_time,
    max(Timestamp) AS end_time,
    greatest(
        round((toUnixTimestamp64Nano(max(Timestamp)) - toUnixTimestamp64Nano(min(Timestamp))) / 1e9, 6),
        0
    ) AS duration_seconds,
    argMax(ResourceAttributes['smith.git.root'], Timestamp) AS git_root,
    argMax(ResourceAttributes['smith.git.branch'], Timestamp) AS git_branch,
    argMax(ResourceAttributes['smith.git.remote'], Timestamp) AS git_remote,
    count() AS span_count,
    countIf(SpanKind = 'SPAN_KIND_INTERNAL') AS internal_span_count,
    countIf(SpanKind = 'SPAN_KIND_CLIENT') AS client_span_count,
    coalesce(
        nullIf(argMax(ResourceAttributes['smith.agent.name'], Timestamp), ''),
        nullIf(argMax(SpanAttributes['smith.agent.name'], Timestamp), ''),
        nullIf(replaceRegexpAll(any(ServiceName), '^smith-agent-', ''), ''),
        ''
    ) AS agent_name,
    coalesce(
        toInt64OrNull(argMax(SpanAttributes['llm.usage.prompt_tokens'], Timestamp)),
        toInt64OrNull(argMax(SpanAttributes['llm.usage.input_tokens'], Timestamp)),
        toInt64OrNull(argMax(SpanAttributes['gen_ai.usage.prompt_tokens'], Timestamp)),
        toInt64OrNull(argMax(SpanAttributes['gen_ai.usage.input_tokens'], Timestamp)),
        toInt64OrNull(argMax(SpanAttributes['bifrost.usage.prompt_tokens'], Timestamp)),
        toInt64OrNull(argMax(SpanAttributes['usage.prompt_tokens'], Timestamp)),
        toInt64OrNull(argMax(SpanAttributes['usage.input_tokens'], Timestamp)),
        0
    ) AS prompt_tokens,
    coalesce(
        toInt64OrNull(argMax(SpanAttributes['llm.usage.completion_tokens'], Timestamp)),
        toInt64OrNull(argMax(SpanAttributes['llm.usage.output_tokens'], Timestamp)),
        toInt64OrNull(argMax(SpanAttributes['gen_ai.usage.completion_tokens'], Timestamp)),
        toInt64OrNull(argMax(SpanAttributes['gen_ai.usage.output_tokens'], Timestamp)),
        toInt64OrNull(argMax(SpanAttributes['bifrost.usage.completion_tokens'], Timestamp)),
        toInt64OrNull(argMax(SpanAttributes['usage.completion_tokens'], Timestamp)),
        toInt64OrNull(argMax(SpanAttributes['usage.output_tokens'], Timestamp)),
        0
    ) AS completion_tokens,
    coalesce(
        toInt64OrNull(argMax(SpanAttributes['llm.usage.total_tokens'], Timestamp)),
        toInt64OrNull(argMax(SpanAttributes['gen_ai.usage.total_tokens'], Timestamp)),
        toInt64OrNull(argMax(SpanAttributes['bifrost.usage.total_tokens'], Timestamp)),
        toInt64OrNull(argMax(SpanAttributes['usage.total_tokens'], Timestamp)),
        toInt64OrNull(argMax(SpanAttributes['usage.token_count'], Timestamp)),
        coalesce(
            toInt64OrNull(argMax(SpanAttributes['llm.usage.prompt_tokens'], Timestamp)),
            toInt64OrNull(argMax(SpanAttributes['bifrost.usage.prompt_tokens'], Timestamp)),
            toInt64OrNull(argMax(SpanAttributes['usage.prompt_tokens'], Timestamp)),
            toInt64OrNull(argMax(SpanAttributes['usage.input_tokens'], Timestamp)),
            0
        ) + coalesce(
            toInt64OrNull(argMax(SpanAttributes['llm.usage.completion_tokens'], Timestamp)),
            toInt64OrNull(argMax(SpanAttributes['bifrost.usage.completion_tokens'], Timestamp)),
            toInt64OrNull(argMax(SpanAttributes['usage.completion_tokens'], Timestamp)),
            toInt64OrNull(argMax(SpanAttributes['usage.output_tokens'], Timestamp)),
            0
        )
    ) AS total_tokens
FROM otel_traces
WHERE TraceId != ''
GROUP BY TraceId;

CREATE OR REPLACE VIEW session_timeline AS
SELECT
    git_root,
    git_branch,
    git_remote,
    bucket_start,
    count() AS run_count,
    sum(duration_seconds) AS total_run_seconds,
    min(start_time) AS first_run_started_at,
    max(end_time) AS last_run_finished_at,
    groupArrayDistinct(agent_name) AS agents,
    sum(prompt_tokens) AS total_prompt_tokens,
    sum(completion_tokens) AS total_completion_tokens,
    sum(total_tokens) AS total_tokens
FROM
(
    SELECT
        trace_id,
        git_root,
        git_branch,
        git_remote,
        toStartOfInterval(start_time, INTERVAL 1 HOUR) AS bucket_start,
        start_time,
        end_time,
        duration_seconds,
        agent_name,
        prompt_tokens,
        completion_tokens,
        total_tokens
    FROM agent_runs
) runs
GROUP BY
    git_root,
    git_branch,
    git_remote,
    bucket_start;
