"""Black-box conformance scenarios. A failed assertion is a nonzero exit."""
import argparse
import http.client
import json
import re
import shlex
from tempfile import TemporaryDirectory
import time
from contextlib import ExitStack
from pathlib import Path
from urllib.parse import quote
from urllib.request import Request, urlopen
from compare import get, ready, process


def call(url, body=None, rank=None, method=None):
    data = None if body is None else json.dumps(body).encode()
    headers = {"Content-Type": "application/json"}
    if rank is not None:
        headers["X-Data-Parallel-Rank"] = str(rank)
    req = Request(url, data, headers, method=method)
    with urlopen(req, timeout=6) as res:
        raw=res.read()
        return json.loads(raw) if raw else None


def until(check, timeout=10):
    deadline=time.monotonic()+timeout
    while True:
        last=check()
        if last:
            return last
        if time.monotonic()>deadline:
            raise AssertionError("condition timed out: "+check.__name__)
        time.sleep(.05)


def state(worker,rank=0):
    return call(worker+"/lab/state",rank=rank)


def control(worker, action, rank=0):
    return call(worker+"/lab/"+action,rank=rank,method="POST")


def body(tokens,stream=False,n=1):
    return {"model":"lab","prompt":tokens,"max_tokens":n,"stream":stream,"temperature":0}


def complete(target,tokens,rank=None):
    return call(target+"/v1/completions",body(tokens),rank)


def metric(metrics):
    text=get(metrics+"/metrics")
    m=re.search(r'^vllm_router_kv_placement_lookups_total\{[^}]*result="hit"[^}]*\} ([\d.e+E-]+)',text,re.M)
    return float(m[1]) if m else 0


def has_hit(target,metrics,tokens):
    before=metric(metrics)
    res=complete(target,tokens)
    return metric(metrics)>before,res


def kv(worker,target,metrics,issue,log,observed):
    prompts=[list(range(offset,offset+129)) for offset in (1000,4000)]
    for r,tokens in enumerate(prompts):
        complete(worker,tokens,r)
        control(worker,"freeze?value=1",r)
    for r,tokens in enumerate(prompts):
        def warmed():
            prev=state(worker,r)
            hit,res=has_hit(target,metrics,tokens)
            after=state(worker,r)
            return hit and after["received"]==prev["received"]+1 and after["hits"]>prev["hits"]
        until(warmed)
    log.append("separate warm prefixes reached their actual ranks with Router credit")
    if issue:
        before=state(worker,1)["metrics_reads"]
        control(worker,"algorithm?value=sha256",1)
        # Check after a fresh metadata observation. Keep polling through reconciliation.
        def revoked():
            good,_=has_hit(target,metrics,prompts[0])
            bad,_=has_hit(target,metrics,prompts[1])
            after = state(worker,1)["metrics_reads"]
            observed.update(metrics_reads_before=before, metrics_reads_after=after,
                            rank0_prefix_lookup_hit=good, rank1_prefix_lookup_hit=bad)
            return after>before and good and not bad
        until(revoked,45)
        # With one competing long request on rank 0, the ineligible cache rank can still serve.
        req=Request(target+"/v1/completions",json.dumps(body(prompts[0],True,300)).encode(),{"Content-Type":"application/json"})
        with urlopen(req,timeout=8) as stream:
            stream.readline()
            until(lambda:state(worker,0)["active"]>0)
            before=state(worker,1)["received"]
            until(lambda: (complete(target,[65001]),state(worker,1)["received"]>before)[1],2)
        log.append("rank 1 lost cache credit and remained routable; rank 0 retained credit")
        return
    before=state(worker,1)["replays"]
    control(worker,"drop-next",1)
    control(worker,"clear",1)
    control(worker,"freeze?value=0",1)
    complete(worker,list(range(10000,10129)),1)
    control(worker,"freeze?value=1",1)
    until(lambda:state(worker,1)["replays"]>before and not has_hit(target,metrics,prompts[1])[0])
    assert has_hit(target,metrics,prompts[0])[0], "loss on rank 1 cleared rank 0"
    assert has_hit(target,metrics,list(range(10000,10129)))[0], "new rank 1 prefix missing after gap"
    log.append("missing clear recovered via replay; healthy rank retained credit")


def streams(worker,target,log):
    tokens=list(range(65))
    pre=state(worker)
    req=Request(target+"/v1/completions",json.dumps(body(tokens,True,300)).encode(),{"Content-Type":"application/json"})
    with urlopen(req,timeout=8) as stream:
        chunks=0
        for line in stream:
            if line.startswith(b"data:") and b"[DONE]" not in line:
                chunks+=1
                if chunks==3: break
        assert chunks==3
    until(lambda:state(worker)["canceled"]>pre["canceled"] and state(worker)["active"]==0,4)
    control(worker,"cut?value=3")
    received=b""
    try:
        with urlopen(req,timeout=8) as stream:
            for line in stream:
                received+=line
    except (http.client.IncompleteRead, ConnectionResetError):
        pass
    assert b"[DONE]" not in received and state(worker)["broken"]>pre["broken"]
    until(lambda:state(worker)["active"]==0)
    assert complete(target,tokens)["choices"]
    log.append("client cancel and upstream cut released slots; next completion succeeded")


