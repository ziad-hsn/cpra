#!/usr/bin/env python3
"""Exercise the real bundled dashboard, API and CLI against one running process."""
import argparse
import http.server
import json
import os
import pathlib
import signal
import subprocess
import tempfile
import threading
import time
import urllib.request
from playwright.sync_api import sync_playwright


def main():
    p=argparse.ArgumentParser()
    p.add_argument('--binary',required=True)
    p.add_argument('--client',required=True)
    p.add_argument('--out',required=True)
    a=p.parse_args()
    out=pathlib.Path(a.out).resolve();out.mkdir(parents=True,exist_ok=True)
    counters={'checks':0,'alerts':0}
    class Target(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            counters['checks']+=1;self.send_response(503);self.end_headers()
        def do_POST(self):
            self.rfile.read(int(self.headers.get('Content-Length',0)));counters['alerts']+=1;self.send_response(204);self.end_headers()
        def log_message(self,*_):pass
    server=http.server.ThreadingHTTPServer(('127.0.0.1',0),Target)
    thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
    process=None
    try:
        with tempfile.TemporaryDirectory(prefix='cpra-browser-') as directory:
            directory=pathlib.Path(directory)
            manifest={'monitors':[{'id':'browser-fixture','name':'Durable browser fixture','pulse_check':{'type':'http','interval':'100ms','timeout':'1s','unhealthy_threshold':1,'config':{'url':'http://127.0.0.1:'+str(server.server_port)}},'codes':{'red':{'notify':'webhook','config':{'url':'http://127.0.0.1:'+str(server.server_port)+'/alert'}}}}]}
            (directory/'monitors.json').write_text(json.dumps(manifest))
            token='local-fixture-'+os.urandom(16).hex()
            env=dict(os.environ,CPRA_AUTH_TOKEN=token)
            # This port is dedicated to this short local browser scenario.
            process=subprocess.Popen([str(pathlib.Path(a.binary).resolve()),'-yaml',str(directory/'monitors.json'),'-web.addr','127.0.0.1:18067'],cwd=directory,env=env,stdout=(out/'controller.log').open('wb'),stderr=subprocess.STDOUT)
            base='http://127.0.0.1:18067'
            def api(path):
                req=urllib.request.Request(base+path,headers={'Authorization':'Bearer '+token})
                with urllib.request.urlopen(req,timeout=5) as response:return json.load(response)
            deadline=time.monotonic()+15
            while True:
                try:
                    if api('/api/v1/overview')['total']==1 and counters['alerts']>=1:break
                except OSError:pass
                if time.monotonic()>deadline:raise RuntimeError('browser fixture did not become ready')
                time.sleep(.1)
            ident=api('/api/v1/monitors')['monitors'][0]['id']
            history=api('/api/v1/history?monitor_id=browser-fixture')
            with sync_playwright() as browser_tools:
                browser=browser_tools.chromium.launch(executable_path='/usr/bin/google-chrome',headless=True,args=['--no-sandbox'])
                page=browser.new_page(viewport={'width':1440,'height':1080})
                errors=[]
                page.on('pageerror',lambda error:errors.append(str(error)))
                page.set_extra_http_headers({'Authorization':'Bearer '+token})
                page.goto(base+'/system')
                page.get_by_text('Persistence and latency targets',exact=True).wait_for()
                page.get_by_text('raft',exact=True).wait_for()
                page.screenshot(path=str(out/'system.png'),full_page=True)
                page.goto(base+'/monitors/'+str(ident))
                page.get_by_text('Event timeline',exact=True).wait_for()
                page.get_by_text('incident opened',exact=False).first.wait_for()
                page.screenshot(path=str(out/'timeline.png'),full_page=True)
                if errors:raise RuntimeError('browser errors: '+str(errors))
                browser.close()
            # Match the CLI's JSON response to the API event identities.
            cli=subprocess.check_output([str(pathlib.Path(a.client).resolve()),'--server',base,'get','history','browser-fixture','-o','json'],text=True,env=env)
            records=json.loads(cli)
            if [e['id'] for e in records['events']]!=[e['id'] for e in history['events']]:raise RuntimeError('CLI/API history differ')
            report={'status':'pass','browser_errors':errors,'numeric_monitor_id':ident,'stable_monitor_id':'browser-fixture','history_events':len(history['events']),'checks':counters['checks'],'notifications':counters['alerts'],'api_cli_agree':True}
            (out/'result.json').write_text(json.dumps(report,indent=2)+'\n')
    finally:
        if process is not None:
            process.send_signal(signal.SIGTERM)
            try:process.wait(timeout=30)
            except subprocess.TimeoutExpired:process.kill();process.wait()
        server.shutdown();server.server_close();thread.join()


if __name__=='__main__':main()
