import { FormEvent, useEffect, useState } from "react";

type Drive = { id: string; kind: string; name: string; rootEntryId: string; myRole?: string };
type DriveMember = { userId: string; email: string; displayName: string; role: string };
type Permission = { id: string; entryId: string; granteeUserId: string; email: string; displayName: string; role: string };
type ShareLink = { id: string; entryId: string; entryName?: string; entryKind?: string; token?: string; hasPassword?: boolean; permission: string; expiresAt?: string; downloadCount: number; maxDownloads?: number };
type Device = { id: string; name: string; createdAt: string; lastSeenAt: string };
type Activity = { id: string; kind: string; message: string; actorName?: string; createdAt: string };
type Notification = { id: string; kind: string; title: string; body: string; readAt?: string; createdAt: string };
type Comment = { id: string; body: string; displayName?: string; email?: string; createdAt: string };
type Version = { id: string; version: number; sizeBytes: number; createdAt: string };

type Props = {
  request: <T>(path: string, init?: RequestInit) => Promise<T>;
  onOpenFolder: (id: string, name: string) => void;
  selectedEntryId?: string | null;
  selectedEntryName?: string;
};

export function CollabSidebar(props: {
  view: string;
  setView: (v: string) => void;
  drives: Drive[];
  unread: number;
}) {
  const items = [
    { id: "files", label: "Dosyalarım" },
    { id: "shared", label: "Benimle paylaşılan" },
    { id: "drives", label: "Ortak alanlar" },
    { id: "starred", label: "Yıldızlı" },
    { id: "recent", label: "Son görüntülenen" },
    { id: "trash", label: "Çöp kutusu" },
    { id: "activity", label: "Aktivite" },
    { id: "notifications", label: `Bildirimler${props.unread ? ` (${props.unread})` : ""}` },
    { id: "shares", label: "Linklerim" },
    { id: "settings", label: "Ayarlar" }
  ];
  return (
    <nav className="collab-nav">
      {items.map((item) => (
        <button key={item.id} className={props.view === item.id || (item.id === "drives" && props.view.startsWith("drive:")) ? "active" : ""} onClick={() => props.setView(item.id)}>
          {item.label}
        </button>
      ))}
      {props.drives.filter((d) => d.kind === "shared").map((d) => (
        <button key={d.id} className={props.view === `drive:${d.id}` ? "active nested" : "nested"} onClick={() => props.setView(`drive:${d.id}`)}>
          {d.name}
        </button>
      ))}
    </nav>
  );
}

export function DrivesPanel({ request, onOpenFolder }: Props) {
  const [drives, setDrives] = useState<Drive[]>([]);
  const [name, setName] = useState("");
  const [selected, setSelected] = useState<Drive | null>(null);
  const [members, setMembers] = useState<DriveMember[]>([]);
  const [email, setEmail] = useState("");
  const [role, setRole] = useState("contributor");
  const [msg, setMsg] = useState("");

  async function refresh() {
    setDrives(await request<Drive[]>("/api/drives"));
  }
  useEffect(() => { void refresh(); }, []);

  async function create(e: FormEvent) {
    e.preventDefault();
    await request("/api/drives", { method: "POST", body: JSON.stringify({ name }) });
    setName("");
    await refresh();
  }

  async function openMembers(d: Drive) {
    setSelected(d);
    setMembers(await request<DriveMember[]>(`/api/drives/${d.id}/members`));
  }

  async function addMember(e: FormEvent) {
    e.preventDefault();
    if (!selected) return;
    try {
      await request(`/api/drives/${selected.id}/members`, { method: "POST", body: JSON.stringify({ email, role }) });
      setEmail("");
      setMembers(await request<DriveMember[]>(`/api/drives/${selected.id}/members`));
      setMsg("Üye eklendi");
    } catch (err) {
      setMsg(err instanceof Error ? err.message : "Eklenemedi");
    }
  }

  return (
    <div className="collab-panel">
      <h2>Ortak alanlar</h2>
      <form className="row" onSubmit={create}>
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Yeni ortak alan adı" required />
        <button className="primary" type="submit">Oluştur</button>
      </form>
      <ul className="plain-list">
        {drives.filter((d) => d.kind === "shared").map((d) => (
          <li key={d.id}>
            <strong>{d.name}</strong>
            <div className="row">
              <button onClick={() => onOpenFolder(d.rootEntryId, d.name)}>Aç</button>
              <button onClick={() => void openMembers(d)}>Üyeler</button>
            </div>
          </li>
        ))}
      </ul>
      {selected && (
        <section className="card-lite">
          <h3>{selected.name} üyeleri</h3>
          <ul className="plain-list">
            {members.map((m) => (
              <li key={m.userId}>
                <span>{m.displayName} ({m.email}) — {m.role}</span>
                <button onClick={() => void request(`/api/drives/${selected.id}/members/${m.userId}`, { method: "DELETE" }).then(() => openMembers(selected))}>Çıkar</button>
              </li>
            ))}
          </ul>
          <form className="row" onSubmit={addMember}>
            <input value={email} onChange={(e) => setEmail(e.target.value)} placeholder="e-posta" required />
            <select value={role} onChange={(e) => setRole(e.target.value)}>
              <option value="viewer">viewer</option>
              <option value="commenter">commenter</option>
              <option value="contributor">contributor</option>
              <option value="content_manager">content_manager</option>
              <option value="manager">manager</option>
            </select>
            <button className="primary" type="submit">Ekle</button>
          </form>
          {msg && <p>{msg}</p>}
        </section>
      )}
    </div>
  );
}

