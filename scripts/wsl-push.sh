#!/usr/bin/env bash
# 提交并推送到 origin（凭据从环境变量取，不落盘）
set -uo pipefail
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
git add -A
git -c core.autocrlf=false commit -q -F scripts/_msg.txt
rm -f scripts/_msg.txt
echo "--- 本地提交 ---"
git --no-pager log --oneline | head -3
