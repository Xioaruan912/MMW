#!/usr/bin/env bash
set -uo pipefail
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
cp /mnt/c/Users/Administrator/AppData/Local/Temp/msgfile.txt /tmp/msgfile.txt 2>/dev/null || true
git -c core.autocrlf=false commit --amend -q -F /tmp/msgfile.txt
git --no-pager log -1 --format=%B | head -14
