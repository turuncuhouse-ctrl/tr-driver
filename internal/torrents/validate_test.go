package torrents

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

func multiFile(name string, paths ...[]string) *metainfo.Info {
	info := &metainfo.Info{Name: name, PieceLength: 16 << 10, Pieces: make([]byte, 20)}
	for _, path := range paths {
		info.Files = append(info.Files, metainfo.FileInfo{Path: path, Length: 10})
	}
	return info
}

func TestValidateInfoRejectsPathTraversal(t *testing.T) {
	unsafe := map[string]*metainfo.Info{
		"parent in file path":    multiFile("paket", []string{"..", "kacak.txt"}),
		"deep parent":            multiFile("paket", []string{"a", "..", "..", "..", "etc", "passwd"}),
		"separator in component": multiFile("paket", []string{"../../kacak.txt"}),
		"backslash traversal":    multiFile("paket", []string{`..\..\kacak.txt`}),
		"absolute component":     multiFile("paket", []string{"/etc/passwd"}),
		"dot component":          multiFile("paket", []string{".", "x"}),
		"empty component":        multiFile("paket", []string{"a", "", "b"}),
		"empty path":             multiFile("paket", []string{}),
		"NUL in component":       multiFile("paket", []string{"a\x00b"}),
		"name is parent":         multiFile("..", []string{"x"}),
		"name has separator":     multiFile("a/b", []string{"x"}),
		"name has backslash":     multiFile(`a\b`, []string{"x"}),
		"name is absolute":       multiFile("/etc", []string{"x"}),
		"no name":                multiFile("", []string{"x"}),
		"sentinel no-name":       multiFile(metainfo.NoName, []string{"x"}),
		"reserved directory":     multiFile(".trdriver", []string{"tmp", "x"}),
		"reserved, other case":   multiFile(".TrDriver", []string{"x"}),
		"no files":               {Name: "bos", PieceLength: 16 << 10},
		"negative length":        {Name: "eksi", PieceLength: 16 << 10, Length: -5},
		"single file traversal":  {Name: "../kacak.txt", PieceLength: 16 << 10, Length: 10},
		"utf8 name traversal":    {Name: "masum", NameUtf8: "../kacak", PieceLength: 16 << 10, Length: 10},
	}
	unsafe["utf8 path traversal"] = &metainfo.Info{Name: "paket", PieceLength: 16 << 10, Files: []metainfo.FileInfo{
		{Path: []string{"masum.txt"}, PathUtf8: []string{"..", "kacak.txt"}, Length: 10},
	}}
	for label, info := range unsafe {
		if top, _, err := validateInfo(info); !errors.Is(err, ErrUnsafeTorrent) {
			t.Errorf("%s: validateInfo = %q, %v; want ErrUnsafeTorrent", label, top, err)
		}
	}
	if _, _, err := validateInfo(nil); !errors.Is(err, ErrUnsafeTorrent) {
		t.Errorf("nil info: %v", err)
	}

	top, total, err := validateInfo(multiFile("Belgesel 2024", []string{"Sezon 1", "bölüm 1.mkv"}, []string{"oku..beni.txt"}))
	if err != nil || top != "Belgesel 2024" || total != 20 {
		t.Fatalf("valid multi-file torrent: %q, %d, %v", top, total, err)
	}
	top, total, err = validateInfo(&metainfo.Info{Name: "tek.iso", PieceLength: 16 << 10, Length: 42})
	if err != nil || top != "tek.iso" || total != 42 {
		t.Fatalf("valid single-file torrent: %q, %d, %v", top, total, err)
	}
}

// The guard is the last stop before the client writes anything; a hostile
// layout must leave no trace on disk, inside or outside the shared folder.
func TestGuardStorageRefusesUnsafeLayouts(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "paylasim")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	guard := newGuardStorage(dir, storage.NewFileOpts(storage.NewFileClientOpts{ClientBaseDir: dir}))
	var hash metainfo.Hash
	hash[0] = 1

	// Zero-length files are created eagerly by the storage layer, which makes
	// them the most direct way to write outside the folder.
	hostile := &metainfo.Info{Name: "paket", PieceLength: 16 << 10, Pieces: make([]byte, 20), Files: []metainfo.FileInfo{
		{Path: []string{"..", "..", "kacak.txt"}, Length: 0},
		{Path: []string{"icerik.bin"}, Length: 10},
	}}
	if _, err := guard.OpenTorrent(context.Background(), hostile, hash); err == nil {
		t.Fatal("guard accepted a torrent with a traversal path")
	}
	if got := guard.takeFailure(hash.HexString()); got != msgUnsafe {
		t.Fatalf("recorded failure = %q", got)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 1 {
		t.Fatalf("hostile torrent created files outside the shared folder: %v", entries)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("hostile torrent created files: %v", entries)
	}

	// An existing entry is never adopted or overwritten.
	if err := os.WriteFile(filepath.Join(dir, "rapor.pdf"), []byte("kullanicinin dosyasi"), 0o644); err != nil {
		t.Fatal(err)
	}
	clash := &metainfo.Info{Name: "rapor.pdf", PieceLength: 16 << 10, Pieces: make([]byte, 20), Length: 20}
	if _, err := guard.OpenTorrent(context.Background(), clash, hash); err == nil {
		t.Fatal("guard let a torrent take over an existing file")
	}
	if got := guard.takeFailure(hash.HexString()); got != msgNameTaken {
		t.Fatalf("recorded failure = %q", got)
	}

	// A free name is claimed; a second torrent cannot claim the same one.
	good := &metainfo.Info{Name: "Yeni", PieceLength: 16 << 10, Pieces: make([]byte, 20), Files: []metainfo.FileInfo{{Path: []string{"a.bin"}, Length: 10}}}
	if _, err := guard.OpenTorrent(context.Background(), good, hash); err != nil {
		t.Fatalf("guard refused a safe torrent: %v", err)
	}
	if !guard.busy("Yeni") || !guard.busy("Yeni/a.bin") || guard.busy("Yeniden") || guard.busy("rapor.pdf") {
		t.Fatal("busy() does not match the claimed name")
	}
	var other metainfo.Hash
	other[0] = 2
	if _, err := guard.OpenTorrent(context.Background(), good, other); err == nil {
		t.Fatal("two torrents claimed the same folder")
	}
	// The owner may reopen it (resume after restart).
	if _, err := guard.OpenTorrent(context.Background(), good, hash); err != nil {
		t.Fatalf("owner could not reopen its storage: %v", err)
	}
	guard.release(hash.HexString())
	if guard.busy("Yeni") {
		t.Fatal("released claim still busy")
	}
}

