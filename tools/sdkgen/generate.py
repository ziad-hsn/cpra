#!/usr/bin/env python3
"""Reproducible local SDK generation. --check never changes tracked outputs."""
import argparse,hashlib,json,os,pathlib,subprocess,tempfile
ROOT=pathlib.Path(__file__).resolve().parents[2]
GO=os.environ.get('GO','go'); TOOL=ROOT/'tools/sdkgen'
args=argparse.ArgumentParser();args.add_argument('--check',action='store_true');args=args.parse_args()
env=dict(os.environ,GOTOOLCHAIN='local',GOWORK='off')
def run(*cmd):subprocess.run(cmd,cwd=TOOL,env=env,check=True)
def digest(p):return hashlib.sha256(p.read_bytes()).hexdigest()
outputs={}
with tempfile.TemporaryDirectory(prefix='cpra-sdkgen-') as td:
 t=pathlib.Path(td)
 for enabled in (False,True):
  models=json.loads((ROOT/'api/openapi/models.base.json').read_text());contract=json.loads((ROOT/'api/openapi/contract.base.json').read_text())
  if enabled:
   ext=json.loads((ROOT/'api/openapi/externaljobs.json').read_text());models['components']['schemas'].update(ext['schemas']);contract['paths'].update(ext['paths'])
  (t/'models.json').write_text(json.dumps(models,sort_keys=True));(t/'contract.json').write_text(json.dumps(contract,sort_keys=True))
  variant='external' if enabled else 'base';tag='externaljobs' if enabled else '!externaljobs'
  for package,source,kind in [('api','models.json','models'),('transport','contract.json','client')]:
   target=ROOT/'sdk/go'/('api' if package=='api' else 'internal/transport')/('generated_'+variant+'.go')
   config={'package':package,'generate':{kind:True},'output-options':{'skip-prune':True},'output':str(t/'output.go')}
   if package=='transport':config['generate']['models']=True;config['import-mapping']={'./models.json':'github.com/ziad-hsn/cpra/sdk/go/api'}
   (t/'config.json').write_text(json.dumps(config))
   run(GO,'run','-mod=readonly','github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen','-config',str(t/'config.json'),str(t/source))
   generated=(t/'output.go').read_text()
   if package=='api':
    # The handwritten doc.go owns the public package overview. Retain the
    # generated-code marker without concatenating a second package comment.
    header='// Package api provides primitives to interact with the openapi HTTP API.\n//\n'
    if not generated.startswith(header):raise SystemExit('Unexpected generated api package header')
    generated=generated[len(header):].replace(' DO NOT EDIT.\npackage api',' DO NOT EDIT.\n\npackage api',1)
   outputs[target]='//go:build '+tag+'\n\n'+generated
  contract['components']=models['components']
  raw=json.dumps(contract,sort_keys=True,indent=2).replace('./models.json#/components/','#/components/')+'\n'
  outputs[ROOT/'sdk/go/api'/('schema_'+variant+'.json')]=raw
  outputs[ROOT/'sdk/go/api'/('schema_'+variant+'.go')]='//go:build '+tag+'\n\npackage api\n\nimport _ "embed"\n\n//go:embed schema_'+variant+'.json\nvar schemaBytes []byte\n\n// Schema returns an independent copy of this build\'s OpenAPI contract.\nfunc Schema() []byte { return append([]byte(nil), schemaBytes...) }\n'
outputs[ROOT/'sdk/go/testdata/operations.json']=(ROOT/'api/openapi/operations.json').read_text()
metadata={'generator':'github.com/oapi-codegen/oapi-codegen/v2@v2.8.0','runtime':'github.com/oapi-codegen/runtime@v1.6.0','inputs':{str(p.relative_to(ROOT)):digest(p) for p in sorted([*(ROOT/'api/openapi').glob('*.json'),TOOL/'go.mod',TOOL/'go.sum',TOOL/'generate.py'])},'outputs':{str(p.relative_to(ROOT)):hashlib.sha256(v.encode()).hexdigest() for p,v in sorted(outputs.items())}}
outputs[ROOT/'sdk/go/generation.json']=json.dumps(metadata,sort_keys=True,indent=2)+'\n'
changed=[]
for p,content in outputs.items():
 if not p.exists() or p.read_text()!=content:
  changed.append(str(p.relative_to(ROOT)))
  if not args.check:p.write_text(content)
if args.check and changed:raise SystemExit('Regeneration differs: '+', '.join(changed))
print('Verified' if args.check else 'Generated',len(outputs),'SDK outputs from pinned OpenAPI inputs.')
