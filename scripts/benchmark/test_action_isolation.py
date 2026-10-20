"""Reject false action-fairness passes; these are accounting tests, not a load run."""
from copy import deepcopy
from datetime import datetime, timezone
import unittest
import asyncio
import json
from pathlib import Path
from tempfile import TemporaryDirectory
from unittest.mock import patch

from action_isolation import EXPECTED, finish_cleanup, scenario, validate_actions


def stamp(seconds):
    return datetime.fromtimestamp(seconds, timezone.utc).isoformat()


def fixture(kind):
    budget = 45 if kind == 'notification' else 15
    actions, effects = [], {}
    for identity in sorted(EXPECTED):
        row = dict(id=identity, monitorID=identity, queuedAt=stamp(100),
                   deadline=stamp(100+budget), state='cancelled', held=False)
        if identity.startswith('healthy-') or identity == 'slow-0':
            row.update(startedAt=stamp(110), state='succeeded')
            effects[identity] = [dict(action_id=identity, received_wall=110.1)]
            if identity == 'slow-0':
                row.update(state='unknown', held=True)
                effects[identity][0]['closed_wall'] = min(100+budget, 120)
        actions.append(row)
    return actions, effects


class ActionAccountingTests(unittest.TestCase):
    def test_received_and_definitely_unsent_outcomes_reconcile(self):
        for kind in ('notification', 'intervention'):
            self.assertEqual(set(validate_actions(kind, *fixture(kind))), EXPECTED)

    def test_original_deadline_cannot_be_refreshed_after_queue_wait(self):
        actions, effects = fixture('intervention')
        effects['slow-0'][0]['closed_wall'] = 125
        with self.assertRaisesRegex(RuntimeError, 'original deadline'):
            validate_actions('intervention', actions, effects)

    def test_partial_unobserved_request_keeps_unknown_hold(self):
        actions, effects = fixture('intervention')
        effects.pop('slow-0')
        validate_actions('intervention', actions, effects)
        next(a for a in actions if a['id'] == 'slow-0')['state'] = 'failed'
        with self.assertRaisesRegex(RuntimeError, 'conservative hold'):
            validate_actions('intervention', actions, effects)

    def test_lost_and_duplicate_intents_and_wrong_effects_fail(self):
        actions, effects = fixture('notification')
        for changed in (actions[:-1], actions+[actions[0]]):
            with self.assertRaisesRegex(RuntimeError, 'inventory'):
                validate_actions('notification', changed, effects)
        effects['slow-0'][0]['action_id'] = 'another-action'
        with self.assertRaisesRegex(RuntimeError, 'different action'):
            validate_actions('notification', actions, effects)

    def test_every_effect_requires_unknown_hold_or_confirmed_success(self):
        actions, effects = fixture('notification')
        for field, value in (('state', 'failed'), ('held', False)):
            changed = deepcopy(actions)
            next(a for a in changed if a['id'] == 'slow-0')[field] = value
            with self.assertRaisesRegex(RuntimeError, 'unknown and held'):
                validate_actions('notification', changed, effects)
        effects['slow-0'].append(effects['slow-0'][0])
        with self.assertRaisesRegex(RuntimeError, 'repeated effect'):
            validate_actions('notification', actions, effects)


class CleanupTests(unittest.IsolatedAsyncioTestCase):
    async def test_missing_binary_reports_failure_without_leaking_setup(self):
        with TemporaryDirectory() as directory:
            root = Path(directory)
            self.assertFalse(await scenario(root/'absent', root, 'notification'))
            self.assertEqual(list(root.iterdir()), [root/'notification.json'])
            self.assertFalse(json.loads((root/'notification.json').read_text())['passed'])

    async def test_setup_cancellation_closes_receiver_and_records_failure(self):
        start = asyncio.start_server
        servers = []
        async def intercept(*args, **kwargs):
            if servers:
                raise asyncio.CancelledError
            server = await start(*args, **kwargs)
            servers.append(server)
            return server
        with TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root/'binary'
            binary.write_bytes(b'not executed')
            with patch('action_isolation.asyncio.start_server', intercept):
                with self.assertRaises(asyncio.CancelledError):
                    await scenario(binary, root, 'notification')
            self.assertTrue(servers)
            self.assertFalse(servers[0].is_serving())
            self.assertFalse(any(p.is_dir() for p in root.iterdir()))
            self.assertEqual(json.loads((root/'notification.json').read_text())['failure'], 'cancelled')

    async def test_cancellation_waits_for_cleanup_and_is_reported(self):
        began, release, finished = asyncio.Event(), asyncio.Event(), asyncio.Event()
        async def cleanup():
            began.set()
            await release.wait()
            finished.set()
        task = asyncio.create_task(finish_cleanup(cleanup, 1))
        await began.wait()
        task.cancel()
        await asyncio.sleep(0)
        self.assertFalse(task.done())
        release.set()
        self.assertEqual(await task, (True, None))
        self.assertTrue(finished.is_set())

    async def test_timeout_after_cancellation_still_reports_both(self):
        began = asyncio.Event()
        async def cleanup():
            began.set()
            await asyncio.Event().wait()
        task = asyncio.create_task(finish_cleanup(cleanup, .02))
        await began.wait()
        task.cancel()
        self.assertEqual(await task, (True, 'TimeoutError'))


if __name__ == '__main__':
    unittest.main()