def run(a):
    out=Path(a.results);out.mkdir(parents=True,exist_ok=True)
    w=f"http://127.0.0.1:{a.base}";target=f"http://127.0.0.1:{a.port}";metrics=f"http://127.0.0.1:{a.port+1}"
    dual=a.case in ("kv","619")
    lab=[a.lab,"fleet","-workers","1","-ranks","2" if dual else "1","-port",str(a.base),"-decode","30ms"]
    if a.case=="619":lab+=["-metrics-only"]
    router=shlex.split(a.router)+["--worker-urls",w,"--host","127.0.0.1","--port",str(a.port),"--prometheus-port",str(a.port+1),"--policy","kv_aware" if dual else "round_robin","--log-level","debug"]
    if dual:router+=["--intra-node-data-parallel-size","2"]
    passed=[]
    observed={}
    if a.worker_version:
        lab+=["-vllm-version",a.worker_version]
        if a.hash_seed is not None:
            lab+=["-hash-seed",a.hash_seed]
    try:
        with ExitStack() as stack:
            proc=stack.enter_context(process(lab,out/(a.case+".fleet.log")));ready(w+"/health",proc)
            if dual and a.worker_version:
                # Only the harness can read /lab/event-sources. Older vLLM tags need
                # explicit Router publisher addresses; newer tags discover them.
                from urllib.error import HTTPError
                try:
                    get(w+"/kv_event_sources")
                except HTTPError as e:
                    if e.code != 404: raise
                    feeds=call(w+"/lab/event-sources")
                    router+=["--kv-event-model","lab"]
                    for flag,field in [("--kv-event-source","endpoint"),("--kv-event-replay-source","replay_endpoint"),("--kv-event-topic","topic")]:
                        router.extend([flag,w+"="+",".join(feeds[str(i)][field] for i in range(2))])
                if a.hash_seed is not None:
                    tmp=Path(stack.enter_context(TemporaryDirectory()))/"layout.json"
                    ranks=[{"rank":i,"model":"lab","hash_block_size":16,"hash_seed":a.hash_seed,"declared_groups":1,"speculative_tokens":0,"groups":[{"kind":"full_attention","block_size":16}]} for i in range(2)]
                    tmp.write_text(json.dumps({w:{"ranks":ranks}}))
                    router+=["--kv-hybrid-layout",str(tmp)]
            route=stack.enter_context(process(router,out/(a.case+".router.log")));ready(target+"/health",route)
            if dual:kv(w,target,metrics,a.case=="619",passed,observed)
            elif a.case=="stream":streams(w,target,passed)
            else:
                other=f"http://127.0.0.1:{a.base+1}";cmd=[a.lab,"fleet","-port",str(a.base+1),"-workers","1"]
                for i in range(2):
                    with process(cmd,out/("epoch-"+str(i)+".log")) as added:
                        ready(other+"/health",added)
                        info=state(other)
                        if i:assert info["instance"]!=old and info["hits"]==0
                        old=info["instance"]
                        call(target+"/workers",{"url":other})
                        until(lambda:(complete(target,list(range(65))),state(other)["received"]>0)[1])
                        call(target+"/workers/"+quote(other,safe=""),method="DELETE")
                        before=state(other)["received"]
                        for _ in range(5):complete(target,list(range(65)))
                        assert state(other)["received"]==before
                passed.append("scale out/in and same-address restart with new worker identity")
    except Exception as e:
        report={"case":a.case,"router":a.version,"worker_version":a.worker_version,"observed":observed,"passed":passed,"status":"failed","error":repr(e)}
        (out/(a.case+".json")).write_text(json.dumps(report,indent=2)+"\n")
        raise
    report={"case":a.case,"router":a.version,"worker_version":a.worker_version,"observed":observed,"passed":passed,"status":"passed"}
    (out/(a.case+".json")).write_text(json.dumps(report,indent=2)+"\n")
    print(report)


if __name__=="__main__":
    p=argparse.ArgumentParser()
    p.add_argument("case",choices=["kv","619","stream","scale"])
    p.add_argument("--router",default="vllm-router")
    p.add_argument("--lab",default="./routerlab")
    p.add_argument("--version",required=True)
    p.add_argument("--worker-version")
    p.add_argument("--hash-seed")
    p.add_argument("--port",type=int,default=30100)
    p.add_argument("--base",type=int,default=18100)
    p.add_argument("--results",default="results/regression")
    run(p.parse_args())
