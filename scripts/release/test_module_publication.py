"""Tag/public-download contracts; loopback fixtures are not public-release proof."""
from contextlib import contextmanager
import http.server
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import threading
import unittest
import urllib.error
from unittest.mock import patch

import module_publication as modules
import publish
import qualification
import release
import test_sdk_evidence as sdk_fixtures


class GitHubFixture:
    def __init__(self, commit, *, git=None):
        self.commit, self.git = commit, git
        self.refs, self.requests, self.failure, self.lost = {}, [], None, False
        owner = self
        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def respond(self, code, value):
                data = value if isinstance(value, bytes) else release.encoded(value)
                self.send_response(code)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def do_GET(self):
                owner.requests.append(('GET', self.path))
                if owner.failure:
                    self.respond(*owner.failure)
                elif self.path == '/repos/ziad-hsn/cpra':
                    self.respond(200, {'full_name': 'ziad-hsn/cpra', 'private': False, 'permissions': {'push': True}})
                elif self.path == '/repos/ziad-hsn/cpra/git/commits/' + owner.commit:
                    self.respond(200, {'sha': owner.commit})
                elif '/git/ref/tags/' in self.path:
                    tag = self.path.split('/git/ref/tags/', 1)[1]
                    current = owner.lookup(tag)
                    self.respond(200, owner.reference(tag, current)) if current else self.respond(404, {'message': 'Not Found'})
                else:
                    self.respond(404, {'message': 'Not Found'})

            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
                owner.requests.append(('POST', self.path, body))
                tag = body['ref'].removeprefix('refs/tags/')
                if owner.failure:
                    self.respond(*owner.failure)
                    return
                if owner.lookup(tag):
                    self.respond(422, {'message': 'Reference already exists'})
                    return
                owner.store(tag, body['sha'])
                if owner.lost:
                    self.close_connection = True
                    return
                self.respond(201, owner.reference(tag, body['sha']))

        self.server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        self.server.daemon_threads = True
        self.origin = 'http://127.0.0.1:' + str(self.server.server_port)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    @staticmethod
    def reference(tag, commit):
        return {'ref': 'refs/tags/' + tag, 'object': {'type': 'commit', 'sha': commit}}

    def lookup(self, tag):
        if self.git is None:
            return self.refs.get(tag)
        result = subprocess.run(['git', 'show-ref', '--verify', '--hash', 'refs/tags/' + tag],
                                cwd=self.git, capture_output=True, text=True, timeout=5)
        return result.stdout.strip() if result.returncode == 0 else None

    def store(self, tag, commit):
        if self.git is None:
            self.refs[tag] = commit
        else:
            subprocess.run(['git', 'update-ref', 'refs/tags/' + tag, commit, '0' * 40],
                           cwd=self.git, capture_output=True, check=True, timeout=5)

    def client(self):
        return modules.GitHub(origin=self.origin, allow_loopback_http=True, timeout=2)

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)


