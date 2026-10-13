#!/usr/bin/env bash
set -uo pipefail
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
git add -A
git -c core.autocrlf=false commit -q -F scripts/_msg.txt 2>/dev/null || git -c core.autocrlf=false commit -q -m "feat(P9): Docker / CI / 一键安装 / 文档索引"
rm -f scripts/_msg.txt
git --no-pager log --oneline | head -3
