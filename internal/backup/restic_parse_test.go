package backup

import (
	"testing"
)

func TestParseBackupSummary(t *testing.T) {
	stdout := `{"message_type":"status","percent_done":0.5}
{"message_type":"summary","files_new":3,"files_changed":1,"files_unmodified":10,"data_added":12345,"total_bytes_processed":99999,"total_files_processed":14,"snapshot_id":"abc123"}
`
	summary, err := parseBackupSummary(stdout)
	if err != nil {
		t.Fatal(err)
	}
	if summary.SnapshotID != "abc123" || summary.FilesNew != 3 || summary.DataAdded != 12345 || summary.TotalBytes != 99999 {
		t.Fatalf("bad summary: %+v", summary)
	}
	if _, err := parseBackupSummary("not json\n"); err == nil {
		t.Fatal("expected error for garbage")
	}
}

// Old restic printed the summary without message_type; stray warning lines
// must not hide it either.
func TestParseBackupSummaryLegacy(t *testing.T) {
	stdout := "warning: something odd\n" +
		`{"files_new":1,"snapshot_id":"deadbeef"}` + "\n"
	summary, err := parseBackupSummary(stdout)
	if err != nil {
		t.Fatal(err)
	}
	if summary.SnapshotID != "deadbeef" || summary.FilesNew != 1 {
		t.Fatalf("bad summary: %+v", summary)
	}
}

func TestParseFindPaths(t *testing.T) {
	// Documented shape: per-snapshot objects with a matches array.
	grouped := `[{"hits":1,"snapshot":"abc","matches":[{"path":"/data/files/docs/small.txt","name":"small.txt"}]}]`
	got := parseFindPaths(grouped)
	if len(got) != 1 || got[0] != "/data/files/docs/small.txt" {
		t.Fatalf("grouped shape: %v", got)
	}
	// Flat array of bare match objects.
	array := `[{"path":"/data/files/a.txt"},{"path":"/data/files/b.txt"}]`
	got = parseFindPaths(array)
	if len(got) != 2 || got[0] != "/data/files/a.txt" {
		t.Fatalf("array shape: %v", got)
	}
	// Single object envelope.
	envelope := `{"files":[{"path":"/x/db.dump"}],"hits":1}`
	got = parseFindPaths(envelope)
	if len(got) != 1 || got[0] != "/x/db.dump" {
		t.Fatalf("envelope shape: %v", got)
	}
	// JSON-lines, mixed grouped and bare objects.
	lines := "{\"path\":\"/a\"}\n{\"snapshot\":\"s\",\"matches\":[{\"path\":\"/b\"}]}\nnot json\n"
	got = parseFindPaths(lines)
	if len(got) != 2 || got[1] != "/b" {
		t.Fatalf("json-lines shape: %v", got)
	}
	if got := parseFindPaths(""); len(got) != 0 {
		t.Fatalf("empty: %v", got)
	}
	if got := parseFindPaths("[]"); len(got) != 0 {
		t.Fatalf("empty array: %v", got)
	}
}

func TestEscapePattern(t *testing.T) {
	// Character classes: [[] matches literal [, [*] literal *, [?] literal ?.
	// A bare ] stays literal.
	got := escapePattern("/data/files/[final]*?.jpg")
	want := "/data/files/[[]final][*][?].jpg"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if escapePattern("/plain/path.txt") != "/plain/path.txt" {
		t.Fatal("plain path must be untouched")
	}
}
