package main

import (
	"context"
	"reflect"
	"strconv"
	"time"
)

// Pinned native summary/Step wire definitions also consumed by Harness's AGY
// reader. Decode only terminal authority; prompts, thinking and text stay private.
// SQL observes native WAL, bounds input before reading blobs, then reopens the
// summaries so a resumed turn cannot certify an earlier stop.
const agyCompletionQuery = agyNativeMetadataQuery + `
 now=int(sys.argv[2]); budget=4194304
 def fields(data):
  assert isinstance(data,bytes) and len(data)<=1048576
  cursor=0;result={};seen=set()
  def integer():
   nonlocal cursor
   value=0
   for i in range(10):
    byte=data[cursor];cursor+=1
    assert i!=9 or byte<=1
    value|=(byte&127)<<(7*i)
    if byte<128:
     assert value<=9007199254740991
     return value
   raise ValueError()
  count=0
  while cursor<len(data):
   count+=1;assert count<=4096
   tag=integer();number=tag>>3;wire=tag&7
   assert 0<number<=536870911
   if wire==0:value=integer()
   elif wire==2:
    length=integer();assert cursor+length<=len(data)
    value=data[cursor:cursor+length];cursor+=length
   elif wire in (1,5):
    length=8 if wire==1 else 4;assert cursor+length<=len(data)
    cursor+=length;value=None
   else:raise ValueError()
   if number in seen:result[number]=None
   else:result[number]=value;seen.add(number)
  return result
 def number(f,k,default=0):
  v=f.get(k,default);assert type(v)==int
  return v
 def nested(f,k):
  v=f.get(k);assert isinstance(v,bytes)
  return fields(v)
 def clock(f,k):
  if k not in f:return None
  v=nested(f,k);sec=number(v,1);nano=number(v,2)
  at=sec*1000+nano//1000000
  assert sec>0 and 0<=nano<=999999999 and at<=now
  return at
 def snapshot():
  db=connect(summary)
  try:
   db.execute('BEGIN')
   rows=db.execute('SELECT conversation_id,parent_conversation_id,step_count,not_fully_idle,killed,CASE WHEN length(raw_summary)<=1048576 THEN raw_summary ELSE NULL END FROM conversation_summaries ORDER BY conversation_id LIMIT 21').fetchall()
   assert 0<len(rows)<=20
   return rows
  finally:db.close()
 before=snapshot();selected={identity}
 for _ in before:
  selected.update(r[0] for r in before if r[1] in selected)
 assert len(selected)==len(before)
 complete=True
 for cid,parent,count,notidle,killed,raw in before:
  assert re.fullmatch(r'[a-f0-9]{8}-(?:[a-f0-9]{4}-){3}[a-f0-9]{12}',cid)
  assert parent in (None,'') if cid==identity else parent in selected and parent!=cid
  assert type(count)==int and 0<count<=4096
  native=fields(raw);budget-=len(raw)
  db=connect(directory/(cid+'.db'))
  try:
   db.execute('BEGIN')
   identity_rows=db.execute('SELECT trajectory_id,cascade_id FROM trajectory_meta LIMIT 2').fetchall()
   assert len(identity_rows)==1 and identity_rows[0][1]==cid
   trajectory=identity_rows[0][0]
   assert re.fullmatch(r'[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}',trajectory)
   assert native.get(4)==trajectory.encode() and number(native,2)==count
   size=db.execute('SELECT count(*),coalesce(sum(length(metadata)+length(step_payload)+coalesce(length(error_details),0)),0),coalesce(max(length(metadata)),0),coalesce(max(length(step_payload)),0),coalesce(max(length(error_details)),0) FROM (SELECT metadata,step_payload,error_details FROM steps ORDER BY idx LIMIT 4097)').fetchone()
   assert size[0]==count and size[1]<=budget and all(n<=1048576 for n in size[2:])
   budget-=size[1]
   rows=db.execute('SELECT idx,step_type,status,metadata,error_details,step_payload,step_format FROM steps ORDER BY idx LIMIT 4097').fetchall()
  finally:db.close()
  started=clock(native,7);assert started is not None
  updated=started;user=-1;pending=False;final=None
  for index,(idx,kind,status,metadata,error,payload,fmt) in enumerate(rows):
   assert idx==index and fmt==0 and status in (0,1,2,3,4,5,6,7,8,9,11,12)
   frame=fields(payload);header=fields(metadata)
   assert number(frame,1,-1)==kind and number(frame,4,-1)==status
   assert kind!=15 or 5 in frame
   assert 5 not in frame or frame[5]==metadata
   at=clock(header,1);end=clock(header,8);recent=clock(header,22)
   assert at is not None and at>=started and (end is None or end>=at)
   updated=max(updated,recent if recent is not None else end if end is not None else at)
   if kind==14:user=index;pending=False;final=None
   if user>=0 and status in (1,2,8,9,11):pending=True
   text=b'';stop=0
   if kind==15 and 20 in frame:
    response=nested(frame,20);text=response.get(8,response.get(1,b''))
    assert isinstance(text,bytes)
    text=text.decode('utf-8');stop=number(response,12)
   if user>=0:final=None if end is None else (end,status,kind,bool(text.strip()),stop,bool(error))
  assert number(native,16)==user
  modified=clock(native,3);assert modified is not None and modified>=updated
  idle=number(native,5)==1 and number(native,21)==0 and number(native,18)==0 and notidle==0 and killed==0
  safe=idle and number(native,23)==0 and number(native,25)==0 and not pending and final is not None
  safe=safe and final[0]>=updated and final[1:]==(3,15,True,2,False)
  complete=complete and safe
 assert snapshot()==before
 print(json.dumps({'id':identity,'completed':complete}))
` + agyNativeQueryErrors

