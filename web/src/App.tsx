import { useCallback, useEffect, useState } from 'react'
import { InboundsPage, NodesPage, UsersPage } from './pages'
import {
  ApiError,
  api,
  fmtBytes,
  fmtDuration,
  fmtPct,
  type AuditLog,
  type Server,
  type User,
} from './api'

type View = 'servers' | 'inbounds' | 'nodes' | 'users' | 'audit'

export default function App() {
  const [user, setUser] = useState<User | null>(null)
  const [needsInit, setNeedsInit] = useState<boolean | null>(null)
  const [booting, setBooting] = useState(true)

  const bootstrap = useCallback(async () => {
    setBooting(true)
    try {
      const me = await api.me()
      setUser(me.user)
    } catch {
      try {
        const st = await api.setupStatus()
        setNeedsInit(st.needs_init)
      } catch {
        setNeedsInit(false)
      }
    } finally {
      setBooting(false)
    }
  }, [])

  useEffect(() => {
    void bootstrap()
  }, [bootstrap])

  if (booting) return <div className="center muted">加载中…</div>

  if (!user) {
    return (
      <AuthScreen
        needsInit={!!needsInit}
        onDone={(u) => {
          setUser(u)
          setNeedsInit(false)
        }}
      />
    )
  }
  return <Console user={user} onLogout={() => setUser(null)} />
}

// ---------------------------------------------------------------------------

function AuthScreen({ needsInit, onDone }: { needsInit: boolean; onDone: (u: User) => void }) {
  const [username, setUsername] = useState('admin')
  const [password, setPassword] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setErr('')
    try {
      const r = needsInit ? await api.setupInit(username, password) : await api.login(username, password)
      onDone(r.user)
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="center">
      <form className="card auth" onSubmit={submit}>
        <h1>MMX</h1>
        <p className="muted">{needsInit ? '首次启动：创建管理员账号' : '登录控制台'}</p>
        <label>
          用户名
          <input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" />
        </label>
        <label>
          密码
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete={needsInit ? 'new-password' : 'current-password'}
            placeholder={needsInit ? '至少 10 位' : ''}
          />
        </label>
        {err && <div className="err">{err}</div>}
        <button type="submit" disabled={busy}>
          {busy ? '处理中…' : needsInit ? '创建管理员' : '登录'}
        </button>
      </form>
    </div>
  )
}

// ---------------------------------------------------------------------------

