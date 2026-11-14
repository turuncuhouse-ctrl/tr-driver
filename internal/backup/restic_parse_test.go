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

func TestParseFindPaths(t *testing.T) {
	array := `[{"path":"/data/files/a.txt"},{"path":"/data/files/b.txt"}]`
	got := parseFindPaths(array)
	if len(got) != 2 || got[0] != "/data/files/a.txt" {
		t.Fatalf("array shape: %v", got)
	}
	envelope := `{"files":[{"path":"/x/db.dump"}],"hits":1}`
	got = parseFindPaths(envelope)
	if len(got) != 1 || got[0] != "/x/db.dump" {
		t.Fatalf("envelope shape: %v", got)
	}
	lines := "{\"path\":\"/a\"}\n{\"path\":\"/b\"}\n"
	got = parseFindPaths(lines)
	if len(got) != 2 || got[1] != "/b" {
		t.Fatalf("json-lines shape: %v", got)
	}
	if got := parseFindPaths(""); len(got) != 0 {
		t.Fatalf("empty: %v", got)
	}
}