export function ShareModal({ request, entryId, entryName, onClose }: { request: Props["request"]; entryId: string; entryName: string; onClose: () => void }) {
  const [email, setEmail] = useState("");
  const [role, setRole] = useState("viewer");
  const [perms, setPerms] = useState<Permission[]>([]);
  const [password, setPassword] = useState("");
  const [days, setDays] = useState(7);
  const [maxDownloads, setMaxDownloads] = useState("");
  const [linkUrl, setLinkUrl] = useState("");
  const [mailEnabled, setMailEnabled] = useState(false);
  const [mailChecked, setMailChecked] = useState(false);
  const [mailTo, setMailTo] = useState("");
  const [msg, setMsg] = useState("");

  function friendlyShareError(raw: string) {
    const t = raw.toLowerCase();
    if (t.includes("user not found") || t.includes("kullanıcı bulunamadı")) {
      return "Kullanıcı bulunamadı (kayıtlı e-posta veya görünen ad girin)";
    }
    if (t.includes("cannot share with yourself") || t.includes("kendiniz")) {
      return "Kendinizle paylaşamazsınız";
    }
    if (t.includes("mail not configured") || t.includes("mail disabled")) {
      return "E-posta gönderimi kapalı. Admin → Mail ayarlarından SMTP’yi açın.";
    }
    if (t.includes("mail settings incomplete")) {
      return "SMTP ayarları eksik (host / gönderen adresi).";
    }
    if (t.includes("smtp connect") || t.includes("smtp auth") || t.includes("smtp tls")) {
      return `E-posta sunucusuna bağlanılamadı: ${raw}`;
    }
    if (t.includes("to and url required")) {
      return "Alıcı e-posta ve paylaşım linki gerekli.";
    }
    return raw;
  }

  async function refresh() {
    setPerms(await request<Permission[]>(`/api/permissions?entryId=${encodeURIComponent(entryId)}`));
  }
  useEffect(() => {
    void refresh();
    void request<{ enabled: boolean }>("/api/mail/status")
      .then((s) => {
        setMailEnabled(!!s.enabled);
        setMailChecked(true);
      })
      .catch(() => {
        setMailEnabled(false);
        setMailChecked(true);
      });
  }, [entryId]);

  async function grant(e: FormEvent) {
    e.preventDefault();
    try {
      await request("/api/permissions", { method: "POST", body: JSON.stringify({ entryId, email, role }) });
      setEmail("");
      await refresh();
      setMsg("Kullanıcıya paylaşıldı");
    } catch (err) {
      setMsg(friendlyShareError(err instanceof Error ? err.message : "Paylaşılamadı"));
    }
  }

  async function ensureLink() {
    if (linkUrl) return linkUrl;
    const body: Record<string, unknown> = { entryId, password, expiresInDays: days, permission: "download" };
    if (maxDownloads) body.maxDownloads = Number(maxDownloads);
    const res = await request<{ url: string }>("/api/shares", { method: "POST", body: JSON.stringify(body) });
    const full = location.origin + res.url;
    setLinkUrl(full);
    await navigator.clipboard.writeText(full).catch(() => undefined);
    return full;
  }

  async function createLink(e: FormEvent) {
    e.preventDefault();
    try {
      const body: Record<string, unknown> = { entryId, password, expiresInDays: days, permission: "download" };
      if (maxDownloads) body.maxDownloads = Number(maxDownloads);
      const res = await request<{ url: string }>("/api/shares", { method: "POST", body: JSON.stringify(body) });
      const full = location.origin + res.url;
      setLinkUrl(full);
      await navigator.clipboard.writeText(full).catch(() => undefined);
      setMsg("Link oluşturuldu ve kopyalandı");
    } catch (err) {
      setMsg(friendlyShareError(err instanceof Error ? err.message : "Link oluşturulamadı"));
    }
  }

  async function emailLink(e: FormEvent) {
    e.preventDefault();
    try {
      const url = await ensureLink();
      if (!url) {
        setMsg("Önce bir link oluşturun.");
        return;
      }
      await request("/api/shares/email", {
        method: "POST",
        body: JSON.stringify({ to: mailTo, url, subject: `Paylaşım: ${entryName}` })
      });
      setMsg("Link e-posta ile gönderildi.");
    } catch (err) {
      setMsg(friendlyShareError(err instanceof Error ? err.message : "E-posta gönderilemedi"));
    }
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <header className="row between"><h2>Paylaş: {entryName}</h2><button onClick={onClose}>Kapat</button></header>
        <form className="stack" onSubmit={grant}>
          <h3>Kullanıcıya paylaş</h3>
          <input
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="Kayıtlı e-posta veya görünen ad"
            required
          />
          <small>Yalnızca sistemde hesabı olan kullanıcılar. Tam e-posta veya görünen ad yazın.</small>
          <select value={role} onChange={(e) => setRole(e.target.value)}>
            <option value="viewer">Görüntüle</option>
            <option value="commenter">Yorum</option>
            <option value="editor">Düzenle</option>
          </select>
          <button className="primary" type="submit">Paylaş</button>
        </form>
        <ul className="plain-list">
          {perms.map((p) => (
            <li key={p.id}>
              <span>{p.displayName} ({p.email}) — {p.role}</span>
              <button onClick={() => void request("/api/permissions", { method: "DELETE", body: JSON.stringify({ entryId, granteeUserId: p.granteeUserId }) }).then(refresh)}>Kaldır</button>
            </li>
          ))}
        </ul>
        <form className="stack" onSubmit={createLink}>
          <h3>Dışarıya link</h3>
          <input value={password} onChange={(e) => setPassword(e.target.value)} placeholder="şifre (opsiyonel)" />
          <label>Son kullanma (gün)<input type="number" min={0} value={days} onChange={(e) => setDays(Number(e.target.value))} /></label>
          <input value={maxDownloads} onChange={(e) => setMaxDownloads(e.target.value)} placeholder="max indirme (opsiyonel)" />
          <button className="primary" type="submit">Link oluştur</button>
          {linkUrl && <code>{linkUrl}</code>}
        </form>
        {mailChecked && mailEnabled && (
          <form className="stack" onSubmit={emailLink}>
            <h3>Linki e-posta ile gönder</h3>
            <input type="email" required value={mailTo} onChange={(e) => setMailTo(e.target.value)} placeholder="alıcı@ornek.com" />
            <button className="primary" type="submit">
              {linkUrl ? "Gönder" : "Link oluştur ve gönder"}
            </button>
          </form>
        )}
        {mailChecked && !mailEnabled && (
          <p className="mail-hint">E-posta ile link göndermek için Admin → Mail’den SMTP ayarlarını açın.</p>
        )}
        {msg && <p>{msg}</p>}
      </div>
    </div>
  );
}

