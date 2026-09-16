#!/usr/bin/env python3
"""Negate each new branch guard, relax each loop bound, and run its paired controls.

Run from the repository with an executable TMPDIR/GOTMPDIR. Each mutation and
restored control retains its actual command, exit code and redacted output.
No live service is invoked: the suite uses synthetic native protocol fixtures.
"""
import argparse,hashlib,json,os,shlex,subprocess,tempfile,time
from pathlib import Path

ROOT=Path(__file__).resolve().parents[1]
OUT=ROOT/'.llm/runs/native-dispatch-goals--381'
args=argparse.ArgumentParser();args.add_argument('--only');args.add_argument('--attempt',default='2');opts=args.parse_args()
OUT=OUT/('mutation-attempt-'+opts.attempt)
OUT.mkdir(parents=True,exist_ok=True)
CATALOG_SOURCE='package main\nimport("encoding/json";"go/ast";"go/parser";"go/token";"os")\nfunc main(){out:=[]map[string]any{};for _,name:=range []string{"cmd/divybot/native_goal.go","cmd/divybot/native_goal_rpc.go"}{b,e:=os.ReadFile(name);if e!=nil{panic("source unavailable")};set:=token.NewFileSet();f,e:=parser.ParseFile(set,name,b,0);if e!=nil{panic("AST unavailable")};for _,d:=range f.Decls{fn,ok:=d.(*ast.FuncDecl);if !ok || fn.Body==nil{continue};ast.Inspect(fn.Body,func(n ast.Node)bool{var cond ast.Expr;switch v:=n.(type){case *ast.IfStmt:cond=v.Cond;case *ast.ForStmt:cond=v.Cond};if cond==nil{return true};a,z:=set.Position(cond.Pos()).Offset,set.Position(cond.End()).Offset;out=append(out,map[string]any{"file":name,"function":fn.Name.Name,"start":a,"end":z,"condition":string(b[a:z]),"loop":func()bool{_,ok:=n.(*ast.ForStmt);return ok}()});return true})}};json.NewEncoder(os.Stdout).Encode(out)}\n'
FILTERS={
 'parseGoalBudget':'TestNativeGoalBudget', 'nativeGoalIntent':'TestNativeGoalObjective',
 'acquireNativeGoalBinding':'TestNativeGoalBindingAcquisition',
 'bindDispatchGoal':'TestNativeGoalStartOwnershipAndFailures',
 'readGoalIdentity':'TestNativeGoalPrivateBindingRead|TestNativeGoalFrameAndLoopBounds',
 'createDispatchGoal':'TestNativeGoalCreation|TestNativeGoalTransport',
 'transitionDispatchGoal':'TestNativeGoalTransition',
 'startDispatchGoal':'TestNativeGoalStartOwnershipAndFailures|TestNativeGoalUnownedAndDryWritesRefused',
 'closedGoalReason':'TestNativeGoalWaitAndClosedReason|TestNativeGoalUnownedAndDryWritesRefused',
 'transitionGoal':'TestNativeGoalWrappedTransition|TestNativeGoalUnownedAndDryWritesRefused',
 'assignmentGoalStatus':'TestNativeGoalAssignmentStatus',
 'finishAssignmentGoal':'TestNativeGoalFinishAssignment|TestNativeGoalUnownedAndDryWritesRefused',
 'send':'TestNativeGoalRPCGuards|TestNativeGoalCreation',
 'frame':'TestNativeGoalRPCGuards|TestNativeGoalCreation|TestNativeGoalFrameAndLoopBounds',
 'notification':'TestNativeGoalNotificationGuards|TestNativeGoalCreation',
 'request':'TestNativeGoalRPCGuards|TestNativeGoalCreation|TestNativeGoalFrameAndLoopBounds',
 'decodeGoal':'TestNativeGoalDecodeGuards',
 'get':'TestNativeGoalRPCGuards|TestNativeGoalCreation',
 'set':'TestNativeGoalNotificationGuards|TestNativeGoalCreation|TestNativeGoalFrameAndLoopBounds',
 'initialize':'TestNativeGoalTransport', 'withGoalConnection':'TestNativeGoalTransport',
}
def redact(text):
 for p in sorted({str(ROOT),str(Path.home()),os.environ.get('TMPDIR',''),os.environ.get('GOTMPDIR','')},key=len,reverse=True):
  if p:text=text.replace(p,'<private>')
 return text

