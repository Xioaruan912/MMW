import { useCallback, useEffect, useState } from 'react'
import {
  ApiError,
  api,
  type ApplyResult,
  type Inbound,
  type NodeItem,
  type Server,
  type UserItem,
} from './api'

// ---------------------------------------------------------------------------
// 入站管理
// ---------------------------------------------------------------------------

const PROTOCOLS = ['vless', 'vmess', 'trojan', 'shadowsocks']
const TRANSPORTS = ['tcp', 'ws', 'grpc', 'httpupgrade']
const SECURITIES = ['none', 'tls', 'reality']

export function InboundsPage({ servers, flash }: { servers: Server[]; flash: (m: string) => void }) {
  const [list, setList] = useState<Inbound[]>([])
  const [showAdd, setShowAdd] = useState(false)
  const [applyRes, setApplyRes] = useState<ApplyResult | null>(null)
  const [busy, setBusy] = useState(false)

  const refresh = useCallback(async () => {
    try {
      const r = await api.listInbounds()
      setList(r.inbounds)
    } catch (e) {
      flash('加载入站失败: ' + (e instanceof ApiError ? e.message : String(e)))
    }
  }, [flash])

  useEffect(() => {
    void refresh()
  }, [refresh])

  async function applyTo(serverId: string) {
    setBusy(true)
    try {
      const r = await api.applyXray(serverId)
      setApplyRes(r.result)
      flash(r.result.applied ? `配置已下发（${r.result.rev}）` : `未下发：${r.result.error ?? r.result.status}`)
    } catch (e) {
      flash('下发失败: ' + (e instanceof ApiError ? e.message : String(e)))
    } finally {
      setBusy(false)
    }
  }

  const byServer = new Map<string, Inbound[]>()
  for (const inb of list) {
    const arr = byServer.get(inb.server_id) ?? []
    arr.push(inb)
    byServer.set(inb.server_id, arr)
  }

  return (
    <>
      <header>
        <h2>节点与入站</h2>
        <div className="actions">
          <button className="ghost" onClick={() => void refresh()}>
            刷新
          </button>
          <button onClick={() => setShowAdd(true)} disabled={servers.length === 0}>
            新增入站
          </button>
        </div>
      </header>

      {servers.length === 0 && (
        <div className="empty">先到「服务管理」添加一台服务器，Agent 上线后才能配置入站。</div>
      )}

      {[...byServer.entries()].map(([serverId, inbounds]) => {
        const srv = servers.find((s) => s.id === serverId)
        return (
          <section key={serverId} className="panel">
            <div className="panel-head">
              <div>
                <strong>{srv?.name ?? serverId}</strong>
                <span className="muted small"> · {inbounds.length} 个入站</span>
              </div>
              <button
                className="ghost"
                disabled={busy || !srv?.online}
                title={srv?.online ? '生成配置并下发到 Agent' : '服务器离线'}
                onClick={() => void applyTo(serverId)}
              >
                下发配置
              </button>
            </div>
            <table className="table">
              <thead>
                <tr>
                  <th>tag</th>
                  <th>协议</th>
                  <th>传输</th>
                  <th>安全层</th>
                  <th>端口</th>
                  <th>备注</th>
                  <th>状态</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {inbounds.map((i) => (
                  <tr key={i.id}>
                    <td>
                      <code>{i.tag}</code>
                    </td>
                    <td>{i.protocol}</td>
                    <td>{i.transport}</td>
                    <td>
                      <span className={`chip ${i.security}`}>{i.security}</span>
                    </td>
                    <td>{i.port}</td>
                    <td className="muted small">{i.remark}</td>
                    <td>{i.enabled ? '启用' : '停用'}</td>
                    <td>
                      <button
                        className="ghost danger sm"
                        onClick={async () => {
                          if (!confirm(`删除入站 ${i.tag}？`)) return
                          await api.deleteInbound(i.id)
                          flash('已删除')
                          void refresh()
                        }}
                      >
                        删除
                      </button>
                    </td>
                  </tr>
                ))}
                {inbounds.length === 0 && (
                  <tr>
                    <td colSpan={8} className="muted small">
                      尚无入站
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </section>
        )
      })}

      {applyRes && (
        <section className="panel">
          <div className="panel-head">
            <strong>最近一次下发</strong>
            <button className="ghost sm" onClick={() => setApplyRes(null)}>
              关闭
            </button>
          </div>
          <div className="kv">
            <span>rev</span>
            <code>{applyRes.rev}</code>
            <span>入站数</span>
            <span>{applyRes.inbounds}</span>
            <span>状态</span>
            <span>
              {applyRes.status} {applyRes.applied ? '（已落地）' : ''}
            </span>
          </div>
          {applyRes.output && <pre className="code">{applyRes.output}</pre>}
          {applyRes.error && <div className="err">{applyRes.error}</div>}
        </section>
      )}

      {showAdd && (
        <AddInboundDialog
          servers={servers}
          onClose={() => setShowAdd(false)}
          onCreated={() => {
            setShowAdd(false)
            void refresh()
          }}
        />
      )}
    </>
  )
}

function AddInboundDialog({
  servers,
  onClose,
  onCreated,
}: {
  servers: Server[]
  onClose: () => void
  onCreated: () => void
}) {
  const [serverId, setServerId] = useState(servers[0]?.id ?? '')
  const [tag, setTag] = useState('')
  const [nodeName, setNodeName] = useState('')
  const [region, setRegion] = useState('')
  const [protocol, setProtocol] = useState('vless')
  const [transport, setTransport] = useState('tcp')
  const [security, setSecurity] = useState('reality')
  const [port, setPort] = useState('443')
  const [remark, setRemark] = useState('')
  const [wsPath, setWsPath] = useState('/')
  const [serviceName, setServiceName] = useState('mmx')
  const [serverName, setServerName] = useState('')
  const [realityDest, setRealityDest] = useState('www.example.com:443')
  const [realityNames, setRealityNames] = useState('www.example.com')
  const [realityKey, setRealityKey] = useState('')
  const [realityShort, setRealityShort] = useState('')
  const [flow, setFlow] = useState('xtls-rprx-vision')
  const [ssMethod, setSsMethod] = useState('aes-256-gcm')
  const [ssPassword, setSsPassword] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const realityOnlyTCP = security === 'reality'
  useEffect(() => {
    if (realityOnlyTCP && transport !== 'tcp') setTransport('tcp')
  }, [realityOnlyTCP, transport])

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setErr('')
    const body: Record<string, unknown> = {
      server_id: serverId,
      tag: tag || `${protocol}-${port}`,
      protocol,
      transport,
      security,
      port: Number(port),
      remark,
      node_name: nodeName,
      region,
      settings: { flow: protocol === 'vless' ? flow : '', method: ssMethod, password: ssPassword },
      stream: {
        ws_path: wsPath,
        service_name: serviceName,
        server_name: serverName,
        reality_dest: realityDest,
        reality_server_names: realityNames.split(',').map((s) => s.trim()).filter(Boolean),
        reality_private_key: realityKey,
        reality_short_ids: realityShort.split(',').map((s) => s.trim()).filter(Boolean),
      },
    }
    try {
      await api.createInbound(body)
      onCreated()
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="card modal wide" onClick={(e) => e.stopPropagation()}>
        <form onSubmit={submit}>
          <h3>新增入站</h3>

          <div className="grid2">
            <label>
              服务器
              <select value={serverId} onChange={(e) => setServerId(e.target.value)}>
                {servers.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name} {s.online ? '（在线）' : '（离线）'}
                  </option>
                ))}
              </select>
            </label>
            <label>
              端口
              <input value={port} onChange={(e) => setPort(e.target.value)} />
            </label>
            <label>
              协议
              <select value={protocol} onChange={(e) => setProtocol(e.target.value)}>
                {PROTOCOLS.map((p) => (
                  <option key={p}>{p}</option>
                ))}
              </select>
            </label>
            <label>
              传输
              <select value={transport} onChange={(e) => setTransport(e.target.value)}>
                {TRANSPORTS.map((t) => (
                  <option key={t} disabled={realityOnlyTCP && t !== 'tcp'}>
                    {t}
                  </option>
                ))}
              </select>
            </label>
            <label>
              安全层
              <select value={security} onChange={(e) => setSecurity(e.target.value)}>
                {SECURITIES.map((s) => (
                  <option key={s}>{s}</option>
                ))}
              </select>
            </label>
            <label>
              tag（唯一标识）
              <input value={tag} onChange={(e) => setTag(e.target.value)} placeholder="vless-reality-443" />
            </label>
            <label>
              节点名
              <input value={nodeName} onChange={(e) => setNodeName(e.target.value)} placeholder="香港-01" />
            </label>
            <label>
              地区
              <input value={region} onChange={(e) => setRegion(e.target.value)} placeholder="🇭🇰 香港" />
            </label>
          </div>

          {transport === 'ws' && (
            <label>
              WS 路径
              <input value={wsPath} onChange={(e) => setWsPath(e.target.value)} />
            </label>
          )}
          {transport === 'grpc' && (
            <label>
              gRPC service name
              <input value={serviceName} onChange={(e) => setServiceName(e.target.value)} />
            </label>
          )}
          {security === 'tls' && (
            <label>
              TLS SNI（server_name）
              <input value={serverName} onChange={(e) => setServerName(e.target.value)} />
            </label>
          )}
          {security === 'reality' && (
            <div className="grid2">
              <label>
                伪装目标 dest
                <input value={realityDest} onChange={(e) => setRealityDest(e.target.value)} />
              </label>
              <label>
                serverNames（逗号分隔）
                <input value={realityNames} onChange={(e) => setRealityNames(e.target.value)} />
              </label>
              <label>
                REALITY 私钥
                <input value={realityKey} onChange={(e) => setRealityKey(e.target.value)} placeholder="xray x25519 生成" />
              </label>
              <label>
                shortIds（逗号分隔）
                <input value={realityShort} onChange={(e) => setRealityShort(e.target.value)} />
              </label>
            </div>
          )}
          {protocol === 'vless' && (
            <label>
              flow
              <input value={flow} onChange={(e) => setFlow(e.target.value)} />
            </label>
          )}
          {protocol === 'shadowsocks' && (
            <div className="grid2">
              <label>
                加密方式
                <input value={ssMethod} onChange={(e) => setSsMethod(e.target.value)} />
              </label>
              <label>
                服务端口令
                <input value={ssPassword} onChange={(e) => setSsPassword(e.target.value)} />
              </label>
            </div>
          )}
          <label>
            备注
            <input value={remark} onChange={(e) => setRemark(e.target.value)} />
          </label>

          {err && <div className="err">{err}</div>}
          <div className="actions end">
            <button type="button" className="ghost" onClick={onClose}>
              取消
            </button>
            <button type="submit" disabled={busy}>
              {busy ? '创建中…' : '创建'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// 节点列表
// ---------------------------------------------------------------------------

export function NodesPage({ flash }: { flash: (m: string) => void }) {
  const [nodes, setNodes] = useState<NodeItem[]>([])

  const refresh = useCallback(async () => {
    try {
      const r = await api.listNodes()
      setNodes(r.nodes)
    } catch (e) {
      flash('加载节点失败: ' + (e instanceof ApiError ? e.message : String(e)))
    }
  }, [flash])

  useEffect(() => {
    void refresh()
  }, [refresh])

  async function rename(n: NodeItem) {
    const name = window.prompt('节点名', n.name)
    if (name === null) return
    await api.updateNode(n.id, { name, region: n.region, tags: n.tags, sort: n.sort, enabled: n.enabled })
    flash('已更新')
    void refresh()
  }

  return (
    <>
      <header>
        <h2>节点列表</h2>
        <button className="ghost" onClick={() => void refresh()}>
          刷新
        </button>
      </header>
      <table className="table">
        <thead>
          <tr>
            <th>排序</th>
            <th>节点名</th>
            <th>地区</th>
            <th>服务器</th>
            <th>入站</th>
            <th>端口</th>
            <th>状态</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {nodes.map((n) => (
            <tr key={n.id}>
              <td className="muted">{n.sort}</td>
              <td>{n.name}</td>
              <td className="muted">{n.region || '-'}</td>
              <td className="muted small">{n.server_name}</td>
              <td>
                <code>{n.inbound.tag}</code>
              </td>
              <td>{n.inbound.port}</td>
              <td>{n.enabled ? '启用' : '停用'}</td>
              <td>
                <button className="ghost sm" onClick={() => void rename(n)}>
                  改名
                </button>
              </td>
            </tr>
          ))}
          {nodes.length === 0 && (
            <tr>
              <td colSpan={8} className="muted small">
                尚无节点。先在「节点与入站」创建入站。
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </>
  )
}

// ---------------------------------------------------------------------------
// 用户与订阅
// ---------------------------------------------------------------------------

export function UsersPage({ flash }: { flash: (m: string) => void }) {
  const [users, setUsers] = useState<UserItem[]>([])
  const [showAdd, setShowAdd] = useState(false)
  const [subUser, setSubUser] = useState<UserItem | null>(null)

  const refresh = useCallback(async () => {
    try {
      const r = await api.listUsers()
      setUsers(r.users)
    } catch (e) {
      flash('加载用户失败: ' + (e instanceof ApiError ? e.message : String(e)))
    }
  }, [flash])

  useEffect(() => {
    void refresh()
  }, [refresh])

  return (
    <>
      <header>
        <h2>用户与订阅</h2>
        <button onClick={() => setShowAdd(true)}>新增用户</button>
      </header>

      <table className="table">
        <thead>
          <tr>
            <th>用户名</th>
            <th>昵称</th>
            <th>角色</th>
            <th>状态</th>
            <th>订阅凭据</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {users.map((u) => (
            <tr key={u.id}>
              <td>{u.username}</td>
              <td className="muted">{u.nickname}</td>
              <td>{u.role}</td>
              <td>{u.status}</td>
              <td>
                {u.sub_token ? (
                  <code>{u.sub_token.slice(0, 10)}…</code>
                ) : (
                  <span className="muted small">无</span>
                )}
              </td>
              <td className="actions">
                <button className="ghost sm" onClick={() => setSubUser(u)} disabled={!u.sub_token}>
                  订阅链接
                </button>
                <button
                  className="ghost sm"
                  onClick={async () => {
                    const r = await api.rotateSubToken(u.id)
                    flash('凭据已重置，旧链接立即失效')
                    void refresh()
                    window.prompt('新的订阅链接', r.sub_url)
                  }}
                >
                  重置凭据
                </button>
                <button
                  className="ghost danger sm"
                  onClick={async () => {
                    if (!confirm(`删除用户 ${u.username}？其全部节点凭据会一并失效。`)) return
                    await api.deleteUser(u.id)
                    flash('已删除')
                    void refresh()
                  }}
                >
                  删除
                </button>
              </td>
            </tr>
          ))}
          {users.length === 0 && (
            <tr>
              <td colSpan={6} className="muted small">
                尚无用户
              </td>
            </tr>
          )}
        </tbody>
      </table>

      {subUser && (
        <SubscriptionDialog
          user={subUser}
          onClose={() => setSubUser(null)}
          onChanged={() => void refresh()}
          flash={flash}
        />
      )}

      {showAdd && (
        <AddUserDialog
          onClose={() => setShowAdd(false)}
          onCreated={() => {
            setShowAdd(false)
            void refresh()
          }}
        />
      )}
    </>
  )
}

function AddUserDialog({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [nickname, setNickname] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setErr('')
    try {
      await api.createUser(username, password, nickname)
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
        <form onSubmit={submit}>
          <h3>新增用户</h3>
          <label>
            用户名
            <input value={username} onChange={(e) => setUsername(e.target.value)} autoFocus />
          </label>
          <label>
            初始密码（至少 10 位）
            <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
          </label>
          <label>
            昵称（可选）
            <input value={nickname} onChange={(e) => setNickname(e.target.value)} />
          </label>
          {err && <div className="err">{err}</div>}
          <div className="actions end">
            <button type="button" className="ghost" onClick={onClose}>
              取消
            </button>
            <button type="submit" disabled={busy || !username}>
              {busy ? '创建中…' : '创建'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

function SubscriptionDialog({
  user,
  onClose,
  onChanged,
  flash,
}: {
  user: UserItem
  onClose: () => void
  onChanged: () => void
  flash: (m: string) => void
}) {
  const [tab, setTab] = useState<'links' | 'clients' | 'grant'>('links')

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="card modal" onClick={(e) => e.stopPropagation()}>
        <h3>{user.username} · 订阅</h3>
        <div className="tabs">
          <button className={tab === 'links' ? 'active' : ''} onClick={() => setTab('links')}>
            订阅链接
          </button>
          <button className={tab === 'clients' ? 'active' : ''} onClick={() => setTab('clients')}>
            已授权节点
          </button>
          <button className={tab === 'grant' ? 'active' : ''} onClick={() => setTab('grant')}>
            签发凭据
          </button>
        </div>

        {tab === 'links' && <SubscriptionLinks user={user} />}
        {tab === 'clients' && <UserClients user={user} onChanged={onChanged} flash={flash} />}
        {tab === 'grant' && <GrantClient user={user} onChanged={onChanged} flash={flash} />}

        <div className="actions end">
          <button onClick={onClose}>完成</button>
        </div>
      </div>
    </div>
  )
}

function SubscriptionLinks({ user }: { user: UserItem }) {
  const base = api.subURL(user.sub_token)
  const [formats, setFormats] = useState<{ format: string; label: string }[] | null>(null)
  const [err, setErr] = useState('')

  useEffect(() => {
    api
      .subFormats()
      .then((r) => setFormats(r.formats))
      .catch((e) => setErr(e instanceof ApiError ? e.message : String(e)))
  }, [])

  // 「自动识别」放在首位：客户端按 UA 拿对应格式，用户多数场景不需要关心格式名
  const items: [string, string][] = [
    ['自动识别（按 UA）', base],
    ...(formats ?? []).map((f) => [f.label, `${base}?format=${f.format}`] as [string, string]),
  ]

  return (
    <>
      <p className="muted small">
        订阅地址凭 token 访问，请勿泄露。客户端请求时会按 UA 自动分流；也可用 ?format= 强制指定。
      </p>
      {err && <div className="err">{err}</div>}
      {!formats && !err && <div className="muted small">加载格式列表…</div>}
      {items.map(([label, url]) => (
        <div key={url} className="link-row">
          <div className="muted small">{label}</div>
          <input readOnly value={url} onFocus={(e) => e.currentTarget.select()} />
          <button
            className="ghost sm"
            onClick={() => {
              void navigator.clipboard?.writeText(url)
            }}
          >
            复制
          </button>
        </div>
      ))}
      <div className="muted small">
        提示：Surge / Loon / Quantumult X 各有私有格式；Surfboard 与 QX 不支持 REALITY ——
        遇到不支持的组合时服务端会明确报错，而不是给你一份导入即失败的配置。
      </div>
    </>
  )
}

function UserClients({
  user,
  onChanged,
  flash,
}: {
  user: UserItem
  onChanged: () => void
  flash: (m: string) => void
}) {
  const [rows, setRows] = useState<{ inbound: Inbound; uuid: string; clientId: string }[]>([])
  const [loading, setLoading] = useState(true)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const inb = await api.listInbounds()
      const out: { inbound: Inbound; uuid: string; clientId: string }[] = []
      for (const i of inb.inbounds) {
        const c = await api.listClients(i.id)
        const mine = c.clients.find((x) => x.user_id === user.id)
        if (mine) out.push({ inbound: i, uuid: mine.uuid, clientId: mine.id })
      }
      setRows(out)
    } finally {
      setLoading(false)
    }
  }, [user.id])

  useEffect(() => {
    void load()
  }, [load])

  return (
    <>
      {loading ? (
        <div className="muted small">加载中…</div>
      ) : rows.length === 0 ? (
        <div className="muted small">该用户尚未获得任何节点凭据。切到「签发凭据」给他授权。</div>
      ) : (
        <table className="table">
          <thead>
            <tr>
              <th>入站</th>
              <th>端口</th>
              <th>UUID / 口令</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.clientId}>
                <td>
                  <code>{r.inbound.tag}</code>
                </td>
                <td>{r.inbound.port}</td>
                <td>
                  <code>{r.uuid}</code>
                </td>
                <td>
                  <button
                    className="ghost danger sm"
                    onClick={async () => {
                      await api.deleteClient(r.clientId)
                      flash('已吊销')
                      await load()
                      onChanged()
                    }}
                  >
                    吊销
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  )
}

function GrantClient({
  user,
  onChanged,
  flash,
}: {
  user: UserItem
  onChanged: () => void
  flash: (m: string) => void
}) {
  const [inbounds, setInbounds] = useState<Inbound[]>([])
  const [picked, setPicked] = useState<string[]>([])
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    void api.listInbounds().then((r) => setInbounds(r.inbounds))
  }, [])

  async function grant() {
    setBusy(true)
    try {
      for (const id of picked) {
        await api.addClient(id, user.id, user.username)
      }
      flash(`已签发 ${picked.length} 个凭据`)
      setPicked([])
      onChanged()
    } catch (e) {
      flash('签发失败: ' + (e instanceof ApiError ? e.message : String(e)))
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <p className="muted small">勾选该用户可用的入站，系统会为每个入站生成独立的 UUID 凭据。</p>
      {inbounds.map((i) => (
        <label key={i.id} className="check-row">
          <input
            type="checkbox"
            checked={picked.includes(i.id)}
            onChange={(e) =>
              setPicked((p) => (e.target.checked ? [...p, i.id] : p.filter((x) => x !== i.id)))
            }
          />
          <span>
            <code>{i.tag}</code> · {i.protocol}/{i.transport}/{i.security} · :{i.port}
          </span>
        </label>
      ))}
      {inbounds.length === 0 && <div className="muted small">还没有任何入站。</div>}
      <div className="actions end">
        <button disabled={busy || picked.length === 0} onClick={() => void grant()}>
          {busy ? '签发中…' : `签发 ${picked.length} 个凭据`}
        </button>
      </div>
    </>
  )
}
