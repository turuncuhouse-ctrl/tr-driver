import { ChangeEvent, DragEvent, FormEvent, useEffect, useMemo, useRef, useState } from "react";
import { AdminPanel } from "./AdminPanel";
import {
  ActivityPanel,
  CollabSidebar,
  CommentsVersionsPanel,
  DrivesPanel,
  loadDrives,
  NotificationsPanel,
  SettingsPanel,
  ShareModal,
  SharesPanel,
  TrashPanel,
  unreadCount
} from "./Collab";
import {
  UploadQueue,
  QueuedFile,
  QueueProgress,
  PersistedBatch,
  collectDroppedFiles,
  filesFromList,
  listPersistedBatches
} from "./uploadQueue";

type User = {
  id: string;
  email: string;
  displayName: string;
  role: string;
  planCode: string;
  quotaBytes: number;
  usedBytes: number;
  reservedBytes?: number;
  maxBatchBytes?: number;
  uploadChunkBytes?: number;
  email2FAEnabled?: boolean;
};

type FileEntry = {
  id: string;
  name: string;
  kind: "file" | "folder";
  sizeBytes: number;
  mimeType: string;
  parentId?: string | null;
  updatedAt: string;
  starred?: boolean;
};

type Plan = {
  code: string;
  name: string;
  quotaBytes: number;
  priceCents: number;
  billingTerm: string;
};

type Crumb = { id: string | null; name: string };
type LayoutMode = "list" | "grid";
type ThemeMode = "light" | "dark";
type PreviewKind = "image" | "video" | "audio" | "pdf";
type PreviewState = { entry: FileEntry; kind: PreviewKind };

const LAYOUT_KEY = "necipdrive.fileLayout";
const THEME_KEY = "trdriver.theme";
const INTERNAL_DRAG_TYPE = "application/x-necipdrive-entry";

const formatBytes = (value: number) => {
  if (!value) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let size = value;
  let index = 0;
  while (size >= 1024 && index < units.length - 1) {
    size /= 1024;
    index++;
  }
  return `${size.toFixed(index === 0 ? 0 : 1)} ${units[index]}`;
};

const csrfToken = () => {
  const cookie = document.cookie
    .split("; ")
    .find((item) => item.startsWith("csrf_token="));
  return cookie ? decodeURIComponent(cookie.slice("csrf_token=".length)) : "";
};

const extensionOf = (name: string) => {
  const idx = name.lastIndexOf(".");
  return idx >= 0 ? name.slice(idx).toLowerCase() : "";
};

const previewKindOf = (entry: FileEntry): PreviewKind | null => {
  if (entry.kind !== "file") return null;
  const mime = (entry.mimeType || "").toLowerCase();
  const ext = extensionOf(entry.name);
  if (mime.startsWith("image/") && mime !== "image/svg+xml") return "image";
  if (mime.startsWith("video/")) return "video";
  if (mime.startsWith("audio/")) return "audio";
  if (mime === "application/pdf") return "pdf";
  if ([".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif", ".bmp"].includes(ext)) return "image";
  if ([".mp4", ".webm", ".ogg", ".ogv"].includes(ext)) return "video";
  if ([".mp3", ".wav", ".m4a", ".aac", ".oga"].includes(ext)) return "audio";
  if (ext === ".pdf") return "pdf";
  return null;
};

const fileIconLabel = (entry: FileEntry) => {
  if (entry.kind === "folder") return "▰";
  const kind = previewKindOf(entry);
  if (kind === "image") return "▣";
  if (kind === "video") return "▶";
  if (kind === "audio") return "♪";
  if (kind === "pdf") return "▤";
  return "▤";
};

const readStoredLayout = (): LayoutMode => {
  try {
    return localStorage.getItem(LAYOUT_KEY) === "grid" ? "grid" : "list";
  } catch {
    return "list";
  }
};

const readStoredTheme = (): ThemeMode => {
  try {
    return localStorage.getItem(THEME_KEY) === "dark" ? "dark" : "light";
  } catch {
    return "light";
  }
};

const applyTheme = (theme: ThemeMode) => {
  document.documentElement.setAttribute("data-theme", theme);
};