def check(pattern):
 command=['go','test','./cmd/divybot','-run','^('+pattern+')$','-count=1','-timeout=30s']
 try:
  p=subprocess.run(command,cwd=ROOT,capture_output=True,text=True,timeout=45)
  return {'command':shlex.join(command),'exit_code':p.returncode,'output':redact(p.stdout+p.stderr)}
 except subprocess.TimeoutExpired:
  return {'command':shlex.join(command),'exit_code':124,'output':'INCONCLUSIVE: test process exceeded the outer bound'}

with tempfile.TemporaryDirectory() as scratch:
 source=Path(scratch)/'catalog.go';source.write_text(CATALOG_SOURCE)
 p=subprocess.run(['go','run',str(source)],cwd=ROOT,capture_output=True,text=True)
 if p.returncode:raise SystemExit('guard catalog failed')
 guards=json.loads(p.stdout)
original={name:(ROOT/name).read_text() for name in {g['file'] for g in guards}}
for g in guards:
 g['replacement']=g['condition'].replace('< 128','< 256').replace('< 40','< 41') if g['loop'] else '!('+g['condition']+')'
 g['filter']=FILTERS[g['function']]

def extra(file,needle,replacement,pattern,label):
 text=original.setdefault(file,(ROOT/file).read_text())
 if text.count(needle)!=1:raise SystemExit('mutation anchor is not unique: '+label)
 start=text.index(needle)
 guards.append({'file':file,'function':label,'condition':needle,'start':start,'end':start+len(needle),'replacement':replacement,'filter':pattern})
extra('cmd/divybot/native_goal.go','return utf8.ValidString(s) && strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= 4000 && !strings.ContainsRune(s, 0)','return true || (utf8.ValidString(s) && strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= 4000 && !strings.ContainsRune(s, 0))','TestNativeGoalObjective','objective-validation')
extra('cmd/divybot/native_goal_rpc.go','return n >= 0 && n <= maxGoalNumber','return true','TestNativeGoalDecodeGuards','counter-validation')
extra('cmd/divybot/native_goal_rpc.go','return g != nil && g.Objective == i.Objective && reflect.DeepEqual(g.TokenBudget, i.TokenBudget)','return true','TestNativeGoalTransition|TestNativeGoalNotificationGuards','goal-ownership')
extra('cmd/divybot/native_goal_rpc.go','scan.Buffer(make([]byte, 4096), 1048577)','scan.Buffer(make([]byte, 4096), 2097154)','TestNativeGoalFrameAndLoopBounds','frame-bound')
extra('cmd/divybot/native_goal.go','if e := r.writeNativeIdentity(&id); e != nil {','if e := error(nil); e != nil {','TestNativeGoalBindingAcquisition','binding-write')
extra('cmd/divybot/native_goal.go','scale = 1000\n','scale = 100\n','TestNativeGoalBudget','k-unit')
extra('cmd/divybot/native_goal.go','scale = 1000000\n','scale = 10000\n','TestNativeGoalBudget','m-unit')
extra('cmd/divybot/native_goal.go','map[string]any{"threadId": p.thread, "status": status}','map[string]any{"threadId": p.thread, "status": status, "objective": intent.Objective}','TestNativeGoalTransition','status-only')
extra('cmd/divybot/main.go','c.startDispatchGoal(ctx, host, j, receipt)','_ = receipt','TestNativeGoalDispatchWiring','launch-hook')
extra('cmd/divybot/main.go','c.finishAssignmentGoal(ctx, jc)','_ = jc','TestNativeGoalDispatchWiring','completion-hook')
extra('cmd/divybot/main.go','c.transitionGoal(ctx, j, "paused")','_ = j','TestNativeGoalDispatchWiring','deadline-hook')
extra('cmd/divybot/main.go','c.transitionGoal(ctx, j, "blocked")','_ = j','TestNativeGoalDispatchWiring','blocked-hook')
extra('cmd/divybot/main.go','c.transitionGoal(ctx, d.job, "blocked")','_ = d.job','TestNativeGoalDispatchWiring','abandonment-hook')
extra('cmd/divybot/overrides.go','emptyGoalBudgetKey.MatchString(t)','false','TestNativeGoalBudgetPresence','empty-budget-presence')


extra('cmd/divybot/matrix_diagnostics.go',' || r.ReasonCode == "goal-prompt-delivery-failed"','','TestNativeGoalRefusalDoesNotDenyAnExistingLaunch','uncertain-launch-comment')

