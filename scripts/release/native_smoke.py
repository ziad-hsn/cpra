#!/usr/bin/env python3
"""Exercise a staged binary on its native OS: identity, validation, readiness and durable restart."""
import argparse
import json
import os
import platform
from pathlib import Path
import signal
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
from release import sha


def request(url,token):
    req=urllib.request.Request(url,headers={'Authorization':'Bearer '+token})
    with urllib.request.urlopen(req,timeout=3) as response:return response.read()


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--stage',type=Path,required=True)
    parser.add_argument('--out',type=Path,required=True)
    args=parser.parse_args();args.stage=args.stage.resolve()
    manifest=json.loads((args.stage/'RELEASE.json').read_text())
    extension='.exe' if os.name=='nt' else ''
    binary=args.stage/('cpra'+extension);cli=args.stage/('cpractl'+extension)
    for program,flag in ((binary,'-version'),(cli,'--version')):
        identity=subprocess.check_output([str(program),flag],text=True)
        if manifest['version'] not in identity or manifest['commit'] not in identity:raise RuntimeError('Release identity mismatch')
    capabilities=json.loads(subprocess.check_output([str(binary),'-capabilities'],text=True))
    token='release-smoke-local-token'
    with tempfile.TemporaryDirectory(prefix='cpra-native-smoke-') as temp:
        root=Path(temp);(root/'monitors.yaml').write_text('monitors: []\n');(root/'auth.token').write_text(token)
        with socket.socket() as sock:sock.bind(('127.0.0.1',0));port=sock.getsockname()[1]
        argv=[str(binary),'-yaml',str(root/'monitors.yaml'),'-data-dir',str(root/'state'),'-allow-empty',
              '-web.addr',f'127.0.0.1:{port}','-web.auth-file',str(root/'auth.token'),'-shutdown-timeout','10s']
        subprocess.run(argv+['-validate'],check=True,capture_output=True,text=True)
        states=[]
        node_ids=[]
        for attempt in range(2):
            log=(root/f'run-{attempt}.log').open('w')
            kwargs={'creationflags':subprocess.CREATE_NEW_PROCESS_GROUP} if os.name=='nt' else {}
            process=subprocess.Popen(argv,stdout=log,stderr=subprocess.STDOUT,**kwargs)
            try:
                deadline=time.monotonic()+45
                while True:
                    if process.poll() is not None:raise RuntimeError('Controller exited before ready')
                    try:
                        request(f'http://127.0.0.1:{port}/api/v1/readyz',token);break
                    except (urllib.error.URLError,TimeoutError):
                        if time.monotonic()>deadline:raise
                        time.sleep(.2)
                states.append(json.loads(request(f'http://127.0.0.1:{port}/api/v1/state',token)))
                node_ids.append(json.loads((root/'state/identity.json').read_text())['id'])
                try:request(f'http://127.0.0.1:{port}/api/v1/state','wrong-token')
                except urllib.error.HTTPError as error:
                    if error.code!=401:raise
                else:raise RuntimeError('Authenticated API accepted wrong token')
                # Windows has no POSIX SIGTERM: kill first run intentionally;
                # second launch must recover the same committed store.
                if os.name=='nt':process.kill()
                else:process.send_signal(signal.SIGTERM)
                process.wait(timeout=20)
                if os.name!='nt' and process.returncode!=0:raise RuntimeError('Graceful stop failed')
            finally:
                if process.poll() is None:process.kill();process.wait()
                log.close()
        if not any((root/'state').iterdir()):raise RuntimeError('No durable storage was created')
        if node_ids[0]!=node_ids[1]:raise RuntimeError('Restart changed durable node identity')
    args.out.parent.mkdir(parents=True,exist_ok=True)
    args.out.write_text(json.dumps({'status':'pass','version':manifest['version'],'commit':manifest['commit'],
        'binary_sha256':sha(binary),'cli_sha256':sha(cli),
        'os':platform.system(),'architecture':platform.machine(),'os_version':platform.platform(),
        'source_candidate':manifest['candidate'],'source_dirty':manifest['dirty'],'capabilities':capabilities,'restart_states':states,
        'scope':'native executable, identity, empty-config validation, authenticated readiness, durable reopen; Windows stop is forced'},indent=2)+'\n')

if __name__=='__main__':main()
