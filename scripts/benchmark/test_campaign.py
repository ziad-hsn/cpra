import json
import pathlib
import tempfile
import unittest
from unittest.mock import patch

import campaign
import preflight


class CampaignContracts(unittest.TestCase):
    def test_missing_physical_host_evidence_blocks_campaign(self):
        with patch.object(pathlib.Path, 'exists', return_value=False):
            result = preflight.inspect()
        self.assertFalse(result['ready'])
        self.assertGreaterEqual(result['required_host_free_bytes'], 30 * 1000 ** 3)

    def test_manifest_has_distinct_ids_and_fixed_cadence(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'monitors.json'
            campaign.manifest(path, 103, 'http://127.0.0.1:12345', 7)
            monitors = json.loads(path.read_text())['monitors']
        self.assertEqual(103, len(monitors))
        self.assertEqual(103, len({m['id'] for m in monitors}))
        self.assertEqual(103, len({m['pulse_check']['config']['url'] for m in monitors}))
        self.assertTrue(all(m['pulse_check']['interval'] == '60s' for m in monitors))
        self.assertEqual(100, sum('intervention' in m for m in monitors))


if __name__ == '__main__':
    unittest.main()