function Console({ user, onLogout }: { user: User; onLogout: () => void }) {
  const [view, setView] = useState<View>('servers')
  const [servers, setServers] = useState<Server[]>([])
  const [overview, setOverview] = useState<Record<string, number | string>>({})
  const [showAdd, setShowAdd] = useState(false)
  const [toast, setToast] = useState('')

  const refresh = useCallback(async () => {
    try {
      const [s, o] = await Promise.all([api.listServers(), api.overview()])
      setServers(s.servers)
      setOverview(o as unknown as Record<string, number | string>)
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) onLogout()
    }
  }, [onLogout])

  useEffect(() => {
    void refresh()
    const t = setInterval(() => void refresh(), 5000)
    return () => clearInterval(t)
  }, [refresh])

  function flash(msg: string) {
    setToast(msg)
    setTimeout(() => setToast(''), 3000)
  }

  return (
    <div className="app">
      <aside>
        <div className="brand">MMX</div>
        <nav>
          <button className={view === 'servers' ? 'active' : ''} onClick={() => setView('servers')}>
            服务管理
          </button>
          <button className={view === 'inbounds' ? 'active' : ''} onClick={() => setView('inbounds')}>
            节点与入站
          </button>
          <button className={view === 'nodes' ? 'active' : ''} onClick={() => setView('nodes')}>
            节点列表
          </button>
          <button className={view === 'users' ? 'active' : ''} onClick={() => setView('users')}>
            用户与订阅
          </button>
          <button className={view === 'audit' ? 'active' : ''} onClick={() => setView('audit')}>
            审计日志
          </button>
        </nav>
        <div className="spacer" />
        <div className="who">
          <div>{user.nickname || user.username}</div>
          <div className="muted small">{user.role}</div>
        </div>
        <button className="ghost" onClick={() => void api.logout().then(onLogout)}>
          退出
        </button>
      </aside>

      <main>
        {view === 'servers' && (
          <header>
            <h2>服务管理</h2>
            <button onClick={() => setShowAdd(true)}>添加服务器</button>
          </header>
        )}

        {view === 'servers' && (
          <>
            <section className="kpis">
              <Kpi label="服务器总数" value={String(overview.servers_total ?? 0)} />
              <Kpi label="在线" value={String(overview.servers_online ?? 0)} tone="ok" />
              <Kpi label="Agent 连接" value={String(overview.agents_connected ?? 0)} />
              <Kpi label="用户" value={String(overview.users_total ?? 0)} />
              <Kpi label="主控运行" value={fmtDuration(Number(overview.uptime_sec ?? 0))} />
            </section>

            {servers.length === 0 ? (
              <div className="empty">还没有服务器。点右上角「添加服务器」开始。</div>
            ) : (
              <div className="grid">
                {servers.map((s) => (
                  <ServerCard key={s.id} s={s} onChanged={refresh} flash={flash} />
                ))}
              </div>
            )}
          </>
        )}

        {view === 'inbounds' && <InboundsPage servers={servers} flash={flash} />}
        {view === 'nodes' && <NodesPage flash={flash} />}
        {view === 'users' && <UsersPage flash={flash} />}
        {view === 'audit' && <AuditTable />}

        {showAdd && (
          <AddServerDialog
            onClose={() => setShowAdd(false)}
            onCreated={() => {
              setShowAdd(false)
              void refresh()
            }}
          />
        )}
      </main>

      {toast && <div className="toast">{toast}</div>}
    </div>
  )
}

function Kpi({ label, value, tone }: { label: string; value: string; tone?: 'ok' }) {
  return (
    <div className={`kpi ${tone ?? ''}`}>
      <div className="muted small">{label}</div>
      <div className="kpi-value">{value}</div>
    </div>
  )
}

function ServerCard({ s, onChanged, flash }: { s: Server; onChanged: () => void; flash: (m: string) => void }) {
  const [busy, setBusy] = useState(false)
  const m = s.metrics

  async function run(kind: string) {
    setBusy(true)
    try {
      const r = await api.dispatchTask(s.id, kind)
      flash(`${kind}: ${r.result.status}${r.result.error ? ' — ' + r.result.error : ''}`)
    } catch (e) {
      flash(`${kind} 失败: ${e instanceof ApiError ? e.message : String(e)}`)
    } finally {
      setBusy(false)
      onChanged()
    }
  }

  async function showInstall() {
    try {
      const r = await api.installCommand(s.id)
      await navigator.clipboard?.writeText(r.install_cmd).catch(() => {})
      window.prompt('一键安装命令（已尝试复制到剪贴板）', r.install_cmd)
    } catch (e) {
      flash(`生成安装命令失败: ${e instanceof ApiError ? e.message : String(e)}`)
    }
  }

  return (
    <div className="card server-card">
      <div className="row">
        <div>
          <div className="name">{s.name}</div>
          <div className="muted small">
            {s.address || '未填地址'} · {s.xray_mode}
          </div>
        </div>
        <span className={`dot ${s.online ? 'on' : 'off'}`} title={s.online ? '在线' : '离线'} />
      </div>

      {s.online && m ? (
        <div className="metrics">
          <Metric label="CPU" value={fmtPct(m.cpu_percent)} />
          <Metric
            label="内存"
            value={`${fmtBytes(m.mem_used)} / ${fmtBytes(m.mem_total)}`}
          />
          <Metric label="负载" value={m.load1.toFixed(2)} />
          <Metric label="↓ / ↑" value={`${fmtBytes(m.net_down)}/s · ${fmtBytes(m.net_up)}/s`} />
          <Metric label="连接" value={String(m.tcp_conns)} />
          <Metric label="运行" value={fmtDuration(m.uptime_sec)} />
          <Metric label="Xray" value={m.xray_running ? '运行中' : '未运行'} tone={m.xray_running ? 'ok' : ''} />
        </div>
      ) : (
        <div className="muted small block">
          {s.status === 'offline' ? '离线' : s.status} · 最后上报 {s.last_seen_human || '从未'}
        </div>
      )}

      <div className="actions">
        <button
          className="ghost"
          onClick={() => void run('xray.restart')}
          disabled={busy || !s.online}
          title="下发给 Agent 执行"
        >
          重启 Xray
        </button>
        <button className="ghost" onClick={() => void showInstall()}>
          安装命令
        </button>
        <button
          className="ghost danger"
          onClick={async () => {
            if (!confirm(`确认删除「${s.name}」？该操作不可撤销。`)) return
            await api.deleteServer(s.id)
            flash('已删除')
            onChanged()
          }}
        >
          删除
        </button>
      </div>
    </div>
  )
}

