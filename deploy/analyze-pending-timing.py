#!/usr/bin/env python3
"""Analyze saved debug_pendingTiming pages; never connects to a node."""
import argparse
from collections import Counter, defaultdict
import json
import math
from pathlib import Path


PAIRS = {
    "fetch_grace_us": ("fetch_wait_begin", "fetch_wait_end"),
    "fetch_schedule_us": ("fetch_request_queued", "fetch_request_begin"),
    "fetch_request_call_us": ("fetch_request_begin", "fetch_request_end"),
    "body_decode_us": ("body_received", "body_decoded"),
    "pool_add_us": ("pool_add_begin", "pool_add_end"),
    "legacy_validation_us": ("legacy_add_begin", "legacy_lock_wait"),
    "legacy_mutex_wait_us": ("legacy_lock_wait", "legacy_lock_acquired"),
    "legacy_mutex_hold_us": ("legacy_lock_acquired", "legacy_lock_released"),
    "legacy_promote_schedule_us": ("legacy_promote_begin", "legacy_promote_scheduled"),
    "feed_send_us": ("feed_emit_begin", "feed_emit_end"),
    "subscriber_send_us": ("subscriber_send_begin", "subscriber_send_end"),
    "simulation_queue_us": ("simulation_batch_dequeued", "simulation_started"),
    "simulation_worker_schedule_us": ("simulation_scheduled", "simulation_started"),
    "simulation_us": ("simulation_started", "simulation_finished"),
    "simulation_result_queue_us": ("simulation_finished", "simulation_result_dequeued"),
    "result_to_notify_us": ("simulation_result_dequeued", "notify_begin"),
    "notifier_mutex_wait_us": ("notify_begin", "notify_lock_acquired"),
    "codec_write_us": ("codec_write_begin", "codec_write_end"),
}

INGRESS_PAIRS = {
    "hash_to_fetcher_us": ("hash_received", "fetch_announce"),
    "hash_to_body_us": ("hash_received", "body_received"),
    "fetch_peer_slot_us": ("fetch_wait_end", "fetch_request_queued"),
    "request_to_body_us": ("fetch_request_begin", "body_received"),
    "body_to_pool_begin_us": ("body_received", "pool_add_begin"),
    "local_submit_to_pool_begin_us": ("local_submit", "pool_add_begin"),
}


def differences(events, pairs=PAIRS):
    stages = defaultdict(list)
    for event in events:
        stages[event["stage"]].append(event)
    values, issues = {}, []
    for name, (begin, end) in pairs.items():
        if begin not in stages and end not in stages:
            continue
        values[name] = None
        if len(stages[begin]) != 1 or len(stages[end]) != 1:
            issues.append(name + ":missing_or_ambiguous_boundary")
            continue
        delta = stages[end][0]["monotonic_us"] - stages[begin][0]["monotonic_us"]
        if delta < 0:
            issues.append(name + ":negative_span")
        else:
            values[name] = delta
    return values, issues


def operation_kind(stage):
    for kind, names in {
        "fetch_wait": {"fetch_wait_begin", "fetch_wait_end"},
        "fetch_request": {"fetch_request_queued", "fetch_request_begin", "fetch_request_end"},
        "body": {"body_received", "body_decoded"},
        "admission": {"pool_add_begin", "pool_add_end"},
        "feed": {"feed_emit_begin", "feed_emit_end"},
        "dispatch": {"subscriber_send_begin", "subscriber_send_end"},
    }.items():
        if stage in names:
            return kind
    return "legacy" if stage.startswith("legacy_") else None


