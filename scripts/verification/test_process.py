import os
import pathlib
import tempfile
import unittest
from unittest.mock import patch

from process import private_process


class ProcessIsolation(unittest.TestCase):
    def test_explicit_private_paths_and_no_ambient_credentials(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            with patch.dict(os.environ, {'CPRA_AUTH_TOKEN': 'not-a-real-secret', 'AWS_PROFILE': 'private'}):
                env = private_process(root, state=root/'state', configuration=root/'config',
                                      credentials=root/'token', logs=root/'logs')
            self.assertNotIn('CPRA_AUTH_TOKEN', env)
            self.assertNotIn('AWS_PROFILE', env)
            self.assertEqual(env['HOME'], str(root/'home'))
            for invalid in (pathlib.Path('relative'), pathlib.Path.home()/'.local/state/cpra'):
                with self.assertRaises(ValueError):
                    private_process(root, state=invalid, configuration=root/'config', credentials=root/'token', logs=root/'logs')

    def test_normal_state_and_symlink_escape_rejected_without_touching_store(self):
        with tempfile.TemporaryDirectory() as directory:
            parent=pathlib.Path(directory)
            normal=parent/'cpra';normal.mkdir(mode=0o700)
            sentinel=normal/'raft.db';sentinel.write_bytes(b'unchanged')
            with patch.dict(os.environ, {'XDG_STATE_HOME':str(parent)}):
                with self.assertRaises(ValueError):
                    private_process(normal,state=normal/'state',configuration=normal/'config',credentials=normal/'token',logs=normal/'logs')
            root=parent/'test';root.mkdir(mode=0o700)
            (root/'redirect').symlink_to(normal,target_is_directory=True)
            with self.assertRaises(ValueError):
                private_process(root,state=root/'redirect'/'db',configuration=root/'config',credentials=root/'token',logs=root/'logs')
            self.assertEqual(sentinel.read_bytes(),b'unchanged')
