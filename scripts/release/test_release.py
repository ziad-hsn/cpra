import copy
import hashlib
import io
import json
import os
import re
import sys
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
import urllib.error
from unittest.mock import patch

import release
import linux_packages
import publish
import registry
import sbom


class ReleaseTests(unittest.TestCase):
    def test_publication_lock_precedes_payload_reads_and_external_io(self):
        with patch.object(sys, 'argv', ['publish.py', '--cosign', '/unused/cosign', '--out', '/unused/payload']), \
                patch.object(publish.Path, 'read_text', side_effect=AssertionError('payload read')), \
                patch.object(publish.subprocess, 'run', side_effect=AssertionError('external process')), \
                patch.object(publish.subprocess, 'check_output', side_effect=AssertionError('external process')), \
                patch.object(publish, 'verify') as verify, patch.object(publish, 'evidence') as evidence, \
                patch('sys.stderr', new_callable=io.StringIO) as stderr:
            with self.assertRaises(SystemExit) as stopped:
                publish.main()
            self.assertEqual(stopped.exception.code, 2)
            self.assertIn('Public publication is locked', stderr.getvalue())
            verify.assert_not_called()
            evidence.assert_not_called()

    def test_storage_recipe_matches_current_reader(self):
        self.assertEqual(release.storage_format(release.RECIPE['build_tags']), release.RECIPE['storage_format_version'])
        self.assertNotIn('externaljobs', release.RECIPE['build_tags'])
        self.assertEqual(release.storage_format(['externaljobs']), 21)

    def test_storage_recipe_rejects_ambiguous_or_cyclic_format(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            storage = root / 'internal/persistence'
            storage.mkdir(parents=True)
            (storage / 'extensions_base.go').write_text('//go:build !externaljobs\n\npackage persistence\nconst LatestFormatVersion = Selected\n')
            definitions = storage / 'versions.go'
            with patch.object(release, 'ROOT', root):
                definitions.write_text('package persistence\nconst Selected = Missing\n')
                with self.assertRaises(ValueError): release.storage_format([])
                definitions.write_text('package persistence\nconst Selected = Selected\n')
                with self.assertRaises(ValueError): release.storage_format([])
                definitions.write_text('package persistence\nconst Selected = 14\n')
                self.assertEqual(release.storage_format([]), 14)
                (storage / 'ambiguous.go').write_text('package persistence\nconst Selected = 14\n')
                with self.assertRaises(ValueError): release.storage_format([])

    def test_build_config_matches_recipe(self):
        text=(release.ROOT/'.goreleaser.yaml').read_text().split('\n',1)[1]
        config=json.loads(text)
        for build in config['builds']:
            self.assertEqual(build['flags'],release.RECIPE['build_flags'])
            self.assertEqual(build['tags'],release.RECIPE['build_tags'])
            self.assertNotIn('-s',build['ldflags'][0].split())
            self.assertNotIn('-w',build['ldflags'][0].split())
            self.assertIn(release.RECIPE['module']+'/internal/version.Version=',build['ldflags'][0])
        self.assertEqual(config['release']['disable'],True)

    def test_environment_drops_build_injection(self):
        manifest={'recipe':release.RECIPE,'source_date_epoch':123}
        with patch.dict(os.environ,{'GOFLAGS':'-ldflags=-s','GOOS':'plan9','GOEXPERIMENT':'bogus','GOWORK':'/evil/go.work','CGO_CFLAGS':'-malicious'},clear=False):
            env=release.environment('/usr/bin/go',manifest,'linux_amd64')
        self.assertEqual(env['GOFLAGS'],'')
        self.assertEqual(env['GOOS'],'linux')
        self.assertEqual(env['GOEXPERIMENT'],'')
        self.assertEqual(env['GOWORK'],'off')
        self.assertNotIn('CGO_CFLAGS',env)

    def test_archive_is_reproducible_and_preserves_executable_mode(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            entries={'b.txt':(b'hello',False),'a.sh':(b'#!/bin/sh\n',True)}
            release.package.archive(root/'a.tar.gz',entries,1234567890)
            release.package.archive(root/'b.tar.gz',dict(reversed(list(entries.items()))),1234567890)
            self.assertEqual(release.sha(root/'a.tar.gz'),release.sha(root/'b.tar.gz'))
            with tarfile.open(root/'a.tar.gz') as archive:
                self.assertEqual(archive.getmember('a.sh').mode,0o755)
                self.assertEqual(archive.getmember('b.txt').uid,0)

    def test_committed_source_does_not_include_untracked_and_retains_symlink(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);out=root/'dist';out.mkdir()
            subprocess.run(['git','init','-q',str(root)],check=True)
            (root/'script.sh').write_text('#!/bin/sh\n');(root/'script.sh').chmod(0o755)
            (root/'link').symlink_to('script.sh')
            (root/'.gitignore').write_text('dist/\nsecret\n')
            subprocess.run(['git','add','.'],cwd=root,check=True)
            subprocess.run(['git','-c','user.name=Test','-c','user.email=test@example.invalid','commit','-qm','fixture'],cwd=root,check=True)
            (root/'secret').write_text('must not ship')
            commit=subprocess.check_output(['git','rev-parse','HEAD'],cwd=root,text=True).strip()
            manifest={'commit':commit,'candidate':False,'dirty':False,'source_date_epoch':1234567890,'version':'v1.0.0'}
            def run(args,**kwargs):return subprocess.check_output(args,cwd=root,text=True).strip()
            with patch.object(release,'ROOT',root),patch.object(release,'run',run):
                path=release.source_archive(manifest,out)
            with tarfile.open(path) as archive:
                self.assertNotIn('secret',archive.getnames())
                self.assertEqual(archive.getmember('script.sh').mode,0o755)
                self.assertTrue(archive.getmember('link').issym())
                self.assertEqual(json.load(archive.extractfile('RELEASE.json'))['commit'],commit)

    def test_candidate_cannot_make_source_release(self):
        with self.assertRaisesRegex(ValueError,'Candidate'):
            release.source_archive({'candidate':True,'dirty':True},Path('/unused'))

    def test_checksums_reject_mutation_and_traversal(self):
        with tempfile.TemporaryDirectory() as directory:
            out=Path(directory);(out/'artifact').write_bytes(b'correct')
            release.checksums(out);release.verify(out)
            (out/'artifact').write_bytes(b'wrong')
            with self.assertRaisesRegex(ValueError,'mismatch'):release.verify(out)
            (out/'SHA256SUMS').write_text('0'*64+'  ../artifact\n')
            with self.assertRaisesRegex(ValueError,'Unsafe'):release.verify(out)

    def test_package_versions_preserve_prerelease_order(self):
        self.assertEqual(linux_packages.version_fields('v1.2.3-rc.1','deb'),'1.2.3~bfkevaa1a')
        self.assertEqual(linux_packages.version_fields('v1.2.3','rpm'),'1.2.3')
        for bad in ('v1.2.3-rc.01','v01.2.3','v1.2.3+build','v1.2.3-'):
            with self.assertRaises(ValueError):linux_packages.version_fields(bad,'deb')
        if subprocess.run(['sh','-c','command -v dpkg >/dev/null']).returncode==0:
            versions=('v1.2.3-0','v1.2.3-1','v1.2.3-10','v1.2.3-A','v1.2.3-a','v1.2.3-a.1','v1.2.3-a-1','v1.2.3-a0','v1.2.3-rc.1','v1.2.3-rc.10','v1.2.3')
            for first,second in zip(versions,versions[1:]):
                subprocess.run(['dpkg','--compare-versions',linux_packages.version_fields(first,'deb')+'-1','lt',linux_packages.version_fields(second,'deb')+'-1'],check=True)
                # Fixed-width lexical components also retain RPM ordering; the
                # executable package lifecycle tests validate it using real RPM.
                self.assertEqual(linux_packages.version_fields(first,'rpm'),linux_packages.version_fields(first,'deb'))

    def test_invalid_versions_are_rejected_before_expensive_builds(self):
        for version in ('v1.2.3-rc..1', 'v1.2.3-01', 'v1.2.3-rc.01', 'v01.2.3', '1.2.3', 'v1.2.3+local'):
            self.assertFalse(release.valid_version(version), version)
        for version in ('v1.2.3', 'v0.0.0-rc.0', 'v1.2.3-rc-1.22'):
            self.assertTrue(release.valid_version(version), version)

    def test_source_change_after_preparation_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            (root/'internal/persistence').mkdir(parents=True)
            storage_version=release.RECIPE['storage_format_version']
            (root/'internal/persistence/extensions_base.go').write_text(f'//go:build !externaljobs\n\npackage persistence\nconst LatestFormatVersion = {storage_version}\n')
            (root/'internal/httpserver/assets').mkdir(parents=True)
            source=root/'main.go';source.write_text('package main\n')
            manifest={'schema_version':1,'version':'v1.2.3','commit':'a'*40,'source_date_epoch':0,
                      'source_date':'1970-01-01T00:00:00Z','recipe':release.RECIPE,'candidate':False,
                      'dashboard_sha256':release.tree_digest(root/'internal/httpserver/assets'),
                      'toolchain_archives':release.TOOLCHAINS[release.RECIPE['go_version']],
                      'storage_format_version':storage_version,'source_files':{'main.go':{'mode':'100644','sha256':release.sha(source)},'internal/persistence/extensions_base.go':{'mode':'100644','sha256':release.sha(root/'internal/persistence/extensions_base.go')}}}
            with patch.object(release,'ROOT',root):
                release.check_manifest(manifest)
                source.write_text('package injected\n')
                with self.assertRaisesRegex(ValueError,'changed after release preparation'):
                    release.check_manifest(manifest)

    def test_missing_native_evidence_cannot_satisfy_publication_gate(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            with self.assertRaises(FileNotFoundError):publish.evidence(root, {'commit':'a'*40})
            (root/'reproducibility.json').write_text('{"status":"incomplete"}')
            with self.assertRaisesRegex(ValueError,'did not pass'):
                publish.evidence(root, {'commit':'a'*40})

    def test_stale_source_evidence_cannot_satisfy_publication_gate(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            (root/'reproducibility.json').write_text(json.dumps({'status':'pass','commit':'b'*40}))
            with self.assertRaisesRegex(ValueError,'different source'):
                publish.evidence(root, {'commit':'a'*40})

    def test_vendored_font_identity_and_license_are_observed(self):
        font=release.package.bundled_font_inventory()
        self.assertEqual(font['name'],'Roboto Slab Bold')
        self.assertEqual(font['version'],'2.002')
        self.assertEqual(font['declared_license'],'Apache-2.0')
        self.assertEqual(font['sha256'],release.sha(release.ROOT/font['source_file']))
        document={'SPDXID':'SPDXRef-DOCUMENT'}
        sbom.append_frontend(document,[font])
        entry=document['packages'][0]
        self.assertEqual(entry['checksums'][0]['checksumValue'],font['sha256'])
        self.assertEqual(entry['versionInfo'],'2.002')
        self.assertEqual(entry['licenseDeclared'],'Apache-2.0')
        self.assertEqual(entry['downloadLocation'],'NOASSERTION')
        self.assertNotIn('externalRefs',entry)
        self.assertIn('2.002;GOOG;RobotoSlab-Bold',entry['sourceInfo'])

    def test_archive_evidence_binds_actual_program_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);manifest={'version':'v1.2.3','commit':'a'*40}
            binaries={'cpra':b'controller','cpractl':b'client'}
            entries={name:(data,True) for name,data in binaries.items()}
            entries['DEPENDENCIES.json']=(release.encoded({'binaries':{name:hashlib.sha256(data).hexdigest() for name,data in binaries.items()}}),False)
            entries['RELEASE.json']=(release.encoded(manifest),False)
            archive=root/'cpra-v1.2.3-linux-amd64.tar.gz'
            release.package.archive(archive,entries,1234567890)
            expected=publish.archive_programs(root,manifest,'linux_amd64')
            self.assertEqual(expected['cpra'],hashlib.sha256(b'controller').hexdigest())
            entries['cpra']=(b'different executable',True)
            release.package.archive(archive,entries,1234567890)
            with self.assertRaisesRegex(ValueError,'disagrees'):
                publish.archive_programs(root,manifest,'linux_amd64')

    def test_compose_bundle_is_versioned_and_self_contained(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);manifest={'version':'v1.2.3','commit':'a'*40,'source_date_epoch':1234567890}
            path=release.compose_bundle(manifest,root);first=path.read_bytes()
            release.compose_bundle(manifest,root)
            self.assertEqual(path.read_bytes(),first)
            with tarfile.open(path) as archive:
                self.assertEqual(set(archive.getnames()),{'docker-compose.yml','runtime.yaml','.env.example','LICENSE','RELEASE.json','README.md','OPERATIONS.md'})
                self.assertIn(b'CPRA_IMAGE=ghcr.io/ziad-hsn/cpra:v1.2.3',archive.extractfile('.env.example').read())
                self.assertIn(b'-f docker-compose.yml up -d',archive.extractfile('README.md').read())

    def test_registry_uncertainty_cannot_prove_absence(self):
        class Opener:
            def __init__(self,error):self.calls=0;self.error=error
            def open(self,*args,**kwargs):
                self.calls+=1
                if self.calls==1:return io.BytesIO(b'{"token":"test-fixture-token"}')
                if self.error:raise self.error
                return io.BytesIO(b'{}')
        def failure(code,body):
            return urllib.error.HTTPError('https://ghcr.io/fixture',code,'fixture',{},io.BytesIO(body))
        with patch.dict(os.environ,{'GH_TOKEN':'fixture','GITHUB_ACTOR':'fixture'}):
            cases=((None,False),(failure(403,b'{}'),False),(failure(404,b'{}'),False),
                   (failure(404,b'{"errors":[{"code":"DENIED"}]}'),False),
                   (failure(404,b'{"errors":[{"code":"MANIFEST_UNKNOWN"}]}'),True))
            for error,accepted in cases:
                with patch.object(registry.urllib.request,'build_opener',return_value=Opener(error)):
                    if accepted:registry.assert_absent('ziad-hsn/charts/cpra','0.1.0')
                    else:
                        with self.assertRaises(ValueError):registry.assert_absent('ziad-hsn/charts/cpra','0.1.0')

    def test_package_hooks_reject_existing_root_or_wrong_group(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);tool=root/'getent'
            for uid,gid in ((0,999),(999,998)):
                tool.write_text('#!/bin/sh\ncase "$1" in passwd) echo cpra:x:'+str(uid)+':'+str(gid)+'::/var/lib/cpra:/usr/sbin/nologin ;; group) echo cpra:x:999: ;; esac\n');tool.chmod(0o755)
                for hook in ('deb-preinstall.sh','rpm-preinstall.sh'):
                    result=subprocess.run(['sh',str(release.ROOT/'packaging/linux'/hook),'install' if hook.startswith('deb') else '1'],env=dict(os.environ,PATH=str(root)+':'+os.environ['PATH']),capture_output=True,text=True)
                    self.assertNotEqual(result.returncode,0)
                    self.assertIn('refusing existing identity',result.stderr)

    def test_hooks_no_restart_on_fresh_install_or_rpm_upgrade_remove(self):
        # Execute actual scripts with only absolute locations redirected to a
        # disposable root; fake systemctl records state-changing invocations.
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);bin=root/'bin';bin.mkdir()
            log=root/'calls'
            for name,body in {'systemctl':f'echo "$*" >> "{log}"\nexit 0', 'getent':'case "$1" in passwd) echo cpra:x:999:999::/var/lib/cpra:/usr/sbin/nologin ;; group) echo cpra:x:999: ;; esac',
                              'install':'exit 0','chmod':'exit 0','chown':'exit 0','rm':'exit 0'}.items():
                path=bin/name;path.write_text('#!/bin/sh\n'+body+'\n');path.chmod(0o755)
            env=dict(os.environ,PATH=str(bin)+':'+os.environ['PATH'])
            (root/'etc/cpra').mkdir(parents=True);(root/'etc/cpra/auth.token').write_text('existing')
            for hook,arg in [('deb-preinstall.sh','install'),('rpm-preremove.sh','1'),('rpm-postremove.sh','1')]:
                text=(release.ROOT/'packaging/linux'/hook).read_text()
                text=text.replace('/etc/cpra',str(root/'etc/cpra')).replace('/var/lib/cpra',str(root/'var/lib/cpra')).replace('/run/cpra-package',str(root/'run/cpra-package'))
                subprocess.run(['sh','-s','--',arg],input=text,text=True,env=env,check=True)
            self.assertFalse(log.exists(),'fresh install or removal of old RPM must not alter running state')

if __name__=='__main__':unittest.main()
