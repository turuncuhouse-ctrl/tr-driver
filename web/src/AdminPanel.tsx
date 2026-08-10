import { useEffect, useMemo, useState } from "react";

type Plan = {
  code: string;
  name: string;
  quotaBytes: number;
  priceCents: number;
  billingTerm: string;
};

type AdminUser = {
  id: string;
  email: string;
  displayName: string;
  role: "user" | "admin";
  planCode: string;
  quotaBytes: number;
  usedBytes: number;
  createdAt: string;
  lastLoginAt: string;
};

type Summary = {
  userCount: number;
  adminCount: number;
  fileCount: number;
  shareCount: number;
  usedBytes: number;
  assignedBytes: number;
};

type Settings = {
  maxUploadBatchBytes: number;
  uploadChunkBytes: number;
};

type RequestFn = <T>(path: string, init?: RequestInit) => Promise<T>;

type Props = {
  currentUserId: string;
  plans: Plan[];
  request: RequestFn;
  onMessage: (message: string) => void;
  onCurrentUserChanged: () => Promise<void>;
};

const formatBytes = (value: number) => {
  if (!value) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let size = value;
  let index = 0;
  while (size >= 1024 && index < units.length - 1) {
    size /= 1024;
    index++;
  }
  return `${size.toFixed(index === 0 ? 0 : 1)} ${units[index]}`;
};

