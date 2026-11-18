package torrents

import (
	"bytes"
	"crypto/sha1"
	"io"
	"os"
	"path/filepath"

	"github.com/anacrolix/torrent/metainfo"
)

// quarantineBadPieces compares on-disk bytes with the torrent's piece hashes.
// A piece that is fully present but does not match is wiped and marked
// incomplete, so the client downloads it again instead of trusting a stale
// completion bit or a cached file mapping. Short (not-yet-downloaded) pieces
// are left untouched.
func (s *Service) quarantineBadPieces(raw []byte) {
	mi, err := metainfo.Load(bytes.NewReader(raw))
	if err != nil {
		return
	}
	info, err := mi.UnmarshalInfo()
	if err != nil || !info.HasV1() || info.PieceLength <= 0 {
		return
	}
	files := info.UpvertedFiles()
	handles := make([]*os.File, len(files))
	defer func() {
		for _, f := range handles {
			if f != nil {
				_ = f.Close()
			}
		}
	}()
	for i, fi := range files {
		if fi.Length <= 0 {
			continue
		}
		path := torrentDataPath(s.space.Dir(), &info, fi)
		f, err := os.OpenFile(path+".part", os.O_RDWR, 0)
		if err != nil {
			f, err = os.OpenFile(path, os.O_RDWR, 0)
		}
		if err != nil {
			continue
		}
		handles[i] = f
	}
	infoHash := mi.HashInfoBytes()
	for i := 0; i < info.NumPieces(); i++ {
		piece := info.Piece(i)
		buf := make([]byte, piece.Length())
		if !readPiece(handles, files, piece.Offset(), buf) {
			continue
		}
		want := piece.V1Hash()
		sum := sha1.Sum(buf)
		if !want.Ok || !bytes.Equal(sum[:], want.Value[:]) {
			zeroPiece(handles, files, piece.Offset(), buf)
			s.markPieceIncomplete(infoHash, i)
		}
	}
}

func (s *Service) markPieceIncomplete(infoHash metainfo.Hash, index int) {
	if s.completion == nil {
		return
	}
	_ = s.completion.Set(metainfo.PieceKey{InfoHash: infoHash, Index: index}, false)
}

// torrentDataPath matches the file layout the embedded client uses.
func torrentDataPath(dir string, info *metainfo.Info, fi metainfo.FileInfo) string {
	parts := []string{dir}
	if info.BestName() != "" {
		parts = append(parts, info.BestName())
	}
	parts = append(parts, fi.BestPath()...)
	return filepath.Join(parts...)
}

// readPiece fills buf with the piece at torrent offset off. It reports false
// when any byte is missing, so a partial download is not treated as corrupt.
func readPiece(handles []*os.File, files []metainfo.FileInfo, off int64, buf []byte) bool {
	return copyPiece(handles, files, off, buf, false)
}

func zeroPiece(handles []*os.File, files []metainfo.FileInfo, off int64, buf []byte) {
	for i := range buf {
		buf[i] = 0
	}
	_ = copyPiece(handles, files, off, buf, true)
}

func copyPiece(handles []*os.File, files []metainfo.FileInfo, off int64, buf []byte, write bool) bool {
	end := off + int64(len(buf))
	for i, fi := range files {
		if fi.Length <= 0 {
			continue
		}
		fStart := fi.TorrentOffset
		fEnd := fStart + fi.Length
		if fEnd <= off || fStart >= end {
			continue
		}
		from := off
		if fStart > from {
			from = fStart
		}
		to := end
		if fEnd < to {
			to = fEnd
		}
		f := handles[i]
		if f == nil {
			return false
		}
		dst := int(from - off)
		src := from - fStart
		n := int(to - from)
		var got int
		var err error
		if write {
			got, err = f.WriteAt(buf[dst:dst+n], src)
		} else {
			got, err = f.ReadAt(buf[dst:dst+n], src)
		}
		if got != n || (err != nil && err != io.EOF) {
			return false
		}
	}
	return true
}
