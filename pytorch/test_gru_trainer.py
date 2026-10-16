import unittest
import json
from types import SimpleNamespace
from unittest.mock import patch
from datetime import datetime, timedelta, timezone

from gru_trainer import temporal_partitions, training_segment, prepare_history


class TemporalPartitionTests(unittest.TestCase):
    def test_holdout_extremes_cannot_change_normalization(self):
        start = datetime(2026, 10, 1, tzinfo=timezone.utc)
        samples = [(start + timedelta(minutes=i), float(i % 100)) for i in range(2000)]
        train_end, calibration_end, minimum, scale = temporal_partitions(samples, 12)
        modified = list(samples)
        for i in range(train_end + 13, len(samples)):
            modified[i] = (modified[i][0], 1e12)
        self.assertEqual(temporal_partitions(modified, 12)[2:], (minimum, scale))
        self.assertLess(train_end + 12, calibration_end)
        self.assertLess(calibration_end + 12, len(samples) - 1)

    def test_short_history_rejected(self):
        with self.assertRaises(RuntimeError):
            temporal_partitions([(None, 1.0)] * 100, 12)

    def test_training_never_joins_outage(self):
        start = datetime(2026, 10, 1, tzinfo=timezone.utc)
        samples = [(start + timedelta(minutes=i), 10.0) for i in range(100)]
        samples += [(start + timedelta(minutes=200+i), 20.0) for i in range(10)]
        self.assertEqual(len(training_segment(samples, 60, 60)), 100)
        with self.assertRaises(RuntimeError):
            training_segment(samples, 60, 110)

    @patch("gru_trainer.subprocess.run")
    def test_gru_uses_shared_repair_helper(self, run):
        end = datetime(2026, 10, 16, tzinfo=timezone.utc)
        run.return_value = SimpleNamespace(returncode=0, stdout=json.dumps({"missing_before": 3, "missing_after": 0}), stderr="")
        self.assertEqual(prepare_history(end)["missing_before"], 3)
        args, options = run.call_args
        self.assertEqual(args[0], ["/replicasense-history"])
        self.assertEqual(options["env"]["REPLICASENSE_HISTORY_END"], end.isoformat())
        self.assertFalse(options.get("shell", False))

    @patch("gru_trainer.subprocess.run")
    def test_gru_stops_when_source_cannot_repair_history(self, run):
        run.return_value = SimpleNamespace(returncode=1, stdout="{}", stderr="training history incomplete")
        with self.assertRaisesRegex(RuntimeError, "training history incomplete"):
            prepare_history(datetime(2026, 10, 16, tzinfo=timezone.utc))


if __name__ == "__main__":
    unittest.main()
