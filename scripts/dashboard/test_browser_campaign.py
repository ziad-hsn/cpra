import unittest

import browser_campaign


class BrowserCampaignTests(unittest.TestCase):
    def results(self):
        return [{"Package": package, "Test": name, "Action": "pass"}
                for package, names in browser_campaign.TESTS.items() for name in names]

    def test_requires_every_test_to_pass(self):
        browser_campaign.verify_results(self.results())
        for action in ("skip", "fail"):
            with self.subTest(action=action):
                results = self.results()
                results[0]["Action"] = action
                with self.assertRaisesRegex(ValueError, "did not pass"):
                    browser_campaign.verify_results(results)
        with self.assertRaises(ValueError):
            browser_campaign.verify_results([])


if __name__ == "__main__":
    unittest.main()
