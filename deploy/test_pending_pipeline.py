import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("pending_timing", Path(__file__).with_name("analyze-pending-timing.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


def event(sequence, stage, end, duration=0, **fields):
    return dict(sequence=sequence, stage=stage, unix_us=end, monotonic_us=end,
                duration_us=duration, hash="0x" + "00"*32, **fields)


def report(events, lost=0):
    return module.analyze([dict(boot_id="boot", lost_since_cursor=lost, events=events)])


class PipelineTest(unittest.TestCase):
    def test_reader_holds_are_a_union_not_a_sum(self):
        events = [event(1, "pool_lock_hold", 70, 60, scope="lock", pool=1, operation=1, source="Pending", outcome="reader"),
                  event(2, "pool_lock_hold", 90, 60, scope="lock", pool=1, operation=2, source="ContentFrom", outcome="reader"),
                  event(3, "pool_lock_wait", 100, 100, scope="lock", pool=1, operation=3, source="Add")]
        result = report(events)
        self.assertEqual(result["sampled_hashes"], 0)
        wait = result["pipeline"]["lock_waits"][0]
        self.assertEqual(wait["retained_owner_union_us"], 80)
        self.assertEqual(wait["unattributed_us"], 20)
        self.assertEqual(sum(x["overlap_us"] for x in wait["retained_owners"]), 120)

    def test_missing_owner_and_capture_gap_remain_unknown(self):
        result = report([event(2, "pool_lock_wait", 100, 100, scope="lock", pool=1, operation=3, source="Add")], lost=1)
        wait = result["pipeline"]["lock_waits"][0]
        self.assertEqual(wait["unattributed_us"], 100)
        self.assertTrue(wait["retention_gap_prevents_complete_attribution"])
        self.assertIsNone(result["pipeline"]["remote_propagation_us"])

    def test_another_reader_is_not_assumed_to_block_a_reader(self):
        result = report([event(1, "pool_lock_hold", 100, 100, scope="lock", pool=1, operation=1, source="Pending", outcome="reader"),
                         event(2, "pool_lock_wait", 100, 100, scope="lock", pool=1, operation=2, source="Stats", outcome="reader")])
        wait = result["pipeline"]["lock_waits"][0]
        self.assertEqual(wait["retained_owners"], [])
        self.assertEqual(wait["unattributed_us"], 100)

    def test_packet_and_pool_identities_do_not_cross(self):
        events = [event(1, "pool_lock_hold", 100, 100, scope="lock", pool=2, operation=1, source="runReorg", outcome="writer"),
                  event(2, "pool_lock_wait", 100, 100, scope="lock", pool=1, operation=2, source="Add", packet=20),
                  event(3, "fetch_rejection_backoff", 200000, 200000, scope="packet", packet=20, connection=1, operation=7, rejected=33),
                  event(4, "p2p_handoff_begin", 10, scope="packet", packet=21, connection=2, operation=21),
                  event(5, "p2p_handoff_end", 40, scope="packet", packet=21, connection=2, operation=21, outcome="closed")]
        pipeline = report(events)["pipeline"]
        self.assertEqual(pipeline["lock_waits"][0]["retained_owners"], [])
        packets = {p["packet"]: p for p in pipeline["packets"]}
        self.assertNotIn("protocol_handoff_us", packets[20]["metrics_us"])
        self.assertEqual(packets[21]["metrics_us"]["protocol_handoff_us"], 30)
        self.assertEqual(pipeline["summary_us"]["fetch_rejection_backoff"]["n"], 1)
        self.assertNotIn("fetch_grace_us", pipeline["summary_us"])

    def test_repeated_hash_links_use_packet_identity_and_gap_is_unknown(self):
        h = "0x" + "00"*31 + "01"
        events = []
        for packet, begin in [(1, 10), (2, 100)]:
            events.extend([event(len(events)+1, "hash_received", begin, packet=packet, operation=packet),
                           event(len(events)+2, "fetch_announce", begin+5, packet=packet)])
        for e in events:
            e["hash"] = h
        result = report(events)
        self.assertEqual([p["hash_ingress"][0]["metrics_us"]["hash_to_fetcher_us"] for p in result["pipeline"]["packets"]], [5, 5])
        result = report(events, lost=1)
        self.assertTrue(all(p["hash_ingress"][0]["metrics_us"]["hash_to_fetcher_us"] is None for p in result["pipeline"]["packets"]))


if __name__ == "__main__":
    unittest.main()