export function SharesPanel({ request }: Props) {
  const [items, setItems] = useState<ShareLink[]>([]);
  async function refresh() { setItems(await request<ShareLink[]>("/api/shares")); }
  useEffect(() => { void refresh(); }, []);
  return (
    <div className="collab-panel">
      <h2>Paylaşım linklerim</h2>
      <ul className="plain-list">
        {items.map((s) => (
          <li key={s.id}>
            <div>
              <strong>{s.entryName || s.entryId}</strong>
              <small> {s.permission} · {s.downloadCount} indirme{s.expiresAt ? ` · bitiş ${new Date(s.expiresAt).toLocaleDateString()}` : ""}</small>
            </div>
            <div className="row">
              <button onClick={() => void navigator.clipboard.writeText(location.origin + "/s/" + (s.token || ""))}>Kopyala</button>
              <button onClick={() => void request("/api/shares", { method: "DELETE", body: JSON.stringify({ id: s.id }) }).then(refresh)}>İptal</button>
            </div>
          </li>
        ))}
      </ul>
    </div>
  );
}

export function TrashPanel({ request }: Props) {
  const [items, setItems] = useState<{ id: string; name: string; kind: string; deletedAt?: string }[]>([]);
  async function refresh() { setItems(await request("/api/trash")); }
  useEffect(() => { void refresh(); }, []);
  return (
    <div className="collab-panel">
      <h2>Çöp kutusu</h2>
      <ul className="plain-list">
        {items.map((item) => (
          <li key={item.id}>
            <span>{item.name} ({item.kind})</span>
            <div className="row">
              <button onClick={() => void request("/api/sync/restore", { method: "POST", body: JSON.stringify({ fileId: item.id }) }).then(refresh)}>Geri yükle</button>
              <button onClick={() => void request("/api/sync/purge", { method: "POST", body: JSON.stringify({ fileId: item.id }) }).then(refresh)}>Kalıcı sil</button>
            </div>
          </li>
        ))}
        {!items.length && <li>Çöp kutusu boş</li>}
      </ul>
    </div>
  );
}

