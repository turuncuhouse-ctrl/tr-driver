# Changelog

## 0.4.0 — TR Driver, lisans, güvenlik

### Added
- Ürün markası: **TR Driver**
- Kullanıcı koltuğu lisansları (`instance_license`, Ed25519 anahtar `TRD1.…`)
  - 1 / 10 / 50 / sınırsız — 99 / 499 / 1499 / 2999 TL / yıl
- Admin lisans etkinleştirme UI + `GET/POST /api/admin/license`, public `GET /api/license`
- Merkezi güncelleme: `GET /api/updates/check` + `UPDATE_MANIFEST_URL`
- `cmd/trdriver-licensegen`
- CONTRIBUTING.md, SECURITY.md, update manifest örneği

### Security
- Public share IDOR (fileId / parentId ağaç sınırı)
- Share browse XSS (innerHTML → textContent / createElement)
- Şifreli share meta sızıntısı azaltıldı; indirme limiti atomik; sabit zamanlı parola karşılaştırması
- Compose dosyalarından hardcode secret kaldırıldı; production placeholder kontrolü
- `content_manager` drive üyelik/silme yükseltemez (`ActionAdminDrive`)
- Device bearer ile admin API engellendi
- İlk admin ataması advisory lock; kayıt koltuk limiti + `ALLOW_REGISTRATION`
- Temel güvenlik başlıkları

## 0.3.0 — Collaboration (Google Drive / OneDrive parity core)

### Added
- Shared Drives (`drives`, `drive_members`) with roles: viewer, commenter, contributor, content_manager, manager
- Central access control (`internal/access`) for personal ownership, drive membership, and inherited ACL
- User-to-user file/folder permissions (`file_permissions`) and Shared with me
- Enhanced public share links: folder browse, password page, expiry, max downloads, list/revoke
- Stars, recent files, server-side name search
- File version snapshots + restore, comments, activity feed, in-app notifications
- Devices settings UI; trash UI; share modal; collab sidebar in web client
- Sync snapshot/changes include shared drive memberships
- MIT LICENSE

### Notes
- Shared drive storage counts against the drive owner’s user quota
- Billing gateway / OAuth / MFA / real-time co-edit intentionally out of scope
