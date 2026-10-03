#!/usr/bin/env python3
"""Sequential real database/broker verification through an auditing TCP relay.

The relay only forwards bytes to the actual server. It does not implement the
protocol or replace a CPRa transport. Fresh connections and bidirectional byte
counts independently establish that the production job contacted the fixture.
"""
import argparse
import hashlib
import http.server
import json
import pathlib
import select
import socket
import socketserver
import subprocess
import sys
import tempfile
import threading
import time
import uuid

import fixture_preflight

PORTS={'redis':16379,'postgres':15432,'mysql':13306,'mongo':17017,'rabbitmq':15672,'kafka':19092}


def native_query(command, seconds=5):
    # A Docker client timeout alone does not cancel the exec process inside the
    # container. Bound that process as well, including its Java/shell children.
    return subprocess.run(command, timeout=seconds+5, capture_output=True, text=True)


def mysql_ready(compose):
    """Require the final TCP server, configured account/database, and log table.

    Docker can publish its host socket while mysqld is still initializing. The
    temporary initialization server also must not satisfy readiness: both CLI
    checks explicitly use TCP, which that server disables.
    """
    cli=compose+['exec','-T','-e','MYSQL_PWD=cpra-disposable','mysql',
                 'timeout','--kill-after=1s','5s','mysql',
                 '--protocol=TCP','--host=127.0.0.1','--port=3306',
                 '--batch','--skip-column-names']
    try:
        user=native_query(cli+['--user=cpra','--database=cpra','--execute','SELECT 1; SELECT DATABASE();'])
        if user.returncode!=0 or user.stdout.split()!=['1','cpra']:
            return False
        table=native_query(cli+['--user=root','--execute',
                                "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='mysql' AND table_name='general_log';"])
        return table.returncode==0 and table.stdout.strip()=='1'
    except subprocess.TimeoutExpired:
        return False


def database_ready(driver, compose, diagnostics=None):
    """Use each server's own client; an open published socket is insufficient."""
    if driver == 'mysql':
        return mysql_ready(compose)
    commands = {
        'redis': ['redis-cli', '--no-auth-warning', '-a', 'cpra-disposable', 'PING'],
        'postgres': ['psql', '-X', '--host=127.0.0.1', '--username=cpra', '--dbname=cpra',
                     '--tuples-only', '--no-align', '--command=SELECT 1, current_database();'],
        'mongo': ['mongosh', '--quiet', '--eval', 'db.adminCommand({ping: 1}).ok'],
        'rabbitmq': ['rabbitmq-diagnostics', '-q', 'check_running'],
        'kafka': ['/opt/kafka/bin/kafka-topics.sh', '--bootstrap-server', '127.0.0.1:9092', '--list'],
    }
    command = compose + ['exec', '-T']
    if driver == 'postgres':
        command += ['-e', 'PGPASSWORD=cpra-disposable']
    seconds = 15 if driver == 'kafka' else 5
    try:
        result = native_query(command + [driver, 'timeout', '--kill-after=1s', str(seconds)+'s'] + commands[driver], seconds)
    except subprocess.TimeoutExpired:
        if diagnostics is not None:
            diagnostics.clear()
            diagnostics.update(timed_out=True, exit_code=None)
        return False
    if diagnostics is not None:
        diagnostics.clear()
        diagnostics.update(exit_code=result.returncode, timed_out=result.returncode in (124,137),
                           stdout_sha256=hashlib.sha256(result.stdout.encode()).hexdigest(),
                           stderr_sha256=hashlib.sha256(result.stderr.encode()).hexdigest())
        if driver == 'kafka' and result.returncode != 0:
            # This dedicated profile has no account credentials; retain bounded
            # native-client failure context without copying provider secrets.
            diagnostics['detail']=(result.stderr or result.stdout)[-4096:]
    expected = {'redis': 'PONG', 'postgres': '1|cpra', 'mongo': '1'}
    return result.returncode == 0 and (driver not in expected or result.stdout.strip() == expected[driver])


