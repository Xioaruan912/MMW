#!/usr/bin/env bash
set -uo pipefail
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
git add -A
git -c core.autocrlf=false commit -q -m "docs: 同步功能矩阵与路线图进度（P6 追加完成项）"
git --no-pager log --oneline | head -8
echo "--- 统计 ---"
echo "提交数: $(git rev-list --count HEAD)"
echo "跟踪文件: $(git ls-files | wc -l)"
