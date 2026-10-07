# k6's summary export into the platform's one report shape (BE-4.9, BE-9.1).
#
# Two things come out of a load run and both matter. A threshold is the closest thing a
# load script has to an assertion — the stated limit, crossed or not — so each becomes a
# result and the run reads like a functional one. And the metrics are what a load test
# is actually for: latency percentiles and throughput, which no functional run produces.
# A load test reported as an average is a load test that hid its tail, so the
# percentiles are carried explicitly.
def num($v): if $v == null then 0 else $v end;

{
  schema: "qavia.run/1",
  framework: "k6",
  results: [
    (.metrics // {}) | to_entries[] as $metric
    | ($metric.value.thresholds // {}) | to_entries[]
    # A threshold entry is either a bare boolean or an object with `ok`. In the
    # boolean form, k6 reports `false` for a threshold that held; in the object form it
    # is `ok: true`. `held` normalises both so the platform's pass means "the limit
    # held" whatever k6 version wrote the summary.
    | (.value | if type == "object" then (.ok // false) else (. == false) end) as $held
    | {
        name: ($metric.key + " " + .key),
        file: $file,
        status: (if $held then "passed" else "failed" end),
        durationMs: 0,
        failureMessage: (if $held then null else ("threshold " + .key + " was crossed") end)
      }
  ],

  metrics: {
    requests:    (num(.metrics.http_reqs.count) | floor),
    throughput:  num(.metrics.http_reqs.rate),
    errorRate:   num(.metrics.http_req_failed.value),

    latencyAvgMs: num(.metrics.http_req_duration.avg),
    latencyP50Ms: num(.metrics.http_req_duration.med),
    latencyP90Ms: num(.metrics.http_req_duration["p(90)"]),
    latencyP95Ms: num(.metrics.http_req_duration["p(95)"]),
    latencyP99Ms: num(.metrics.http_req_duration["p(99)"]),
    latencyMaxMs: num(.metrics.http_req_duration.max),

    # vus_max is in the summary; the run duration is not reliably, so the platform
    # fills it from the load profile it generated the script from.
    virtualUsers: (num(.metrics.vus_max.value) | floor),
    durationMs:   0
  }
}