export function SettingsPanel({ request }: Props) {
  const [devices, setDevices] = useState<Device[]>([]);
  const [security, setSecurity] = useState<{ email2FAEnabled: boolean; mailConfigured: boolean } | null>(null);
  const [password, setPassword] = useState("");
  const [note, setNote] = useState("");
  const [saving, setSaving] = useState(false);

  async function refresh() {
    setDevices(await request<Device[]>("/api/auth/devices"));
    setSecurity(await request("/api/auth/security"));
  }
  useEffect(() => { void refresh(); }, []);

  async function toggle2FA(enabled: boolean) {
    if (!password.trim()) {
      setNote("Değişikliği onaylamak için şifrenizi girin.");
      return;
    }
    setSaving(true);
    setNote("");
    try {
      const next = await request<{ email2FAEnabled: boolean; mailConfigured: boolean }>("/api/auth/security", {
        method: "POST",
        body: JSON.stringify({ email2FAEnabled: enabled, password })
      });
      setSecurity(next);
      setPassword("");
      setNote(enabled ? "E-posta ile iki adımlı doğrulama açıldı." : "İki adımlı doğrulama kapatıldı.");
    } catch (error) {
      setNote(error instanceof Error ? error.message : "Kayıt başarısız");
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="collab-panel">
      <h2>Güvenlik</h2>
      <section className="admin-settings" style={{ marginBottom: 24 }}>
        <div>
          <h3>E-posta ile iki adımlı doğrulama</h3>
          <p>İsteğe bağlı. Açıkken girişte şifreden sonra e-postanıza 6 haneli kod gelir.</p>
          {security && !security.mailConfigured && (
            <p className="notice error">SMTP yapılandırılmamış. Yönetici Mail ayarlarını açmalı.</p>
          )}
        </div>
        <div className="admin-settings-form">
          <label>
            Hesap şifreniz (onay)
            <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" />
          </label>
          <div className="row" style={{ gap: 8 }}>
            <button type="button" className="primary" disabled={saving || !!security?.email2FAEnabled} onClick={() => void toggle2FA(true)}>
              2FA aç
            </button>
            <button type="button" disabled={saving || !security?.email2FAEnabled} onClick={() => void toggle2FA(false)}>
              2FA kapat
            </button>
          </div>
          {note && <div className="notice">{note}</div>}
          {security && (
            <small>Durum: {security.email2FAEnabled ? "Açık" : "Kapalı"}</small>
          )}
        </div>
      </section>

      <h2>Cihazlar</h2>
      <ul className="plain-list">
        {devices.map((d) => (
          <li key={d.id}>
            <span>{d.name}<small> · son: {new Date(d.lastSeenAt).toLocaleString()}</small></span>
            <button onClick={() => void request(`/api/auth/devices/${d.id}`, { method: "DELETE" }).then(refresh)}>İptal et</button>
          </li>
        ))}
      </ul>
    </div>
  );
}

export function ActivityPanel({ request }: Props) {
  const [items, setItems] = useState<Activity[]>([]);
  useEffect(() => { void request<Activity[]>("/api/activities").then(setItems); }, []);
  return (
    <div className="collab-panel">
      <h2>Aktivite</h2>
      <ul className="plain-list">
        {items.map((a) => (
          <li key={a.id}><strong>{a.actorName || a.kind}</strong> — {a.message}<small> · {new Date(a.createdAt).toLocaleString()}</small></li>
        ))}
      </ul>
    </div>
  );
}

export function NotificationsPanel({ request }: Props) {
  const [items, setItems] = useState<Notification[]>([]);
  async function refresh() { setItems(await request<Notification[]>("/api/notifications")); }
  useEffect(() => { void refresh(); }, []);
  return (
    <div className="collab-panel">
      <header className="row between">
        <h2>Bildirimler</h2>
        <button onClick={() => void request("/api/notifications", { method: "POST", body: "{}" }).then(refresh)}>Tümünü okundu say</button>
      </header>
      <ul className="plain-list">
        {items.map((n) => (
          <li key={n.id} className={n.readAt ? "" : "unread"}>
            <strong>{n.title}</strong>
            <div>{n.body}</div>
            <small>{new Date(n.createdAt).toLocaleString()}</small>
          </li>
        ))}
      </ul>
    </div>
  );
}

export function CommentsVersionsPanel({
  request,
  entryId,
  entryName,
  onClose
}: {
  request: Props["request"];
  entryId: string;
  entryName?: string;
  onClose?: () => void;
}) {
  const [comments, setComments] = useState<Comment[]>([]);
  const [versions, setVersions] = useState<Version[]>([]);
  const [body, setBody] = useState("");
  async function refresh() {
    setComments(await request<Comment[]>(`/api/files/comments?entryId=${encodeURIComponent(entryId)}`));
    setVersions(await request<Version[]>(`/api/files/versions?entryId=${encodeURIComponent(entryId)}`));
  }
  useEffect(() => { void refresh(); }, [entryId]);
  return (
    <div className="collab-panel">
      <div className="collab-panel-header">
        <h3>Yorumlar{entryName ? ` · ${entryName}` : ""}</h3>
        {onClose && (
          <button type="button" onClick={onClose} title="Kapat">Kapat</button>
        )}
      </div>
      <ul className="plain-list">{comments.map((c) => <li key={c.id}><strong>{c.displayName}</strong>: {c.body}</li>)}</ul>
      <form className="row" onSubmit={(e) => { e.preventDefault(); void request("/api/files/comments", { method: "POST", body: JSON.stringify({ entryId, body }) }).then(() => { setBody(""); return refresh(); }); }}>
        <input value={body} onChange={(e) => setBody(e.target.value)} placeholder="Yorum yaz" required />
        <button className="primary" type="submit">Gönder</button>
      </form>
      <h3>Sürümler</h3>
      <ul className="plain-list">
        {versions.map((v) => (
          <li key={v.id}>
            <span>v{v.version} · {(v.sizeBytes / 1024).toFixed(1)} KB · {new Date(v.createdAt).toLocaleString()}</span>
            <button onClick={() => void request("/api/files/versions/restore", { method: "POST", body: JSON.stringify({ entryId, versionId: v.id }) }).then(refresh)}>Geri yükle</button>
          </li>
        ))}
      </ul>
    </div>
  );
}

export async function loadDrives(request: Props["request"]) {
  return request<Drive[]>("/api/drives");
}

export async function unreadCount(request: Props["request"]) {
  const items = await request<Notification[]>("/api/notifications?unread=1");
  return items.length;
}
