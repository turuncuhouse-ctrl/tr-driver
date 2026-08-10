const DB_NAME = "necipdrive-uploads";
const STORE = "batches";

export type QueuedFile = {
  relativePath: string;
  fileName: string;
  mimeType: string;
  expectedSize: number;
  lastModifiedMs: number;
  file: File;
  targetEntryId?: string;
};

export type UploadSessionState = {
  id: string;
  relativePath: string;
  fileName: string;
  expectedSize: number;
  receivedBytes: number;
  lastModifiedMs: number;
  status: string;
};

export type PersistedBatch = {
  id: string;
  parentId: string | null;
  totalBytes: number;
  createdAt: number;
  files: Array<{
    id: string;
    relativePath: string;
    fileName: string;
    expectedSize: number;
    lastModifiedMs: number;
    receivedBytes: number;
    status: string;
  }>;
};

export type QueueProgress = {
  batchId: string | null;
  totalBytes: number;
  sentBytes: number;
  currentFile: string;
  status: "idle" | "uploading" | "paused" | "waiting" | "error" | "done";
  message: string;
  percent: number;
};

type RequestFn = <T>(path: string, init?: RequestInit) => Promise<T>;

const openDB = () =>
  new Promise<IDBDatabase>((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, 1);
    req.onupgradeneeded = () => {
      const db = req.result;
      if (!db.objectStoreNames.contains(STORE)) db.createObjectStore(STORE, { keyPath: "id" });
    };
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });

export async function savePersistedBatch(batch: PersistedBatch) {
  const db = await openDB();
  await new Promise<void>((resolve, reject) => {
    const tx = db.transaction(STORE, "readwrite");
    tx.objectStore(STORE).put(batch);
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error);
  });
  db.close();
}

export async function listPersistedBatches() {
  const db = await openDB();
  const items = await new Promise<PersistedBatch[]>((resolve, reject) => {
    const tx = db.transaction(STORE, "readonly");
    const req = tx.objectStore(STORE).getAll();
    req.onsuccess = () => resolve(req.result as PersistedBatch[]);
    req.onerror = () => reject(req.error);
  });
  db.close();
  return items;
}

export async function deletePersistedBatch(id: string) {
  const db = await openDB();
  await new Promise<void>((resolve, reject) => {
    const tx = db.transaction(STORE, "readwrite");
    tx.objectStore(STORE).delete(id);
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error);
  });
  db.close();
}

export function fingerprint(file: QueuedFile) {
  return `${file.relativePath}|${file.expectedSize}|${file.lastModifiedMs}`;
}

export async function collectDroppedFiles(dataTransfer: DataTransfer): Promise<QueuedFile[]> {
  const items = Array.from(dataTransfer.items || []);
  if (items.some((item) => typeof item.webkitGetAsEntry === "function" && item.webkitGetAsEntry())) {
    const entries = items
      .map((item) => item.webkitGetAsEntry?.())
      .filter((entry): entry is FileSystemEntry => !!entry);
    const files: QueuedFile[] = [];
    for (const entry of entries) {
      await walkEntry(entry, "", files);
    }
    return files;
  }
  return Array.from(dataTransfer.files || []).map((file) => toQueued(file, file.webkitRelativePath || file.name));
}

async function walkEntry(entry: FileSystemEntry, parent: string, out: QueuedFile[]) {
  if (entry.isFile) {
    const fileEntry = entry as FileSystemFileEntry;
    const file = await new Promise<File>((resolve, reject) => fileEntry.file(resolve, reject));
    const relative = parent ? `${parent}/${file.name}` : file.name;
    out.push(toQueued(file, relative));
    return;
  }
  if (entry.isDirectory) {
    const dir = entry as FileSystemDirectoryEntry;
    const reader = dir.createReader();
    const children: FileSystemEntry[] = [];
    for (;;) {
      const batch = await new Promise<FileSystemEntry[]>((resolve, reject) => reader.readEntries(resolve, reject));
      if (!batch.length) break;
      children.push(...batch);
    }
    const nextParent = parent ? `${parent}/${dir.name}` : dir.name;
    for (const child of children) {
      await walkEntry(child, nextParent, out);
    }
  }
}

