package torrents

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"golang.org/x/time/rate"
)

const downloadTimeout = 90 * time.Second

func TestOnlyAdministratorsControlTorrentsAtServiceLevel(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	source := newSeeder(t)
	data, hash := source.share(t, "dosya.bin", map[string][]byte{"": randomBytes(t, 1000)})

	for _, actor := range []Actor{regular, {ID: "", Role: ""}, {ID: "x", Role: "Admin"}, {ID: "x", Role: "administrator"}} {
		if _, err := env.service.AddFile(ctx, actor, data); !isErr(err, ErrForbidden) {
			t.Errorf("AddFile as %+v: %v, want ErrForbidden", actor, err)
		}
		if _, err := env.service.AddMagnet(ctx, actor, magnetFor(hash, "dosya.bin")); !isErr(err, ErrForbidden) {
			t.Errorf("AddMagnet as %+v: %v, want ErrForbidden", actor, err)
		}
		if _, err := env.service.Overview(ctx, actor); !isErr(err, ErrForbidden) {
			t.Errorf("Overview as %+v: %v, want ErrForbidden", actor, err)
		}
		if _, err := env.service.SetLimits(ctx, actor, Limits{DownloadLimitKBps: 1}); !isErr(err, ErrForbidden) {
			t.Errorf("SetLimits as %+v: %v, want ErrForbidden", actor, err)
		}
	}
	view, err := env.service.AddFile(ctx, admin, data)
	if err != nil {
		t.Fatalf("AddFile as admin: %v", err)
	}
	if _, err := env.service.Pause(ctx, regular, view.ID); !isErr(err, ErrForbidden) {
		t.Errorf("Pause as user: %v", err)
	}
	if _, err := env.service.Resume(ctx, regular, view.ID); !isErr(err, ErrForbidden) {
		t.Errorf("Resume as user: %v", err)
	}
	if err := env.service.Remove(ctx, regular, view.ID, true); !isErr(err, ErrForbidden) {
		t.Errorf("Remove as user: %v", err)
	}
	if _, ok := env.view(view.ID); !ok {
		t.Fatal("a non-admin removed a torrent")
	}
	if got := env.service.limits; got != (Limits{}) {
		t.Fatalf("a non-admin changed the limits: %+v", got)
	}
}

func TestTorrentFileDownloadsIntoSharedFolder(t *testing.T) {
	env := newTestEnv(t)
	source := newSeeder(t)
	files := map[string][]byte{
		"Sezon 1/bölüm 1.mkv": randomBytes(t, 300_000),
		"Sezon 1/bölüm 2.mkv": randomBytes(t, 150_000),
		"oku beni.txt":        []byte("merhaba"),
	}
	data, hash := source.share(t, "Belgesel", files)

	view, err := env.service.AddFile(context.Background(), admin, data)
	if err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	if view.InfoHash != hash || view.Name != "Belgesel" || view.TotalBytes != 450_007 {
		t.Fatalf("view = %+v", view)
	}
	env.connect(view.ID, source)
	// While it downloads, the folder belongs to the torrent.
	if !env.service.Busy("Belgesel") || !env.service.Busy("Belgesel/oku beni.txt") || env.service.Busy("Baska") {
		t.Fatal("Busy() does not cover the active torrent's folder")
	}

	done := env.waitStatus(view.ID, downloadTimeout, StatusCompleted)
	if done.Progress != 1 || done.CompletedBytes != done.TotalBytes || done.CompletedAt == nil || done.Path != "Belgesel" {
		t.Fatalf("completed view = %+v", done)
	}
	for rel, want := range files {
		got, err := env.sharedFile("Belgesel/" + rel)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: content mismatch (%v)", rel, err)
		}
	}
	for _, entry := range env.sharedEntries() {
		if strings.HasSuffix(entry, ".part") || !strings.HasPrefix(entry, "Belgesel") {
			t.Fatalf("unexpected entry in the shared folder: %s", entry)
		}
	}
	// Seeding is off by default: the torrent left the client and released the folder.
	if env.service.Busy("Belgesel") {
		t.Fatal("completed torrent still holds its folder")
	}
	if rec := env.store.record(view.ID); rec.State != StateCompleted || rec.ContentPath != "Belgesel" {
		t.Fatalf("stored record = %+v", rec)
	}
	if env.store.noticeCount() != 1 {
		t.Fatalf("completion notifications = %d, want 1", env.store.noticeCount())
	}

	// Removing with "delete data" goes through the shared space.
	if err := env.service.Remove(context.Background(), admin, view.ID, true); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if entries := env.sharedEntries(); len(entries) != 0 {
		t.Fatalf("data left behind: %v", entries)
	}
	if _, ok := env.view(view.ID); ok {
		t.Fatal("torrent still listed after removal")
	}
}

