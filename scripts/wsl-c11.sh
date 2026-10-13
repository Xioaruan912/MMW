#!/usr/bin/env bash
set -uo pipefail
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
git add -A
git -c core.autocrlf=false commit -q -m "feat(P7): 通知（Telegram/Webhook）+ 公告 + 备份恢复

通知（internal/notify）
- Telegram Bot API 发送 + Webhook POST（带 HMAC-SHA256 签名 X-MMX-Signature）
- **全异步投递**：通知服务器挂了不该拖垮主控
- 同类事件去重窗口（默认 300s），避免服务器抖动时刷屏；顺带清理过期项防内存无界增长
- 事件白名单：可按 kind 订阅
- 敏感字段（bot_token / secret）不回显，改用 has_* 标记；
  保存时空值表示「不修改」，避免面板回填把密钥清掉

公告（迁移 v7）
- announcements 表：标题/正文/级别/启停/置顶/过期时间
- 公开接口只暴露展示所需字段，不泄漏内部字段
- 停用/过期立即从公开接口消失

备份恢复（internal/backup）
- 备份用 SQLite `VACUUM INTO` 生成**一致快照**，而不是直接拷 .db 文件
  （WAL 模式下直拷可能拿到「主库已更新、WAL 未合并」的不一致副本）
- 恢复**不在运行时原地替换数据库**：落到 mmx.db.restore-pending，由下次启动原子替换
  并给旧库留档 .pre-restore-<时间戳> 便于回滚；同时清理 -wal/-shm 防止新旧库串
- 三层校验：zip 合法性 → manifest.app 归属 → mmx.db 的 SQLite 魔数

装配
- 服务器上线事件已接入 hub.OnConnect

测试
- notify 单测 7 组：默认关闭、白名单、去重窗口抑制与过期、map 不无界增长、
  HTML 转义（防注入 Telegram 消息体）、设置读写往返、无通道时明确报错
- backup 单测 4 组：备份包结构 + 魔数 + 表计数、恢复落盘与留档、
  非 zip/缺库/伪造库三种拒绝路径、外来 manifest 拒绝
- e2e 新增 16 项（132/132 通过）"
git --no-pager log --oneline | head -2
