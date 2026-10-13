#!/usr/bin/env bash
set -uo pipefail
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
git add -A
git -c core.autocrlf=false commit -q -m "feat(web): 内嵌前端控制台 + 单二进制交付

前端（web/）
- Vite 6 + React 19 + TypeScript，零 UI 依赖，手写样式
- 登录/首启初始化、服务管理（KPI + 服务器卡片 + 实时指标轮询）
- 添加服务器对话框（一键安装命令 + 复制）、远程任务触发、审计日志表
- dev 时 vite 代理 /api 到本地主控

工程化
- web/embed.go + internal/webui：前端 embed 进主控二进制，单文件交付
- web/dist/index.html 作为未构建时的占位页保留在仓库，
  保证全新 clone 后 go build ./... 不因缺文件失败
- .gitignore 改为只忽略 web/dist/assets/

端到端
- e2e 增加「内嵌前端」断言组：首页 200 / #root 挂载点 / SPA 回落 / 资源可加载
- 结果：23/23 全部通过"
git --no-pager log --oneline | head -3
echo "--- 统计 ---"
echo "文件数: $(git ls-files | wc -l)"
echo "Go  : $(git ls-files '*.go' | xargs wc -l 2>/dev/null | tail -1 | awk '{print $1}') 行"
echo "TS/TSX: $(git ls-files '*.ts' '*.tsx' '*.css' | xargs wc -l 2>/dev/null | tail -1 | awk '{print $1}') 行"
echo "文档: $(git ls-files '*.md' | xargs wc -l 2>/dev/null | tail -1 | awk '{print $1}') 行"