func TestMagnetDownloadsAfterMetadataArrives(t *testing.T) {
	env := newTestEnv(t)
	source := newSeeder(t)
	content := randomBytes(t, 200_000)
	_, hash := source.share(t, "tek-dosya.iso", map[string][]byte{"": content})

	view, err := env.service.AddMagnet(context.Background(), admin, magnetFor(hash, "tek dosya"))
	if err != nil {
		t.Fatalf("AddMagnet: %v", err)
	}
	if view.Status != StatusMetadata || view.Name != "tek dosya" || view.TotalBytes != 0 {
		t.Fatalf("fresh magnet view = %+v", view)
	}
	env.connect(view.ID, source)
	done := env.waitStatus(view.ID, downloadTimeout, StatusCompleted)
	if done.Name != "tek-dosya.iso" || done.TotalBytes != int64(len(content)) {
		t.Fatalf("completed view = %+v", done)
	}
	got, err := env.sharedFile("tek-dosya.iso")
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("downloaded content mismatch (%v)", err)
	}
	// The metadata was kept so a restart would not need to fetch it again.
	if rec := env.store.record(view.ID); len(rec.Metainfo) == 0 {
		t.Fatal("metadata was not persisted")
	}
}

func TestMagnetCannotTakeOverAnExistingName(t *testing.T) {
	env := newTestEnv(t)
	source := newSeeder(t)
	_, hash := source.share(t, "rapor.pdf", map[string][]byte{"": randomBytes(t, 50_000)})
	existing := filepath.Join(env.space.dir, "rapor.pdf")
	if err := os.WriteFile(existing, []byte("kullanicinin kendi dosyasi"), 0o644); err != nil {
		t.Fatal(err)
	}

	view, err := env.service.AddMagnet(context.Background(), admin, magnetFor(hash, "rapor"))
	if err != nil {
		t.Fatalf("AddMagnet: %v", err)
	}
	env.connect(view.ID, source)
	failed := env.waitStatus(view.ID, downloadTimeout, StatusError)
	if failed.Message != msgNameTaken {
		t.Fatalf("error message = %q", failed.Message)
	}
	if got, _ := os.ReadFile(existing); string(got) != "kullanicinin kendi dosyasi" {
		t.Fatalf("existing file was modified: %q", got)
	}
	if entries := env.sharedEntries(); len(entries) != 1 {
		t.Fatalf("unexpected entries: %v", entries)
	}
}

