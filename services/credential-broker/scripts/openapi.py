#!/usr/bin/env python3
"""Generate wire schemas from checked-in Go DTOs; route metadata is explicit."""
import argparse
import json
import re
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]

def ref(name):return {'$ref':'#/components/schemas/'+name}
def kind(t):
    if t.startswith('[]'):return {'type':'array','items':kind(t[2:])}
    if t.startswith('*'):return kind(t[1:])
    if t.startswith('map[string]'):return {'type':'object','additionalProperties':kind(t[11:])}
    if t=='string':return {'type':'string'}
    if t=='bool':return {'type':'boolean'}
    if t in ('int','uint64','int64'):return {'type':'integer'}
    if t=='time.Time':return {'type':'string','format':'date-time'}
    return ref(t.split('.')[-1])

def build():
    schemas={}
    for source in ['contract/contract.go','identity/identity.go','api/v1/types.go']:
        text=(ROOT/source).read_text()
        for name,body in re.findall(r'type (\w+) struct \{\n(.*?)\n\}',text,re.S):
            props={};required=[]
            for line in body.splitlines():
                m=re.match(r'\s*\w+\s+(\S+)\s+`json:"([^",]+)([^"]*)"`',line)
                if not m:continue
                typ,field,opts=m.groups();props[field]=kind(typ)
                if 'omitempty' not in opts:required.append(field)
            if props:
                schemas[name]={'type':'object','properties':props,'additionalProperties':False}
                if required:schemas[name]['required']=required
    for name in ['Claims']:
        schemas[name]['description']='Purpose-separated Ed25519 assertion, NOT JWT; exact signature contract in docs/architecture/api-contract.md.'
    schemas['Materialized']['description']='SENSITIVE: ENV contains plaintext; trusted runtime audience only. Never send to model or ToolHub job state.'
    schemas['Status']={'type':'object','properties':{'status':{'type':'string'}},'required':['status']}
    schemas['Renew']={'type':'object','properties':{'ttl_seconds':{'type':'integer','minimum':1,'maximum':300}},'additionalProperties':False}
    # Go zero values are intentionally accepted for these optional request fields.
    for name,optional in [('CreateRequest',{'owner_kind'}),('AcquireLease',{'ttl_seconds'}),('RuntimeRelease',{'checkpoint','quiesced'})]:
        schemas[name]['required']=[x for x in schemas[name].get('required',[]) if x not in optional]
        if not schemas[name]['required']:schemas[name].pop('required')
    paths={}
    def add(path,method,op,summary,output='Status',input=None,aud='broker:control',status=200):
        parameters=[{'in':'path','name':name,'required':True,'schema':{'type':'string'}} for name in re.findall(r'\{([^}]+)\}',path)]
        if path=='/v1/events':parameters.append({'in':'query','name':'after','required':False,'schema':{'type':'integer','minimum':0,'default':0}})
        schema={'type':'array','items':ref('Contract')} if output=='Contracts' else ref(output)
        o={'operationId':op,'summary':summary,'x-required-audience':aud,'security':[{'HubAssertion':[]}],
           'responses':{str(status):{'description':'Success','content':{'application/json':{'schema':schema}}},'default':{'description':'Sanitized API error','content':{'application/json':{'schema':ref('Error')}}}}}
        if parameters:o['parameters']=parameters
        if input:o['requestBody']={'required':True,'content':{'application/json':{'schema':ref(input)}}}
        paths.setdefault(path,{})[method]=o
    add('/v1/contracts','get','catalog','Reviewed contracts','Contracts')
    add('/v1/events','get','events','Owner-filtered audit events and global cursor','Events')
    add('/v1/requests','post','createRequest','Idempotent credential enrollment','Request','CreateRequest',status=201)
    add('/v1/requests/{request_id}','get','getRequest','Enrollment status','Request')
    add('/v1/requests/{request_id}/approve','post','approve','Approve exact browser pairing code',input='Approve',aud='broker:approve')
    add('/v1/requests/{request_id}/cancel','post','cancel','Cancel enrollment')
    add('/v1/credentials/{credential_id}','get','getCredential','Opaque credential metadata','Credential')
    add('/v1/credentials/{credential_id}','delete','deleteCredential','Delete managed secret versions; preserve imported source')
    add('/v1/credentials/{credential_id}/revoke','post','revokeCredential','Fence local grants/leases; does not revoke upstream provider token')
    add('/v1/credentials/{credential_id}/grants','post','createGrant','Explicit binding grant','Grant','GrantRequest',status=201)
    add('/v1/grants/{grant_id}/revoke','post','revokeGrant','Revoke a single binding grant')
    add('/v1/leases','post','acquire','Acquire a bounded revision-bound lease','Lease','AcquireLease',status=201)
    add('/v1/runtime/leases/{lease_id}','get','inspectLease','Inspect own runtime lease','Lease',aud='broker:runtime')
    add('/v1/runtime/leases/{lease_id}/materialize','post','materialize','SENSITIVE plaintext only to trusted runtime adapter','Materialized',aud='broker:runtime')
    add('/v1/runtime/leases/{lease_id}/renew','post','renew','Renew valid runtime lease','Lease','Renew',aud='broker:runtime')
    add('/v1/runtime/leases/{lease_id}/release','post','release','Optional quiesced checkpoint, then remove lease',input='RuntimeRelease',aud='broker:runtime')
    proxy='/v1/runtime/proxy/{lease_id}/{route_id}/{upstream_path}'
    for method in ['get','head','post','put','patch','delete']:
        paths.setdefault(proxy,{})[method]={'operationId':'proxy'+method.title(),'summary':'Fixed-destination restricted API proxy; route methods remain authoritative','x-required-audience':'broker:runtime','x-upstream-path-greedy':True,'security':[{'HubAssertion':[]}],
            'parameters':[{'in':'path','name':n,'required':True,'schema':{'type':'string'}} for n in ['lease_id','route_id','upstream_path']],
            'responses':{'default':{'description':'Bounded upstream response or sanitized broker error; redirects and secret echo denied','content':{'application/json':{'schema':{}},'application/octet-stream':{'schema':{'type':'string','format':'binary'}}}}}}
        if method not in ['get','head']:paths[proxy][method]['requestBody']={'required':False,'content':{'application/json':{'schema':{}},'application/octet-stream':{'schema':{'type':'string','format':'binary'}}}}
    paths['/healthz']={'get':{'operationId':'health','summary':'Storage readiness; Host/native TLS policy still applies','security':[],'responses':{'200':{'description':'Healthy'},'503':{'description':'Unavailable'}}}}
    for path,method,name,summary in [('/connect/{request_id}','get','form','Open owner-approved browser enrollment; link alone is insufficient'),('/connect/{request_id}/submit','post','submit','CSRF/Origin-bound multipart submission'),('/connect/{request_id}/oauth','post','startOAuth','CSRF/Origin-bound OAuth redirect'),('/oauth/callback','get','oauthCallback','One-use bound state plus same approved browser cookie')]:
        op={'operationId':name,'summary':summary,'security':[],'x-browser-security':'Secure HttpOnly SameSite=Lax __Host-cb_session; separate signed pairing approval; POST exact Origin + CSRF','responses':{'200':{'description':'HTML result'},'303':{'description':'Reviewed OAuth redirect or continuation'},'default':{'description':'Sanitized error'}}}
        if '{request_id}' in path:op['parameters']=[{'in':'path','name':'request_id','required':True,'schema':{'type':'string'}}]
        paths[path]={method:op}
    return {'openapi':'3.1.0','info':{'title':'Hermes Credential Broker','version':'0.1.0','description':'Reviewed credential contracts, owner-bound forms, replaceable providers, grants and isolated runtime delivery. No canonical secret-store requirement.'},'servers':[{'url':'https://credentials.example.com:8443'}],
        'components':{'securitySchemes':{'HubAssertion':{'type':'http','scheme':'bearer','bearerFormat':'purpose-separated Ed25519 assertion v1 (not JWT)'}},'schemas':schemas},'paths':paths}

def main():
    ap=argparse.ArgumentParser();ap.add_argument('--check',action='store_true');a=ap.parse_args()
    target=ROOT/'api/openapi.json';text=json.dumps(build(),ensure_ascii=False,indent=2)+'\n'
    if a.check:
        if not target.exists() or target.read_text()!=text:raise SystemExit('OpenAPI drift; run just api-fix')
        print('OpenAPI DTO and route description drift check passed.')
    else:target.write_text(text)
if __name__=='__main__':main()
