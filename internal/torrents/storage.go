package torrents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// Messages recorded when a torrent cannot be given storage. They are shown to
// the administrator, so they are fixed strings and never carry library error
// text (which may include tracker URLs with private passkeys).
const (
	msgUnsafe     = "Torrent içeriği güvenli değil (geçersiz dosya yolu veya ad)."
	msgNameTaken  = "Ortak Paylaşım'da aynı adda bir öğe zaten var."
	msgNoSpace    = "Ortak Paylaşım'da yeterli boş alan yok."
	msgWriteError = "Diske yazılamadı. Disk alanını ve klasör izinlerini kontrol edin."
	msgAddFailed  = "Torrent istemciye eklenemedi."
	msgBadMeta    = "Kayıtlı torrent bilgisi okunamadı."
)

// guardStorage sits between the BitTorrent client and its file storage. The
// client asks it for storage the moment a torrent's file list becomes known,
// which for a magnet link is long after the administrator pressed "add". It is
// therefore the one place every torrent passes through before any byte is
// written, and where the layout is validated and the destination is claimed.
type guardStorage struct {
	dir   string
	inner storage.ClientImpl

	mu sync.Mutex
	// claims maps an info hash to the top-level name it owns in the shared
	// folder. A torrent resuming after a restart already holds its claim.
	claims map[string]string
	// failures holds the reason storage was refused, for the service to
	// surface. Written here, drained by the service's ticker.
	failures map[string]string
}

func newGuardStorage(dir string, inner storage.ClientImpl) *guardStorage {
	return &guardStorage{dir: dir, inner: inner, claims: map[string]string{}, failures: map[string]string{}}
}

// OpenTorrent is called by the client with its own lock held: it must not call
// back into the client, and it only touches the guard's private state.
func (g *guardStorage) OpenTorrent(ctx context.Context, info *metainfo.Info, infoHash metainfo.Hash) (storage.TorrentImpl, error) {
	hash := infoHash.HexString()
	top, _, err := validateInfo(info)
	if err != nil {
		g.fail(hash, msgUnsafe)
		return storage.TorrentImpl{}, err
	}
	if err := g.claim(hash, top); err != nil {
		g.fail(hash, msgNameTaken)
		return storage.TorrentImpl{}, err
	}
	return g.inner.OpenTorrent(ctx, info, infoHash)
}

var errNameTaken = errors.New("torrents: destination name is already in use")

// claim reserves top for hash. A new torrent may only take a name nothing else
// uses: the client would otherwise adopt an existing file of that name as
// already-downloaded data, or overwrite it piece by piece.
func (g *guardStorage) claim(hash, top string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if owned, ok := g.claims[hash]; ok {
		if owned != top {
			return errNameTaken
		}
		return nil
	}
	for other, name := range g.claims {
		if other != hash && strings.EqualFold(name, top) {
			return errNameTaken
		}
	}
	if _, err := os.Lstat(filepath.Join(g.dir, top)); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errNameTaken
	}
	// A single-file download is written as "<name>.part" until it completes.
	if _, err := os.Lstat(filepath.Join(g.dir, top+".part")); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errNameTaken
	}
	g.claims[hash] = top
	return nil
}

// restore re-establishes a claim persisted before a restart.
func (g *guardStorage) restore(hash, top string) {
	if top == "" {
		return
	}
	g.mu.Lock()
	g.claims[hash] = top
	g.mu.Unlock()
}

func (g *guardStorage) release(hash string) {
	g.mu.Lock()
	delete(g.claims, hash)
	delete(g.failures, hash)
	g.mu.Unlock()
}

func (g *guardStorage) claimOf(hash string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.claims[hash]
}

func (g *guardStorage) fail(hash, message string) {
	g.mu.Lock()
	if _, exists := g.failures[hash]; !exists {
		g.failures[hash] = message
	}
	g.mu.Unlock()
}

func (g *guardStorage) takeFailure(hash string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	message := g.failures[hash]
	delete(g.failures, hash)
	return message
}

// busy reports whether rel (a canonical shared-space path) overlaps the
// content of a torrent that still owns a claim.
func (g *guardStorage) busy(rel string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	first, _, _ := strings.Cut(rel, "/")
	for _, name := range g.claims {
		if rel == "" || first == name || first == name+".part" {
			return true
		}
	}
	return false
}