class ModulePublicationTests(unittest.TestCase):
    def setUp(self):
        self.sdk = sdk_fixtures.SDKEvidenceTests('test_consistent_bytes_and_both_compilers_remain_pending_review')
        self.sdk.setUp()
        self.addCleanup(self.sdk.doCleanups)
        self.root, self.manifest = self.sdk.root, self.sdk.manifest
        self.work = self.root / 'work'
        self.work.mkdir(mode=0o700)
        self.progress = self.root / 'publication.jsonl'
        self.remote = GitHubFixture(self.manifest['commit'])
        self.addCleanup(self.remote.close)
        self.downloads = []
        self.failure = None
        self.graph = publication_evidence_graph(self.sdk)

    @contextmanager
    def reader(self, module, version, work):
        self.downloads.append(module)
        if self.failure:
            raise ValueError(self.failure)
        base = self.sdk.cache / 'modules/cache/download' / module / '@v'
        row = self.graph[module]
        yield {'Path': module, 'Version': version, 'Sum': row['sum'], 'GoModSum': row['go_mod_sum']}, {
            field: base / (version + suffix) for field, suffix in (('Zip', '.zip'), ('GoMod', '.mod'), ('Info', '.info'))}

    def run_publication(self, *, remote=None):
        return modules.publish_modules(self.manifest, self.sdk.cache, remote or self.remote.client(),
                                       self.reader, self.progress, self.work)

    def mutations(self):
        return [row[2]['ref'] for row in self.remote.requests if row[0] == 'POST']

    def test_sdk_download_precedes_worker_and_application_tags(self):
        original = self.remote.store
        observations = []
        def store(tag, commit):
            observations.append((tag, list(self.downloads)))
            original(tag, commit)
        self.remote.store = store
        result = self.run_publication()
        self.assertEqual(observations, [('sdk/go/v0.2.0-rc.1', []),
                                       ('sdk/go/worker/v0.3.0-rc.1', [modules.CORE]),
                                       ('v0.1.0-rc.1', [modules.CORE, modules.WORKER])])
        self.assertEqual(len(result['module_readbacks']), 2)
        self.assertFalse(result['release_gate_passed'])
        self.assertEqual(self.progress.stat().st_mode & 0o777, 0o600)

    def test_completed_resume_rechecks_every_tag_and_both_modules_without_mutations(self):
        self.run_publication()
        first = list(self.mutations())
        self.downloads.clear()
        result = self.run_publication()
        self.assertEqual(self.mutations(), first)
        self.assertEqual(self.downloads, [modules.CORE, modules.WORKER])
        self.assertEqual(len(result['reused_tags']), 3)
        self.assertEqual(result['created_tags'], [])

    def test_reordered_or_duplicated_progress_cannot_authorize_an_existing_application(self):
        self.run_publication()
        data = self.progress.read_bytes().splitlines(keepends=True)
        for rows in ([data[0], *data[-2:], *data[1:-2]], [data[0], data[1], data[1], *data[2:]]):
            self.progress.write_bytes(b''.join(rows))
            before = len(self.remote.requests)
            with self.assertRaisesRegex(ValueError, 'out of order or duplicated'):
                self.run_publication()
            self.assertEqual(len(self.remote.requests), before)
        self.progress.write_bytes(b''.join(data))

    def test_lost_sdk_tag_response_resumes_original_commit_without_recreation(self):
        self.remote.lost = True
        with self.assertRaisesRegex(ValueError, 'could not be confirmed'):
            self.run_publication()
        self.assertEqual(self.remote.lookup(self.graph[modules.CORE]['tag']), self.manifest['commit'])
        self.remote.lost = False
        result = self.run_publication()
        self.assertEqual(self.mutations().count('refs/tags/' + self.graph[modules.CORE]['tag']), 1)
        self.assertEqual(result['reused_tags'], [self.graph[modules.CORE]['tag']])

    def test_failed_sdk_readback_does_not_publish_dependent_tags(self):
        self.failure = 'checksum database unavailable'
        with self.assertRaisesRegex(ValueError, self.failure):
            self.run_publication()
        self.assertEqual(self.mutations(), ['refs/tags/' + self.graph[modules.CORE]['tag']])
        self.failure = None
        self.assertEqual(len(self.run_publication()['created_tags']), 2)

    def test_failed_worker_readback_leaves_application_tag_absent(self):
        original = self.reader
        @contextmanager
        def reader(module, version, work):
            if module == modules.WORKER:
                raise ValueError('worker module unavailable')
            with original(module, version, work) as result:
                yield result
        self.reader = reader
        with self.assertRaisesRegex(ValueError, 'worker module unavailable'):
            self.run_publication()
        self.assertIsNone(self.remote.lookup(self.graph[modules.APP]['tag']))

    def test_all_names_are_preflighted_before_any_sdk_tag(self):
        self.remote.refs[self.graph[modules.APP]['tag']] = self.manifest['commit']
        with self.assertRaisesRegex(ValueError, 'without matching original progress'):
            self.run_publication()
        self.assertEqual(self.mutations(), [])

    def test_started_marker_without_remote_commit_can_resume_create_only(self):
        original = self.remote.client()
        def rejected(*_):
            raise ValueError('confirmed fixture rejection')
        original.create = rejected
        with self.assertRaisesRegex(ValueError, 'fixture rejection'):
            self.run_publication(remote=original)
        self.assertEqual(len(self.run_publication()['created_tags']), 3)

    def test_missing_or_moved_verified_tag_is_never_replaced(self):
        self.run_publication()
        tag = self.graph[modules.CORE]['tag']
        original = self.remote.refs[tag]
        for value in (None, 'b' * 40):
            with self.subTest(commit=value):
                self.remote.refs.pop(tag, None)
                if value is not None:
                    self.remote.refs[tag] = value
                before = len(self.mutations())
                with self.assertRaises(ValueError):
                    self.run_publication()
                self.assertEqual(len(self.mutations()), before)
        self.remote.refs[tag] = original

    def test_changed_graph_or_original_inputs_prevents_remote_operations(self):
        self.run_publication()
        self.manifest['commit'] = 'b' * 40
        self.sdk.dependency['release_identity'] = release.evidence_identity(self.manifest)
        self.sdk.save()
        before = len(self.remote.requests)
        with self.assertRaisesRegex(ValueError, 'different source'):
            self.run_publication()
        self.assertEqual(len(self.remote.requests), before)

    def test_inputs_changed_during_first_tag_stop_before_worker(self):
        original = self.remote.store
        def store(tag, commit):
            original(tag, commit)
            p = self.sdk.cache / 'dependencies.json'
            p.write_bytes(p.read_bytes() + b'\n')
        self.remote.store = store
        with self.assertRaisesRegex(ValueError, 'input changed'):
            self.run_publication()
        self.assertEqual(len(self.mutations()), 1)

    def test_dependency_tag_moved_after_readback_prevents_next_publication(self):
        original = self.reader
        @contextmanager
        def reader(module, version, work):
            with original(module, version, work) as result:
                yield result
            self.remote.refs[self.graph[modules.CORE]['tag']] = 'b' * 40
        self.reader = reader
        with self.assertRaisesRegex(ValueError, 'dependency tag changed'):
            self.run_publication()
        self.assertEqual(len(self.mutations()), 1)

    def test_public_checksum_and_info_mismatches_hold_dependent_tags(self):
        original = self.reader
        for field in ('Sum', 'GoModSum', 'Path', 'Version'):
            @contextmanager
            def reader(module, version, work):
                with original(module, version, work) as (value, paths):
                    yield dict(value, **{field: 'wrong'}), paths
            self.reader = reader
            with self.subTest(field=field), self.assertRaisesRegex(ValueError, 'public checksums'):
                self.run_publication()
            self.assertIsNone(self.remote.lookup(self.graph[modules.WORKER]['tag']))
        self.reader = original
        self.assertEqual(len(self.run_publication()['created_tags']), 2)

    def test_progress_lock_quota_permissions_and_dependency_overlap_fail_before_tags(self):
        with patch.object(publish, 'PROGRESS_LIMIT', 200):
            with self.assertRaises(ValueError):
                self.run_publication()
        self.assertEqual(self.mutations(), [])
        self.progress.unlink()
        self.work.chmod(0o755)
        with self.assertRaisesRegex(ValueError, 'private owned'):
            self.run_publication()
        self.work.chmod(0o700)
        self.work = self.sdk.cache / 'scratch'
        self.work.mkdir(mode=0o700)
        with self.assertRaisesRegex(ValueError, 'cannot mutate'):
            self.run_publication()
        self.assertEqual(self.mutations(), [])

    def test_incomplete_source_cannot_start_publication(self):
        for key in ('candidate', 'dirty'):
            self.manifest[key] = True
            with self.assertRaises(ValueError):
                self.run_publication()
            self.manifest[key] = False
        self.assertEqual(self.remote.requests, [])

    def test_real_process_kill_after_native_git_tag_does_not_repeat_it(self):
        git = self.root / 'git'
        git.mkdir()
        environment = dict(os.environ, GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL='/dev/null',
                           GIT_CONFIG_SYSTEM='/dev/null', GIT_AUTHOR_DATE=self.manifest['source_date'],
                           GIT_COMMITTER_DATE=self.manifest['source_date'])
        subprocess.run(['git', 'init', '-q', str(git)], env=environment, check=True, capture_output=True)
        subprocess.run(['git', '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid',
                        'commit', '--allow-empty', '-m', 'Native tag fixture'], cwd=git, env=environment,
                       check=True, capture_output=True)
        commit = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=git, text=True).strip()
        self.manifest['commit'] = commit
        self.sdk.dependency['release_identity'] = release.evidence_identity(self.manifest)
        self.sdk.save()
        remote = GitHubFixture(commit, git=git)
        self.addCleanup(remote.close)
        (self.root / 'manifest.json').write_bytes(release.encoded(self.manifest))
        code = '''import json, os, signal, sys
from pathlib import Path
sys.path.insert(0, sys.argv[1])
import module_publication as m
class Killed(m.GitHub):
 def create(self, tag, commit):
  super().create(tag, commit)
  os.kill(os.getpid(), signal.SIGKILL)
root=Path(sys.argv[2])
client=Killed(origin=sys.argv[3], allow_loopback_http=True)
m.publish_modules(json.loads((root/'manifest.json').read_text()), root/'dependencies', client,
                  None, root/'publication.jsonl', root/'work')
'''
        child = subprocess.run([sys.executable, '-B', '-c', code, str(Path(modules.__file__).parent),
                                str(self.root), remote.origin], capture_output=True, timeout=15)
        self.assertEqual(child.returncode, -signal.SIGKILL, child.stderr.decode())
        self.assertEqual(remote.lookup(self.graph[modules.CORE]['tag']), commit)
        result = self.run_publication(remote=remote.client())
        self.assertEqual(result['reused_tags'], [self.graph[modules.CORE]['tag']])
        self.assertEqual(len([r for r in remote.requests if r[0] == 'POST']), 3)
        self.assertEqual(self.run_publication(remote=remote.client())['created_tags'], [])