func TestTorrentFileIsCheckedBeforeAnythingIsWritten(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	source := newSeeder(t)

	// Traversal in the file list.
	hostile := metainfo.Info{Name: "paket", PieceLength: 16 << 10, Pieces: make([]byte, 20), Files: []metainfo.FileInfo{
		{Path: []string{"..", "..", "kacak.txt"}, Length: 0},
		{Path: []string{"icerik.bin"}, Length: 10},
	}}
	var buf bytes.Buffer
	if err := (&metainfo.MetaInfo{InfoBytes: bencode.MustMarshal(hostile)}).Write(&buf); err != nil {
		t.Fatal(err)
	}
	if _, err := env.service.AddFile(ctx, admin, buf.Bytes()); !isErr(err, ErrUnsafeTorrent) {
		t.Fatalf("hostile torrent: %v, want ErrUnsafeTorrent", err)
	}
	// Not a torrent at all.
	for _, junk := range [][]byte{nil, []byte("merhaba"), []byte("d4:infoi5ee"), bytes.Repeat([]byte("x"), MaxMetainfoBytes+1)} {
		if _, err := env.service.AddFile(ctx, admin, junk); !isErr(err, ErrInvalid) {
			t.Fatalf("junk upload (%d bytes): %v, want ErrInvalid", len(junk), err)
		}
	}
	for _, junk := range []string{"", "http://example.com/x.torrent", "magnet:?dn=adsiz", "magnet:?xt=urn:btih:kisa", strings.Repeat("magnet:?", MaxMagnetBytes)} {
		if _, err := env.service.AddMagnet(ctx, admin, junk); !isErr(err, ErrInvalid) {
			t.Fatalf("junk magnet %.40q: %v, want ErrInvalid", junk, err)
		}
	}

	// An existing name is refused up front.
	data, _ := source.share(t, "Mevcut", map[string][]byte{"a.bin": randomBytes(t, 1000)})
	if err := os.Mkdir(filepath.Join(env.space.dir, "Mevcut"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := env.service.AddFile(ctx, admin, data); !isErr(err, ErrNameTaken) {
		t.Fatalf("name clash: %v, want ErrNameTaken", err)
	}

	// Not enough room.
	big, _ := source.share(t, "Buyuk", map[string][]byte{"a.bin": randomBytes(t, 100_000)})
	env.space.setFree(99_999)
	if _, err := env.service.AddFile(ctx, admin, big); !isErr(err, ErrNoSpace) {
		t.Fatalf("too large for the free space: %v, want ErrNoSpace", err)
	}
	env.space.setFree(0)
	if _, err := env.service.AddMagnet(ctx, admin, magnetFor(strings.Repeat("ab", 20), "x")); !isErr(err, ErrNoSpace) {
		t.Fatalf("full shared space: %v, want ErrNoSpace", err)
	}
	env.space.setFree(1 << 30)

	// The same torrent twice.
	if _, err := env.service.AddFile(ctx, admin, big); err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	if _, err := env.service.AddFile(ctx, admin, big); !isErr(err, ErrDuplicate) {
		t.Fatalf("duplicate: %v, want ErrDuplicate", err)
	}

	overview, err := env.service.Overview(ctx, admin)
	if err != nil || len(overview.Torrents) != 1 {
		t.Fatalf("overview = %+v, %v", overview, err)
	}
	if stored, _ := env.store.List(ctx); len(stored) != 1 {
		t.Fatalf("rejected torrents were stored: %d records", len(stored))
	}
	// Only the shared folder and the piece-completion state directory may
	// exist; rejected torrents must not have created any file.
	entries, _ := os.ReadDir(env.root)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if want := []string{"paylasim", "torrent-state"}; !slices.Equal(names, want) {
		t.Fatalf("something was written outside the shared folder: %v, want %v", names, want)
	}
}

func TestMagnetThatDoesNotFitIsStoppedAndCleanedUp(t *testing.T) {
	env := newTestEnv(t)
	source := newSeeder(t)
	_, hash := source.share(t, "Kocaman", map[string][]byte{"a.bin": randomBytes(t, 200_000), "bos.txt": {}})
	env.space.setFree(100_000) // known only once the metadata arrives

	view, err := env.service.AddMagnet(context.Background(), admin, magnetFor(hash, "Kocaman"))
	if err != nil {
		t.Fatalf("AddMagnet: %v", err)
	}
	env.connect(view.ID, source)
	failed := env.waitStatus(view.ID, downloadTimeout, StatusError)
	if failed.Message != msgNoSpace {
		t.Fatalf("error message = %q", failed.Message)
	}
	if entries := env.sharedEntries(); len(entries) != 0 {
		t.Fatalf("rejected download left files behind: %v", entries)
	}
	if env.service.Busy("Kocaman") {
		t.Fatal("rejected download still holds its name")
	}
}

func TestPauseResumeAndLiveSpeedLimit(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	source := newSeeder(t)
	content := randomBytes(t, 3<<20)
	data, _ := source.share(t, "yavas.bin", map[string][]byte{"": content})

	// A low limit keeps the download running long enough to observe it.
	if _, err := env.service.SetLimits(ctx, admin, Limits{DownloadLimitKBps: 64}); err != nil {
		t.Fatal(err)
	}
	view, err := env.service.AddFile(ctx, admin, data)
	if err != nil {
		t.Fatal(err)
	}
	env.connect(view.ID, source)

	deadline := time.Now().Add(downloadTimeout)
	for {
		current, _ := env.view(view.ID)
		if current.CompletedBytes > 0 {
			if current.Status != StatusDownloading {
				t.Fatalf("status while downloading = %q", current.Status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("download never started")
		}
		time.Sleep(25 * time.Millisecond)
	}
	// At 64 KiB/s (plus the initial burst) 3 MiB cannot be done in two seconds.
	time.Sleep(2 * time.Second)
	if current, _ := env.view(view.ID); current.Status != StatusDownloading || current.CompletedBytes >= int64(len(content)) {
		t.Fatalf("the speed limit was not applied: %+v", current)
	}

	paused, err := env.service.Pause(ctx, admin, view.ID)
	if err != nil || paused.Status != StatusPaused {
		t.Fatalf("Pause = %+v, %v", paused, err)
	}
	if _, err := env.service.Pause(ctx, admin, view.ID); !isErr(err, ErrInvalidState) {
		t.Fatalf("pausing twice: %v, want ErrInvalidState", err)
	}
	// Paused means off the wire: the file on disk stops changing at once.
	partial := filepath.Join(env.space.dir, "yavas.bin.part")
	before, err := os.ReadFile(partial)
	if err != nil {
		t.Fatalf("partial file: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	after, err := os.ReadFile(partial)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("download continued while paused (%v)", err)
	}
	if current, _ := env.view(view.ID); current.Status != StatusPaused || current.DownloadRate != 0 || current.Peers != 0 {
		t.Fatalf("paused view = %+v", current)
	}
	// The partial data stays reserved for the torrent.
	if !env.service.Busy("yavas.bin") || !env.service.Busy("yavas.bin.part") {
		t.Fatal("paused torrent gave up its claim on the partial data")
	}
	if rec := env.store.record(view.ID); rec.State != StatePaused {
		t.Fatalf("paused state not stored: %q", rec.State)
	}

	// Lifting the limit takes effect on the running client.
	if _, err := env.service.SetLimits(ctx, admin, Limits{}); err != nil {
		t.Fatal(err)
	}
	resumed, err := env.service.Resume(ctx, admin, view.ID)
	if err != nil || resumed.Status != StatusDownloading {
		t.Fatalf("Resume = %+v, %v", resumed, err)
	}
	if resumed.CompletedBytes == 0 {
		t.Fatal("progress was lost by pausing")
	}
	env.connect(view.ID, source)
	env.waitStatus(view.ID, downloadTimeout, StatusCompleted)
	if got, err := env.sharedFile("yavas.bin"); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("content mismatch after pause/resume (%v)", err)
	}
	if _, err := env.service.Pause(ctx, admin, view.ID); !isErr(err, ErrInvalidState) {
		t.Fatalf("pausing a completed torrent: %v", err)
	}
	if _, err := env.service.Resume(ctx, admin, "yok"); !isErr(err, ErrNotFound) {
		t.Fatalf("resuming an unknown torrent: %v", err)
	}
}

func TestDownloadStallsWhileTheSharedSpaceIsFull(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	source := newSeeder(t)
	content := randomBytes(t, 2<<20)
	data, _ := source.share(t, "dolu.bin", map[string][]byte{"": content})
	if _, err := env.service.SetLimits(ctx, admin, Limits{DownloadLimitKBps: 64}); err != nil {
		t.Fatal(err)
	}
	view, err := env.service.AddFile(ctx, admin, data)
	if err != nil {
		t.Fatal(err)
	}
	env.connect(view.ID, source)
	env.space.setHardFull(true)
	stalled := env.waitStatus(view.ID, downloadTimeout, StatusStalled)
	if stalled.Message != msgNoSpace {
		t.Fatalf("stalled message = %q", stalled.Message)
	}
	// Space was freed: the download continues on its own.
	env.space.setHardFull(false)
	if _, err := env.service.SetLimits(ctx, admin, Limits{}); err != nil {
		t.Fatal(err)
	}
	env.waitStatus(view.ID, downloadTimeout, StatusCompleted)
}

func TestSpeedLimitsAreValidatedAppliedAndPersisted(t *testing.T) {
	env := newTestEnv(t, func(o *Options) { o.DownloadLimitKBps, o.UploadLimitKBps = 500, 100 })
	ctx := context.Background()
	if env.service.dl.Limit() != rate.Limit(500*1024) || env.service.ul.Limit() != rate.Limit(100*1024) {
		t.Fatalf("environment defaults not applied: %v / %v", env.service.dl.Limit(), env.service.ul.Limit())
	}
	for _, bad := range []Limits{{DownloadLimitKBps: -1}, {UploadLimitKBps: -1}, {DownloadLimitKBps: MaxLimitKBps + 1}, {UploadLimitKBps: MaxLimitKBps + 1}} {
		if _, err := env.service.SetLimits(ctx, admin, bad); !isErr(err, ErrInvalidLimit) {
			t.Errorf("SetLimits(%+v): %v, want ErrInvalidLimit", bad, err)
		}
	}
	if _, err := env.service.SetLimits(ctx, admin, Limits{DownloadLimitKBps: 2048, UploadLimitKBps: 16}); err != nil {
		t.Fatal(err)
	}
	if env.service.dl.Limit() != rate.Limit(2048*1024) || env.service.ul.Limit() != rate.Limit(16*1024) {
		t.Fatalf("limits not applied: %v / %v", env.service.dl.Limit(), env.service.ul.Limit())
	}
	// The upload burst must stay large enough for a whole block, or the
	// client would panic when serving a peer.
	if env.service.ul.Burst() < 1<<20 || env.service.dl.Burst() <= 0 {
		t.Fatalf("bursts = %d / %d", env.service.dl.Burst(), env.service.ul.Burst())
	}
	// Zero means unlimited, never "stopped".
	if _, err := env.service.SetLimits(ctx, admin, Limits{}); err != nil {
		t.Fatal(err)
	}
	if env.service.dl.Limit() != rate.Inf || env.service.ul.Limit() != rate.Inf {
		t.Fatalf("zero did not mean unlimited: %v / %v", env.service.dl.Limit(), env.service.ul.Limit())
	}

	if _, err := env.service.SetLimits(ctx, admin, Limits{DownloadLimitKBps: 777, UploadLimitKBps: 33}); err != nil {
		t.Fatal(err)
	}
	env.service.Close()
	restarted := env.start(func(o *Options) { o.DownloadLimitKBps, o.UploadLimitKBps = 500, 100 })
	overview, err := restarted.Overview(ctx, admin)
	if err != nil || overview.Limits != (Limits{DownloadLimitKBps: 777, UploadLimitKBps: 33}) {
		t.Fatalf("limits after restart = %+v, %v", overview.Limits, err)
	}
	if restarted.dl.Limit() != rate.Limit(777*1024) {
		t.Fatalf("saved limit not applied after restart: %v", restarted.dl.Limit())
	}
}

func TestTorrentsResumeAfterRestart(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	source := newSeeder(t)
	content := randomBytes(t, 400_000)
	data, _ := source.share(t, "devam.bin", map[string][]byte{"": content})
	_, magnetHash := source.share(t, "bekleyen.bin", map[string][]byte{"": randomBytes(t, 1000)})

	active, err := env.service.AddFile(ctx, admin, data)
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := env.service.AddMagnet(ctx, admin, magnetFor(magnetHash, "bekleyen"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.service.Pause(ctx, admin, waiting.ID); err != nil {
		t.Fatal(err)
	}
	env.service.Close()

	env.service = env.start()
	if view, ok := env.view(active.ID); !ok || (view.Status != StatusDownloading && view.Status != StatusMetadata) {
		t.Fatalf("active torrent after restart = %+v (%v)", view, ok)
	}
	if view, ok := env.view(waiting.ID); !ok || view.Status != StatusPaused {
		t.Fatalf("paused torrent after restart = %+v (%v)", view, ok)
	}
	if !env.service.Busy("devam.bin") {
		t.Fatal("restored torrent does not hold its name")
	}
	// The restored torrent finishes, into the files it had already claimed.
	env.connect(active.ID, source)
	env.waitStatus(active.ID, downloadTimeout, StatusCompleted)
	if got, err := env.sharedFile("devam.bin"); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("content mismatch after restart (%v)", err)
	}
	// A paused torrent is not handed to the client until it is resumed.
	env.service.mu.Lock()
	loaded := env.service.items[waiting.ID].handle != nil
	env.service.mu.Unlock()
	if loaded {
		t.Fatal("a paused torrent was started after the restart")
	}
	if _, err := env.sharedFile("bekleyen.bin"); err == nil {
		t.Fatal("a paused torrent downloaded its content")
	}
	if _, err := env.service.Resume(ctx, admin, waiting.ID); err != nil {
		t.Fatal(err)
	}
	env.connect(waiting.ID, source)
	env.waitStatus(waiting.ID, downloadTimeout, StatusCompleted)
}

func TestDisabledClientRefusesWork(t *testing.T) {
	env := newTestEnv(t, func(o *Options) { o.Enabled = false })
	ctx := context.Background()
	overview, err := env.service.Overview(ctx, admin)
	if err != nil || overview.Enabled || overview.Available || overview.Reason == "" {
		t.Fatalf("overview = %+v, %v", overview, err)
	}
	if _, err := env.service.AddMagnet(ctx, admin, magnetFor(strings.Repeat("ab", 20), "x")); !isErr(err, ErrUnavailable) {
		t.Fatalf("AddMagnet on a disabled client: %v", err)
	}
	if env.service.Busy("x") {
		t.Fatal("disabled client reports busy paths")
	}
}

func TestSecretsStayOutOfLogsAndResponses(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })

	env := newTestEnv(t)
	ctx := context.Background()
	source := newSeeder(t)
	content := randomBytes(t, 100_000)
	data, _ := source.share(t, "gizli-izleyici.bin", map[string][]byte{"": content})
	_, magnetHash := source.share(t, "magnet-izleyici.bin", map[string][]byte{"": randomBytes(t, 50_000)})

	fromFile, err := env.service.AddFile(ctx, admin, data)
	if err != nil {
		t.Fatal(err)
	}
	fromMagnet, err := env.service.AddMagnet(ctx, admin, magnetFor(magnetHash, "magnet izleyici"))
	if err != nil {
		t.Fatal(err)
	}
	env.connect(fromFile.ID, source)
	env.connect(fromMagnet.ID, source)
	env.waitStatus(fromFile.ID, downloadTimeout, StatusCompleted)
	env.waitStatus(fromMagnet.ID, downloadTimeout, StatusCompleted)
	// Provoke the error paths too.
	_, _ = env.service.AddMagnet(ctx, admin, "magnet:?xt=urn:btih:bozuk&tr=https://t.example.com/a?passkey="+trackerPasskey)
	_, _ = env.service.AddFile(ctx, admin, data)

	overview, err := env.service.Overview(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(overview)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{trackerPasskey, "magnet:", "tracker.example.com", "announce"} {
		if bytes.Contains(payload, []byte(secret)) {
			t.Fatalf("API response contains %q: %s", secret, payload)
		}
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("log output contains %q:\n%s", secret, logs.String())
		}
	}
	// The passkey is still stored, or resuming a private torrent would break.
	if rec := env.store.record(fromMagnet.ID); !strings.Contains(rec.Magnet, trackerPasskey) {
		t.Fatal("magnet link was not kept for resuming")
	}
}

// Re-adding a torrent that was removed WITHOUT deleting its data must find the
// files again, re-check every piece (this test corrupts one on purpose) and
// continue instead of starting over.
func TestReaddWithoutDataRechecksPiecesAndContinues(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	seeder := newSeeder(t)
	_, hash := seeder.share(t, "resume.bin", map[string][]byte{"": randomBytes(t, 400000)})
	original, err := os.ReadFile(filepath.Join(seeder.dir, "resume.bin"))
	if err != nil {
		t.Fatalf("seeder file: %v", err)
	}

	view, err := env.service.AddMagnet(ctx, admin, magnetFor(hash, ""))
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	env.connect(view.ID, seeder)
	env.waitStatus(view.ID, downloadTimeout, StatusSeeding, StatusCompleted)
	if got, err := env.sharedFile("resume.bin"); err != nil || !slices.Equal(got, original) {
		t.Fatalf("downloaded data wrong (%d bytes, %v)", len(got), err)
	}

	// Remove WITHOUT deleting the data: the row stays hidden, the file stays.
	if err := env.service.Remove(ctx, admin, view.ID, false); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if listed, err := env.service.Overview(ctx, admin); err != nil || len(listed.Torrents) != 0 {
		t.Fatalf("torrent still listed after remove: %d, %v", len(listed.Torrents), err)
	}
	// Corrupt a byte: only a real piece re-check can notice and repair it.
	onDisk, err := env.sharedFile("resume.bin")
	if err != nil {
		t.Fatalf("file vanished with deleteData=false: %v", err)
	}
	onDisk[12345] ^= 0xFF
	target := filepath.Join(env.space.dir, "resume.bin")
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, onDisk, 0o644); err != nil {
		t.Fatal(err)
	}

	// Adding the same magnet again revives the hidden row.
	again, err := env.service.AddMagnet(ctx, admin, magnetFor(hash, ""))
	if err != nil {
		t.Fatalf("re-add: %v", err)
	}
	env.connect(again.ID, seeder)
	if again.ID != view.ID {
		t.Fatalf("re-add created a new row %s, want revival of %s", again.ID, view.ID)
	}
	env.waitStatus(again.ID, downloadTimeout, StatusSeeding, StatusCompleted)
	repaired, err := env.sharedFile("resume.bin")
	if err != nil {
		t.Fatalf("file gone after re-add: %v", err)
	}
	if !slices.Equal(repaired, original) {
		t.Fatal("corrupted piece was not detected and repaired after re-adding")
	}
}

// A completed torrent seeds until its configured seed time limit is reached,
// then stops by itself and stays stopped (also across a restart).
func TestSeedTimeLimitStopsSeedingAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, func(o *Options) { o.SeedAfterComplete = true })
	seeder := newSeeder(t)
	_, hash := seeder.share(t, "seedlimit.bin", map[string][]byte{"": randomBytes(t, 200000)})

	view, err := env.service.AddMagnet(ctx, admin, magnetFor(hash, ""))
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if view, err = env.service.SetItemLimits(ctx, admin, view.ID, ItemLimits{SeedTimeLimitSec: 1}); err != nil {
		t.Fatalf("limits: %v", err)
	}
	env.connect(view.ID, seeder)
	// Seeds first, then the limit stops it.
	env.waitStatus(view.ID, downloadTimeout, StatusSeeding)
	deadline := time.Now().Add(20 * time.Second)
	for {
		current, ok := env.view(view.ID)
		if !ok {
			t.Fatal("torrent disappeared")
		}
		if current.Status == StatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("seed time limit never stopped seeding (status=%s)", current.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}
	stopped := env.store.record(view.ID)
	if !stopped.SeedStopped || stopped.SeededSeconds < 1 {
		t.Fatalf("seed stop not persisted: stopped=%t seeded=%ds", stopped.SeedStopped, stopped.SeededSeconds)
	}
	// Restart: it must NOT start seeding again on its own.
	env.service.Close()
	env.service = env.start(func(o *Options) { o.SeedAfterComplete = true })
	if current, _ := env.view(view.ID); current.Status != StatusCompleted {
		t.Fatalf("after restart status = %s, want completed (not auto-seeding)", current.Status)
	}
}

// Per-file "skip" is persisted, shown in the detail drawer, re-applied after a
// restart, and the torrent still completes once the other files arrived.
func TestFileSkipIsPersistedAndReapplied(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	seeder := newSeeder(t)
	bytesA := randomBytes(t, 90000)
	bytesB := randomBytes(t, 90000)
	bytesC := randomBytes(t, 400000)
	_, hash := seeder.share(t, "multi", map[string][]byte{
		"a.bin": bytesA, "b.bin": bytesB, "c.bin": bytesC,
	})

	view, err := env.service.AddMagnet(ctx, admin, magnetFor(hash, ""))
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	env.connect(view.ID, seeder)
	// Wait until the file list is known, then skip the big file.
	var skipPath string
	deadline := time.Now().Add(20 * time.Second)
	for {
		detail, err := env.service.Detail(ctx, admin, view.ID)
		if err == nil && len(detail.Files) == 3 {
			for _, f := range detail.Files {
				if strings.HasSuffix(f.Path, "c.bin") {
					skipPath = f.Path
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("file list never appeared: files=%d err=%v", len(detail.Files), err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if skipPath == "" {
		t.Fatal("c.bin not found in detail")
	}
	if view, err = env.service.SetFilePriority(ctx, admin, view.ID, skipPath, PrioritySkip); err != nil {
		t.Fatalf("skip: %v", err)
	}
	env.waitStatus(view.ID, downloadTimeout, StatusCompleted, StatusSeeding)

	gotA, errA := env.sharedFile(filepath.Join("multi", "a.bin"))
	gotB, errB := env.sharedFile(filepath.Join("multi", "b.bin"))
	if errA != nil || !slices.Equal(gotA, bytesA) {
		t.Fatalf("a.bin wrong: %v", errA)
	}
	if errB != nil || !slices.Equal(gotB, bytesB) {
		t.Fatalf("b.bin wrong: %v", errB)
	}
	stored := env.store.record(view.ID)
	if !strings.Contains(stored.FilePriorities, skipPath) {
		t.Fatalf("file priority not persisted: %q", stored.FilePriorities)
	}
	detail, err := env.service.Detail(ctx, admin, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range detail.Files {
		if f.Path == skipPath && f.Priority != PrioritySkip {
			t.Fatalf("detail priority = %s, want skip", f.Priority)
		}
	}
	// After a restart the skip must be re-applied, not downloaded fresh.
	env.service.Close()
	env.service = env.start()
	detail, err = env.service.Detail(ctx, admin, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range detail.Files {
		if f.Path == skipPath && f.Priority != PrioritySkip {
			t.Fatalf("priority lost after restart: %s", f.Priority)
		}
	}
}

// Per-torrent speed limits are validated, stored and survive a restart.
func TestItemLimitsAreValidatedAndPersisted(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	seeder := newSeeder(t)
	_, hash := seeder.share(t, "limits.bin", map[string][]byte{"": randomBytes(t, 2000)})

	view, err := env.service.AddMagnet(ctx, admin, magnetFor(hash, ""))
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	env.connect(view.ID, seeder)
	if _, err := env.service.SetItemLimits(ctx, admin, view.ID, ItemLimits{SeedRatioLimit: -1}); !isErr(err, ErrInvalidLimit) {
		t.Fatalf("negative ratio = %v, want ErrInvalidLimit", err)
	}
	if _, err := env.service.SetItemLimits(ctx, admin, view.ID, ItemLimits{DownloadLimitKBps: MaxLimitKBps + 1}); !isErr(err, ErrInvalidLimit) {
		t.Fatalf("huge limit = %v, want ErrInvalidLimit", err)
	}
	want := ItemLimits{DownloadLimitKBps: 512, UploadLimitKBps: 128, SeedRatioLimit: 8, SeedTimeLimitSec: 3600}
	view, err = env.service.SetItemLimits(ctx, admin, view.ID, want)
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if view.DownloadLimitKBps != 512 || view.UploadLimitKBps != 128 || view.SeedRatioLimit != 8 || view.SeedTimeLimitSec != 3600 {
		t.Fatalf("view limits = %+v", view)
	}
	env.service.Close()
	env.service = env.start()
	stored := env.store.record(view.ID)
	if stored.DownloadLimitKBps != 512 || stored.UploadLimitKBps != 128 ||
		stored.SeedRatioLimit != 8 || stored.SeedTimeLimitSec != 3600 {
		t.Fatalf("limits lost on restart: %+v", stored)
	}
}
