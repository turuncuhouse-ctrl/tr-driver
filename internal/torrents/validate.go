// Package torrents embeds a BitTorrent client in the server. Downloads land in
// the shared space ("Ortak Paylaşım"); only administrators may control it.
package torrents

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"necipdrive/internal/sharedspace"
)

const (
	maxTorrentFiles  = 100_000
	maxTrackers      = 100
	maxTrackerLength = 2048
)

// ErrUnsafeTorrent is returned for torrents whose content would not stay
// inside its own folder of the shared space.
var ErrUnsafeTorrent = errors.New("torrent içeriği güvenli değil")

// validateInfo checks that a torrent's file layout is safe to materialise in
// the shared folder and returns the top-level name it will occupy and its size.
//
// The storage layer also refuses paths that escape its base directory; this
// check is stricter on purpose. It rejects the layout instead of sanitising
// it, keeps torrents out of the reserved internal directory, and refuses the
// "no name" form that would scatter files directly into the shared root.
func validateInfo(info *metainfo.Info) (top string, total int64, err error) {
	if info == nil {
		return "", 0, fmt.Errorf("%w: bilgi yok", ErrUnsafeTorrent)
	}
	top = info.BestName()
	if top == "" || top == metainfo.NoName {
		return "", 0, fmt.Errorf("%w: torrentin adı yok", ErrUnsafeTorrent)
	}
	if sharedspace.ValidName(top) != nil || strings.EqualFold(top, sharedspace.InternalDir) {
		return "", 0, fmt.Errorf("%w: geçersiz ad", ErrUnsafeTorrent)
	}
	files := info.UpvertedFiles()
	if len(files) == 0 {
		return "", 0, fmt.Errorf("%w: dosya içermiyor", ErrUnsafeTorrent)
	}
	if len(files) > maxTorrentFiles {
		return "", 0, fmt.Errorf("%w: çok fazla dosya içeriyor", ErrUnsafeTorrent)
	}
	multi := info.IsDir()
	for i := range files {
		file := &files[i]
		parts := file.BestPath()
		if multi && len(parts) == 0 {
			return "", 0, fmt.Errorf("%w: adsız dosya", ErrUnsafeTorrent)
		}
		if !multi && len(parts) != 0 {
			return "", 0, fmt.Errorf("%w: beklenmeyen dosya yolu", ErrUnsafeTorrent)
		}
		for _, part := range parts {
			// ValidName refuses "..", ".", separators of both kinds and
			// control characters: everything a traversal is built from.
			if sharedspace.ValidName(part) != nil {
				return "", 0, fmt.Errorf("%w: geçersiz dosya yolu", ErrUnsafeTorrent)
			}
		}
		if file.Length < 0 || file.Length > math.MaxInt64-total {
			return "", 0, fmt.Errorf("%w: geçersiz dosya boyutu", ErrUnsafeTorrent)
		}
		total += file.Length
	}
	if total <= 0 {
		return "", 0, fmt.Errorf("%w: içerik boş", ErrUnsafeTorrent)
	}
	return top, total, nil
}

// publicAddr reports whether addr is a routable, public unicast address. The
// server must not be steered into talking to itself or to its private network
// by a tracker URL inside a torrent.
func publicAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsUnspecified() || addr.IsLoopback() || addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() ||
		addr.IsInterfaceLocalMulticast() {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),  // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),   // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"),  // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),    // reserved
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64, can map to private IPv4
	netip.MustParsePrefix("fc00::/7"),       // unique local
	netip.MustParsePrefix("169.254.0.0/16"), // link-local, incl. cloud metadata
	netip.MustParsePrefix("fe80::/10"),      // link-local
	netip.MustParsePrefix("0.0.0.0/8"),      // "this network"
	netip.MustParsePrefix("255.255.255.255/32"),
}

// sanitizeTrackers keeps tracker URLs that use a tracker protocol and do not
// name a local or private host. Names that only resolve to a private address
// are caught later, at connection time, by guardedDialer.
func sanitizeTrackers(tiers [][]string) [][]string {
	var out [][]string
	kept := 0
	for _, tier := range tiers {
		var clean []string
		for _, raw := range tier {
			if kept >= maxTrackers {
				break
			}
			if trackerAllowed(raw) {
				clean = append(clean, raw)
				kept++
			}
		}
		if len(clean) > 0 {
			out = append(out, clean)
		}
	}
	return out
}

func trackerAllowed(raw string) bool {
	if raw == "" || len(raw) > maxTrackerLength {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "udp", "udp4", "udp6":
	default:
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return false
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return publicAddr(addr)
	}
	// A bare single-label name ("db", "router") is a local host, not a tracker.
	return strings.Contains(host, ".")
}

// guardedDialer connects only to public addresses. The check runs after name
// resolution, on the address actually being dialled, so a tracker name that
// resolves (or is later re-pointed) to a private address is refused too.
func guardedDialer() func(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout:   20 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			addrPort, err := netip.ParseAddrPort(address)
			if err != nil || !publicAddr(addrPort.Addr()) {
				return errors.New("torrents: refusing to connect to a non-public address")
			}
			return nil
		},
	}
	return dialer.DialContext
}
