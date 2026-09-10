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

PORTS={'redis':16379,'postgres':15432,'mysql':13306,'mongo':17017,'rabbitmq':15672,'kafka':19092}


def main():
    p=argparse.ArgumentParser()
    p.add_argument('--binary',required=True)
    p.add_argument('--driver',choices=list(PORTS),required=True)
    p.add_argument('--out',required=True)
    p.add_argument('--pull',action='store_true',help='Explicitly allow fixture image downloads after host preflight')
    args=p.parse_args()
    root=pathlib.Path(__file__).resolve().parents[2]
    sys.path.insert(0,str(root/'scripts/benchmark'))
    from preflight import inspect
    ready=inspect(5*1024**3)
    if not ready['ready']:
        path=pathlib.Path(args.out);path.parent.mkdir(parents=True,exist_ok=True)
        path.write_text(json.dumps({'status':'not_verified','reason':ready['reason'],'preflight':ready},indent=2)+'\n')
        return 2
    project='cpra-verify-'+uuid.uuid4().hex[:10]
    compose=['docker','compose','-p',project,'-f',str(root/'examples/verification/fixtures.compose.yaml'),'--profile',args.driver]
    counters={'connections':0,'request_bytes':0,'response_bytes':0}
    lock=threading.Lock()
    upstream=PORTS[args.driver]

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
        subprocess.run(compose+['up','-d','--pull','always' if args.pull else 'never',args.driver],check=True)
        deadline=time.monotonic()+180
        while True:
            try:
                with socket.create_connection(('127.0.0.1',upstream),timeout=1):break
            except OSError:
                if time.monotonic()>=deadline:raise
                time.sleep(1)
        # Opening a port precedes some servers' readiness; initialization grace
        # is real elapsed time and any subsequent failure remains a failure.
        time.sleep(15)
        base=json.loads('\n'.join((root/'examples/verification/live.yaml').read_text().splitlines()[1:]))
        monitor=next(m for m in base['manifest']['monitors'] if m['id']=='pulse-'+args.driver)
        config=monitor['pulse_check']['config'];port=relay.server_address[1]
        for key,value in list(config.items()):
            if isinstance(value,str):config[key]=value.replace('REPLACE_WITH_CONFIGURED_PASSWORD','cpra-disposable').replace(str(upstream),str(port))
        if 'port' in config:config['port']=port
        if args.driver=='kafka':config['brokers']=['127.0.0.1:'+str(port)]
        if args.driver=='rabbitmq':config['url']='amqp://cpra:cpra-disposable@127.0.0.1:'+str(port)+'/'
        case={'kind':'pulse','driver':args.driver,'configured':True,'monitor_id':monitor['id'],
              'observer':[sys.executable,str(root/'scripts/verification/observe_relay.py'),'http://127.0.0.1:'+str(audit.server_port)]}
        with tempfile.TemporaryDirectory(prefix='cpra-database-') as directory:
            path=pathlib.Path(directory)/'config.yaml'
            path.write_text(json.dumps({'manifest':{'monitors':[monitor]},'cases':[case]}))
            result=subprocess.run([str(pathlib.Path(args.binary).resolve()),'-live','-config',str(path),'-out',str(pathlib.Path(args.out).resolve())])
            if result.returncode not in (0,2):return result.returncode
            rows=json.loads(pathlib.Path(args.out).read_text())['records']
            row=next(r for r in rows if r['kind']=='pulse' and r['driver']==args.driver)
            return 0 if row['status']=='pass' else 1
    finally:
        subprocess.run(compose+['down','--volumes'],check=True)
        for server in (relay,audit):server.shutdown();server.server_close()
        for thread in threads:thread.join()


if __name__=='__main__':raise SystemExit(main())