extra('cmd/divybot/main.go','if agent == "codex" {\n\t\tvar e error','if agent != "codex" {\n\t\tvar e error','TestNativeGoalDispatchPreflight','spawn-transport-preflight')
extra('cmd/divybot/main.go','if e != nil {\n\t\t\treturn matrixReason(closedGoalReason(e))','if e == nil {\n\t\t\treturn matrixReason(closedGoalReason(e))','TestNativeGoalDispatchPreflight','spawn-intent-preflight')
extra('cmd/divybot/matrix.go','if agent == "codex" {\n\t\tassignment := is','if agent != "codex" {\n\t\tassignment := is','TestCommonMatrixAttempt','matrix-transport-preflight')
extra('cmd/divybot/matrix.go','nativeGoalIntent(c.cfg.Inbox, assignment, o); e != nil','nativeGoalIntent(c.cfg.Inbox, assignment, o); e == nil','TestCommonMatrixAttempt','matrix-intent-preflight')
extra('cmd/divybot/native_goal_rpc.go','\treturn false\n}\n\nconst maxGoalNumber','\treturn true\n}\n\nconst maxGoalNumber','TestNativeGoalDecodeGuards','unknown-status')
extra('cmd/divybot/native_goal.go','case <-ctx.Done():\n\t\treturn false','case <-ctx.Done():\n\t\treturn true','TestNativeGoalWaitAndClosedReason|TestNativeGoalUnownedAndDryWritesRefused','cancelled-binding-wait')
extra('cmd/divybot/native_goal.go','case <-t.C:\n\t\treturn true','case <-t.C:\n\t\treturn false','TestNativeGoalWaitAndClosedReason','completed-binding-wait')
extra('cmd/divybot/native_goal_rpc.go','defer children.hold()()','// mutation removes owned child protection','TestReaperEveryExecSiteHoldsGate','child-reaper-protection')
extra('cmd/divybot/overrides.go','o.MaxTokens = val\n\t\t\t\t\to.MaxTokensPresent = true','o.MaxTokens = val','TestNativeGoalBudgetPresence','comment-empty-budget')

extra('cmd/divybot/main.go','if agent == "codex" {\n\t\tj.NativeGoal =','if agent != "codex" {\n\t\tj.NativeGoal =','TestNativeGoalLaunchOwnershipWiring','goal-ownership-transport')
extra('cmd/divybot/main.go','return matrixReason("goal-prompt-delivery-failed")','// mutation allows goal creation after failed prompt delivery','TestNativeGoalLaunchOwnershipWiring','prompt-failure-fence')

extra('cmd/divybot/native_goal_rpc.go','p.updates = nil','// mutation retains stale pre-write notifications','TestNativeGoalFreshWriteNotification','fresh-write-notification')

selected=set(int(x) for x in opts.only.split(',')) if opts.only else set(range(1,len(guards)+1))
results=[]
try:
 for index,g in enumerate(guards,1):
  if index not in selected:continue
  path=ROOT/g['file'];source=original[g['file']]
  before=check(g['filter'])
  if before['exit_code']!=0:raise RuntimeError('baseline control failed')
  try:
   path.write_text(source[:g['start']]+g['replacement']+source[g['end']:])
   mutated=check(g['filter'])
  finally:path.write_text(source)
  restored=check(g['filter'])
  code=mutated['exit_code'];output=mutated['output']
  verdict='SURVIVED' if code==0 else 'INCONCLUSIVE' if code==124 or '[build failed]' in output else 'KILLED'
  record={'id':index,'file':g['file'],'function':g['function'],'condition':g['condition'],'replacement':g['replacement'],'verdict':verdict,'baseline':before,'mutation':mutated,'restored':restored}
  results.append(record);(OUT/f'mutation-{index:03}.json').write_text(json.dumps(record,indent=2)+'\n')
  print(json.dumps({'id':index,'verdict':verdict,'restored_exit_code':restored['exit_code']}),flush=True)
  if restored['exit_code']!=0:break
finally:
 for name,text in original.items():(ROOT/name).write_text(text)
summary={'planned':len(selected),'executed':len(results),'killed':sum(r['verdict']=='KILLED' for r in results),'survivors':sum(r['verdict']=='SURVIVED' for r in results),'inconclusive':sum(r['verdict']=='INCONCLUSIVE' for r in results),'restored_controls':sum(r['restored']['exit_code']==0 for r in results),'broken_controls':sum(r['restored']['exit_code']!=0 for r in results)}
(OUT/'mutation-summary.json').write_text(json.dumps(summary,indent=2)+'\n');print(json.dumps(summary),flush=True)
raise SystemExit(0 if summary['executed']==summary['planned'] and summary['survivors']==summary['inconclusive']==summary['broken_controls']==0 else 2)
