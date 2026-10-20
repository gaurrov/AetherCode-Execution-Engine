#!/usr/bin/env bash
# Smoke test — requires a running stack (make up).
# Submits one "Hello, World!" run for each supported language and
# streams the verdict. Exits 0 if all pass, non-zero otherwise.
set -euo pipefail

BASE="${AC_ENGINE_URL:-http://127.0.0.1:5100}"
TIMEOUT=60

RED='\033[0;31m'; GREEN='\033[0;32m'; NC='\033[0m'
pass=0; fail=0

run_test() {
  local lang="$1" source="$2" expected="$3"
  printf "  %-14s " "$lang"

  resp=$(curl -sf -X POST "$BASE/api/v1/execute" \
    -H 'Content-Type: application/json' \
    -d "{\"language\":\"$lang\",\"source_code\":\"$source\",\"tests\":[{\"input\":\"\",\"expected_output\":\"$expected\"}]}" \
    2>/dev/null)
  job_id=$(printf '%s' "$resp" | python3 -c "import sys,json; print(json.load(sys.stdin)['job_id'])" 2>/dev/null)
  stream_token=$(printf '%s' "$resp" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('stream_token',''))" 2>/dev/null)

  if [ -z "$job_id" ]; then
    echo -e "${RED}FAIL (no job_id)${NC}"; ((fail++)); return
  fi

  local url="$BASE/api/v1/stream?job_id=$job_id"
  [ -n "$stream_token" ] && url="$url&stream_token=$stream_token"

  verdict=$(timeout "$TIMEOUT" curl -sN "$url" 2>/dev/null \
    | grep '^data:' \
    | python3 -c "
import sys, json
for line in sys.stdin:
    try:
        d = json.loads(line[5:])
        if d.get('type') == 'VERDICT':
            print(d.get('data', {}).get('verdict', 'unknown'))
            break
    except Exception:
        pass
" 2>/dev/null || echo "timeout")

  if [ "$verdict" = "accepted" ]; then
    echo -e "${GREEN}PASS${NC}"; ((pass++))
  else
    echo -e "${RED}FAIL (verdict=$verdict)${NC}"; ((fail++))
  fi
}

echo "==> Smoke test against $BASE"
echo ""

run_test "python"     'print("Hello, World!")'                       "Hello, World!"
run_test "javascript" 'console.log("Hello, World!")'                 "Hello, World!"
run_test "c"          '#include<stdio.h>\nint main(){puts("Hello, World!");}' "Hello, World!"
run_test "cpp"        '#include<iostream>\nint main(){std::cout<<"Hello, World!"<<std::endl;}' "Hello, World!"
run_test "go"         'package main\nimport "fmt"\nfunc main(){fmt.Println("Hello, World!")}' "Hello, World!"
run_test "java"       'public class Main{public static void main(String[] a){System.out.println("Hello, World!");}}' "Hello, World!"
run_test "bash"       'echo "Hello, World!"'                         "Hello, World!"
run_test "rust"       'fn main(){println!("Hello, World!")}'         "Hello, World!"
run_test "ruby"       'puts "Hello, World!"'                         "Hello, World!"
run_test "php"        '<?php echo "Hello, World!\n";'                "Hello, World!"

echo ""
echo "==> Results: ${GREEN}${pass} passed${NC}, ${RED}${fail} failed${NC}"
[ "$fail" -eq 0 ]