def main():
    p=argparse.ArgumentParser()
    p.add_argument('--binary',required=True)
    p.add_argument('--driver',choices=list(PORTS),required=True)
    p.add_argument('--out',required=True)
    p.add_argument('--pull',action='store_true',help='Explicitly allow fixture image downloads after host preflight')
    args=p.parse_args()
    root=pathlib.Path(__file__).resolve().parents[2]
    output=fixture_preflight.output_path(args.out)
    if args.pull:
        # Downloads remain subject to the full physical-space prerequisite.
        sys.path.insert(0,str(root/'scripts/benchmark'))
        from preflight import inspect
        ready=inspect(5*1024**3)
    else:
        ready=fixture_preflight.inspect(args.driver)
    if not ready['ready']:
        fixture_preflight.write_new(output,{'status':'not_verified','reason':ready['reason'],'preflight':ready})
        return 2
    project='cpra-verify-'+uuid.uuid4().hex[:10]
    compose=['docker','compose','-p',project,'-f',str(root/'examples/verification/fixtures.compose.yaml'),'--profile',args.driver]
    counters={'connections':0,'request_bytes':0,'response_bytes':0}
    lock=threading.Lock()
    upstream=PORTS[args.driver]
    # Refuse a conflicting host endpoint before creating any disposable service.
    with socket.socket() as available:
        available.bind(('127.0.0.1',upstream))
    environment={'driver':args.driver,'project':project,'preflight':ready,'cleanup_confirmed':False}

    class Relay(socketserver.BaseRequestHandler):
        def handle(self):
            with socket.create_connection(('127.0.0.1',upstream),timeout=5) as target:
                with lock:counters['connections']+=1
                pair=[self.request,target]
                while True:
                    readable,_,_=select.select(pair,[],[],10)
                    if not readable:return
                    for source in readable:
                        data=source.recv(65536)
                        if not data:return
                        destination=target if source is self.request else self.request
                        destination.sendall(data)
                        key='request_bytes' if source is self.request else 'response_bytes'
                        with lock:counters[key]+=len(data)

    class Server(socketserver.ThreadingTCPServer):
        daemon_threads=True
        allow_reuse_address=True

    class Audit(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            with lock:data=json.dumps(counters).encode()
            self.send_response(200);self.end_headers();self.wfile.write(data)
        def log_message(self,*_):pass

    relay=Server(('127.0.0.1',0),Relay)
    audit=http.server.ThreadingHTTPServer(('127.0.0.1',0),Audit)
    threads=[threading.Thread(target=s.serve_forever,daemon=True) for s in (relay,audit)]
    for thread in threads:thread.start()
    try:
        subprocess.run(compose+['up','-d','--pull','always' if args.pull else 'never',args.driver],check=True,timeout=120)
        container_id=subprocess.check_output(compose+['ps','-q',args.driver],text=True,timeout=15).strip()
        container=json.loads(subprocess.check_output(['docker','inspect',container_id],timeout=15))[0]
        image=json.loads(subprocess.check_output(['docker','image','inspect',container['Image']],timeout=15))[0]
        settings=container['HostConfig']
        environment.update(container_id=container_id,image_id=image['Id'],image_digests=image.get('RepoDigests',[]),
                           native_client_ready=False,
                           runtime_limits={key:settings[key] for key in ('Memory','MemorySwap','NanoCpus','PidsLimit','Tmpfs','LogConfig')})
        deadline=time.monotonic()+180
        attempts=0
        diagnostics={}
        while True:
            attempts+=1
            environment.update(readiness_probes=attempts,last_readiness_probe=diagnostics)
            if database_ready(args.driver,compose,diagnostics):break
            if time.monotonic()>=deadline:
                environment['failure']='native_client_readiness_deadline'
                raise RuntimeError(args.driver+' did not complete its native-client readiness check within 180 seconds')
            time.sleep(1)
        environment['native_client_ready']=True
        if args.driver=='mysql':
            print(f'MySQL authenticated TCP SELECT 1, cpra database, and general_log table ready after {attempts} probe(s)',flush=True)
            environment.update(configured_user_select_1=True,configured_database='cpra',general_log_table_available=True)
        base=json.loads('\n'.join((root/'examples/verification/live.yaml').read_text().splitlines()[1:]))
        monitor=next(m for m in base['manifest']['monitors'] if m['id']=='pulse-'+args.driver)
        config=monitor['pulse_check']['config'];port=relay.server_address[1]
        for key,value in list(config.items()):
            if isinstance(value,str):config[key]=value.replace('REPLACE_WITH_CONFIGURED_PASSWORD','cpra-disposable').replace(str(upstream),str(port))
        if 'port' in config:config['port']=port
        if args.driver=='kafka':config['brokers']=['127.0.0.1:'+str(port)]
        if args.driver=='rabbitmq':config['url']='amqp://cpra:cpra-disposable@127.0.0.1:'+str(port)+'/'
        case={'kind':'pulse','driver':args.driver,'configured':True,'evidence_type':'local_integration','monitor_id':monitor['id'],
              'observer':[sys.executable,str(root/'scripts/verification/observe_relay.py'),'http://127.0.0.1:'+str(audit.server_port)]}
        with tempfile.TemporaryDirectory(prefix='cpra-database-') as directory:
            path=pathlib.Path(directory)/'config.yaml'
            path.write_text(json.dumps({'manifest':{'monitors':[monitor]},'cases':[case]}))
            result=subprocess.run([str(pathlib.Path(args.binary).resolve()),'-live','-config',str(path),'-out',str(output)],timeout=180)
            if result.returncode not in (0,2):return result.returncode
            rows=json.loads(pathlib.Path(args.out).read_text())['records']
            row=next(r for r in rows if r['kind']=='pulse' and r['driver']==args.driver)
            return 0 if row['status']=='pass' else 1
    finally:
        try:
            subprocess.run(compose+['down','--volumes'],check=True,timeout=60)
            environment['cleanup_confirmed']=True
        finally:
            for server in (relay,audit):server.shutdown();server.server_close()
            for thread in threads:thread.join(timeout=5)
            environment['binary_sha256']=hashlib.sha256(pathlib.Path(args.binary).read_bytes()).hexdigest()
            fixture_preflight.write_new(str(output)+'.environment.json',environment)


if __name__=='__main__':raise SystemExit(main())
