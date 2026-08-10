package uploads

import "testing"

func TestNormalizeManifest(t *testing.T) {
	got, err := normalizeManifest(ManifestFile{
		RelativePath: `docs\rapor.pdf`,
		FileName:     "",
		ExpectedSize: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.RelativePath != "docs/rapor.pdf" || got.FileName != "rapor.pdf" {
		t.Fatalf("unexpected normalize result: %+v", got)
	}
}

func TestNormalizeManifestRejectsTraversal(t *testing.T) {
	cases := []string{"../secret.txt", "a/../../b.txt", "/abs.txt", "foo/./bar.txt"}
	for _, path := range cases {
		if _, err := normalizeManifest(ManifestFile{RelativePath: path, ExpectedSize: 1}); err == nil {
			t.Fatalf("expected traversal/invalid path to fail for %q", path)
		}
	}
}

func TestNormalizeManifestRejectsNegativeSize(t *testing.T) {
	if _, err := normalizeManifest(ManifestFile{RelativePath: "ok.txt", ExpectedSize: -1}); err == nil {
		t.Fatal("expected negative size to fail")
	}
}
