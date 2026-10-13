#!/bin/sh
# MMX 容器入口：补齐默认环境变量、确认数据目录可写，然后交给主进程。
#
# 设计原则：容器里**不做任何隐式初始化**（不建管理员、不改配置）。
# 首启初始化由面板向导完成，避免"默认口令"这类隐患。
set -e

: "${MMX_DATA_DIR:=/data}"
: "${MMX_LISTEN:=0.0.0.0:12889}"
export MMX_DATA_DIR MMX_LISTEN

# 数据目录必须是卷挂载点且可写；否则 SQLite 会在第一次写入时失败，
# 而那时用户已经走完了初始化向导，报错时机很糟。
if [ ! -d "$MMX_DATA_DIR" ]; then
  echo "[mmx] 数据目录不存在，正在创建: $MMX_DATA_DIR" >&2
  mkdir -p "$MMX_DATA_DIR"
fi
if [ ! -w "$MMX_DATA_DIR" ]; then
  echo "[mmx] 数据目录不可写: $MMX_DATA_DIR（检查卷挂载与属主）" >&2
  exit 1
fi

# 支持三种调用方式：
#   mmx-master / mmx-agent  → 走对应二进制
#   -config ...             → 默认走 mmx-master
#   <其它命令>              → 原样执行（便于 docker run 里调试）
case "${1:-}" in
  mmx-master|mmx-agent)
    exec "/usr/local/bin/$1" "$@"
    ;;
  -*)
    exec /usr/local/bin/mmx-master "$@"
    ;;
  "")
    exec /usr/local/bin/mmx-master
    ;;
  *)
    exec "$@"
    ;;
esac