def publication_evidence_graph(sdk):
    return modules.publication_evidence.module_graph(sdk.cache, sdk.manifest, qualification.Inputs())


class GitHubBoundaryTests(unittest.TestCase):
    def setUp(self):
        self.remote = GitHubFixture('a' * 40)
        self.addCleanup(self.remote.close)

    def test_repository_access_and_source_are_required_before_404_is_absence(self):
        client = self.remote.client()
        with self.assertRaises(ValueError):
            client.reference('v0.1.0')
        client.prepare('a' * 40)
        self.assertIsNone(client.reference('sdk/go/v0.1.0'))

    def test_unsafe_origins_credentials_and_references_are_rejected(self):
        for origin in ('http://api.github.com', 'https://evil.invalid', self.remote.origin + '/path'):
            with self.subTest(origin=origin), self.assertRaises(ValueError):
                modules.GitHub('fixture-secret', origin=origin, allow_loopback_http=True)
        with self.assertRaises(ValueError):
            modules.GitHub('fixture-secret', origin=self.remote.origin, allow_loopback_http=True)
        client = self.remote.client()
        client.prepare('a' * 40)
        for tag in ('../v0.1.0', 'sdk/go/v01.0.0', 'v0.1.0\nsecret', 'other/v0.1.0'):
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                client.reference(tag)

    def test_auth_server_errors_invalid_and_oversized_json_are_not_absence(self):
        client = self.remote.client()
        client.prepare('a' * 40)
        for failure in ((401, {'message': 'fixture-secret'}), (403, {}), (500, {}),
                        (404, {'message': 'not an absent tag'}), (200, b'{broken'),
                        (200, b'x' * (registry_limit() + 1))):
            with self.subTest(code=failure[0]), self.assertRaises(ValueError) as stopped:
                self.remote.failure = failure
                client.reference('v0.1.0')
            self.assertNotIn('fixture-secret', str(stopped.exception))

    def test_network_uncertainty_does_not_expose_credentials(self):
        client = self.remote.client()
        client.prepare('a' * 40)
        with patch.object(client.opener, 'open', side_effect=urllib.error.URLError('fixture-secret')):
            with self.assertRaises(ValueError) as stopped:
                client.reference('v0.1.0')
        self.assertNotIn('fixture-secret', str(stopped.exception))


