#!/bin/sh
# NecipDrive tek komut kurulum
# Kullanim: cd /mnt/1tb_disk/necipdrive && sh deploy/install.sh

set -e

PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$PROJECT_DIR"

echo "Proje dizini: $PROJECT_DIR"

if [ ! -f Dockerfile ]; then
	echo "HATA: Dockerfile bulunamadi. Yanlis dizindesin."
	exit 1
fi

echo "1/4 Klasorler olusturuluyor..."
mkdir -p "$PROJECT_DIR/data" "$PROJECT_DIR/postgres"
chown -R 1000:1000 "$PROJECT_DIR/data" 2>/dev/null || true

echo "2/4 Image derleniyor (3-8 dakika surebilir)..."
docker build -t necipdrive:latest .

echo "3/4 Stack baslatiliyor..."
docker compose -f docker-compose.stack.yml up -d

echo "4/4 Saglik kontrolu..."
i=0
while [ $i -lt 30 ]; do
	if wget -qO- http://127.0.0.1:3080/healthz >/dev/null 2>&1; then
		echo "BASARILI: http://127.0.0.1:3080 ayakta"
		echo "Simdi Nginx Proxy Manager'da su yonlendirmeyi olustur:"
		echo "  drive.neciparmagan.net.tr  ->  http://SUNUCU_IP:3080  (SSL: Let's Encrypt)"
		exit 0
	fi
	i=$((i + 1))
	sleep 3
done

echo "UYARI: Saglik kontrolu zaman asimina ugradi. Loglara bak:"
echo "  docker compose -f docker-compose.stack.yml logs -f app"
exit 1