def analyze(pages):
    retained, page_loss = {}, Counter()
    for page in pages:
        if "error" in page:
            raise ValueError("RPC returned an error, not an empty trace")
        page = page.get("result", page)
        boot = page["boot_id"]
        page_loss[boot] = max(page_loss[boot], page["lost_since_cursor"])
        for event in page["events"]:
            key = (boot, event["sequence"])
            if key in retained and retained[key] != event:
                raise ValueError("conflicting event for the same boot/sequence")
            retained[key] = event
    targets = defaultdict(list)
    sequences = defaultdict(list)
    for (boot, sequence), event in retained.items():
        sequences[boot].append(sequence)
        targets[(boot, event["hash"])].append(event)
    gaps = {boot: sum(b - a - 1 for a, b in zip(sorted(seq), sorted(seq)[1:])) for boot, seq in sequences.items()}
    records, aggregate = [], defaultdict(list)
    for (boot, hash_value), events in sorted(targets.items()):
        operations, attempts = defaultdict(list), defaultdict(list)
        for event in events:
            if event.get("attempt"):
                attempts[(event["subscription"], event["attempt"])].append(event)
            elif (kind := operation_kind(event["stage"])) is not None:
                operations[(kind, event.get("subscription", ""), event.get("operation", 0))].append(event)
        ingress_metrics, ingress_issues = differences(events, INGRESS_PAIRS)
        bodies = [e for e in events if e["stage"] == "body_received"]
        if "request_to_body_us" in ingress_metrics and (len(bodies) != 1 or bodies[0].get("source") != "request_reply"):
            ingress_metrics["request_to_body_us"] = None
            ingress_issues.append("request_to_body_us:not_a_unique_request_reply")
        if gaps.get(boot) or page_loss[boot]:
            # Cross-operation matching relies on uniqueness. An evicted second
            # request/body/admission could make the retained endpoints look unique.
            for name, value in ingress_metrics.items():
                if value is not None:
                    ingress_metrics[name] = None
                    ingress_issues.append(name + ":retention_gap_prevents_unique_match")
        row = {"boot_id": boot, "hash": hash_value, "operations": [], "attempts": [],
               "ingress_metrics_us": ingress_metrics, "ingress_issues": ingress_issues,
               "observed_stages": dict(Counter(e["stage"] for e in events)),
               "unattempted_outcomes": [{"stage": e["stage"], "subscription": e.get("subscription"),
                                         "operation": e.get("operation"), "outcome": e.get("outcome", "")}
                                        for e in events if e["stage"] in ("simulation_skipped", "queued_cancelled")],
               "ingress_sources": sorted({e["source"] for e in events if e.get("source") and e["stage"] in ("body_received", "local_submit")})}
        for (kind, subscription, operation), group in sorted(operations.items()):
            metrics, issues = differences(group)
            row["operations"].append({"kind": kind, "subscription": subscription, "operation": operation,
                                      "metrics_us": metrics, "issues": issues,
                                      "outcomes": dict(Counter(e["outcome"] for e in group if e.get("outcome")))})
        ingress = [e for e in events if e["stage"] in ("body_received", "local_submit")]
        first_seen = [e for e in events if e["stage"] == "tracker_first_seen"]
        for (subscription, attempt), group in sorted(attempts.items()):
            metrics, issues = differences(group)
            batch = [e for e in group if e["stage"] == "simulation_batch_dequeued"]
            sends = [e for e in events if e["stage"] == "subscriber_send_begin" and e.get("subscription") == subscription]
            metrics["subscriber_pre_batch_us"] = None
            # Do not guess across repeated feeds, missing ingress, evictions or
            # distinct subscriptions. The raw operation IDs remain available.
            if len(batch) == len(sends) == len(first_seen) == 1 and ingress and not gaps.get(boot) and not page_loss[boot]:
                delta = batch[0]["monotonic_us"] - sends[0]["monotonic_us"]
                if delta >= 0:
                    metrics["subscriber_pre_batch_us"] = delta
            if metrics["subscriber_pre_batch_us"] is None:
                issues.append("subscriber_pre_batch_us:missing_or_ambiguous_boundary")
            row["attempts"].append({"subscription": subscription, "attempt": attempt,
                                    "rpc_subscription": group[0].get("rpc_subscription"),
                                    "metrics_us": metrics, "issues": issues,
                                    "terminal": [e["stage"] + ":" + e.get("outcome", "") for e in group
                                                 if e["stage"] in ("codec_write_end", "attempt_cancelled")
                                                 or (e["stage"] == "simulation_result_dequeued" and e.get("outcome") != "ok")]})
        for item in row["operations"] + row["attempts"] + [{"metrics_us": ingress_metrics}]:
            for name, value in item["metrics_us"].items():
                if value is not None:
                    aggregate[name].append(value)
        records.append(row)
    summary = {}
    for name, values in sorted(aggregate.items()):
        values.sort()
        summary[name] = {"n": len(values), "p50": values[math.ceil(len(values) * .5) - 1],
                         "p95": values[math.ceil(len(values) * .95) - 1], "max": values[-1]}
    return {"unique_events": len(retained), "sampled_hashes": len(targets),
            "reported_cursor_loss_max_by_boot": dict(page_loss), "internal_sequence_gaps_by_boot": gaps,
            "spans_are_overlapping_not_additive": True, "summary_us": summary, "records": records}


def load_pages(path):
    text = Path(path).read_text(encoding="utf-8-sig")
    try:
        value = json.loads(text)
        return value if isinstance(value, list) else [value]
    except json.JSONDecodeError:
        return [json.loads(line) for line in text.splitlines() if line.strip()]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("input", help="saved JSON or JSONL RPC pages")
    parser.add_argument("--output", help="optional report JSON path")
    args = parser.parse_args()
    report = json.dumps(analyze(load_pages(args.input)), indent=2)
    if args.output:
        Path(args.output).write_text(report + "\n", encoding="utf-8")
    else:
        print(report)


if __name__ == "__main__":
    main()
