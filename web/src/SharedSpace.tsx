import { ChangeEvent, FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";

// "Ortak Paylaşım": the server-wide shared folder, its admin settings
// (permissions, groups, quota) and the admin-only torrent client.

type Request = <T>(path: string, init?: RequestInit) => Promise<T>;

type Level = "none" | "read" | "write" | "admin";

export type SharedAccess = { available: boolean; rootLevel: Level; maxLevel: Level; admin: boolean };

export type SharedQuota = {
  quotaBytes: number;
  limitBytes: number;
  usedBytes: number;
  freeBytes: number;
  diskTotalBytes: number;
  diskFreeBytes: number;
  usedPercent: number;
  warnPercent: number;
  level: "ok" | "warning" | "full";
  message?: string;
};

export type SharedStatus = { access: SharedAccess; quota?: SharedQuota; unavailable?: boolean };

type Entry = { name: string; path: string; isDir: boolean; size: number; modifiedAt: string; level: Level };
type Listing = { path: string; level: Level; entries: Entry[] };

type PublicLink = {
  id: string;
  path: string;
  name: string;
  hasPassword: boolean;
  expiresAt: string;
  maxDownloads: number | null;
  downloadCount: number;
  createdAt: string;
  createdBy?: string;
  url?: string;
  token?: string;
};

type Grant = {
  id: string;
  path: string;
  subjectType: "user" | "group" | "everyone";
  userId?: string;
  userEmail?: string;
  userName?: string;
  groupId?: string;
  groupName?: string;
  permission: "read" | "write";
};

type GroupMember = { userId: string; email: string; displayName: string };
type Group = { id: string; name: string; members: GroupMember[] | null };

type DirectoryUser = { id: string; email: string; displayName: string };

type TorrentView = {
  id: string;
  infoHash: string;
  name: string;
  status: "metadata" | "verifying" | "downloading" | "stalled" | "paused" | "completed" | "seeding" | "error";
  message?: string;
  path?: string;
  totalBytes: number;
  completedBytes: number;
  progress: number;
  downloadRate: number;
  uploadRate: number;
  peers: number;
  seeders: number;
  ratio: number;
  etaSec: number;
  uploadedBytes: number;
  seededSeconds: number;
  downloadLimitKBps: number;
  uploadLimitKBps: number;
  seedRatioLimit: number;
  seedTimeLimitSec: number;
  addedAt: string;
};

type TorrentFile = {
  path: string;
  length: number;
  completed: number;
  priority: "skip" | "normal" | "high";
};

type TorrentDetail = TorrentView & {
  files: TorrentFile[];
  trackers: { url: string }[];
  peers: { addr: string; client?: string }[];
};

type TorrentFilter = "all" | "downloading" | "seeding" | "completed";

type TorrentOverview = {
  enabled: boolean;
  available: boolean;
  reason?: string;
  listenPort: number;
  seedAfterComplete: boolean;
  limits: { downloadLimitKBps: number; uploadLimitKBps: number };
  torrents: TorrentView[] | null;
  quota?: SharedQuota;
};

const GiB = 1024 * 1024 * 1024;

const formatBytes = (value: number) => {
  if (!value || value < 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let size = value;
  let index = 0;
  while (size >= 1024 && index < units.length - 1) {
    size /= 1024;
    index++;
  }
  return `${size.toFixed(index === 0 ? 0 : 1)} ${units[index]}`;
};

const formatDate = (value: string) => {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "" : date.toLocaleString("tr-TR", { dateStyle: "short", timeStyle: "short" });
};

const errorText = (error: unknown, fallback: string) => (error instanceof Error && error.message ? error.message : fallback);

const csrfToken = () => {
  const cookie = document.cookie.split("; ").find((item) => item.startsWith("csrf_token="));
  return cookie ? decodeURIComponent(cookie.slice("csrf_token=".length)) : "";
};

class HttpError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

// rawSend posts a body as-is (a file, a .torrent). The app-wide request()
// helper always labels the body as JSON, which is wrong for these.
async function rawSend<T>(url: string, body: Blob, contentType: string): Promise<T> {
  const response = await fetch(url, {
    method: "POST",
    credentials: "include",
    headers: { "Content-Type": contentType, "X-CSRF-Token": csrfToken() },
    body
  });
  if (!response.ok) {
    let message = `İşlem başarısız (HTTP ${response.status})`;
    try {
      const parsed = (await response.json()) as { error?: string };
      if (parsed?.error) message = parsed.error;
    } catch {
      // The body was not JSON; the status code is all there is.
    }
    throw new HttpError(response.status, message);
  }
  return response.json() as Promise<T>;
}

// The app passes request/onMessage as fresh closures on every render. Effects
// here must not re-run because of that, so they go through a stable wrapper
// that always calls the latest one.
function useLatest<A extends unknown[], R>(fn: (...args: A) => R): (...args: A) => R {
  const ref = useRef(fn);
  ref.current = fn;
  return useCallback((...args: A) => ref.current(...args), []);
}

function useStableRequest(request: Request): Request {
  const ref = useRef(request);
  ref.current = request;
  return useCallback(<T,>(path: string, init?: RequestInit) => ref.current<T>(path, init), []);
}

const canWrite = (level: Level) => level === "write" || level === "admin";
const joinPath = (dir: string, name: string) => (dir ? `${dir}/${name}` : name);
const query = (params: Record<string, string>) => new URLSearchParams(params).toString();

function QuotaBar({ quota }: { quota: SharedQuota }) {
  const percent = Math.max(0, Math.min(100, quota.usedPercent));
  const free = formatBytes(Math.max(0, quota.freeBytes));
  // Without an explicit quota the only limit is the disk, which other data
  // also fills: the bar then shows how full the disk is.
  const hasQuota = quota.quotaBytes > 0;
  return (
    <div className={`ss-quota ${quota.level}`} role="status">
      <div className="ss-quota-text">
        <strong>
          {hasQuota ? `${formatBytes(quota.usedBytes)} / ${formatBytes(quota.quotaBytes)}` : `${formatBytes(quota.usedBytes)} kullanılıyor`}
        </strong>
        <small>
          {quota.level === "full" ? "Alan dolu" : `${free} boş`} · {hasQuota ? "" : "disk "}%{percent}
        </small>
      </div>
      <div className="ss-quota-track" aria-hidden="true"><span style={{ width: `${percent}%` }} /></div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// User-facing browser
// ---------------------------------------------------------------------------

export function SharedSpacePanel({ request: rawRequest }: { request: Request }) {
  const request = useStableRequest(rawRequest);
  const [status, setStatus] = useState<SharedStatus | null>(null);
  const [path, setPath] = useState("");
  const [listing, setListing] = useState<Listing | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState("");
  const [links, setLinks] = useState<PublicLink[]>([]);
  const [linkTarget, setLinkTarget] = useState<Entry | null>(null);
  const [showLinks, setShowLinks] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);

  const load = useCallback(async (target: string) => {
    setLoading(true);
    setError("");
    try {
      const [nextStatus, nextListing] = await Promise.all([
        request<SharedStatus>("/api/shared/status"),
        request<Listing>(`/api/shared/list?${query({ path: target })}`)
      ]);
      setStatus(nextStatus);
      setListing({ ...nextListing, entries: nextListing.entries ?? [] });
      setPath(nextListing.path);
    } catch (err) {
      setListing(null);
      setError(errorText(err, "Ortak Paylaşım açılamadı"));
      try {
        setStatus(await request<SharedStatus>("/api/shared/status"));
      } catch {
        // Keep whatever status was known.
      }
    } finally {
      setLoading(false);
    }
  }, [request]);

  const loadLinks = useCallback(async () => {
    try {
      setLinks((await request<PublicLink[] | null>("/api/shared/links")) ?? []);
    } catch {
      setLinks([]);
    }
  }, [request]);

  useEffect(() => {
    void load("");
    void loadLinks();
  }, [load, loadLinks]);

  const crumbs = useMemo(() => {
    const parts = path ? path.split("/") : [];
    return parts.map((name, index) => ({ name, path: parts.slice(0, index + 1).join("/") }));
  }, [path]);

  const folderWritable = !!listing && canWrite(listing.level);
  const mayPublish = !!status && canWrite(status.access.maxLevel);

  async function act(key: string, work: () => Promise<void>, fallback: string) {
    setBusy(key);
    setError("");
    setNote("");
    try {
      await work();
    } catch (err) {
      setError(errorText(err, fallback));
    } finally {
      setBusy("");
    }
  }

  async function createFolder() {
    const name = window.prompt("Yeni klasörün adı");
    if (!name?.trim()) return;
    await act("folder", async () => {
      await request("/api/shared/folder", { method: "POST", body: JSON.stringify({ path, name: name.trim() }) });
      await load(path);
    }, "Klasör oluşturulamadı");
  }

  async function uploadFiles(event: ChangeEvent<HTMLInputElement>) {
    const files = Array.from(event.target.files ?? []);
    event.target.value = "";
    if (!files.length) return;
    await act("upload", async () => {
      let done = 0;
      for (const file of files) {
        setNote(`Yükleniyor: ${file.name} (${done + 1}/${files.length})`);
        const url = (overwrite: boolean) =>
          `/api/shared/upload?${query({ path, name: file.name, ...(overwrite ? { overwrite: "1" } : {}) })}`;
        try {
          await rawSend(url(false), file, "application/octet-stream");
        } catch (err) {
          if (err instanceof HttpError && err.status === 409 && window.confirm(`"${file.name}" zaten var. Üzerine yazılsın mı?`)) {
            await rawSend(url(true), file, "application/octet-stream");
          } else if (err instanceof HttpError && err.status === 409) {
            continue;
          } else {
            throw err;
          }
        }
        done++;
      }
      await load(path);
      setNote(done ? `${done} dosya yüklendi.` : "");
    }, "Dosya yüklenemedi");
  }

  async function rename(entry: Entry) {
    const name = window.prompt("Yeni ad", entry.name);
    if (!name?.trim() || name.trim() === entry.name) return;
    await act(entry.path, async () => {
      await request("/api/shared/rename", { method: "POST", body: JSON.stringify({ path: entry.path, name: name.trim() }) });
      await Promise.all([load(path), loadLinks()]);
    }, "Yeniden adlandırılamadı");
  }

  async function remove(entry: Entry) {
    const what = entry.isDir ? `"${entry.name}" klasörü ve içindeki her şey` : `"${entry.name}"`;
    if (!window.confirm(`${what} kalıcı olarak silinecek. Bu işlem geri alınamaz. Devam edilsin mi?`)) return;
    await act(entry.path, async () => {
      await request("/api/shared/delete", { method: "POST", body: JSON.stringify({ path: entry.path }) });
      await Promise.all([load(path), loadLinks()]);
      setNote(`"${entry.name}" silindi.`);
    }, "Silinemedi");
  }

  async function revoke(link: PublicLink) {
    if (!window.confirm(`"${link.name}" bağlantısı iptal edilsin mi? Bağlantıyı bilen kimse artık açamaz.`)) return;
    await act(`link:${link.id}`, async () => {
      await request(`/api/shared/links/${encodeURIComponent(link.id)}`, { method: "DELETE" });
      await loadLinks();
    }, "Bağlantı iptal edilemedi");
  }

  if (status?.unavailable) {
    return (
      <section className="ss-panel">
        <header className="ss-header"><div><h1>Ortak Paylaşım</h1></div></header>
        <div className="notice error">Ortak Paylaşım bu sunucuda kullanılamıyor. Sunucu yöneticisi paylaşım klasörünü (SHARED_DIR) kontrol etmeli.</div>
      </section>
    );
  }

  return (
    <section className="ss-panel">
      <header className="ss-header">
        <div>
          <h1>Ortak Paylaşım</h1>
          <p>Sunucudaki ortak klasör. Yalnızca size izin verilen klasörleri görürsünüz.</p>
        </div>
        {status?.quota && <QuotaBar quota={status.quota} />}
      </header>

      <div className="ss-toolbar">
        <nav className="ss-crumbs" aria-label="Konum">
          <button type="button" onClick={() => void load("")} disabled={!path}>Ortak Paylaşım</button>
          {crumbs.map((crumb, index) => (
            <span key={crumb.path}>
              <i aria-hidden="true">›</i>
              <button type="button" onClick={() => void load(crumb.path)} disabled={index === crumbs.length - 1}>{crumb.name}</button>
            </span>
          ))}
        </nav>
        <div className="ss-actions">
          {mayPublish && (
            <button type="button" onClick={() => setShowLinks((value) => !value)}>
              Bağlantılar{links.length ? ` (${links.length})` : ""}
            </button>
          )}
          {folderWritable && (
            <>
              <button type="button" disabled={!!busy} onClick={() => void createFolder()}>Yeni klasör</button>
              <button type="button" className="primary" disabled={!!busy || status?.quota?.level === "full"} onClick={() => fileInput.current?.click()}>
                {busy === "upload" ? "Yükleniyor…" : "Dosya yükle"}
              </button>
              <input ref={fileInput} type="file" multiple hidden onChange={(event) => void uploadFiles(event)} />
            </>
          )}
        </div>
      </div>

      {status?.quota && status.quota.level !== "ok" && folderWritable && (
        <div className={`notice ${status.quota.level === "full" ? "error" : ""}`}>
          {status.quota.message || (status.quota.level === "full" ? "Ortak Paylaşım alanı dolu; yeni dosya yüklenemez." : "Ortak Paylaşım alanı dolmak üzere.")}
        </div>
      )}
      {error && <div className="notice error"><span>{error}</span><button type="button" aria-label="Kapat" onClick={() => setError("")}>×</button></div>}
      {note && <div className="notice"><span>{note}</span><button type="button" aria-label="Kapat" onClick={() => setNote("")}>×</button></div>}

      {showLinks && mayPublish && (
        <div className="ss-card">
          <div className="ss-card-head">
            <h2>Herkese açık bağlantılar</h2>
            <p>Bağlantı adresi yalnızca oluşturulduğu anda gösterilir. Kaybederseniz iptal edip yenisini oluşturun.</p>
          </div>
          {links.length === 0 ? (
            <div className="ss-empty small">Henüz bağlantı yok. Bir dosya veya klasörün yanındaki “Bağlantı” düğmesini kullanın.</div>
          ) : (
            <ul className="ss-links">
              {links.map((link) => (
                <li key={link.id}>
                  <div>
                    <strong>{link.path}</strong>
                    <small>
                      {link.hasPassword ? "Şifreli" : "Şifresiz"} · bitiş {formatDate(link.expiresAt)} · {link.downloadCount}
                      {link.maxDownloads ? ` / ${link.maxDownloads}` : ""} indirme
                      {link.createdBy ? ` · ${link.createdBy}` : ""}
                    </small>
                  </div>
                  <button type="button" className="danger" disabled={busy === `link:${link.id}`} onClick={() => void revoke(link)}>İptal et</button>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      <div className="ss-list">
        <div className="ss-row ss-row-head"><span>Ad</span><span>Boyut</span><span>Değiştirilme</span><span>İşlemler</span></div>
        {loading && !listing && <div className="ss-empty">Yükleniyor…</div>}
        {!loading && !listing && !error && <div className="ss-empty">Bu klasör açılamadı.</div>}
        {listing && listing.entries.length === 0 && (
          <div className="ss-empty">
            {folderWritable ? "Bu klasör boş. Dosya yükleyerek veya klasör oluşturarak başlayın." : "Bu klasörde görebileceğiniz bir şey yok."}
          </div>
        )}
        {listing?.entries.map((entry) => {
          const rowBusy = busy === entry.path;
          return (
            <div className="ss-row" key={entry.path}>
              <span className="ss-name">
                <i aria-hidden="true">{entry.isDir ? "📁" : "📄"}</i>
                {entry.isDir ? (
                  <button type="button" onClick={() => void load(entry.path)}>{entry.name}</button>
                ) : (
                  <a href={`/api/shared/download?${query({ path: entry.path })}`}>{entry.name}</a>
                )}
                {entry.level === "none" && <em title="Yalnızca içindeki izinli klasörlere geçiş">geçiş</em>}
                {entry.level === "read" && <em>salt okunur</em>}
              </span>
              <span>{entry.isDir ? "—" : formatBytes(entry.size)}</span>
              <span>{formatDate(entry.modifiedAt)}</span>
              <span className="ss-row-actions">
                {!entry.isDir && <a className="ss-btn" href={`/api/shared/download?${query({ path: entry.path })}`}>İndir</a>}
                {canWrite(entry.level) && <button type="button" disabled={rowBusy} onClick={() => setLinkTarget(entry)}>Bağlantı</button>}
                {folderWritable && <button type="button" disabled={rowBusy} onClick={() => void rename(entry)}>Yeniden adlandır</button>}
                {folderWritable && <button type="button" className="danger" disabled={rowBusy} onClick={() => void remove(entry)}>Sil</button>}
              </span>
            </div>
          );
        })}
      </div>

      {linkTarget && (
        <LinkDialog
          entry={linkTarget}
          request={request}
          onClose={() => {
            setLinkTarget(null);
            void loadLinks();
          }}
        />
      )}
    </section>
  );
}

function LinkDialog({ entry, request, onClose }: { entry: Entry; request: Request; onClose: () => void }) {
  const [hours, setHours] = useState("168");
  const [password, setPassword] = useState("");
  const [maxDownloads, setMaxDownloads] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [created, setCreated] = useState<PublicLink | null>(null);
  const [copied, setCopied] = useState(false);

  const fullURL = created?.url ? new URL(created.url, window.location.origin).toString() : "";

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    const limit = maxDownloads.trim() === "" ? null : Number(maxDownloads);
    if (limit !== null && (!Number.isInteger(limit) || limit < 1)) {
      setError("İndirme sınırı 1 veya daha büyük bir tam sayı olmalı.");
      return;
    }
    setSaving(true);
    try {
      setCreated(await request<PublicLink>("/api/shared/links", {
        method: "POST",
        body: JSON.stringify({ path: entry.path, expiresInHours: Number(hours), password, maxDownloads: limit })
      }));
    } catch (err) {
      setError(errorText(err, "Bağlantı oluşturulamadı"));
    } finally {
      setSaving(false);
    }
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(fullURL);
      setCopied(true);
    } catch {
      setCopied(false);
      setError("Panoya kopyalanamadı; adresi elle seçip kopyalayın.");
    }
  }

  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true" aria-label="Herkese açık bağlantı">
      <div className="modal ss-dialog">
        <div className="row between">
          <h3>Herkese açık bağlantı</h3>
          <button type="button" className="ss-close" aria-label="Kapat" onClick={onClose}>×</button>
        </div>
        <p className="ss-muted">
          <strong>{entry.path}</strong> {entry.isDir ? "klasörünü ve içindekileri" : "dosyasını"} bağlantıyı bilen herkes, hesap açmadan indirebilir.
        </p>
        {created ? (
          <div className="stack">
            <label className="ss-field">
              Bağlantı adresi
              <input readOnly value={fullURL} onFocus={(event) => event.target.select()} />
            </label>
            <p className="ss-muted">Bu adres bir daha gösterilmez. Şimdi kopyalayın{created.hasPassword ? " ve şifreyi ayrı bir kanaldan iletin" : ""}.</p>
            {error && <div className="notice error">{error}</div>}
            <div className="row">
              <button type="button" className="primary" onClick={() => void copy()}>{copied ? "Kopyalandı ✓" : "Kopyala"}</button>
              <button type="button" onClick={onClose}>Kapat</button>
            </div>
          </div>
        ) : (
          <form className="stack" onSubmit={(event) => void submit(event)}>
            <label className="ss-field">
              Geçerlilik süresi
              <select value={hours} onChange={(event) => setHours(event.target.value)}>
                <option value="1">1 saat</option>
                <option value="24">1 gün</option>
                <option value="168">7 gün</option>
                <option value="720">30 gün</option>
              </select>
            </label>
            <label className="ss-field">
              Şifre (isteğe bağlı)
              <input type="password" autoComplete="new-password" value={password} onChange={(event) => setPassword(event.target.value)} placeholder="Boş bırakılırsa şifresiz" />
            </label>
            <label className="ss-field">
              İndirme sınırı (isteğe bağlı)
              <input type="number" min={1} inputMode="numeric" value={maxDownloads} onChange={(event) => setMaxDownloads(event.target.value)} placeholder="Sınırsız" />
            </label>
            {error && <div className="notice error">{error}</div>}
            <div className="row">
              <button type="submit" className="primary" disabled={saving}>{saving ? "Oluşturuluyor…" : "Bağlantı oluştur"}</button>
              <button type="button" onClick={onClose}>Vazgeç</button>
            </div>
          </form>
        )}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Admin: permissions, groups, quota
// ---------------------------------------------------------------------------

type AdminSettings = { settings: { quotaBytes: number; warnPercent: number }; quota: SharedQuota; directory: string };

export function SharedAdminSection(props: { request: Request; users: DirectoryUser[]; onMessage: (message: string) => void }) {
  const request = useStableRequest(props.request);
  const onMessage = useLatest(props.onMessage);
  const users = props.users;
  const [unavailable, setUnavailable] = useState(false);
  const [info, setInfo] = useState<AdminSettings | null>(null);
  const [quotaGB, setQuotaGB] = useState("");
  const [warnPercent, setWarnPercent] = useState("90");
  const [grants, setGrants] = useState<Grant[]>([]);
  const [groups, setGroups] = useState<Group[]>([]);
  const [folders, setFolders] = useState<string[]>([]);
  const [saving, setSaving] = useState("");

  const [grantPath, setGrantPath] = useState("");
  const [subjectType, setSubjectType] = useState<"user" | "group" | "everyone">("user");
  const [subjectId, setSubjectId] = useState("");
  const [permission, setPermission] = useState<"read" | "write">("read");
  const [groupName, setGroupName] = useState("");
  const [memberPick, setMemberPick] = useState<Record<string, string>>({});

  const load = useCallback(async () => {
    try {
      const status = await request<SharedStatus>("/api/shared/status");
      if (status.unavailable) {
        setUnavailable(true);
        return;
      }
      setUnavailable(false);
      const [nextInfo, nextGrants, nextGroups, root] = await Promise.all([
        request<AdminSettings>("/api/admin/shared/settings"),
        request<Grant[]>("/api/admin/shared/grants"),
        request<Group[]>("/api/admin/shared/groups"),
        request<Listing>("/api/shared/list?path=")
      ]);
      setInfo(nextInfo);
      setQuotaGB(nextInfo.settings.quotaBytes > 0 ? String(Number((nextInfo.settings.quotaBytes / GiB).toFixed(2))) : "");
      setWarnPercent(String(nextInfo.settings.warnPercent));
      setGrants(nextGrants ?? []);
      setGroups(nextGroups ?? []);
      setFolders((root.entries ?? []).filter((entry) => entry.isDir).map((entry) => entry.path));
    } catch (err) {
      onMessage(errorText(err, "Ortak Paylaşım ayarları yüklenemedi"));
    }
  }, [request, onMessage]);

  useEffect(() => { void load(); }, [load]);

  async function run(key: string, work: () => Promise<void>, success: string, fallback: string) {
    setSaving(key);
    try {
      await work();
      await load();
      if (success) onMessage(success);
    } catch (err) {
      onMessage(errorText(err, fallback));
    } finally {
      setSaving("");
    }
  }

  async function saveQuota(event: FormEvent) {
    event.preventDefault();
    const gb = quotaGB.trim() === "" ? 0 : Number(quotaGB.replace(",", "."));
    const warn = Number(warnPercent);
    if (!Number.isFinite(gb) || gb < 0) {
      onMessage("Kota sıfır veya pozitif bir GB değeri olmalı (boş = disk kapasitesi).");
      return;
    }
    if (!Number.isInteger(warn) || warn < 1 || warn > 99) {
      onMessage("Uyarı eşiği 1 ile 99 arasında bir yüzde olmalı.");
      return;
    }
    await run("quota", async () => {
      await request("/api/admin/shared/settings", { method: "PUT", body: JSON.stringify({ quotaBytes: Math.round(gb * GiB), warnPercent: warn }) });
    }, "Ortak Paylaşım kotası güncellendi.", "Kota kaydedilemedi");
  }

  async function addGrant(event: FormEvent) {
    event.preventDefault();
    if (subjectType !== "everyone" && !subjectId) {
      onMessage(subjectType === "user" ? "Bir kullanıcı seçin." : "Bir grup seçin.");
      return;
    }
    await run("grant", async () => {
      await request("/api/admin/shared/grants", {
        method: "POST",
        body: JSON.stringify({
          path: grantPath.trim().replace(/^\/+|\/+$/g, ""),
          subjectType,
          userId: subjectType === "user" ? subjectId : "",
          groupId: subjectType === "group" ? subjectId : "",
          permission
        })
      });
    }, "İzin kaydedildi.", "İzin kaydedilemedi");
  }

  async function removeGrant(grant: Grant) {
    if (!window.confirm("Bu izin kaldırılsın mı? İlgili kişiler klasöre erişimini hemen kaybeder.")) return;
    await run(`grant:${grant.id}`, async () => {
      await request(`/api/admin/shared/grants/${encodeURIComponent(grant.id)}`, { method: "DELETE" });
    }, "İzin kaldırıldı.", "İzin kaldırılamadı");
  }

  async function createGroup(event: FormEvent) {
    event.preventDefault();
    if (!groupName.trim()) return;
    await run("group", async () => {
      await request("/api/admin/shared/groups", { method: "POST", body: JSON.stringify({ name: groupName.trim() }) });
      setGroupName("");
    }, "Grup oluşturuldu.", "Grup oluşturulamadı");
  }

  async function deleteGroup(group: Group) {
    if (!window.confirm(`"${group.name}" grubu ve bu gruba verilen tüm izinler silinsin mi?`)) return;
    await run(`group:${group.id}`, async () => {
      await request(`/api/admin/shared/groups/${encodeURIComponent(group.id)}`, { method: "DELETE" });
    }, "Grup silindi.", "Grup silinemedi");
  }

  async function addMember(group: Group) {
    const userId = memberPick[group.id];
    if (!userId) return;
    await run(`group:${group.id}`, async () => {
      await request(`/api/admin/shared/groups/${encodeURIComponent(group.id)}/members`, { method: "POST", body: JSON.stringify({ userId }) });
      setMemberPick((current) => ({ ...current, [group.id]: "" }));
    }, "", "Üye eklenemedi");
  }

  async function removeMember(group: Group, member: GroupMember) {
    await run(`group:${group.id}`, async () => {
      await request(`/api/admin/shared/groups/${encodeURIComponent(group.id)}/members?${query({ userId: member.userId })}`, { method: "DELETE" });
    }, "", "Üye çıkarılamadı");
  }

  const subjectLabel = (grant: Grant) => {
    if (grant.subjectType === "everyone") return "Tüm kullanıcılar";
    if (grant.subjectType === "group") return `Grup: ${grant.groupName || "silinmiş"}`;
    return grant.userName ? `${grant.userName} (${grant.userEmail})` : grant.userEmail || "silinmiş kullanıcı";
  };

  if (unavailable) {
    return (
      <section className="admin-settings">
        <div>
          <h2>Ortak Paylaşım</h2>
          <p>Paylaşım klasörü açılamadı. <code>SHARED_DIR</code> yolunun var olduğunu ve sunucunun yazabildiğini kontrol edin, ardından sunucuyu yeniden başlatın.</p>
        </div>
      </section>
    );
  }

  return (
    <section className="ss-admin">
      <div className="ss-admin-head">
        <div>
          <h2>Ortak Paylaşım</h2>
          <p>
            Sunucudaki ortak klasör{info?.directory ? <> (<code>{info.directory}</code>)</> : null}. Kimse izin verilmeden göremez; yöneticiler her şeyi görür.
          </p>
        </div>
        {info?.quota && <QuotaBar quota={info.quota} />}
      </div>

      <form className="admin-settings-form ss-admin-block" onSubmit={(event) => void saveQuota(event)}>
        <label>
          Kota (GB)
          <input inputMode="decimal" value={quotaGB} onChange={(event) => setQuotaGB(event.target.value)} placeholder="Boş = disk kapasitesi" />
        </label>
        <label>
          Uyarı eşiği (%)
          <input type="number" min={1} max={99} value={warnPercent} onChange={(event) => setWarnPercent(event.target.value)} />
        </label>
        <button type="submit" disabled={saving === "quota"}>{saving === "quota" ? "Kaydediliyor…" : "Kotayı kaydet"}</button>
        {info?.quota && (
          <p className="ss-hint">
            Disk: {formatBytes(info.quota.diskFreeBytes)} boş / {formatBytes(info.quota.diskTotalBytes)}. Eşik aşıldığında ve alan dolduğunda yöneticilere bildirim ve e-posta gider.
          </p>
        )}
      </form>

      <div className="ss-admin-block">
        <h3>Klasör izinleri</h3>
        <p className="ss-hint">İzin alt klasörlere de geçerlidir. “Yazma” yükleme, silme, yeniden adlandırma ve herkese açık bağlantı oluşturmayı kapsar. Klasör yolunu boş bırakmak tüm paylaşım alanı demektir.</p>
        <form className="admin-settings-form" onSubmit={(event) => void addGrant(event)}>
          <label>
            Klasör yolu
            <input list="ss-folder-options" value={grantPath} onChange={(event) => setGrantPath(event.target.value)} placeholder="ör. Filmler veya Belgeler/2024" />
            <datalist id="ss-folder-options">{folders.map((folder) => <option value={folder} key={folder} />)}</datalist>
          </label>
          <label>
            Kime
            <select value={subjectType} onChange={(event) => { setSubjectType(event.target.value as typeof subjectType); setSubjectId(""); }}>
              <option value="user">Kullanıcı</option>
              <option value="group">Grup</option>
              <option value="everyone">Tüm kullanıcılar</option>
            </select>
          </label>
          {subjectType === "user" && (
            <label>
              Kullanıcı
              <select value={subjectId} onChange={(event) => setSubjectId(event.target.value)}>
                <option value="">Seçin…</option>
                {users.map((user) => <option value={user.id} key={user.id}>{user.displayName} ({user.email})</option>)}
              </select>
            </label>
          )}
          {subjectType === "group" && (
            <label>
              Grup
              <select value={subjectId} onChange={(event) => setSubjectId(event.target.value)}>
                <option value="">{groups.length ? "Seçin…" : "Önce bir grup oluşturun"}</option>
                {groups.map((group) => <option value={group.id} key={group.id}>{group.name}</option>)}
              </select>
            </label>
          )}
          <label>
            İzin
            <select value={permission} onChange={(event) => setPermission(event.target.value as typeof permission)}>
              <option value="read">Okuma</option>
              <option value="write">Yazma</option>
            </select>
          </label>
          <button type="submit" disabled={saving === "grant"}>{saving === "grant" ? "Kaydediliyor…" : "İzin ver"}</button>
        </form>
        {grants.length === 0 ? (
          <div className="ss-empty small">Henüz izin verilmedi. Şu anda Ortak Paylaşım'ı yalnızca yöneticiler görebilir.</div>
        ) : (
          <ul className="ss-links">
            {grants.map((grant) => (
              <li key={grant.id}>
                <div>
                  <strong>{grant.path || "Tüm paylaşım alanı"}</strong>
                  <small>{subjectLabel(grant)}</small>
                </div>
                <span className={`ss-badge ${grant.permission}`}>{grant.permission === "write" ? "Yazma" : "Okuma"}</span>
                <button type="button" className="danger" disabled={saving === `grant:${grant.id}`} onClick={() => void removeGrant(grant)}>Kaldır</button>
              </li>
            ))}
          </ul>
        )}
      </div>

      <div className="ss-admin-block">
        <h3>Gruplar</h3>
        <form className="admin-settings-form" onSubmit={(event) => void createGroup(event)}>
          <label>
            Yeni grup adı
            <input value={groupName} onChange={(event) => setGroupName(event.target.value)} placeholder="ör. Muhasebe" maxLength={80} />
          </label>
          <button type="submit" disabled={saving === "group" || !groupName.trim()}>Grup oluştur</button>
        </form>
        {groups.length === 0 ? (
          <div className="ss-empty small">Grup yok. Gruplar, aynı izni birden çok kişiye tek seferde vermek içindir.</div>
        ) : (
          <div className="ss-groups">
            {groups.map((group) => {
              const members = group.members ?? [];
              const candidates = users.filter((user) => !members.some((member) => member.userId === user.id));
              const groupBusy = saving === `group:${group.id}`;
              return (
                <article className="ss-group" key={group.id}>
                  <div className="row between">
                    <strong>{group.name}</strong>
                    <button type="button" className="danger" disabled={groupBusy} onClick={() => void deleteGroup(group)}>Grubu sil</button>
                  </div>
                  <div className="ss-chips">
                    {members.length === 0 && <small>Üye yok</small>}
                    {members.map((member) => (
                      <span className="ss-chip" key={member.userId}>
                        {member.displayName || member.email}
                        <button type="button" aria-label={`${member.displayName || member.email} üyesini çıkar`} disabled={groupBusy} onClick={() => void removeMember(group, member)}>×</button>
                      </span>
                    ))}
                  </div>
                  <div className="row">
                    <select aria-label="Eklenecek kullanıcı" value={memberPick[group.id] ?? ""} onChange={(event) => setMemberPick((current) => ({ ...current, [group.id]: event.target.value }))}>
                      <option value="">Kullanıcı ekle…</option>
                      {candidates.map((user) => <option value={user.id} key={user.id}>{user.displayName} ({user.email})</option>)}
                    </select>
                    <button type="button" disabled={groupBusy || !memberPick[group.id]} onClick={() => void addMember(group)}>Ekle</button>
                  </div>
                </article>
              );
            })}
          </div>
        )}
      </div>
    </section>
  );
}

// ---------------------------------------------------------------------------
// Admin: torrent client
// ---------------------------------------------------------------------------

const torrentStatus: Record<TorrentView["status"], string> = {
  metadata: "Bilgi alınıyor",
  verifying: "Doğrulanıyor",
  downloading: "İndiriliyor",
  stalled: "Bekliyor (alan dolu)",
  paused: "Duraklatıldı",
  completed: "Tamamlandı",
  seeding: "Seed",
  error: "Hata"
};

const torrentFilters: { id: TorrentFilter; label: string }[] = [
  { id: "all", label: "Tümü" },
  { id: "downloading", label: "İndirilen" },
  { id: "seeding", label: "Seed" },
  { id: "completed", label: "Tamamlanan" }
];

const formatRate = (bytesPerSecond: number) => (bytesPerSecond > 0 ? `${formatBytes(bytesPerSecond)}/s` : "—");

const formatETA = (seconds: number) => {
  if (!Number.isFinite(seconds) || seconds < 0) return "—";
  if (seconds < 60) return `${Math.round(seconds)} sn`;
  if (seconds < 3600) return `${Math.round(seconds / 60)} dk`;
  if (seconds < 86400) return `${Math.round(seconds / 3600)} sa`;
  return `${Math.round(seconds / 86400)} gün`;
};

const formatSeedTime = (seconds: number) => {
  if (seconds < 60) return `${seconds} sn`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)} dk`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} sa`;
  return `${Math.floor(seconds / 86400)} gün`;
};

const matchesFilter = (item: TorrentView, filter: TorrentFilter) => {
  switch (filter) {
    case "downloading":
      return item.status === "downloading" || item.status === "metadata" || item.status === "stalled" || item.status === "verifying";
    case "seeding":
      return item.status === "seeding";
    case "completed":
      return item.status === "completed" || item.status === "seeding";
    default:
      return true;
  }
};

export function TorrentAdminSection(props: { request: Request; onMessage: (message: string) => void }) {
  const request = useStableRequest(props.request);
  const onMessage = useLatest(props.onMessage);
  const [overview, setOverview] = useState<TorrentOverview | null>(null);
  const [failed, setFailed] = useState("");
  const [magnet, setMagnet] = useState("");
  const [adding, setAdding] = useState(false);
  const [busy, setBusy] = useState("");
  const [downLimit, setDownLimit] = useState("");
  const [upLimit, setUpLimit] = useState("");
  const limitsTouched = useRef(false);
  const fileInput = useRef<HTMLInputElement>(null);
  const [filter, setFilter] = useState<TorrentFilter>("all");
  const [detailId, setDetailId] = useState("");
  const [detail, setDetail] = useState<TorrentDetail | null>(null);
  const [detailFailed, setDetailFailed] = useState("");
  const [detailBusy, setDetailBusy] = useState(false);
  const [itemLimits, setItemLimits] = useState({ down: "", up: "", ratio: "", time: "" });

  const loadDetail = useCallback(async (id: string) => {
    try {
      const next = await request<TorrentDetail>(`/api/admin/torrents/${encodeURIComponent(id)}`);
      setDetail(next);
      setDetailFailed("");
    } catch (err) {
      setDetailFailed(errorText(err, "Torrent ayrıntısı alınamadı"));
    }
  }, [request]);

  // Fill the drawer's limit fields only when a different torrent is opened,
  // so polling never overwrites what the administrator is typing.
  useEffect(() => {
    if (!detail) return;
    setItemLimits({
      down: detail.downloadLimitKBps ? String(detail.downloadLimitKBps) : "",
      up: detail.uploadLimitKBps ? String(detail.uploadLimitKBps) : "",
      ratio: detail.seedRatioLimit ? String(detail.seedRatioLimit) : "",
      time: detail.seedTimeLimitSec ? String(detail.seedTimeLimitSec) : ""
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [detail?.id]);

  const load = useCallback(async () => {
    try {
      const next = await request<TorrentOverview>("/api/admin/torrents");
      setOverview(next);
      setFailed("");
      if (!limitsTouched.current) {
        setDownLimit(next.limits.downloadLimitKBps ? String(next.limits.downloadLimitKBps) : "");
        setUpLimit(next.limits.uploadLimitKBps ? String(next.limits.uploadLimitKBps) : "");
      }
    } catch (err) {
      setFailed(errorText(err, "Torrent listesi alınamadı"));
    }
  }, [request]);

  const active = (overview?.torrents ?? []).some((item) =>
    item.status === "downloading" || item.status === "metadata" || item.status === "stalled" ||
    item.status === "seeding" || item.status === "verifying");

  useEffect(() => { void load(); }, [load]);
  useEffect(() => {
    // Poll quickly while something is moving, slowly otherwise, and not at all
    // while the tab is in the background.
    const timer = window.setInterval(() => {
      if (!document.hidden) void load();
    }, active ? 2500 : 15000);
    return () => window.clearInterval(timer);
  }, [load, active]);
  useEffect(() => {
    if (!detailId) return;
    void loadDetail(detailId);
    const timer = window.setInterval(() => {
      if (!document.hidden) void loadDetail(detailId);
    }, active ? 2500 : 15000);
    return () => window.clearInterval(timer);
  }, [detailId, active, loadDetail]);

  function openDetail(id: string) {
    setDetailId(id);
    setDetail(null);
    setDetailFailed("");
  }

  function closeDetail() {
    setDetailId("");
    setDetail(null);
    setDetailFailed("");
  }

  async function addMagnet(event: FormEvent) {
    event.preventDefault();
    const value = magnet.trim();
    if (!value) return;
    if (!value.toLowerCase().startsWith("magnet:?")) {
      onMessage("Magnet bağlantısı “magnet:?” ile başlamalı.");
      return;
    }
    setAdding(true);
    try {
      await request("/api/admin/torrents", { method: "POST", body: JSON.stringify({ magnet: value }) });
      setMagnet("");
      await load();
      onMessage("Torrent eklendi.");
    } catch (err) {
      onMessage(errorText(err, "Torrent eklenemedi"));
    } finally {
      setAdding(false);
    }
  }

  async function addFile(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    event.target.value = "";
    if (!file) return;
    setAdding(true);
    try {
      await rawSend("/api/admin/torrents", file, "application/x-bittorrent");
      await load();
      onMessage(`"${file.name}" eklendi.`);
    } catch (err) {
      onMessage(errorText(err, "Torrent dosyası eklenemedi"));
    } finally {
      setAdding(false);
    }
  }

  async function saveLimits(event: FormEvent) {
    event.preventDefault();
    const parse = (value: string) => (value.trim() === "" ? 0 : Number(value));
    const down = parse(downLimit);
    const up = parse(upLimit);
    if (![down, up].every((value) => Number.isInteger(value) && value >= 0)) {
      onMessage("Hız sınırları sıfır veya pozitif tam sayı olmalı (KB/sn, boş = sınırsız).");
      return;
    }
    setBusy("limits");
    try {
      await request("/api/admin/torrents/limits", { method: "PUT", body: JSON.stringify({ downloadLimitKBps: down, uploadLimitKBps: up }) });
      limitsTouched.current = false;
      await load();
      onMessage("Hız sınırları güncellendi.");
    } catch (err) {
      onMessage(errorText(err, "Hız sınırları kaydedilemedi"));
    } finally {
      setBusy("");
    }
  }

  async function itemAction(item: TorrentView, action: "pause" | "resume" | "stopseeding") {
    setBusy(item.id);
    try {
      await request(`/api/admin/torrents/${encodeURIComponent(item.id)}/${action}`, { method: "POST" });
      await load();
      if (detailId === item.id) await loadDetail(item.id);
    } catch (err) {
      onMessage(errorText(err, "İşlem tamamlanamadı"));
    } finally {
      setBusy("");
    }
  }

  async function saveItemLimits(event: FormEvent) {
    event.preventDefault();
    if (!detail) return;
    const parse = (value: string) => (value.trim() === "" ? 0 : Number(value));
    const down = parse(itemLimits.down);
    const up = parse(itemLimits.up);
    const ratio = parse(itemLimits.ratio);
    const time = parse(itemLimits.time);
    const valid = Number.isInteger(down) && down >= 0 && Number.isInteger(up) && up >= 0 &&
      Number.isFinite(ratio) && ratio >= 0 && Number.isInteger(time) && time >= 0;
    if (!valid) {
      onMessage("Sınırlar geçersiz: hız sıfır veya pozitif tam sayı (KB/sn), oran ve süre sıfır ya da pozitif olmalı (boş/kapalı = 0).");
      return;
    }
    setDetailBusy(true);
    try {
      await request(`/api/admin/torrents/${encodeURIComponent(detail.id)}/limits`, {
        method: "PUT",
        body: JSON.stringify({
          downloadLimitKBps: down,
          uploadLimitKBps: up,
          seedRatioLimit: ratio,
          seedTimeLimitSec: time
        })
      });
      await loadDetail(detail.id);
      await load();
      onMessage("Torrent sınırları güncellendi.");
    } catch (err) {
      onMessage(errorText(err, "Torrent sınırları kaydedilemedi"));
    } finally {
      setDetailBusy(false);
    }
  }

  async function changeFilePriority(path: string, priority: TorrentFile["priority"]) {
    if (!detail) return;
    setDetailBusy(true);
    try {
      await request(`/api/admin/torrents/${encodeURIComponent(detail.id)}/files`, {
        method: "PUT",
        body: JSON.stringify({ path, priority })
      });
      await loadDetail(detail.id);
    } catch (err) {
      onMessage(errorText(err, "Dosya önceliği değiştirilemedi"));
    } finally {
      setDetailBusy(false);
    }
  }

  async function remove(item: TorrentView) {
    if (!window.confirm(`"${item.name}" listeden kaldırılsın mı?`)) return;
    const deleteData = !!item.path && window.confirm(
      `İndirilen dosyalar da silinsin mi?\n\nTamam: "${item.path}" Ortak Paylaşım'dan kalıcı olarak silinir.\nİptal: dosyalar yerinde kalır, yalnızca torrent listeden çıkar.`
    );
    setBusy(item.id);
    try {
      await request(`/api/admin/torrents/${encodeURIComponent(item.id)}${deleteData ? "?deleteData=1" : ""}`, { method: "DELETE" });
      await load();
      onMessage(deleteData ? "Torrent ve dosyaları silindi." : "Torrent listeden kaldırıldı.");
    } catch (err) {
      onMessage(errorText(err, "Torrent kaldırılamadı"));
    } finally {
      setBusy("");
    }
  }

  const items = overview?.torrents ?? [];
  const usable = !!overview?.available;
  const full = overview?.quota?.level === "full";

  return (
    <section className="ss-admin">
      <div className="ss-admin-head">
        <div>
          <h2>Torrent indirme</h2>
          <p>
            Sunucu, eklediğiniz torrentleri doğrudan Ortak Paylaşım klasörüne indirir. Yalnızca yöneticiler kullanabilir.
            {overview?.available ? ` Dinlenen port: ${overview.listenPort} (TCP/UDP).` : ""}
            {overview?.available ? (overview.seedAfterComplete ? " İndirme bitince paylaşım sürer." : " İndirme bitince paylaşım durur.") : ""}
          </p>
        </div>
      </div>

      {failed && <div className="notice error">{failed}</div>}
      {overview && !usable && (
        <div className="notice error">
          {overview.enabled
            ? `Torrent istemcisi başlatılamadı${overview.reason ? `: ${overview.reason}` : ""}.`
            : "Torrent istemcisi kapalı (TORRENT_ENABLED=false)."}
        </div>
      )}
      {usable && full && <div className="notice error">Ortak Paylaşım alanı dolu. Yeni torrent eklenemez; süren indirmeler alan açılana kadar bekler.</div>}

      {usable && (
        <>
          <form className="admin-settings-form ss-admin-block" onSubmit={(event) => void addMagnet(event)}>
            <label className="ss-wide">
              Magnet bağlantısı
              <input value={magnet} onChange={(event) => setMagnet(event.target.value)} placeholder="magnet:?xt=urn:btih:…" autoComplete="off" spellCheck={false} />
            </label>
            <button type="submit" disabled={adding || full || !magnet.trim()}>{adding ? "Ekleniyor…" : "Magnet ekle"}</button>
            <button type="button" className="secondary" disabled={adding || full} onClick={() => fileInput.current?.click()}>.torrent dosyası seç</button>
            <input ref={fileInput} type="file" accept=".torrent,application/x-bittorrent" hidden onChange={(event) => void addFile(event)} />
          </form>

          <form className="admin-settings-form ss-admin-block" onSubmit={(event) => void saveLimits(event)}>
            <label>
              İndirme sınırı (KB/sn)
              <input type="number" min={0} inputMode="numeric" value={downLimit} placeholder="Sınırsız" onChange={(event) => { limitsTouched.current = true; setDownLimit(event.target.value); }} />
            </label>
            <label>
              Gönderme sınırı (KB/sn)
              <input type="number" min={0} inputMode="numeric" value={upLimit} placeholder="Sınırsız" onChange={(event) => { limitsTouched.current = true; setUpLimit(event.target.value); }} />
            </label>
            <button type="submit" disabled={busy === "limits"}>{busy === "limits" ? "Kaydediliyor…" : "Sınırları uygula"}</button>
            <p className="ss-hint">Sınırlar tüm torrentlerin toplamına uygulanır ve hemen geçerli olur. Boş veya 0 = sınırsız.</p>
          </form>
        </>
      )}

      <div className="ss-admin-block">
        {items.length === 0 ? (
          <div className="ss-empty small">{usable ? "Listede torrent yok. Yukarıdan bir magnet bağlantısı veya .torrent dosyası ekleyin." : "Listede torrent yok."}</div>
        ) : (
          <>
            <div className="tt-filters" role="tablist" aria-label="Torrent süzgeçleri">
              {torrentFilters.map((entry) => (
                <button
                  key={entry.id}
                  type="button"
                  role="tab"
                  aria-selected={filter === entry.id}
                  className={filter === entry.id ? "active" : ""}
                  onClick={() => setFilter(entry.id)}
                >
                  {entry.label} ({items.filter((item) => matchesFilter(item, entry.id)).length})
                </button>
              ))}
            </div>
            <div className="tt-table-wrap">
              <table className="tt-table">
                <thead>
                  <tr>
                    <th scope="col">Torrent</th>
                    <th scope="col" className="tt-num">Boyut</th>
                    <th scope="col" className="tt-progress-col">İlerleme</th>
                    <th scope="col">Durum</th>
                    <th scope="col" className="tt-num">↓</th>
                    <th scope="col" className="tt-num">↑</th>
                    <th scope="col" className="tt-num tt-col-extra">Eş / Tohum</th>
                    <th scope="col" className="tt-num tt-col-extra">Oran</th>
                    <th scope="col" className="tt-num tt-col-extra">ETA</th>
                    <th scope="col" className="tt-actions-col">İşlem</th>
                  </tr>
                </thead>
                <tbody>
                  {items.filter((item) => matchesFilter(item, filter)).map((item) => {
                    const percent = Math.round(Math.max(0, Math.min(1, item.progress)) * 1000) / 10;
                    const itemBusy = busy === item.id;
                    const running = item.status === "downloading" || item.status === "metadata" || item.status === "stalled" || item.status === "verifying";
                    return (
                      <tr key={item.id} className={`tt-row ${item.status}`}>
                        <td data-label="Torrent" className="tt-name">
                          <button type="button" className="tt-name-btn" title={`${item.name} — ayrıntı`} onClick={() => openDetail(item.id)}>{item.name}</button>
                          {item.message && <small className="tt-message">{item.message}</small>}
                        </td>
                        <td data-label="Boyut" className="tt-num">
                          {item.totalBytes > 0 ? formatBytes(item.totalBytes) : "—"}
                          <small className="tt-sub">{formatBytes(item.completedBytes)} indirildi</small>
                        </td>
                        <td data-label="İlerleme" className="tt-progress-col">
                          <div className="ss-progress" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={percent} aria-label={`${item.name} ilerleme`}>
                            <span style={{ width: `${percent}%` }} />
                          </div>
                          <small className="tt-sub">%{percent}</small>
                        </td>
                        <td data-label="Durum"><span className={`ss-badge ${item.status}`}>{torrentStatus[item.status] ?? item.status}</span></td>
                        <td data-label="↓" className="tt-num">{formatRate(item.downloadRate)}</td>
                        <td data-label="↑" className="tt-num">{formatRate(item.uploadRate)}</td>
                        <td data-label="Eş / Tohum" className="tt-num tt-col-extra">{item.peers} / {item.seeders}</td>
                        <td data-label="Oran" className="tt-num tt-col-extra">{item.ratio.toFixed(2)}</td>
                        <td data-label="ETA" className="tt-num tt-col-extra">{item.status === "downloading" ? formatETA(item.etaSec) : "—"}</td>
                        <td data-label="İşlem" className="tt-actions">
                          {running && <button type="button" disabled={itemBusy || !usable} onClick={() => void itemAction(item, "pause")}>Duraklat</button>}
                          {(item.status === "paused" || item.status === "error") && (
                            <button type="button" disabled={itemBusy || !usable} onClick={() => void itemAction(item, "resume")}>
                              {item.status === "error" ? "Yeniden dene" : "Sürdür"}
                            </button>
                          )}
                          {item.status === "seeding" && <button type="button" disabled={itemBusy || !usable} onClick={() => void itemAction(item, "stopseeding")}>Seed'i durdur</button>}
                          {item.status === "completed" && <button type="button" disabled={itemBusy || !usable} onClick={() => void itemAction(item, "resume")}>Seed başlat</button>}
                          <button type="button" className="secondary" onClick={() => openDetail(item.id)}>Detay</button>
                          <button type="button" className="danger" disabled={itemBusy} onClick={() => void remove(item)}>Kaldır</button>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </>
        )}
      </div>

      {detailId && (
        <div className="tt-drawer-backdrop" role="presentation" onClick={closeDetail}>
          <aside
            className="tt-drawer"
            role="dialog"
            aria-modal="true"
            aria-label={detail ? `${detail.name} ayrıntısı` : "Torrent ayrıntısı"}
            onClick={(event) => event.stopPropagation()}
          >
            <div className="tt-drawer-head">
              <strong>{detail?.name ?? "Yükleniyor…"}</strong>
              <button type="button" aria-label="Ayrıntıyı kapat" onClick={closeDetail}>×</button>
            </div>
            {detailFailed && <div className="notice error">{detailFailed}</div>}
            {detail && (
              <div className="tt-drawer-body">
                <div className="tt-drawer-status">
                  <span className={`ss-badge ${detail.status}`}>{torrentStatus[detail.status] ?? detail.status}</span>
                  <span>%{Math.round(Math.max(0, Math.min(1, detail.progress)) * 1000) / 10}</span>
                  <span className="tt-sub">↓ {formatRate(detail.downloadRate)} · ↑ {formatRate(detail.uploadRate)}</span>
                  <span className="tt-sub">Oran {detail.ratio.toFixed(2)}</span>
                  <span className="tt-sub">ETA {detail.status === "downloading" ? formatETA(detail.etaSec) : "—"}</span>
                </div>
                <div className="ss-progress" role="progressbar" aria-valuemin={0} aria-valuemax={100}
                  aria-valuenow={Math.round(detail.progress * 100)} aria-label="İlerleme">
                  <span style={{ width: `${Math.round(Math.max(0, Math.min(1, detail.progress)) * 100)}%` }} />
                </div>
                <dl className="tt-meta">
                  <div><dt>Yol</dt><dd>{detail.path || "—"}</dd></div>
                  <div><dt>İnfo hash</dt><dd className="tt-hash">{detail.infoHash}</dd></div>
                  <div><dt>Toplam / indirilen</dt><dd>{formatBytes(detail.totalBytes)} / {formatBytes(detail.completedBytes)}</dd></div>
                  <div><dt>Yüklenen / süre</dt><dd>{formatBytes(detail.uploadedBytes)} · {formatSeedTime(detail.seededSeconds)}</dd></div>
                  <div><dt>Eklenme</dt><dd>{formatDate(detail.addedAt)}</dd></div>
                </dl>

                <form className="tt-limits" onSubmit={(event) => void saveItemLimits(event)}>
                  <h3>Sınırlar</h3>
                  <div className="tt-limit-grid">
                    <label>
                      İndirme (KB/sn)
                      <input type="number" min={0} inputMode="numeric" value={itemLimits.down} placeholder="Sınırsız"
                        onChange={(event) => setItemLimits((current) => ({ ...current, down: event.target.value }))} />
                    </label>
                    <label>
                      Gönderme (KB/sn)
                      <input type="number" min={0} inputMode="numeric" value={itemLimits.up} placeholder="Sınırsız"
                        onChange={(event) => setItemLimits((current) => ({ ...current, up: event.target.value }))} />
                    </label>
                    <label>
                      Seed oranı sınırı
                      <input type="number" min={0} step="0.1" inputMode="decimal" value={itemLimits.ratio} placeholder="Kapalı (0)"
                        onChange={(event) => setItemLimits((current) => ({ ...current, ratio: event.target.value }))} />
                    </label>
                    <label>
                      Seed süresi (sn)
                      <input type="number" min={0} inputMode="numeric" value={itemLimits.time} placeholder="Kapalı (0)"
                        onChange={(event) => setItemLimits((current) => ({ ...current, time: event.target.value }))} />
                    </label>
                  </div>
                  <p className="ss-hint">Hız sınırları bu torrente özeldir; seed oranı/sınırı 0 ise kapalıdır. Yeni değerler hemen uygulanır.</p>
                  <button type="submit" disabled={detailBusy}>{detailBusy ? "Kaydediliyor…" : "Sınırları uygula"}</button>
                </form>

                <section className="tt-drawer-section">
                  <h3>Dosyalar ({detail.files.length})</h3>
                  {detail.files.length === 0 ? (
                    <p className="ss-hint">Dosya listesi henüz bilinmiyor (magnet ile eklenen torrentte metadata bekleniyor).</p>
                  ) : (
                    <ul className="tt-files">
                      {detail.files.map((file) => (
                        <li key={file.path}>
                          <span className="tt-file-path" title={file.path}>{file.path}</span>
                          <span className="tt-sub">{formatBytes(file.completed)} / {formatBytes(file.length)}</span>
                          <select
                            aria-label={`${file.path} önceliği`}
                            value={file.priority}
                            disabled={detailBusy}
                            onChange={(event) => void changeFilePriority(file.path, event.target.value as TorrentFile["priority"])}
                          >
                            <option value="normal">Normal</option>
                            <option value="high">Yüksek</option>
                            <option value="skip">Atla</option>
                          </select>
                        </li>
                      ))}
                    </ul>
                  )}
                </section>

                <section className="tt-drawer-section">
                  <h3>Trackers ({detail.trackers.length})</h3>
                  {detail.trackers.length === 0 ? (
                    <p className="ss-hint">Trackers bilgisi yok.</p>
                  ) : (
                    <ul className="tt-trackers">
                      {detail.trackers.map((tracker) => <li key={tracker.url}>{tracker.url}</li>)}
                    </ul>
                  )}
                  <p className="ss-hint">Yol ve anahtar (passkey) gizlenmiştir.</p>
                </section>

                <section className="tt-drawer-section">
                  <h3>Eşler ({detail.peers.length})</h3>
                  {detail.peers.length === 0 ? (
                    <p className="ss-hint">Şu an bağlı eş yok.</p>
                  ) : (
                    <ul className="tt-peers">
                      {detail.peers.map((peer) => (
                        <li key={peer.addr}><span>{peer.addr}</span>{peer.client ? <span className="tt-sub">{peer.client}</span> : null}</li>
                      ))}
                    </ul>
                  )}
                </section>

                <div className="tt-drawer-actions">
                  {running(detail) && <button type="button" disabled={detailBusy} onClick={() => void itemAction(detail, "pause")}>Duraklat</button>}
                  {(detail.status === "paused" || detail.status === "error") && (
                    <button type="button" disabled={detailBusy} onClick={() => void itemAction(detail, "resume")}>
                      {detail.status === "error" ? "Yeniden dene" : "Sürdür"}
                    </button>
                  )}
                  {detail.status === "seeding" && <button type="button" disabled={detailBusy} onClick={() => void itemAction(detail, "stopseeding")}>Seed'i durdur</button>}
                  {detail.status === "completed" && <button type="button" disabled={detailBusy} onClick={() => void itemAction(detail, "resume")}>Seed başlat</button>}
                  <button type="button" className="danger" disabled={detailBusy} onClick={() => void remove(detail)}>Kaldır</button>
                </div>
              </div>
            )}
          </aside>
        </div>
      )}
    </section>
  );
}

function running(item: TorrentView) {
  return item.status === "downloading" || item.status === "metadata" || item.status === "stalled" || item.status === "verifying";
}
