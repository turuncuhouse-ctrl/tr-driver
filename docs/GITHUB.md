# GitHub’a ilk push (tr-driver)

Bu makinede `git` / `gh` PATH’te yoksa önce kurun.

## 1) Kurulum (Windows)

```powershell
winget install --id Git.Git -e --source winget
winget install --id GitHub.cli -e --source winget
# Yeni terminal açın
gh auth login
```

## 2) Repo

```powershell
cd C:\Users\necip\Documents\necipdrive
git init
git add .
git commit -m "Initial public release: TR Driver self-host cloud with seat licenses"
gh repo create tr-driver --public --source=. --remote=origin --push
```

Alternatif (web’den boş repo oluşturup):

```powershell
git remote add origin https://github.com/<KULLANICI>/tr-driver.git
git branch -M main
git push -u origin main
```

## 3) Push öncesi kontrol

- `.env` ve gerçek secret yok (`.gitignore` içinde `.env`)
- `LICENSE_PRIVATE_KEY` asla commit edilmesin
- `data/`, `web/node_modules/` ignore

## 4) VPS’i Git’e bağlama

```bash
cd /mnt/1tb_disk/necipdrive
git remote add origin https://github.com/<KULLANICI>/tr-driver.git
git fetch origin
git checkout -B main origin/main
# veya ilk sefer clone’u yeni dizine alıp data/postgres volume yollarını koruyun
```