func (h Host) readAGYCompletion(ctx context.Context, store *nativeStore, native string) (bool, error) {
	if ctx.Err() != nil || store == nil || store.Source != "agy" || !agyTrustScope(store.Directory) || !agyConversationID.MatchString(native) {
		return false, errMatrix
	}
	raw, err := h.agyTrustCommand(ctx, "exec python3 -c "+shq(agyCompletionQuery)+" "+shq(store.Directory)+" "+strconv.FormatInt(time.Now().UnixMilli(), 10)+" 2>/dev/null")
	var proof struct {
		ID        string `json:"id"`
		Completed bool   `json:"completed"`
	}
	if err != nil || ctx.Err() != nil || len(raw) > 1024 || strictJSON(raw, &proof) != nil || proof.ID != native {
		return false, errMatrix
	}
	return proof.Completed, nil
}

func (c *Coord) agyCompletionEvidence(ctx context.Context, h Host, j *Job, native string) (bool, error) {
	return c.agyCompletionWithObserver(ctx, j, native, h.agentInfoOf, h.readAGYCompletion)
}

func (c *Coord) agyCompletionWithObserver(ctx context.Context, j *Job, native string,
	agent func(context.Context, string) (AgentInfo, error), observe func(context.Context, *nativeStore, string) (bool, error)) (bool, error) {
	if ctx.Err() != nil || !agyBindingEligible(j) || !agyConversationID.MatchString(native) {
		return false, errMatrix
	}
	owner, err := configuredReceiptOwner(c.cfg.Matrix)
	if err != nil {
		return false, errMatrix
	}
	read := func() (*durableMatrixReceipt, string, *nativeStore, error) {
		r, id, err := loadNativeBindingReceipt(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, j, c.cfg.Inbox, owner, "agy")
		if err != nil || id != native || r.dispatch.Host != j.Host || r.dispatch.Location == nil || r.dispatch.Location.PaneID != j.Pane || r.dispatch.Location.WorkspaceID != j.Workspace {
			return nil, "", nil, errMatrix
		}
		digest, err := readAGYBindingAuthority(r, j)
		if err != nil {
			return nil, "", nil, err
		}
		store, err := readAGYStoreBinding(r)
		return r, digest, store, err
	}
	r, digest, store, err := read()
	if err != nil {
		return false, err
	}
	before, err := agent(ctx, j.Pane)
	if err != nil || ctx.Err() != nil || !agyIdentityOccupant(before, j) || before.AgentStatus != "done" {
		return false, errMatrix
	}
	directory, err := agyStoreDirectory(before.Cwd, r.dispatch.RunID)
	if err != nil || store.Directory != directory {
		return false, errMatrix
	}
	complete, err := observe(ctx, store, native)
	if err != nil || !complete || ctx.Err() != nil || !agyBindingEligible(j) {
		return false, errMatrix
	}
	after, err := agent(ctx, j.Pane)
	if err != nil || ctx.Err() != nil || !agyIdentityOccupant(after, j) || after.AgentStatus != "done" || before.Cwd != after.Cwd || before.StateChangeSeq != after.StateChangeSeq {
		return false, errMatrix
	}
	verified, currentDigest, currentStore, err := read()
	if err != nil || ctx.Err() != nil || currentDigest != digest || !reflect.DeepEqual(r.dispatch, verified.dispatch) || !reflect.DeepEqual(store, currentStore) {
		return false, errMatrix
	}
	return true, nil
}
