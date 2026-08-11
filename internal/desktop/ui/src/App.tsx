import { FormEvent, useEffect, useMemo, useState } from "react";

type Status = {
  State?: string;
  LastError?: string;
  CurrentFile?: string;
  BytesSynced?: number;
  BytesTotal?: number;
  BytesDone?: number;
  Percent?: number;
  PendingJobs?: number;
  Message?: string;
};

type Folder = {
  ID: string;
  LocalPath: string;
  RemoteParentID: string;
  Cursor: number;
  Paused: boolean;
};

type Activity = {
  ID: number;
  RootID: string;
  Kind: string;
  Path: string;
  Message: string;
  CreatedAt: number;
};

type Snapshot = {
  connected: boolean;
  serverUrl: string;
  email: string;
  deviceName: string;
  autostart: boolean;
  dataDir: string;
  status: Status;
  folders: Folder[];
  activities: Activity[];
  pendingJobs: number;
};

const stateLabel: Record<string, string> = {
  idle: "Hazır",
  connecting: "Bağlanıyor",
  synced: "Senkron",
  syncing: "Senkronize ediliyor",
  paused: "Duraklatıldı",
  offline: "Çevrimdışı",
  error: "Hata",
  stopped: "Durdu"
};

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: { "Content-Type": "application/json", ...(init?.headers || {}) }
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(text || res.statusText);
  }
  if (res.status === 204) return undefined as T;
  return res.json();
}

