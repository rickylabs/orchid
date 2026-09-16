#!/usr/bin/env python3
"""Scan the complete tracked diff and untracked text without echoing matches."""
import json,re,subprocess,sys
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
PATTERNS={
 'operator_path':r'/(?:home|Users|root)/[A-Za-z0-9_.-]+',
 'tailnet_name':r'\b[a-z0-9.-]+\.ts\.net\b',
 'native_session':r'\b(?:ses_|cse_)[A-Za-z0-9_-]+|\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b',
 'credential':r'\b(?:sk-[A-Za-z0-9_-]{15,}|gh[pousr]_[A-Za-z0-9]{20,})\b',
 'private_address':r'(?<![0-9])(?:100\.(?:6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])|192\.168|10\.[0-9]{1,3})\.[0-9]{1,3}\.[0-9]{1,3}(?![0-9])',
 'spend_literal':r'\$[0-9]+\.[0-9]{2,}|(?:USD|EUR)\s+[0-9]+\.[0-9]+'
}
# Deliberately split synthetic canaries so this source does not contain a match.
CANARIES={
 'operator_path':'/'+ 'home' + '/synthetic',
 'tailnet_name':'synthetic.' + 'ts.net',
 'native_session':'ses_' + 'synthetic',
 'credential':'sk-' + 'x'*20,
 'private_address':'.'.join(['10','12','34','56']),
 'spend_literal':'$' + '12.34'
}
if '--self-test' in sys.argv:
 broken=sum(not re.search(PATTERNS[k],v) for k,v in CANARIES.items())
 broken+=sum(bool(re.search(pattern,'synthetic harmless prose')) for pattern in PATTERNS.values())
 print(json.dumps({'negative_controls':len(CANARIES),'clean_controls':len(PATTERNS),'broken_controls':broken}))
 sys.exit(0 if broken==0 else 2)
base=subprocess.run(['git','merge-base','HEAD','origin/main'],cwd=ROOT,capture_output=True,check=True).stdout.decode().strip()
diff=subprocess.run(['git','diff','--binary',base],cwd=ROOT,capture_output=True,check=True).stdout.decode()
names=subprocess.run(['git','ls-files','--others','--exclude-standard','-z'],cwd=ROOT,capture_output=True,check=True).stdout.decode().split('\0')
texts=[diff]+[(ROOT/name).read_text() for name in names if name and (ROOT/name).is_file()]
counts={k:sum(len(re.findall(pattern,text)) for text in texts) for k,pattern in PATTERNS.items()}
print(json.dumps({'changed_texts':len(texts),'findings':counts}))
sys.exit(2 if any(counts.values()) else 0)
