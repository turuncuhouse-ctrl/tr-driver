package torrents

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/anacrolix/generics"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"golang.org/x/time/rate"
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

// Per-torrent speed limiters: burst is one second of the configured rate,
// bounded so that a very small limit does not hand out megabytes at once and
// a large one still wakes connections quickly.
const (
	minItemBurst = 64 << 10
	maxItemBurst = 4 << 20
)

func itemLimiter(kbps int64) *rate.Limiter {
	if kbps <= 0 {
		return rate.NewLimiter(rate.Inf, 1)
	}
	bytesPerSec := kbps * 1024
	burst := int64(max(minItemBurst, min(bytesPerSec, maxItemBurst)))
	return rate.NewLimiter(rate.Limit(bytesPerSec), int(burst))
}

// waitRate blocks until the limiter has accounted for n bytes. A cancelled
// context stops the wait (the caller then performs the I/O anyway: refusing to
// write would lose data during shutdown, and refusing to read would fail an
// upload mid-close).
func waitRate(ctx context.Context, lim *rate.Limiter, n int) {
	if lim == nil || lim.Limit() == rate.Inf || n <= 0 {
		return
	}
	for n > 0 {
		step := n
		if burst := lim.Burst(); burst > 0 && step > burst {
			step = burst
		}
		if err := lim.WaitN(ctx, step); err != nil {
			return
		}
		n -= step
	}
}

// guardStorage sits between the BitTorrent client and its file storage. The
// client asks it for storage the moment a torrent's file list becomes known,
// which for a magnet link is long after the administrator pressed "add". It is
// therefore the one place every torrent passes through before any byte is
// written, and where the layout is validated and the destination is claimed.
//
// It also wraps every piece with the torrent's own speed limits: writes carry
// downloaded data (throttled on purpose), reads serve uploads to peers, while
// piece hashing and re-checks use WriteTo and stay unthrottled so verification
// never drags behind an upload limit.
type guardStorage struct {
	dir   string
	inner storage.ClientImpl
	// ctx cancels limiter waits when the client shuts down.
	ctx context.Context

	mu sync.Mutex
	// claims maps an info hash to the top-level name it owns in the shared
	// folder. A torrent resuming after a restart already holds its claim.
	claims map[string]string
	// failures holds the reason storage was refused, for the service to
	// surface. Written here, drained by the service's ticker.
	failures map[string]string
	// itemLimits holds the per-torrent speed limits, keyed by info hash.
	itemLimits map[string][2]*rate.Limiter // [download, upload]
}

func newGuardStorage(dir string, inner storage.ClientImpl) *guardStorage {
	return &guardStorage{
		dir:        dir,
		inner:      inner,
		ctx:        context.Background(),
		claims:     map[string]string{},
		failures:   map[string]string{},
		itemLimits: map[string][2]*rate.Limiter{},
	}
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
	torrentStorage, err := g.inner.OpenTorrent(ctx, info, infoHash)
	if err != nil {
		return storage.TorrentImpl{}, err
	}
	g.wrap(torrentStorage, hash)
	return torrentStorage, nil
}

// wrap installs the per-torrent speed limiters on a storage handle.
func (g *guardStorage) wrap(t storage.TorrentImpl, hash string) {
	if t.Piece != nil {
		inner := t.Piece
		t.Piece = func(p metainfo.Piece) storage.PieceImpl {
			return &limitedPiece{PieceImpl: inner(p), g: g, hash: hash, mip: p}
		}
	}
	if t.PieceWithHash != nil {
		inner := t.PieceWithHash
		t.PieceWithHash = func(p metainfo.Piece, h generics.Option[[]byte]) storage.PieceImpl {
			return &limitedPiece{PieceImpl: inner(p, h), g: g, hash: hash, mip: p}
		}
	}
}

// limitedPiece applies the torrent's own download/upload limits. Downloaded
// bytes arrive through WriteAt; bytes read for upload go through ReadAt.
// Hashing and re-checks go through WriteTo, which delegates without a wait so
// verification speed never depends on an upload limit.
type limitedPiece struct {
	storage.PieceImpl
	g    *guardStorage
	hash string
	mip  metainfo.Piece
}

func (p *limitedPiece) WriteAt(b []byte, off int64) (int, error) {
	waitRate(p.g.ctx, p.g.rateFor(p.hash, true), len(b))
	return p.PieceImpl.WriteAt(b, off)
}

func (p *limitedPiece) ReadAt(b []byte, off int64) (int, error) {
	waitRate(p.g.ctx, p.g.rateFor(p.hash, false), len(b))
	return p.PieceImpl.ReadAt(b, off)
}

func (p *limitedPiece) WriteTo(w io.Writer) (int64, error) {
	if inner, ok := p.PieceImpl.(io.WriterTo); ok {
		return inner.WriteTo(w)
	}
	return io.CopyN(w, io.NewSectionReader(p.PieceImpl, 0, p.mip.Length()), p.mip.Length())
}

// setItemLimits stores the per-torrent limits; kbps <= 0 means unlimited.
func (g *guardStorage) setItemLimits(hash string, downKBps, upKBps int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if downKBps <= 0 && upKBps <= 0 {
		delete(g.itemLimits, hash)
		return
	}
	g.itemLimits[hash] = [2]*rate.Limiter{itemLimiter(downKBps), itemLimiter(upKBps)}
}

func (g *guardStorage) rateFor(hash string, download bool) *rate.Limiter {
	g.mu.Lock()
	defer g.mu.Unlock()
	limits, ok := g.itemLimits[hash]
	if !ok {
		return nil
	}
	if download {
		return limits[0]
	}
	return limits[1]
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

// restore re-establishes a claim persisted before a restart or held by a
// detached torrent whose files are still on disk. It reports whether the name
// was free for this torrent: another torrent may have claimed it meanwhile.
func (g *guardStorage) restore(hash, top string) bool {
	if top == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for other, name := range g.claims {
		if other != hash && strings.EqualFold(name, top) {
			return false
		}
	}
	g.claims[hash] = top
	return true
}

func (g *guardStorage) release(hash string) {
	g.mu.Lock()
	delete(g.claims, hash)
	delete(g.failures, hash)
	delete(g.itemLimits, hash)
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
