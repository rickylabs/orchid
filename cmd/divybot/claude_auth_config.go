package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

// The canonical login owns account metadata, not the host owner's Remote
// Control consent, workspace trust or UI history. Missing choices stay absent.
func claudeAccountMetadata(raw []byte) ([]byte, error) {
	var config map[string]json.RawMessage
	if len(raw) > 1048576 || strictJSON(raw, &config) != nil || config == nil {
		return nil, fmt.Errorf("claude_config_invalid")
	}
	account := map[string]json.RawMessage{}
	for _, key := range []string{"oauthAccount", "userID"} {
		if value, ok := config[key]; ok {
			account[key] = value
		}
	}
	return json.Marshal(account)
}

const mergeClaudeAccountPython = `import os,sys,json,stat,tempfile
def unique(items):
 result={}
 for k,v in items:
  if k in result:raise ValueError()
  result[k]=v
 return result
try:
 account=json.loads(sys.stdin.read(1048577),object_pairs_hook=unique)
 if not isinstance(account,dict) or not set(account)<=set(['oauthAccount','userID']):raise ValueError()
 path=os.path.join(os.environ['HOME'],'.claude.json');current={}
 if os.path.lexists(path):
  fd=os.open(path,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK);s=os.fstat(fd)
  if not stat.S_ISREG(s.st_mode) or s.st_uid!=os.getuid() or s.st_size>1048576:raise ValueError()
  raw=os.read(fd,1048577);os.close(fd)
  current=json.loads(raw,object_pairs_hook=unique)
  if not isinstance(current,dict):raise ValueError()
 current.update(account)
 directory=os.path.join(os.environ['HOME'],'.claude')
 os.makedirs(directory,mode=0o700,exist_ok=True)
 d=os.lstat(directory)
 if not stat.S_ISDIR(d.st_mode) or d.st_uid!=os.getuid():raise ValueError()
 fd,tmp=tempfile.mkstemp(prefix='.orchid-auth-',dir=directory)
 try:
  os.fchmod(fd,0o600)
  with os.fdopen(fd,'w') as f:json.dump(current,f);f.flush();os.fsync(f.fileno())
  os.replace(tmp,path)
 finally:
  if os.path.exists(tmp):os.unlink(tmp)
except Exception:sys.exit(2)
`

func (h Host) syncClaudeAccountConfig(ctx context.Context, source string) error {
	raw, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("claude_config_unavailable")
	}
	metadata, err := claudeAccountMetadata(raw)
	if err != nil {
		return err
	}
	script := "export HOME=" + shq(h.agentHome()) + "; python3 -c " + shq(mergeClaudeAccountPython) + " <<'ORCHID_ACCOUNT_METADATA'\n" + string(metadata) + "\nORCHID_ACCOUNT_METADATA"
	_, err = h.runRemote(ctx, script)
	if err != nil || ctx.Err() != nil {
		return fmt.Errorf("claude_config_sync_unconfirmed")
	}
	return nil
}