function Metric({ label, value, tone }: { label: string; value: string; tone?: string }) {
  return (
    <div className={`metric ${tone ?? ''}`}>
      <div className="muted small">{label}</div>
      <div>{value}</div>
    </div>
  )
}

function AuditTable() {
  const [logs, setLogs] = useState<AuditLog[]>([])
  useEffect(() => {
    const load = () => void api.audit().then((r) => setLogs(r.logs))
    load()
    const t = setInterval(load, 5000)
    return () => clearInterval(t)
  }, [])
  return (
    <table className="table">
      <thead>
        <tr>
          <th>时间</th>
          <th>动作</th>
          <th>操作者</th>
          <th>目标</th>
          <th>详情</th>
        </tr>
      </thead>
      <tbody>
        {logs.map((l) => (
          <tr key={l.id}>
            <td className="muted">{new Date(l.ts).toLocaleString()}</td>
            <td>
              <code>{l.action}</code>
            </td>
            <td>{l.actor || '-'}</td>
            <td className="muted small">{l.target}</td>
            <td className="muted small">{l.detail}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

function AddServerDialog({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [name, setName] = useState('')
  const [address, setAddress] = useState('')
  const [mode, setMode] = useState('external')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [created, setCreated] = useState<{ install_cmd: string } | null>(null)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setErr('')
    try {
      const r = await api.createServer(name, address, mode)
      setCreated({ install_cmd: r.install_cmd })
      onCreated()
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="card modal" onClick={(e) => e.stopPropagation()}>
        {!created ? (
          <form onSubmit={submit}>
            <h3>添加服务器</h3>
            <label>
              名称
              <input value={name} onChange={(e) => setName(e.target.value)} placeholder="香港-01" autoFocus />
            </label>
            <label>
              地址
              <input value={address} onChange={(e) => setAddress(e.target.value)} placeholder="1.2.3.4" />
            </label>
            <label>
              Xray 模式
              <select value={mode} onChange={(e) => setMode(e.target.value)}>
                <option value="external">external —— 使用系统安装的 Xray</option>
                <option value="embedded">embedded —— Agent 内嵌 Xray（P6）</option>
                <option value="none">none —— 仅监控</option>
              </select>
            </label>
            {err && <div className="err">{err}</div>}
            <div className="actions end">
              <button type="button" className="ghost" onClick={onClose}>
                取消
              </button>
              <button type="submit" disabled={busy || !name}>
                {busy ? '创建中…' : '创建并生成令牌'}
              </button>
            </div>
          </form>
        ) : (
          <div>
            <h3>在目标服务器执行</h3>
            <pre className="code">{created.install_cmd}</pre>
            <p className="muted small">
              该命令内嵌一次性入驻令牌（30 分钟有效，用后即废）。Agent 会自动注册 systemd 服务并连回主控。
            </p>
            <div className="actions end">
              <button className="ghost" onClick={() => void navigator.clipboard?.writeText(created.install_cmd)}>
                复制
              </button>
              <button onClick={onClose}>完成</button>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