export function filesFromList(list: FileList | File[], asDirectory: boolean): QueuedFile[] {
  return Array.from(list).map((file) => {
    const relative = asDirectory
      ? (file.webkitRelativePath || file.name)
      : file.name;
    return toQueued(file, relative);
  });
}

function toQueued(file: File, relativePath: string): QueuedFile {
  const normalized = relativePath.replace(/\\/g, "/").replace(/^\/+/, "");
  return {
    relativePath: normalized || file.name,
    fileName: file.name,
    mimeType: file.type || "application/octet-stream",
    expectedSize: file.size,
    lastModifiedMs: file.lastModified,
    file
  };
}

export class UploadQueue {
  private paused = false;
  private aborted = false;
  private files: QueuedFile[] = [];
  private parentId: string | null = null;
  private chunkBytes = 8 * 1024 * 1024;
  private maxBatchBytes = 10 * 1024 * 1024 * 1024;
  private csrf: () => string;
  private request: RequestFn;
  private onProgress: (progress: QueueProgress) => void;
  private onComplete: () => Promise<void> | void;

  constructor(options: {
    request: RequestFn;
    csrf: () => string;
    onProgress: (progress: QueueProgress) => void;
    onComplete: () => Promise<void> | void;
  }) {
    this.request = options.request;
    this.csrf = options.csrf;
    this.onProgress = options.onProgress;
    this.onComplete = options.onComplete;
  }

  configure(limits: { chunkBytes?: number; maxBatchBytes?: number }) {
    if (limits.chunkBytes && limits.chunkBytes > 0) this.chunkBytes = limits.chunkBytes;
    if (limits.maxBatchBytes && limits.maxBatchBytes > 0) this.maxBatchBytes = limits.maxBatchBytes;
  }

  pause() { this.paused = true; this.emit({ status: "paused", message: "Yükleme duraklatıldı." }); }
  resume() { this.paused = false; }
  async cancel(batchId?: string | null) {
    this.aborted = true;
    if (batchId) {
      await this.request(`/api/uploads/batches/${batchId}`, { method: "DELETE" }).catch(() => undefined);
      await deletePersistedBatch(batchId).catch(() => undefined);
    }
    this.emit({ status: "idle", message: "Yükleme iptal edildi.", batchId: null, sentBytes: 0, totalBytes: 0, percent: 0, currentFile: "" });
  }