def registry_limit():
    return modules.registry.METADATA_LIMIT


class PublicDownloadTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.work = self.root / 'work'
        self.work.mkdir(mode=0o700)
        self.go = self.root / 'go'
        self.mode = 'pass'

    def executable(self, mode='pass'):
        self.mode = mode
        source = '''import json, os, pathlib, sys, time
mode=MODE
root=pathlib.Path(os.environ['TMPDIR'])
(root/'environment.json').write_text(json.dumps(dict(os.environ)))
if sys.argv[1:] == ['env','GOVERSION']:
 print('go1.27.0' if mode=='wrong-compiler' else 'go1.27.1')
 sys.exit(0)
if mode=='hang':
 time.sleep(60)
if mode=='error':
 print('fixture-secret',file=sys.stderr)
 sys.exit(2)
if mode=='overflow':
 print('x'*100000)
 sys.exit(0)
module,version=sys.argv[-1].split('@')
cache=pathlib.Path(os.environ['GOMODCACHE'])/'cache/download'/module/'@v'
cache.mkdir(parents=True)
paths={name:cache/(version+suffix) for name,suffix in [('Zip','.zip'),('GoMod','.mod'),('Info','.info')]}
for p in paths.values():
 p.write_bytes(b'original fixture payload')
if mode=='escape':
 paths['Zip'].unlink()
 paths['Zip'].symlink_to(root/'environment.json')
if mode=='missing':
 paths['Zip'].unlink()
value={'Path':module,'Version':version,'Sum':'h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=',
       'GoModSum':'h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=',**{k:str(v) for k,v in paths.items()}}
if mode=='wrong-module':
 value['Path']='evil.invalid/other'
if mode=='bad-checksum':
 value['Sum']='not-a-checksum'
if mode=='malformed':
 print('{broken')
elif mode=='array':
 print('[]')
else:
 print(json.dumps(value))
'''.replace('MODE', repr(mode))
        self.go.write_text('#!' + sys.executable + '\n' + source)
        self.go.chmod(0o700)

    def test_isolated_environment_and_fresh_cache_are_removed_on_success(self):
        self.executable()
        hostile = {'GOSUMDB': 'off', 'GOPROXY': 'http://evil.invalid', 'GONOSUMDB': '*',
                   'GH_TOKEN': 'fixture-secret', 'HOME': '/untrusted', 'GOFLAGS': '-race',
                   'GOWORK': '/untrusted/work', 'GOTOOLCHAIN': 'auto', 'LD_PRELOAD': '/untrusted/library'}
        with patch.dict(os.environ, hostile):
            with modules.public_download(self.go, modules.CORE, 'v0.1.0', 'go1.27.1', self.work) as (value, paths):
                temporary = next(self.work.iterdir())
                environment = json.loads((temporary / 'environment.json').read_text())
                self.assertEqual(environment['GOWORK'], 'off')
                self.assertEqual(environment['GOSUMDB'], 'sum.golang.org')
                self.assertEqual(environment['GOPROXY'], 'https://proxy.golang.org')
                self.assertEqual(environment['GONOSUMDB'], '')
                self.assertEqual(environment['GOFLAGS'], '')
                self.assertEqual(environment['GOTOOLCHAIN'], 'local')
                self.assertEqual(environment['GOAUTH'], 'off')
                self.assertEqual(environment['GOVCS'], '*:off')
                self.assertNotIn('GH_TOKEN', environment)
                self.assertNotIn('LD_PRELOAD', environment)
                self.assertNotIn('HOME', environment)
                self.assertEqual(value['Path'], modules.CORE)
                self.assertTrue(all(p.is_file() for p in paths.values()))
        self.assertEqual(list(self.work.iterdir()), [])

    def test_failed_hung_and_oversized_processes_are_bounded_and_cleaned(self):
        for mode in ('error', 'hang', 'overflow'):
            self.executable(mode)
            with self.subTest(mode=mode), self.assertRaises(ValueError) as stopped:
                with modules.public_download(self.go, modules.CORE, 'v0.1.0', 'go1.27.1', self.work, timeout=0.2):
                    self.fail('unexpected successful download')
            self.assertNotIn('fixture-secret', str(stopped.exception))
            self.assertEqual(list(self.work.iterdir()), [])

    def test_invalid_download_identity_checksum_paths_and_json_are_rejected(self):
        for mode in ('wrong-compiler', 'wrong-module', 'bad-checksum', 'escape', 'missing', 'malformed', 'array'):
            self.executable(mode)
            with self.subTest(mode=mode), self.assertRaises(ValueError):
                with modules.public_download(self.go, modules.CORE, 'v0.1.0', 'go1.27.1', self.work):
                    self.fail('unexpected successful download')
            self.assertEqual(list(self.work.iterdir()), [])

    def test_unsafe_module_and_work_paths_fail_before_execution(self):
        self.executable()
        for module, version in (('../credentials', 'v0.1.0'), (modules.CORE, 'v01.0.0'),
                                (modules.CORE, 'v0.1.0;danger')):
            with self.subTest(module=module, version=version), self.assertRaises(ValueError):
                with modules.public_download(self.go, module, version, 'go1.27.1', self.work):
                    pass
        self.work.chmod(0o755)
        with self.assertRaises(ValueError):
            with modules.public_download(self.go, modules.CORE, 'v0.1.0', 'go1.27.1', self.work):
                pass


if __name__ == '__main__':
    unittest.main()
