#!/usr/bin/env bash
set -uo pipefail
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
git add -A
git -c core.autocrlf=false commit -q -m "docs: 同步功能矩阵与路线图（P7 大部 / P8 部分 / P9 完成）"
git --no-pager log --oneline | head -6
echo "--- 最终统计 ---"
echo "提交数 : $(git rev-list --count HEAD)"
echo "文件数 : $(git ls-files | wc -l)"
echo "Go     : $(git ls-files '*.go' | xargs wc -l 2>/dev/null | tail -1 | awk '{print \$1}') 行"
echo "测试   : $(git ls-files '*_test.go' | xargs wc -l 2>/dev/null | tail -1 | awk '{print \$1}') 行"