  async start(files: QueuedFile[], parentId: string | null) {
    this.aborted = false;
    this.paused = false;
    this.files = files;
    this.parentId = parentId;
    const totalBytes = files.reduce((sum, file) => sum + file.expectedSize, 0);
    if (!files.length) throw new Error("Yüklenecek dosya yok.");
    if (totalBytes > this.maxBatchBytes) {
      throw new Error(`Seçilen dosyalar ${formatBytes(this.maxBatchBytes)} sınırını aşıyor.`);
    }

    this.emit({ status: "uploading", message: "Yükleme hazırlanıyor…", totalBytes, sentBytes: 0, percent: 0, currentFile: "", batchId: null });
    const batch = await this.request<{
      id: string;
      totalBytes: number;
      files: UploadSessionState[];
    }>("/api/uploads/batches", {
      method: "POST",
      body: JSON.stringify({
        parentId: parentId || "",
        files: files.map((file) => ({
          relativePath: file.relativePath,
          fileName: file.fileName,
          mimeType: file.mimeType,
          expectedSize: file.expectedSize,
          lastModifiedMs: file.lastModifiedMs,
          ...(file.targetEntryId ? { targetEntryId: file.targetEntryId } : {})
        }))
      })
    });

    const fileMap = new Map(files.map((file) => [fingerprint(file), file]));
    await savePersistedBatch({
      id: batch.id,
      parentId,
      totalBytes: batch.totalBytes,
      createdAt: Date.now(),
      files: batch.files.map((item) => ({
        id: item.id,
        relativePath: item.relativePath,
        fileName: item.fileName,
        expectedSize: item.expectedSize,
        lastModifiedMs: item.lastModifiedMs,
        receivedBytes: item.receivedBytes,
        status: item.status
      }))
    });

    let sentBytes = batch.files.reduce((sum, item) => sum + item.receivedBytes, 0);
    for (const session of batch.files) {
      if (this.aborted) return;
      while (this.paused) {
        this.emit({ status: "paused", message: "Yükleme duraklatıldı.", batchId: batch.id, totalBytes, sentBytes, percent: percent(sentBytes, totalBytes), currentFile: session.fileName });
        await sleep(250);
        if (this.aborted) return;
      }
      const local = fileMap.get(`${session.relativePath}|${session.expectedSize}|${session.lastModifiedMs}`);
      if (!local) throw new Error(`${session.relativePath} dosyası eşleştirilemedi.`);
      await this.uploadFile(batch.id, session, local, (nextOffset) => {
        const already = sentBytes - session.receivedBytes;
        const live = already + nextOffset;
        this.emit({
          status: "uploading",
          message: `${session.fileName} yükleniyor…`,
          batchId: batch.id,
          totalBytes,
          sentBytes: live,
          percent: percent(live, totalBytes),
          currentFile: session.fileName
        });
      });
      sentBytes += session.expectedSize - session.receivedBytes;
      session.receivedBytes = session.expectedSize;
    }

    await deletePersistedBatch(batch.id).catch(() => undefined);
    this.emit({ status: "done", message: "Yükleme tamamlandı.", batchId: null, totalBytes, sentBytes: totalBytes, percent: 100, currentFile: "" });
    await this.onComplete();
  }

  async resumeWithFiles(batch: PersistedBatch, files: QueuedFile[]) {
    this.aborted = false;
    this.paused = false;
    const map = new Map(files.map((file) => [fingerprint(file), file]));
    let sentBytes = batch.files.reduce((sum, file) => sum + file.receivedBytes, 0);
    for (const session of batch.files) {
      if (session.status === "complete") continue;
      const local = map.get(`${session.relativePath}|${session.expectedSize}|${session.lastModifiedMs}`);
      if (!local) {
        this.emit({
          status: "waiting",
          message: "Devam için aynı klasör/dosyaları yeniden seçmen gerekiyor.",
          batchId: batch.id,
          totalBytes: batch.totalBytes,
          sentBytes,
          percent: percent(sentBytes, batch.totalBytes),
          currentFile: session.fileName
        });
        throw new Error("Devam için aynı dosyaları yeniden seç.");
      }
      await this.uploadFile(batch.id, {
        id: session.id,
        relativePath: session.relativePath,
        fileName: session.fileName,
        expectedSize: session.expectedSize,
        receivedBytes: session.receivedBytes,
        lastModifiedMs: session.lastModifiedMs,
        status: session.status
      }, local, (nextOffset) => {
        const already = sentBytes - session.receivedBytes;
        const live = already + nextOffset;
        this.emit({
          status: "uploading",
          message: `${session.fileName} devam ediyor…`,
          batchId: batch.id,
          totalBytes: batch.totalBytes,
          sentBytes: live,
          percent: percent(live, batch.totalBytes),
          currentFile: session.fileName
        });
      });
      sentBytes += session.expectedSize - session.receivedBytes;
      session.receivedBytes = session.expectedSize;
      session.status = "complete";
      await savePersistedBatch(batch);
    }
    await deletePersistedBatch(batch.id).catch(() => undefined);
    this.emit({ status: "done", message: "Yükleme tamamlandı.", batchId: null, totalBytes: batch.totalBytes, sentBytes: batch.totalBytes, percent: 100, currentFile: "" });
    await this.onComplete();
  }