export function App() {
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [user, setUser] = useState<User | null>(null);
  const [files, setFiles] = useState<FileEntry[]>([]);
  const [plans, setPlans] = useState<Plan[]>([]);
  const [view, setView] = useState<"files" | "admin" | string>("files");
  const [layout, setLayout] = useState<LayoutMode>(readStoredLayout);
  const [theme, setTheme] = useState<ThemeMode>(readStoredTheme);
  const [crumbs, setCrumbs] = useState<Crumb[]>([{ id: null, name: "Dosyalarım" }]);
  const [mode, setMode] = useState<"login" | "register" | "forgot" | "reset" | "login2fa">("login");
  const [message, setMessage] = useState("");
  const [challengeToken, setChallengeToken] = useState("");
  const [otpCode, setOtpCode] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [uploadProgress, setUploadProgress] = useState<QueueProgress | null>(null);
  const [dragActive, setDragActive] = useState(false);
  const [dropTargetId, setDropTargetId] = useState<string | null>(null);
  const [draggedEntryId, setDraggedEntryId] = useState<string | null>(null);
  const [preview, setPreview] = useState<PreviewState | null>(null);
  const [pendingResume, setPendingResume] = useState<PersistedBatch | null>(null);
  const [form, setForm] = useState({ email: "", password: "", displayName: "" });
  const [drives, setDrives] = useState<{ id: string; kind: string; name: string; rootEntryId: string; myRole?: string }[]>([]);
  const [unread, setUnread] = useState(0);
  const [searchQ, setSearchQ] = useState("");
  const [shareTarget, setShareTarget] = useState<FileEntry | null>(null);
  const [detailEntry, setDetailEntry] = useState<FileEntry | null>(null);
  const [qrLogin, setQrLogin] = useState<{ token: string; expiresAt: string; payload: string } | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const folderInput = useRef<HTMLInputElement>(null);
  const queueRef = useRef<UploadQueue | null>(null);
  const folderRef = useRef<string | null>(null);

  const currentFolder = crumbs[crumbs.length - 1]?.id ?? null;
  folderRef.current = currentFolder;
  const usageRate = useMemo(() => {
    if (!user?.quotaBytes) return 0;
    return Math.min(100, Math.round((user.usedBytes / user.quotaBytes) * 100));
  }, [user]);

  async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
    const response = await fetch(path, {
      ...init,
      credentials: "include",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrfToken(),
        ...(init.headers || {})
      }
    });
    if (!response.ok) {
      const text = await response.text();
      let message = `İşlem başarısız (HTTP ${response.status})`;
      try {
        const parsed = JSON.parse(text) as { error?: string };
        if (parsed?.error) message = parsed.error;
      } catch {
        if (text && text.length < 200) message = text;
      }
      throw new Error(message);
    }
    return response.json() as Promise<T>;
  }

  async function refreshUser() {
    const nextUser = await request<User>("/api/auth/me");
    setUser(nextUser);
  }

  async function loadFiles(parentId: string | null) {
    const query = parentId ? `?parentId=${encodeURIComponent(parentId)}` : "";
    setFiles(await request<FileEntry[]>(`/api/files${query}`));
  }

  async function bootstrap() {
    try {
      const nextUser = await request<User>("/api/auth/me");
      const [nextFiles, nextPlans] = await Promise.all([
        request<FileEntry[]>("/api/files"),
        request<Plan[]>("/api/plans")
      ]);
      setUser(nextUser);
      setFiles(nextFiles);
      setPlans(nextPlans);
      setDrives(await loadDrives(request).catch(() => []));
      setUnread(await unreadCount(request).catch(() => 0));
    } catch {
      setUser(null);
      setFiles([]);
      setPlans([]);
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void bootstrap();
    applyTheme(readStoredTheme());
  }, []);

  useEffect(() => {
    applyTheme(theme);
    try {
      localStorage.setItem(THEME_KEY, theme);
    } catch {
      /* ignore */
    }
  }, [theme]);

  useEffect(() => {
    queueRef.current = new UploadQueue({
      request,
      csrf: csrfToken,
      onProgress: setUploadProgress,
      onComplete: async () => {
        await Promise.all([loadFiles(folderRef.current), refreshUser()]);
      }
    });
  }, []);

  useEffect(() => {
    if (!user) return;
    queueRef.current?.configure({
      chunkBytes: user.uploadChunkBytes,
      maxBatchBytes: user.maxBatchBytes
    });
    void listPersistedBatches()
      .then((batches) => setPendingResume(batches[0] || null))
      .catch(() => setPendingResume(null));
  }, [user?.id, user?.maxBatchBytes, user?.uploadChunkBytes]);

  useEffect(() => {
    try {
      localStorage.setItem(LAYOUT_KEY, layout);
    } catch {
      /* ignore */
    }
  }, [layout]);

  useEffect(() => {
    if (!preview) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") setPreview(null);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [preview]);

  // Keep file list fresh so WebDAV / sync / other device changes appear without full reload.
  useEffect(() => {
    if (!user) return;
    const fileViews = new Set(["files", "shared", "starred", "recent"]);
    if (!fileViews.has(view)) return;
    const tick = () => {
      void loadFiles(folderRef.current).catch(() => undefined);
    };
    const id = window.setInterval(tick, 2000);
    const onFocus = () => tick();
    window.addEventListener("focus", onFocus);
    return () => {
      window.clearInterval(id);
      window.removeEventListener("focus", onFocus);
    };
  }, [user?.id, view, crumbs]);

  async function enqueueFiles(selected: QueuedFile[], parentId: string | null = currentFolder) {
    if (!selected.length) return;
    setMessage("");
    try {
      const prepared = [...selected];
      // Same-name conflicts in the current listing (flat uploads / top-level names).
      if (parentId === currentFolder || (parentId == null && currentFolder == null)) {
        const conflicts = prepared.filter((item) => {
          const topLevel = !item.relativePath.includes("/");
          if (!topLevel) return false;
          return files.some((f) => f.kind === "file" && f.name.toLocaleLowerCase("tr-TR") === item.fileName.toLocaleLowerCase("tr-TR"));
        });
        if (conflicts.length) {
          const names = conflicts.slice(0, 5).map((c) => c.fileName).join(", ");
          const more = conflicts.length > 5 ? ` (+${conflicts.length - 5})` : "";
          const ok = window.confirm(
            `${conflicts.length} dosya zaten var (${names}${more}). Üzerine yazılsın mı?`
          );
          if (!ok) {
            setMessage("Yükleme iptal edildi (çakışan dosyalar).");
            return;
          }
          for (const item of prepared) {
            const match = files.find(
              (f) => f.kind === "file" && f.name.toLocaleLowerCase("tr-TR") === item.fileName.toLocaleLowerCase("tr-TR")
            );
            if (match && !item.relativePath.includes("/")) {
              item.targetEntryId = match.id;
            }
          }
        }
      }
      setPendingResume(null);
      const folderName = parentId ? filesListName(parentId) : null;
      setMessage(folderName ? `"${folderName}" klasörüne yükleme başlatıldı.` : "Yükleme başlatıldı.");
      // Don't await: uploads keep running while the user navigates other pages.
      void queueRef.current?.start(prepared, parentId).catch((error) => {
        setMessage(error instanceof Error ? error.message : "Dosyalar yüklenemedi");
      });
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Dosyalar yüklenemedi");
    }
  }

  function continuePendingUpload() {
    const queue = queueRef.current;
    if (!queue) return;
    // In-session pause: continue without re-picking files.
    if (uploadProgress?.hasLiveFiles && (uploadProgress.status === "paused" || uploadProgress.status === "waiting")) {
      queue.resume();
      return;
    }
    if (queue.isRunning() && uploadProgress?.hasLiveFiles) {
      queue.resume();
      return;
    }
    // Only after full page reload File handles are gone — then re-select same folder/files.
    if (pendingResume) {
      folderInput.current?.click();
    }
  }

  function filesListName(id: string) {
    return files.find((item) => item.id === id)?.name;
  }

  async function submitAuth(event: FormEvent) {
    event.preventDefault();
    setMessage("");
    setBusy(true);
    try {
      if (mode === "forgot") {
        const res = await request<{ message?: string }>("/api/auth/forgot-password", {
          method: "POST",
          body: JSON.stringify({ email: form.email })
        });
        setMessage(res.message || "Kod gönderildi.");
        setMode("reset");
        return;
      }
      if (mode === "reset") {
        await request("/api/auth/reset-password", {
          method: "POST",
          body: JSON.stringify({ email: form.email, code: otpCode, newPassword })
        });
        setMessage("Şifre güncellendi. Giriş yapabilirsiniz.");
        setMode("login");
        setOtpCode("");
        setNewPassword("");
        setForm((f) => ({ ...f, password: "" }));
        return;
      }
      if (mode === "login2fa") {
        await request<User>("/api/auth/login/2fa", {
          method: "POST",
          body: JSON.stringify({ challengeToken, code: otpCode })
        });
        setChallengeToken("");
        setOtpCode("");
        setMode("login");
        setCrumbs([{ id: null, name: "Dosyalarım" }]);
        await bootstrap();
        return;
      }
      if (mode === "register" && form.password.length < 8) {
        setMessage("Şifre en az 8 karakter olmalı.");
        return;
      }
      const path = mode === "register" ? "/api/auth/register" : "/api/auth/login";
      const payload = mode === "register"
        ? form
        : { email: form.email, password: form.password };
      const res = await request<User & { requires2FA?: boolean; challengeToken?: string; message?: string }>(path, {
        method: "POST",
        body: JSON.stringify(payload)
      });
      if (res.requires2FA && res.challengeToken) {
        setChallengeToken(res.challengeToken);
        setOtpCode("");
        setMode("login2fa");
        setMessage(res.message || "E-postanıza gelen doğrulama kodunu girin.");
        return;
      }
      setForm({ email: "", password: "", displayName: "" });
      setCrumbs([{ id: null, name: "Dosyalarım" }]);
      await bootstrap();
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "İşlem başarısız");
    } finally {
      setBusy(false);
    }
  }

  async function createFolder() {
    const name = window.prompt("Yeni klasörün adı:");
    if (!name?.trim()) return;
    setBusy(true);
    try {
      await request("/api/files", {
        method: "POST",
        body: JSON.stringify({ name: name.trim(), parentId: currentFolder || "" })
      });
      await loadFiles(currentFolder);
      setMessage("Klasör oluşturuldu.");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Klasör oluşturulamadı");
    } finally {
      setBusy(false);
    }
  }

  async function uploadFiles(event: ChangeEvent<HTMLInputElement>, asDirectory = false) {
    const selected = filesFromList(event.target.files || [], asDirectory);
    event.target.value = "";
    if (pendingResume && asDirectory && !queueRef.current?.hasLiveFiles()) {
      setPendingResume(null);
      void queueRef.current?.resumeWithFiles(pendingResume, selected).catch((error) => {
        setMessage(error instanceof Error ? error.message : "Yüklemeye devam edilemedi");
        void listPersistedBatches().then((batches) => setPendingResume(batches[0] || null));
      });
      setMessage("Yarım kalan yüklemeye devam ediliyor…");
      return;
    }
    await enqueueFiles(selected);
  }

  async function onPanelDrop(event: DragEvent<HTMLElement>) {
    event.preventDefault();
    if (event.dataTransfer.types.includes(INTERNAL_DRAG_TYPE)) {
      setDragActive(false);
      setDropTargetId(null);
      return;
    }
    setDragActive(false);
    setDropTargetId(null);
    const dropped = await collectDroppedFiles(event.dataTransfer);
    await enqueueFiles(dropped, currentFolder);
  }

  async function onFolderDrop(event: DragEvent<HTMLElement>, folder: FileEntry) {
    event.preventDefault();
    event.stopPropagation();
    setDragActive(false);
    setDropTargetId(null);
    const internal = event.dataTransfer.getData(INTERNAL_DRAG_TYPE);
    if (internal) {
      try {
        const source = JSON.parse(internal) as Pick<FileEntry, "id" | "name" | "kind">;
        if (!source.id || source.id === folder.id) {
          setMessage("Bir klasör kendi içine taşınamaz.");
          return;
        }
        await request("/api/files/move", {
          method: "POST",
          body: JSON.stringify({ fileId: source.id, parentId: folder.id })
        });
        await loadFiles(currentFolder);
        setMessage(`"${source.name}", "${folder.name}" klasörüne taşındı.`);
      } catch (error) {
        setMessage(error instanceof Error ? error.message : "Dosya taşınamadı");
      } finally {
        setDraggedEntryId(null);
      }
      return;
    }
    const dropped = await collectDroppedFiles(event.dataTransfer);
    await enqueueFiles(dropped, folder.id);
  }

  function onPanelDragLeave(event: DragEvent<HTMLElement>) {
    event.preventDefault();
    const next = event.relatedTarget as Node | null;
    if (next && event.currentTarget.contains(next)) return;
    setDragActive(false);
    setDropTargetId(null);
  }

  function onFolderDragOver(event: DragEvent<HTMLElement>, folderId: string) {
    event.preventDefault();
    event.stopPropagation();
    if (draggedEntryId === folderId) {
      event.dataTransfer.dropEffect = "none";
      return;
    }
    event.dataTransfer.dropEffect = event.dataTransfer.types.includes(INTERNAL_DRAG_TYPE) ? "move" : "copy";
    setDragActive(false);
    setDropTargetId(folderId);
  }

  function onFolderDragLeave(event: DragEvent<HTMLElement>, folderId: string) {
    event.preventDefault();
    event.stopPropagation();
    const next = event.relatedTarget as Node | null;
    if (next && event.currentTarget.contains(next)) return;
    setDropTargetId((current) => (current === folderId ? null : current));
  }

  function onEntryDragStart(event: DragEvent<HTMLElement>, entry: FileEntry) {
    event.stopPropagation();
    event.dataTransfer.effectAllowed = "move";
    event.dataTransfer.setData(INTERNAL_DRAG_TYPE, JSON.stringify({
      id: entry.id,
      name: entry.name,
      kind: entry.kind
    }));
    event.dataTransfer.setData("text/plain", entry.name);
    setDraggedEntryId(entry.id);
    setDragActive(false);
  }

  function onEntryDragEnd() {
    setDraggedEntryId(null);
    setDropTargetId(null);
    setDragActive(false);
  }

  async function renameEntry(entry: FileEntry) {
    const name = window.prompt("Yeni ad:", entry.name);
    if (!name?.trim() || name.trim() === entry.name) return;
    try {
      await request("/api/files/rename", {
        method: "POST",
        body: JSON.stringify({ fileId: entry.id, name: name.trim() })
      });
      await loadFiles(currentFolder);
      setMessage("Ad değiştirildi.");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Ad değiştirilemedi");
    }
  }

  async function deleteEntry(entry: FileEntry) {
    const text = entry.kind === "folder"
      ? `"${entry.name}" klasörü çöp kutusuna taşınsın mı?`
      : `"${entry.name}" çöp kutusuna taşınsın mı?`;
    if (!window.confirm(text)) return;
    try {
      await request("/api/files/delete", {
        method: "POST",
        body: JSON.stringify({ fileId: entry.id })
      });
      await Promise.all([loadFiles(currentFolder), refreshUser()]);
      setMessage("Çöp kutusuna taşındı.");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Silinemedi");
    }
  }

  async function createShare(entry: FileEntry) {
    setShareTarget(entry);
  }

  async function openSpecialList(path: string, title: string) {
    setCrumbs([{ id: null, name: title }]);
    setFiles(await request<FileEntry[]>(path));
  }

  async function runSearch(q: string) {
    setSearchQ(q);
    if (!q.trim()) {
      await loadFiles(currentFolder);
      return;
    }
    setView("files");
    setFiles(await request<FileEntry[]>(`/api/files/search?q=${encodeURIComponent(q.trim())}`));
  }

  function openDriveRoot(id: string, name: string) {
    setView("files");
    setCrumbs([{ id, name }]);
    void loadFiles(id);
  }

  function openFolder(entry: FileEntry) {
    setCrumbs((current) => [...current, { id: entry.id, name: entry.name }]);
    void loadFiles(entry.id);
  }

  function openPreview(entry: FileEntry) {
    const kind = previewKindOf(entry);
    if (!kind) return;
    setPreview({ entry, kind });
  }

  function activateEntry(entry: FileEntry) {
    if (entry.kind === "folder") {
      openFolder(entry);
      return;
    }
    const kind = previewKindOf(entry);
    if (kind) {
      openPreview(entry);
      return;
    }
    window.location.href = `/api/files/download/${entry.id}`;
  }

  function navigateTo(index: number) {
    const next = crumbs.slice(0, index + 1);
    setCrumbs(next);
    void loadFiles(next[next.length - 1].id);
  }

  function toggleTheme() {
    setTheme((current) => (current === "light" ? "dark" : "light"));
  }

  async function logout() {
    try {
      await request("/api/auth/logout", { method: "POST" });
    } finally {
      setUser(null);
      setFiles([]);
      setCrumbs([{ id: null, name: "Dosyalarım" }]);
      setPreview(null);
      setMessage("");
      setMode("login");
      setView("files");
      setQrLogin(null);
    }
  }

  async function openQRLogin() {
    try {
      const res = await request<{ challengeToken: string; expiresAt: string }>("/api/auth/qr/create", { method: "POST" });
      const payload = JSON.stringify({
        v: 1,
        server: window.location.origin,
        challengeToken: res.challengeToken
      });
      setQrLogin({ token: res.challengeToken, expiresAt: res.expiresAt, payload });
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "QR oluşturulamadı");
    }
  }

  function renderActions(entry: FileEntry) {
    const kind = previewKindOf(entry);
    return (
      <div className="row-actions" onClick={(event) => event.stopPropagation()}>
        {entry.kind === "file" && kind && (
          <button title="Önizle" onClick={() => openPreview(entry)}>◎</button>
        )}
        {entry.kind === "file" && <a title="İndir" href={`/api/files/download/${entry.id}`}>↓</a>}
        <button title="Paylaş" onClick={() => createShare(entry)}>↗</button>
        <button title="Yıldız" onClick={() => void request("/api/files/starred", { method: "POST", body: JSON.stringify({ entryId: entry.id, starred: !entry.starred }) }).then(() => loadFiles(currentFolder))}>{entry.starred ? "★" : "☆"}</button>
        <button title="Detay" onClick={() => setDetailEntry(entry)}>⋯</button>
        <button title="Yeniden adlandır" onClick={() => renameEntry(entry)}>✎</button>
        <button className="danger" title="Sil" onClick={() => deleteEntry(entry)}>×</button>
      </div>
    );
  }

  function renderFileItem(entry: FileEntry) {
    const isDropTarget = entry.kind === "folder" && dropTargetId === entry.id;
    const isDragging = draggedEntryId === entry.id;
    const previewKind = previewKindOf(entry);
    const commonProps = entry.kind === "folder"
      ? {
          onDragOver: (event: DragEvent<HTMLElement>) => onFolderDragOver(event, entry.id),
          onDragEnter: (event: DragEvent<HTMLElement>) => onFolderDragOver(event, entry.id),
          onDragLeave: (event: DragEvent<HTMLElement>) => onFolderDragLeave(event, entry.id),
          onDrop: (event: DragEvent<HTMLElement>) => void onFolderDrop(event, entry)
        }
      : {};

    if (layout === "grid") {
      return (
        <article
          key={entry.id}
          className={`file-card ${entry.kind} ${isDropTarget ? "drop-target" : ""} ${isDragging ? "dragging" : ""} ${previewKind || ""}`}
          draggable
          onDragStart={(event) => onEntryDragStart(event, entry)}
          onDragEnd={onEntryDragEnd}
          onDoubleClick={() => activateEntry(entry)}
          {...commonProps}
        >
          <button className="file-card-main" type="button" onClick={() => activateEntry(entry)}>
            <span className={`file-icon ${entry.kind} ${previewKind || ""}`}>{fileIconLabel(entry)}</span>
            <strong title={entry.name}>{entry.name}</strong>
            <small>
              {entry.kind === "folder"
                ? "Klasör · buraya bırak"
                : `${previewKind ? previewKind.toUpperCase() : "Dosya"} · ${formatBytes(entry.sizeBytes)}`}
            </small>
          </button>
          {renderActions(entry)}
        </article>
      );
    }

    return (
      <article
        key={entry.id}
        className={`file-row ${entry.kind} ${isDropTarget ? "drop-target" : ""} ${isDragging ? "dragging" : ""}`}
        draggable
        onDragStart={(event) => onEntryDragStart(event, entry)}
        onDragEnd={onEntryDragEnd}
        onDoubleClick={() => activateEntry(entry)}
        {...commonProps}
      >
        <button className="file-name" type="button" onClick={() => activateEntry(entry)}>
          <span className={`file-icon ${entry.kind} ${previewKind || ""}`}>{fileIconLabel(entry)}</span>
          <span>
            <strong>{entry.name}</strong>
            <small>{new Date(entry.updatedAt).toLocaleDateString("tr-TR")}</small>
          </span>
        </button>
        <span className="file-meta">
          {entry.kind === "folder"
            ? "Klasör"
            : `${entry.mimeType || "Dosya"} · ${formatBytes(entry.sizeBytes)}`}
        </span>
        {renderActions(entry)}
      </article>
    );
  }

  if (loading) {
    return <main className="center-screen"><div className="spinner" /><p>TR Driver hazırlanıyor…</p></main>;
  }

  if (!user) {
    const title =
      mode === "register" ? "Hesap oluştur"
        : mode === "forgot" ? "Şifremi unuttum"
          : mode === "reset" ? "Yeni şifre belirle"
            : mode === "login2fa" ? "İki adımlı doğrulama"
              : "Tekrar hoş geldin";
    const subtitle =
      mode === "register" ? "Ücretsiz depolama alanınla hemen başla."
        : mode === "forgot" ? "E-posta adresine 6 haneli bir sıfırlama kodu göndereceğiz."
          : mode === "reset" ? "E-postadaki kodu ve yeni şifreni gir."
            : mode === "login2fa" ? "E-postana gelen 6 haneli kodu gir."
              : "Dosyalarına erişmek için giriş yap.";
    return (
      <main className="auth-page">
        <button className="theme-toggle floating" type="button" onClick={toggleTheme} title="Tema">
          {theme === "light" ? "◐" : "◑"}
        </button>
        <section className="auth-brand">
          <span className="brand-mark">TR</span>
          <h1>TR Driver</h1>
          <p>Google Drive’a para ödemeden, kendi sunucunda çalışan dosya bulutu.</p>
        </section>
        <form className="auth-card" onSubmit={submitAuth}>
          <h2>{title}</h2>
          <p>{subtitle}</p>
          {mode === "register" && (
            <label>Adın<input required autoComplete="name" value={form.displayName} onChange={(e) => setForm({ ...form, displayName: e.target.value })} /></label>
          )}
          {(mode === "login" || mode === "register" || mode === "forgot" || mode === "reset") && (
            <label>E-posta<input required type="email" autoComplete="email" value={form.email} onChange={(e) => setForm({ ...form, email: e.target.value })} /></label>
          )}
          {(mode === "login" || mode === "register") && (
            <label>Şifre<input required minLength={8} type="password" autoComplete={mode === "register" ? "new-password" : "current-password"} value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} /></label>
          )}
          {(mode === "login2fa" || mode === "reset") && (
            <label>Doğrulama kodu<input required inputMode="numeric" pattern="[0-9]{6}" maxLength={6} autoComplete="one-time-code" value={otpCode} onChange={(e) => setOtpCode(e.target.value.replace(/\D/g, "").slice(0, 6))} placeholder="6 haneli kod" /></label>
          )}
          {mode === "reset" && (
            <label>Yeni şifre<input required minLength={8} type="password" autoComplete="new-password" value={newPassword} onChange={(e) => setNewPassword(e.target.value)} /></label>
          )}
          <button className="primary" disabled={busy}>
            {busy ? "Lütfen bekle…"
              : mode === "register" ? "Ücretsiz hesap oluştur"
                : mode === "forgot" ? "Kod gönder"
                  : mode === "reset" ? "Şifreyi güncelle"
                    : mode === "login2fa" ? "Doğrula ve giriş yap"
                      : "Giriş yap"}
          </button>
          {message && <div className={`notice ${mode === "forgot" || mode === "reset" ? "" : "error"}`}>{message}</div>}
          {mode === "login" && (
            <button className="text-button" type="button" onClick={() => { setMode("forgot"); setMessage(""); }}>
              Şifremi unuttum
            </button>
          )}
          {(mode === "login" || mode === "register") && (
            <button className="text-button" type="button" onClick={() => { setMode(mode === "login" ? "register" : "login"); setMessage(""); }}>
              {mode === "login" ? "Hesabın yok mu? Üye ol" : "Zaten hesabın var mı? Giriş yap"}
            </button>
          )}
          {(mode === "forgot" || mode === "reset" || mode === "login2fa") && (
            <button className="text-button" type="button" onClick={() => { setMode("login"); setMessage(""); setOtpCode(""); setChallengeToken(""); }}>
              Girişe dön
            </button>
          )}
        </form>
      </main>
    );
  }

  const previewURL = preview ? `/api/files/download/${preview.entry.id}?inline=1` : "";

  return (
    <main className="app-shell">
      <aside className="sidebar">
        <div className="brand"><span className="brand-mark small">TR</span><strong>TR Driver</strong></div>
        <CollabSidebar
          view={view}
          setView={(v) => {
            if (v === "files") {
              setView("files");
              navigateTo(0);
              return;
            }
            if (v === "shared") {
              void openSpecialList("/api/shared-with-me", "Benimle paylaşılan");
              setView("shared");
              return;
            }
            if (v === "starred") {
              void openSpecialList("/api/files/starred", "Yıldızlı");
              setView("starred");
              return;
            }
            if (v === "recent") {
              void openSpecialList("/api/files/recent", "Son görüntülenen");
              setView("recent");
              return;
            }
            if (v.startsWith("drive:")) {
              const id = v.slice(6);
              const d = drives.find((x) => x.id === id);
              if (d) openDriveRoot(d.rootEntryId, d.name);
              return;
            }
            setView(v);
          }}
          drives={drives}
          unread={unread}
        />
        <nav>
          {user.role === "admin" && (
            <button className={`nav-item ${view === "admin" ? "active" : ""}`} onClick={() => setView("admin")}>⚙ Yönetim</button>
          )}
          <button className="nav-item" type="button" onClick={toggleTheme} title="Tema">
            {theme === "light" ? "◐ Açık" : "◑ Koyu"}
          </button>
        </nav>
        <div className="sidebar-footer">
          <div className="quota-card">
            <div><span>Depolama</span><strong>{usageRate}%</strong></div>
            <div className="quota-bar"><span style={{ width: `${usageRate}%` }} /></div>
            <small>{formatBytes(user.usedBytes)} / {formatBytes(user.quotaBytes)}</small>
          </div>
          <a className="mobile-apps-card" href="/download/TRDriver.apk?v=0.4.1" download="TRDriver.apk" type="application/vnd.android.package-archive">
            <span className="android-mark" aria-hidden>▶</span>
            <div>
              <strong>Android APK</strong>
              <small>İndir ve yükle · v0.4.1</small>
            </div>
          </a>
          <button
            type="button"
            className="mobile-apps-card"
            style={{ width: "100%", cursor: "pointer", border: "1px solid #d7e3f5" }}
            onClick={() => void openQRLogin()}
          >
            <span className="android-mark" aria-hidden>QR</span>
            <div style={{ textAlign: "left" }}>
              <strong>QR giriş</strong>
              <small>Telefona oturum aç</small>
            </div>
          </button>
          <div className="profile">
            <span className="avatar">{user.displayName.slice(0, 1).toUpperCase()}</span>
            <div><strong>{user.displayName}</strong><small>{user.email}</small></div>
            <button title="Çıkış yap" onClick={logout}>↪</button>
          </div>
        </div>
      </aside>

      <section className="workspace">
        {view === "admin" && user.role === "admin" ? (
          <AdminPanel
            currentUserId={user.id}
            plans={plans}
            request={request}
            onMessage={setMessage}
            onCurrentUserChanged={refreshUser}
          />
        ) : view === "drives" ? (
          <DrivesPanel request={request} onOpenFolder={openDriveRoot} />
        ) : view === "trash" ? (
          <TrashPanel request={request} onOpenFolder={openDriveRoot} />
        ) : view === "shares" ? (
          <SharesPanel request={request} onOpenFolder={openDriveRoot} />
        ) : view === "settings" ? (
          <SettingsPanel request={request} onOpenFolder={openDriveRoot} />
        ) : view === "activity" ? (
          <ActivityPanel request={request} onOpenFolder={openDriveRoot} />
        ) : view === "notifications" ? (
          <NotificationsPanel request={request} onOpenFolder={openDriveRoot} />
        ) : (
          <>
        <header className="topbar">
          <div>
            <h1>{crumbs[crumbs.length - 1]?.name || "Dosyalarım"}</h1>
            <div className="breadcrumbs">
              {crumbs.map((crumb, index) => (
                <button key={`${crumb.id}-${index}`} onClick={() => navigateTo(index)}>
                  {crumb.name}{index < crumbs.length - 1 && <span> / </span>}
                </button>
              ))}
            </div>
          </div>
          <div className="toolbar">
            <input
              className="search-input"
              value={searchQ}
              onChange={(e) => setSearchQ(e.target.value)}
              onKeyDown={(e) => { if (e.key === "Enter") void runSearch(searchQ); }}
              placeholder="Dosya ara…"
            />
            <div className="layout-toggle" role="group" aria-label="Görünüm">
              <button type="button" className={layout === "list" ? "active" : ""} onClick={() => setLayout("list")} title="Liste">☰</button>
              <button type="button" className={layout === "grid" ? "active" : ""} onClick={() => setLayout("grid")} title="Izgara">▦</button>
            </div>
            <button disabled={busy} onClick={createFolder}>＋ Yeni klasör</button>
            <button disabled={busy} onClick={() => folderInput.current?.click()}>▤ Klasör seç</button>
            <button className="primary" disabled={busy} onClick={() => fileInput.current?.click()}>↑ Dosya yükle</button>
            <input ref={fileInput} hidden multiple type="file" onChange={(event) => void uploadFiles(event, false)} />
            <input ref={folderInput} hidden multiple type="file" {...({ webkitdirectory: "", directory: "" } as object)} onChange={(event) => void uploadFiles(event, true)} />
          </div>
        </header>

        {pendingResume && !(uploadProgress?.hasLiveFiles || (uploadProgress && uploadProgress.status !== "idle" && uploadProgress.status !== "done")) && (
          <div className="notice">
            Yarım kalan bir yükleme var ({pendingResume.files.length} dosya). Sayfa yenilendiği için aynı klasörü yeniden seçmeniz gerekir.
            <button type="button" onClick={continuePendingUpload}>Klasörü yeniden seç</button>
            <button type="button" onClick={() => { void queueRef.current?.cancel(pendingResume.id); setPendingResume(null); }}>İptal</button>
          </div>
        )}

        {message && <div className="notice">{message}<button onClick={() => setMessage("")}>×</button></div>}

        <section
          className={`file-panel layout-${layout} ${dragActive ? "drag-active" : ""}`}
          onDragEnter={(event) => {
            event.preventDefault();
            if (!event.dataTransfer.types.includes(INTERNAL_DRAG_TYPE)) setDragActive(true);
          }}
          onDragOver={(event) => {
            event.preventDefault();
            if (!dropTargetId && !event.dataTransfer.types.includes(INTERNAL_DRAG_TYPE)) setDragActive(true);
          }}
          onDragLeave={onPanelDragLeave}
          onDrop={(event) => void onPanelDrop(event)}
        >
          <div className="file-panel-toolbar">
            {layout === "list" && (
              <div className="file-head"><span>Ad</span><span>Tür / Boyut</span><span>İşlemler</span></div>
            )}
            {layout === "grid" && <div className="file-head grid-head"><span>{files.length} öğe</span><span>Bilgisayardan yükle veya sunucudaki öğeleri klasörlerin üzerine sürükleyerek taşı</span></div>}
          </div>
          <div className={`file-scroll ${layout === "grid" ? "grid-view" : "list-view"}`}>
            {files.map((entry) => renderFileItem(entry))}
            {!files.length && (
              <div className="empty-state">
                <span>☁</span><h2>Bu klasör henüz boş</h2>
                <p>Dosya veya klasörleri buraya sürükleyip bırakabilir, ya da seçerek yükleyebilirsin. Büyük dosyalar parçalı ve kesintiden sonra devam edecek şekilde yüklenir.</p>
                <div className="empty-actions">
                  <button className="primary" onClick={() => fileInput.current?.click()}>Dosya yükle</button>
                  <button onClick={() => folderInput.current?.click()}>Klasör seç</button>
                </div>
              </div>
            )}
          </div>
          {dragActive && !dropTargetId && <div className="drop-hint">Bırak, bu klasöre yükleyelim</div>}
        </section>

        {detailEntry && (
          <CommentsVersionsPanel request={request} entryId={detailEntry.id} />
        )}
          </>
        )}
        {view === "admin" && message && <div className="notice admin-notice">{message}<button onClick={() => setMessage("")}>×</button></div>}
      </section>

      {shareTarget && (
        <ShareModal request={request} entryId={shareTarget.id} entryName={shareTarget.name} onClose={() => setShareTarget(null)} />
      )}

      {uploadProgress && uploadProgress.status !== "idle" && uploadProgress.status !== "done" && (
        <aside className="upload-dock" aria-live="polite">
          <header className="upload-dock-head">
            <div>
              <strong>Yüklemeler</strong>
              <small>{uploadProgress.percent}% · {uploadProgress.message}</small>
            </div>
            <div className="upload-actions">
              {uploadProgress.status === "paused"
                ? <button type="button" onClick={() => queueRef.current?.resume()}>Devam</button>
                : <button type="button" onClick={() => queueRef.current?.pause()}>Duraklat</button>}
              <button type="button" onClick={() => void queueRef.current?.cancel(uploadProgress.batchId)}>İptal</button>
            </div>
          </header>
          <div className="upload-status-bar"><span style={{ width: `${uploadProgress.percent}%` }} /></div>
          <small className="upload-dock-meta">
            {formatBytes(uploadProgress.sentBytes)} / {formatBytes(uploadProgress.totalBytes)}
            {uploadProgress.currentFile ? ` · ${uploadProgress.currentFile}` : ""}
          </small>
          {!!uploadProgress.files?.length && (
            <ul className="upload-dock-list">
              {uploadProgress.files.map((file) => {
                const filePct = file.expectedSize
                  ? Math.min(100, Math.round((file.receivedBytes / file.expectedSize) * 100))
                  : 0;
                return (
                  <li key={file.relativePath} data-status={file.status}>
                    <span title={file.relativePath}>{file.fileName}</span>
                    <em>{file.status === "complete" ? "Tamam" : `${filePct}%`}</em>
                  </li>
                );
              })}
            </ul>
          )}
        </aside>
      )}

      {preview && (
        <div className="preview-backdrop" onClick={() => setPreview(null)}>
          <div className="preview-modal" onClick={(event) => event.stopPropagation()} role="dialog" aria-modal="true" aria-label={preview.entry.name}>
            <header>
              <div>
                <strong>{preview.entry.name}</strong>
                <small>{formatBytes(preview.entry.sizeBytes)} · {preview.entry.mimeType || preview.kind}</small>
              </div>
              <div className="preview-actions">
                <a className="primary" href={`/api/files/download/${preview.entry.id}`}>İndir</a>
                <button type="button" onClick={() => setPreview(null)}>Kapat</button>
              </div>
            </header>
            <div className="preview-body">
              {preview.kind === "image" && <img src={previewURL} alt={preview.entry.name} />}
              {preview.kind === "video" && <video src={previewURL} controls playsInline />}
              {preview.kind === "audio" && <audio src={previewURL} controls />}
              {preview.kind === "pdf" && (
                <iframe title={preview.entry.name} src={previewURL} sandbox="" />
              )}
            </div>
          </div>
        </div>
      )}
      {qrLogin && (
        <div className="qr-modal" onClick={() => setQrLogin(null)}>
          <div className="qr-modal-card" onClick={(event) => event.stopPropagation()}>
            <h3>Telefona QR ile giriş</h3>
            <p>TR Driver Android uygulamasında “QR ile giriş”i açıp bu kodu okutun.</p>
            <img
              alt="QR giriş kodu"
              src={`https://api.qrserver.com/v1/create-qr-code/?size=220x220&data=${encodeURIComponent(qrLogin.payload)}`}
            />
            <small>Geçerlilik: {new Date(qrLogin.expiresAt).toLocaleTimeString("tr-TR")}</small>
            <button type="button" className="primary" onClick={() => setQrLogin(null)}>Kapat</button>
          </div>
        </div>
      )}
    </main>
  );
}