export function AdminPanel({ currentUserId, plans, request, onMessage, onCurrentUserChanged }: Props) {
  const [loading, setLoading] = useState(true);
  const [users, setUsers] = useState<AdminUser[]>([]);
  const [summary, setSummary] = useState<Summary | null>(null);
  const [settings, setSettings] = useState<Settings | null>(null);
  const [batchLimitGB, setBatchLimitGB] = useState("10");
  const [search, setSearch] = useState("");
  const [busyUser, setBusyUser] = useState<string | null>(null);
  const [savingSettings, setSavingSettings] = useState(false);
  const [licenseKey, setLicenseKey] = useState("");
  const [license, setLicense] = useState<{
    tier: string;
    maxUsers: number;
    userCount: number;
    seatsRemaining: number;
    customer?: string;
    usingDefaultKey?: boolean;
    catalog?: { code: string; name: string; maxUsers: number; priceTlYear: number; description: string }[];
  } | null>(null);
  const [activating, setActivating] = useState(false);

  const visibleUsers = useMemo(() => {
    const needle = search.trim().toLocaleLowerCase("tr-TR");
    if (!needle) return users;
    return users.filter((user) =>
      `${user.displayName} ${user.email} ${user.role} ${user.planCode}`
        .toLocaleLowerCase("tr-TR")
        .includes(needle)
    );
  }, [search, users]);

  async function load() {
    setLoading(true);
    try {
      const [nextSummary, nextUsers, nextSettings, nextLicense] = await Promise.all([
        request<Summary>("/api/admin/summary"),
        request<AdminUser[]>("/api/admin/users"),
        request<Settings>("/api/admin/settings"),
        request<NonNullable<typeof license>>("/api/admin/license")
      ]);
      setSummary(nextSummary);
      setUsers(nextUsers);
      setSettings(nextSettings);
      setLicense(nextLicense);
      setBatchLimitGB((nextSettings.maxUploadBatchBytes / 1024 ** 3).toFixed(1));
    } catch (error) {
      onMessage(error instanceof Error ? error.message : "Admin bilgileri yüklenemedi");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  async function mutate(userId: string, path: string, body: object, success: string) {
    setBusyUser(userId);
    try {
      await request(path, { method: "POST", body: JSON.stringify(body) });
      await load();
      if (userId === currentUserId) await onCurrentUserChanged();
      onMessage(success);
    } catch (error) {
      onMessage(error instanceof Error ? error.message : "Güncelleme başarısız");
    } finally {
      setBusyUser(null);
    }
  }

  function setQuota(user: AdminUser) {
    const currentGB = (user.quotaBytes / 1024 ** 3).toFixed(1);
    const raw = window.prompt(`${user.displayName} için kota (GB):`, currentGB);
    if (!raw) return;
    const gigabytes = Number(raw.replace(",", "."));
    if (!Number.isFinite(gigabytes) || gigabytes < 0) {
      onMessage("Kota sıfır veya pozitif bir sayı olmalı.");
      return;
    }
    void mutate(
      user.id,
      "/api/admin/users/quota",
      { userId: user.id, quotaBytes: Math.round(gigabytes * 1024 ** 3) },
      "Kullanıcı kotası güncellendi."
    );
  }

  async function activateLicense() {
    const key = licenseKey.trim();
    if (!key) {
      onMessage("Lisans anahtarı girin.");
      return;
    }
    setActivating(true);
    try {
      const next = await request<NonNullable<typeof license>>("/api/admin/license", {
        method: "POST",
        body: JSON.stringify({ key })
      });
      setLicense(next);
      setLicenseKey("");
      onMessage("Lisans etkinleştirildi.");
    } catch (error) {
      onMessage(error instanceof Error ? error.message : "Lisans etkinleştirilemedi");
    } finally {
      setActivating(false);
    }
  }

  async function saveBatchLimit() {
    const gigabytes = Number(batchLimitGB.replace(",", "."));
    if (!Number.isFinite(gigabytes) || gigabytes <= 0) {
      onMessage("Geçerli bir GB değeri gir.");
      return;
    }
    setSavingSettings(true);
    try {
      await request("/api/admin/settings", {
        method: "POST",
        body: JSON.stringify({ maxUploadBatchBytes: Math.round(gigabytes * 1024 ** 3) })
      });
      await load();
      await onCurrentUserChanged();
      onMessage("Yükleme grubu limiti güncellendi.");
    } catch (error) {
      onMessage(error instanceof Error ? error.message : "Ayar kaydedilemedi");
    } finally {
      setSavingSettings(false);
    }
  }

  if (loading && !summary) {
    return <section className="admin-loading"><div className="spinner" /><p>Yönetim paneli hazırlanıyor…</p></section>;
  }

  return (
    <section className="admin-panel">
      <header className="admin-header">
        <div>
          <h1>Yönetim paneli</h1>
          <p>TR Driver kullanıcıları, lisans ve depolama ayarları.</p>
        </div>
        <button onClick={() => void load()} disabled={loading}>↻ Yenile</button>
      </header>

      <section className="admin-settings">
        <div>
          <h2>Lisans</h2>
          <p>
            Aktif: <strong>{license?.tier ?? "unlicensed"}</strong>
            {" · "}
            Kullanıcı {license?.userCount ?? 0}
            /
            {license?.maxUsers === 0 ? "∞" : (license?.maxUsers ?? 1)}
            {license?.customer ? ` · ${license.customer}` : ""}
          </p>
          {license?.usingDefaultKey && (
            <p className="notice error">Varsayılan geliştirme imza anahtarı kullanılıyor — satış öncesi kendi Ed25519 anahtarlarınızı tanımlayın.</p>
          )}
          {!!license?.catalog?.length && (
            <ul>
              {license.catalog.map((t) => (
                <li key={t.code}>{t.name}: {t.priceTlYear} TL / yıl ({t.maxUsers === 0 ? "sınırsız" : `${t.maxUsers} kullanıcı`})</li>
              ))}
            </ul>
          )}
        </div>
        <div className="admin-settings-form">
          <label>
            Lisans anahtarı
            <input value={licenseKey} onChange={(e) => setLicenseKey(e.target.value)} placeholder="TRD1...." />
          </label>
          <button disabled={activating} onClick={() => void activateLicense()}>
            {activating ? "Etkinleştiriliyor…" : "Lisansı etkinleştir"}
          </button>
        </div>
      </section>

      <div className="stats-grid">
        <article><span>Toplam kullanıcı</span><strong>{summary?.userCount ?? 0}</strong><small>{summary?.adminCount ?? 0} yönetici</small></article>
        <article><span>Toplam kullanım</span><strong>{formatBytes(summary?.usedBytes ?? 0)}</strong><small>{formatBytes(summary?.assignedBytes ?? 0)} atanmış</small></article>
        <article><span>Aktif dosya</span><strong>{summary?.fileCount ?? 0}</strong><small>Silinmemiş dosyalar</small></article>
        <article><span>Paylaşım linki</span><strong>{summary?.shareCount ?? 0}</strong><small>Oluşturulan bağlantılar</small></article>
      </div>

      <section className="admin-settings">
        <div>
          <h2>Yükleme limiti</h2>
          <p>Tek seferde seçilen dosya/klasör grubunun toplam boyutu. Parça boyutu: {formatBytes(settings?.uploadChunkBytes || 8 * 1024 * 1024)}</p>
        </div>
        <div className="admin-settings-form">
          <label>
            Maksimum yükleme grubu (GB)
            <input value={batchLimitGB} onChange={(event) => setBatchLimitGB(event.target.value)} />
          </label>
          <button disabled={savingSettings} onClick={() => void saveBatchLimit()}>
            {savingSettings ? "Kaydediliyor…" : "Kaydet"}
          </button>
        </div>
      </section>

      <section className="admin-users">
        <div className="admin-users-head">
          <div><h2>Kullanıcılar</h2><p>{visibleUsers.length} kayıt gösteriliyor</p></div>
          <input value={search} onChange={(event) => setSearch(event.target.value)} placeholder="Ad veya e-posta ara…" />
        </div>

        <div className="admin-table">
          <div className="admin-table-head">
            <span>Kullanıcı</span><span>Kullanım</span><span>Paket</span><span>Rol</span><span>İşlemler</span>
          </div>
          {visibleUsers.map((account) => {
            const percent = account.quotaBytes
              ? Math.min(100, Math.round((account.usedBytes / account.quotaBytes) * 100))
              : 0;
            const disabled = busyUser === account.id;
            return (
              <article className="admin-user-row" key={account.id}>
                <div className="admin-identity">
                  <span>{account.displayName.slice(0, 1).toUpperCase()}</span>
                  <div><strong>{account.displayName}</strong><small>{account.email}</small></div>
                </div>
                <div className="admin-usage">
                  <strong>{formatBytes(account.usedBytes)} / {formatBytes(account.quotaBytes)}</strong>
                  <div><span style={{ width: `${percent}%` }} /></div>
                </div>
                <select
                  value={account.planCode}
                  disabled={disabled}
                  onChange={(event) => void mutate(
                    account.id,
                    "/api/admin/users/plan",
                    { userId: account.id, planCode: event.target.value },
                    "Kullanıcı paketi güncellendi."
                  )}
                >
                  {plans.map((plan) => <option value={plan.code} key={plan.code}>{plan.name}</option>)}
                </select>
                <span className={`role-badge ${account.role}`}>{account.role === "admin" ? "Yönetici" : "Kullanıcı"}</span>
                <div className="admin-actions">
                  <button disabled={disabled} onClick={() => setQuota(account)}>Kota</button>
                  <button
                    disabled={disabled}
                    onClick={() => void mutate(
                      account.id,
                      "/api/admin/users/role",
                      { userId: account.id, role: account.role === "admin" ? "user" : "admin" },
                      account.role === "admin" ? "Yönetici yetkisi kaldırıldı." : "Yönetici yetkisi verildi."
                    )}
                  >
                    {account.role === "admin" ? "Yetkiyi kaldır" : "Admin yap"}
                  </button>
                </div>
              </article>
            );
          })}
          {!visibleUsers.length && <div className="admin-empty">Aramana uygun kullanıcı bulunamadı.</div>}
        </div>
      </section>
    </section>
  );
}