func TestSanitizeTrackersDropsLocalAndUnknownTargets(t *testing.T) {
	tiers := [][]string{
		{"https://tracker.example.com/announce?passkey=abc", "udp://tracker.example.org:6969/announce"},
		{"http://127.0.0.1:8080/announce", "http://localhost/announce", "http://[::1]/a"},
		{"http://10.0.0.5/announce", "http://192.168.1.1/a", "http://172.16.3.4/a", "udp://169.254.169.254:80/a"},
		{"http://postgres:5432/", "http://db.internal/a", "http://nas.local/a", "http://[fd00::1]/a", "http://[fe80::1]/a"},
		{"file:///etc/passwd", "ftp://example.com/x", "wss://tracker.example.com", "gopher://example.com", "", "::::"},
		{"http://0.0.0.0/a", "http://100.64.1.1/a", "http://[::ffff:127.0.0.1]/a", "http://2130706433/a"},
		{"http://8.8.8.8/announce"},
	}
	got := sanitizeTrackers(tiers)
	want := [][]string{
		{"https://tracker.example.com/announce?passkey=abc", "udp://tracker.example.org:6969/announce"},
		{"http://8.8.8.8/announce"},
	}
	if len(got) != len(want) {
		t.Fatalf("sanitizeTrackers = %v, want %v", got, want)
	}
	for i := range want {
		if len(got[i]) != len(want[i]) {
			t.Fatalf("tier %d = %v, want %v", i, got[i], want[i])
		}
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Fatalf("tier %d = %v, want %v", i, got[i], want[i])
			}
		}
	}
}

func TestPublicAddr(t *testing.T) {
	for _, addr := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111", "93.184.216.34"} {
		if !publicAddr(netip.MustParseAddr(addr)) {
			t.Errorf("%s should be public", addr)
		}
	}
	for _, addr := range []string{
		"127.0.0.1", "10.1.2.3", "172.31.255.255", "192.168.0.1", "169.254.169.254", "100.64.0.1", "0.0.0.0",
		"::1", "::", "fe80::1", "fd12::1", "::ffff:10.0.0.1", "::ffff:127.0.0.1", "224.0.0.1", "ff02::1", "255.255.255.255", "64:ff9b::a00:1",
	} {
		if publicAddr(netip.MustParseAddr(addr)) {
			t.Errorf("%s should not be public", addr)
		}
	}
}

func TestGuardedDialerRefusesPrivateAddresses(t *testing.T) {
	dial := guardedDialer()
	for _, target := range []string{"127.0.0.1:80", "[::1]:80", "10.0.0.1:80", "169.254.169.254:80", "localhost:80"} {
		conn, err := dial(context.Background(), "tcp", target)
		if err == nil {
			conn.Close()
			t.Errorf("dialled %s", target)
		}
	}
}

func TestHardenSpecStripsServerSideRequestTargets(t *testing.T) {
	info := metainfo.Info{Name: "x", PieceLength: 16 << 10, Length: 10, Pieces: make([]byte, 20)}
	mi := &metainfo.MetaInfo{
		InfoBytes:    bencode.MustMarshal(info),
		Announce:     "http://127.0.0.1/announce",
		AnnounceList: [][]string{{"https://tracker.example.com/a"}, {"http://192.168.1.10/a"}},
		UrlList:      []string{"http://169.254.169.254/latest/meta-data/"},
		Nodes:        []metainfo.Node{"10.0.0.1:6881"},
	}
	spec, err := specForMetaInfo(mi)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Webseeds) != 0 || len(spec.Sources) != 0 || len(spec.PeerAddrs) != 0 || len(spec.DhtNodes) != 0 {
		t.Fatalf("spec still carries request targets: %+v", spec)
	}
	if len(spec.Trackers) != 1 || spec.Trackers[0][0] != "https://tracker.example.com/a" {
		t.Fatalf("trackers = %v", spec.Trackers)
	}
}