  private async uploadFile(
    batchId: string,
    session: UploadSessionState,
    local: QueuedFile,
    onOffset: (offset: number) => void
  ) {
    let offset = session.receivedBytes;
    while (offset < session.expectedSize) {
      if (this.aborted) return;
      while (this.paused) {
        await sleep(250);
        if (this.aborted) return;
      }
      const end = Math.min(offset + this.chunkBytes, session.expectedSize);
      const blob = local.file.slice(offset, end);
      offset = await this.putChunkWithRetry(session.id, offset, blob);
      onOffset(offset);
      await savePersistedBatch({
        id: batchId,
        parentId: this.parentId,
        totalBytes: this.files.reduce((sum, file) => sum + file.expectedSize, 0),
        createdAt: Date.now(),
        files: [{
          id: session.id,
          relativePath: session.relativePath,
          fileName: session.fileName,
          expectedSize: session.expectedSize,
          lastModifiedMs: session.lastModifiedMs,
          receivedBytes: offset,
          status: "open"
        }]
      }).catch(() => undefined);
    }
    await this.request(`/api/uploads/files/${session.id}/complete`, { method: "POST", body: "{}" });
  }

  private async putChunkWithRetry(sessionId: string, offset: number, blob: Blob) {
    let attempt = 0;
    for (;;) {
      try {
        return await this.putChunk(sessionId, offset, blob);
      } catch (error) {
        attempt += 1;
        if (attempt > 8) throw error;
        this.emit({
          status: "waiting",
          message: `Bağlantı koptu, yeniden deneniyor (${attempt})…`,
          batchId: null,
          totalBytes: 0,
          sentBytes: offset,
          percent: 0,
          currentFile: ""
        });
        await sleep(Math.min(15000, 500 * 2 ** (attempt - 1)));
      }
    }
  }

  private putChunk(sessionId: string, offset: number, blob: Blob) {
    return new Promise<number>((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open("PUT", `/api/uploads/files/${sessionId}`);
      xhr.withCredentials = true;
      xhr.setRequestHeader("X-CSRF-Token", this.csrf());
      xhr.setRequestHeader("Upload-Offset", String(offset));
      xhr.setRequestHeader("Content-Type", "application/octet-stream");
      xhr.onload = () => {
        if (xhr.status >= 200 && xhr.status < 300) {
          try {
            resolve(JSON.parse(xhr.responseText).offset as number);
          } catch {
            resolve(offset + blob.size);
          }
          return;
        }
        if (xhr.status === 409) {
          const header = xhr.getResponseHeader("Upload-Offset");
          if (header) {
            resolve(Number(header));
            return;
          }
        }
        try {
          reject(new Error(JSON.parse(xhr.responseText).error || "Parça yüklenemedi"));
        } catch {
          reject(new Error("Parça yüklenemedi"));
        }
      };
      xhr.onerror = () => reject(new Error("Sunucu bağlantısı kesildi"));
      xhr.send(blob);
    });
  }

  private emit(partial: Partial<QueueProgress> & Pick<QueueProgress, "status" | "message">) {
    this.onProgress({
      batchId: null,
      totalBytes: 0,
      sentBytes: 0,
      currentFile: "",
      percent: 0,
      ...partial
    });
  }
}

function percent(sent: number, total: number) {
  if (!total) return 0;
  return Math.min(100, Math.round((sent / total) * 100));
}

function sleep(ms: number) {
  return new Promise((resolve) => window.setTimeout(resolve, ms));
}

function formatBytes(value: number) {
  if (!value) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let size = value;
  let index = 0;
  while (size >= 1024 && index < units.length - 1) {
    size /= 1024;
    index++;
  }
  return `${size.toFixed(index === 0 ? 0 : 1)} ${units[index]}`;
}