function useHash(): string {
  const [hash, setHash] = useState(window.location.hash || "#/flyout");
  useEffect(() => {
    const onHash = () => setHash(window.location.hash || "#/flyout");
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);
  return hash;
}

export function App() {
  const hash = useHash();
  const isSettings = hash.startsWith("#/settings");
  const [snap, setSnap] = useState<Snapshot | null>(null);
  const [error, setError] = useState("");
  const [login, setLogin] = useState({ serverUrl: "https://drive.neciparmagan.net.tr", email: "", password: "" });
  const [busy, setBusy] = useState(false);

  function applySnap(next: Snapshot) {
    setSnap(next);
    setLogin((prev) => ({
      ...prev,
      serverUrl: next.serverUrl || prev.serverUrl,
      email: next.email || prev.email
    }));
    setError("");
  }

  async function refresh() {
    try {
      applySnap(await api<Snapshot>("/api/state"));
    } catch (e) {
      setError(e instanceof Error ? e.message : "Durum alınamadı");
    }
  }

  useEffect(() => {
    void refresh();
    const es = new EventSource("/api/events");
    es.addEventListener("state", (ev) => {
      try {
        applySnap(JSON.parse((ev as MessageEvent).data));
      } catch {
        /* ignore malformed */
      }
    });
    es.onerror = () => {
      /* browser reconnects; keep a slow poll fallback */
    };
    const poll = window.setInterval(() => void refresh(), 5000);
    return () => {
      es.close();
      window.clearInterval(poll);
    };
  }, []);

  const statusText = useMemo(() => {
    const state = snap?.status?.State || "idle";
    return stateLabel[state] || state;
  }, [snap]);

  async function pickFolder() {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      (window as unknown as { __trPickerOpen?: boolean }).__trPickerOpen = true;
      const res = await api<{ status?: string; path?: string }>("/api/pick-folder", { method: "POST" });
      if (res.status === "cancelled" || res.status === "busy") {
        return;
      }
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Klasör seçilemedi");
    } finally {
      (window as unknown as { __trPickerOpen?: boolean }).__trPickerOpen = false;
      setBusy(false);
    }
  }

  async function onLogin(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api("/api/login", {
        method: "POST",
        body: JSON.stringify(login)
      });
      setLogin((p) => ({ ...p, password: "" }));
      await refresh();
      window.location.hash = "#/settings";
    } catch (err) {
      setError(err instanceof Error ? err.message : "Giriş başarısız");
    } finally {
      setBusy(false);
    }
  }

  if (!snap) {
    return <div className="shell loading">TR Driver hazırlanıyor…</div>;
  }

  if (isSettings) {
    return (
      <div className="shell settings">
        <header>
          <div>
            <h1>TR Driver Sync</h1>
            <p>Google Drive benzeri arka plan senkronizasyonu</p>
          </div>
          <button className="ghost" onClick={() => { window.location.hash = "#/flyout"; }}>Duruma dön</button>
        </header>

        {!snap.connected ? (
          <form className="card" onSubmit={onLogin}>
            <h2>Hesaba bağlan</h2>
            <label>Sunucu URL<input value={login.serverUrl} onChange={(e) => setLogin({ ...login, serverUrl: e.target.value })} required /></label>
            <label>E-posta<input type="email" value={login.email} onChange={(e) => setLogin({ ...login, email: e.target.value })} required /></label>
            <label>Şifre<input type="password" value={login.password} onChange={(e) => setLogin({ ...login, password: e.target.value })} required /></label>
            <button className="primary" disabled={busy}>{busy ? "Bağlanıyor…" : "Giriş yap"}</button>
          </form>
        ) : (
          <section className="card">
            <h2>Hesap</h2>
            <p className="muted">{snap.email}<br />{snap.serverUrl}</p>
            <div className="row">
              <button onClick={() => void api("/api/logout", { method: "POST" }).then(refresh)}>Çıkış yap</button>
              <button onClick={() => void api("/api/open-logs", { method: "POST" })}>Log klasörünü aç</button>
            </div>
          </section>
        )}

        <section className="card">
          <div className="row between">
            <h2>Senkron klasörleri</h2>
            <button className="primary" disabled={!snap.connected || busy} onClick={() => void pickFolder()}>
              {busy ? "Bekleyin…" : "Klasör seç"}
            </button>
          </div>
          {!snap.folders?.length && snap.connected && (
            <p className="muted">Henüz klasör yok. Bir kez “Klasör seç” ile eşitlemek istediğiniz klasörü seçin — tekrar tekrar açılmaz.</p>
          )}
          <ul className="folder-list">
            {(snap.folders || []).map((f) => (
              <li key={f.ID}>
                <div>
                  <strong>{f.LocalPath}</strong>
                  <small>{f.Paused ? "Duraklatıldı" : "Aktif"}</small>
                </div>
                <div className="row">
                  {f.Paused
                    ? <button type="button" onClick={() => void api("/api/resume-folder", { method: "POST", body: JSON.stringify({ id: f.ID }) }).then(refresh)}>Devam</button>
                    : <button type="button" onClick={() => void api("/api/pause-folder", { method: "POST", body: JSON.stringify({ id: f.ID }) }).then(refresh)}>Duraklat</button>}
                  <button type="button" className="danger" onClick={() => void api("/api/remove-folder", { method: "POST", body: JSON.stringify({ id: f.ID }) }).then(refresh)}>Kaldır</button>
                </div>
              </li>
            ))}
            {!snap.folders?.length && <li className="empty">Henüz klasör eklenmedi.</li>}
          </ul>
        </section>

        <section className="card">
          <label className="check">
            <input
              type="checkbox"
              checked={!!snap.autostart}
              onChange={(e) => void api("/api/autostart", { method: "POST", body: JSON.stringify({ enabled: e.target.checked }) }).then(refresh)}
            />
            Windows ile başlat
          </label>
          <p className="muted">Veri: {snap.dataDir}</p>
        </section>
        {error && <div className="error">{error}</div>}
      </div>
    );
  }

  const transferred = snap.status?.BytesDone || snap.status?.BytesSynced || 0;
  const total = snap.status?.BytesTotal || 0;

  return (
    <div className="shell flyout">
      <header className="flyout-head">
        <div className={`badge ${snap.status?.State || "idle"}`} />
        <div>
          <strong>TR Driver</strong>
          <small>{statusText}{snap.connected ? ` · ${snap.email}` : " · giriş yok"}</small>
        </div>
      </header>

      <section className="progress card">
        <div className="row between">
          <span>{snap.status?.CurrentFile ? snap.status.CurrentFile.split(/[/\\]/).pop() : "Beklemede"}</span>
          <span>{snap.status?.Percent || 0}%</span>
        </div>
        <div className="bar"><span style={{ width: `${snap.status?.Percent || 0}%` }} /></div>
        <small>
          {snap.pendingJobs || 0} bekleyen iş
          {total > 0 ? ` · ${formatBytes(transferred)} / ${formatBytes(total)}` : ` · ${formatBytes(transferred)} aktarıldı`}
        </small>
        {snap.status?.LastError && <div className="error tiny">{snap.status.LastError}</div>}
      </section>

      <section className="card actions">
        <button onClick={() => void api("/api/pause", { method: "POST" }).then(refresh)}>Duraklat</button>
        <button onClick={() => void api("/api/resume", { method: "POST" }).then(refresh)}>Devam</button>
        <button className="primary" onClick={() => { window.location.hash = "#/settings"; void api("/api/show-settings", { method: "POST" }); }}>Ayarlar</button>
      </section>

      <section className="card activity">
        <h3>Son aktiviteler</h3>
        <ul>
          {(snap.activities || []).slice(0, 8).map((a) => (
            <li key={a.ID}>
              <strong>{a.Kind}</strong>
              <span>{a.Message || a.Path}</span>
            </li>
          ))}
          {!snap.activities?.length && <li className="empty">Henüz aktivite yok</li>}
        </ul>
      </section>
      {error && <div className="error">{error}</div>}
    </div>
  );
}

function formatBytes(value: number) {
  if (!value) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  let size = value;
  let i = 0;
  while (size >= 1024 && i < units.length - 1) {
    size /= 1024;
    i++;
  }
  return `${size.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}
