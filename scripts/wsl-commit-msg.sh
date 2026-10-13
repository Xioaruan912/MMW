#!/usr/bin/env bash
set -uo pipefail
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
git add -A
git -c core.autocrlf=false commit -q -F scripts/_msg.txt
rm -f scripts/_msg.txt
git --no-pager log --oneline | head -4
echo "--- 统计 ---"
echo "文件: $(git ls-files | wc -l)  提交: $(git rev-list --count HEAD)"
