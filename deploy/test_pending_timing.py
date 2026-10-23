import importlib.util
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location("pending_timing", Path(__file__).with_name("analyze-pending-timing.py"))
timing = importlib.util.module_from_spec(spec)
spec.loader.exec_module(timing)


class PendingTimingTests(unittest.TestCase):
    def page(self, events, boot="boot-1", lost=0):
        return {"boot_id": boot, "lost_since_cursor": lost, "events": events}

    def event(self, seq, stage, at, operation=1, attempt=0, sub=""):
        return {"sequence": seq, "hash": "target", "stage": stage, "monotonic_us": at,
                "unix_us": 1000000 + at, "operation": operation, "attempt": attempt, "subscription": sub}

    def test_separates_grace_mutex_and_pre_batch_from_simulation(self):
        stages = [
            ("fetch_wait_begin", 0, 1, 0, ""), ("fetch_wait_end", 200000, 1, 0, ""),
            ("body_received", 210000, 2, 0, ""), ("body_decoded", 211000, 2, 0, ""),
            ("legacy_lock_wait", 212000, 3, 0, ""), ("legacy_lock_acquired", 312000, 3, 0, ""),
            ("legacy_lock_released", 314000, 3, 0, ""), ("tracker_first_seen", 315000, 0, 0, ""),
            ("subscriber_send_begin", 316000, 4, 0, "sub-1"), ("subscriber_send_end", 316001, 4, 0, "sub-1"),
            ("simulation_batch_dequeued", 366000, 5, 6, "sub-1"),
            ("simulation_started", 376000, 5, 6, "sub-1"), ("simulation_finished", 379000, 5, 6, "sub-1"),
        ]
        events = [self.event(i + 1, *stage) for i, stage in enumerate(stages)]
        # Recording after an unlock/Send is intentionally not timestamp order.
        report = timing.analyze([self.page(list(reversed(events)))])
        summary = report["summary_us"]
        self.assertEqual(summary["fetch_grace_us"]["p50"], 200000)
        self.assertEqual(summary["legacy_mutex_wait_us"]["p50"], 100000)
        self.assertEqual(summary["subscriber_pre_batch_us"]["p50"], 50000)
        self.assertEqual(summary["simulation_queue_us"]["p50"], 10000)
        self.assertEqual(summary["simulation_us"]["p50"], 3000)
        self.assertTrue(report["spans_are_overlapping_not_additive"])

    def test_missing_negative_and_lost_boundaries_are_not_zero(self):
        events = [self.event(1, "fetch_wait_begin", 500), self.event(2, "fetch_wait_end", 400),
                  self.event(4, "simulation_started", 600, 2, 3, "sub")]
        report = timing.analyze([self.page(events, lost=12)])
        self.assertNotIn("fetch_grace_us", report["summary_us"])
        self.assertEqual(report["internal_sequence_gaps_by_boot"]["boot-1"], 1)
        self.assertIsNone(report["records"][0]["attempts"][0]["metrics_us"]["simulation_queue_us"])
        self.assertEqual(report["reported_cursor_loss_max_by_boot"]["boot-1"], 12)

    def test_boots_subscribers_and_retries_do_not_merge(self):
        first = [self.event(1, "simulation_started", 1, attempt=1, sub="sub-a"),
                 self.event(2, "simulation_finished", 6, attempt=1, sub="sub-a"),
                 self.event(3, "simulation_started", 10, attempt=2, sub="sub-a"),
                 self.event(4, "simulation_finished", 18, attempt=2, sub="sub-a"),
                 self.event(5, "simulation_started", 20, attempt=3, sub="sub-b"),
                 self.event(6, "simulation_finished", 29, attempt=3, sub="sub-b")]
        pages = [self.page(first), self.page(first), self.page(first[:2], boot="boot-2")]
        report = timing.analyze(pages)
        self.assertEqual(report["unique_events"], 8)
        self.assertEqual([len(row["attempts"]) for row in report["records"]], [3, 1])
        self.assertEqual(report["summary_us"]["simulation_us"]["n"], 4)

    def test_rpc_errors_and_conflicting_duplicates_fail(self):
        with self.assertRaises(ValueError):
            timing.analyze([{"error": {"code": -32601}}])
        a = self.event(1, "simulation_started", 1)
        b = dict(a, monotonic_us=99)
        with self.assertRaises(ValueError):
            timing.analyze([self.page([a]), self.page([b])])

    def test_broadcast_is_not_mislabelled_as_request_roundtrip(self):
        request = self.event(1, "fetch_request_begin", 10)
        body = dict(self.event(2, "body_received", 40010, operation=2), source="request_reply")
        report = timing.analyze([self.page([request, body])])
        self.assertEqual(report["summary_us"]["request_to_body_us"]["p50"], 40000)
        self.assertEqual(report["records"][0]["ingress_sources"], ["request_reply"])
        broadcast = dict(body, source="broadcast")
        report = timing.analyze([self.page([request, broadcast])])
        self.assertNotIn("request_to_body_us", report["summary_us"])
        self.assertIsNone(report["records"][0]["ingress_metrics_us"]["request_to_body_us"])

    def test_successful_simulation_is_not_a_terminal_write(self):
        result = dict(self.event(1, "simulation_result_dequeued", 5, attempt=2, sub="sub"), outcome="ok")
        skipped = dict(self.event(2, "simulation_skipped", 6, sub="sub"), outcome="seen_or_inflight")
        cancelled = self.event(3, "queued_cancelled", 7, sub="sub")
        report = timing.analyze([self.page([result, skipped, cancelled])])
        row = report["records"][0]
        self.assertEqual(row["attempts"][0]["terminal"], [])
        self.assertEqual(len(row["unattempted_outcomes"]), 2)
        self.assertEqual(row["observed_stages"]["queued_cancelled"], 1)


if __name__ == "__main__":
    unittest.main()
