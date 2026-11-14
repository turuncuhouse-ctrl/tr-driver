package integration

import (
	"context"
	"testing"

	"necipdrive/internal/access"
	"necipdrive/internal/collab"
	"necipdrive/internal/files"
	"necipdrive/internal/storage"
)

func TestStarredFlow(t *testing.T) {
	db, cfg := testDB(t)
	ctx := context.Background()
	_, email := registerUser(t, db, cfg)
	var userID, rootID, role string
	if err := db.QueryRow(ctx, `select id::text, storage_root_id::text, role from users where email = $1`, email).Scan(&userID, &rootID, &role); err != nil {
		t.Fatalf("query user: %v", err)
	}
	dir := t.TempDir()
	storeLocal, err := storage.NewLocal(dir)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	accessSvc := access.New(db)
	fileSvc := files.NewService(db, storeLocal, cfg, accessSvc, nil)
	collabSvc := collab.NewService(db, accessSvc, storeLocal)

	if err := fileSvc.UploadBytes(ctx, userID, role, rootID, "star-me.txt", "text/plain", []byte("star"), ""); err != nil {
		t.Fatalf("upload: %v", err)
	}
	var fileID string
	if err := db.QueryRow(ctx, `select id::text from file_entries where parent_id = $1::uuid and name = 'star-me.txt'`, rootID).Scan(&fileID); err != nil {
		t.Fatalf("query file: %v", err)
	}

	starred, err := collabSvc.ListStarred(ctx, userID)
	if err != nil {
		t.Fatalf("list starred: %v", err)
	}
	if len(starred) != 0 {
		t.Fatalf("expected no starred entries, got %d", len(starred))
	}

	if err := collabSvc.SetStar(ctx, userID, role, fileID, true); err != nil {
		t.Fatalf("star: %v", err)
	}
	starred, err = collabSvc.ListStarred(ctx, userID)
	if err != nil {
		t.Fatalf("list starred: %v", err)
	}
	if len(starred) != 1 || starred[0].ID != fileID {
		t.Fatalf("expected 1 starred entry %s, got %+v", fileID, starred)
	}

	if err := collabSvc.SetStar(ctx, userID, role, fileID, false); err != nil {
		t.Fatalf("unstar: %v", err)
	}
	starred, err = collabSvc.ListStarred(ctx, userID)
	if err != nil {
		t.Fatalf("list starred: %v", err)
	}
	if len(starred) != 0 {
		t.Fatalf("expected no starred entries after unstar, got %d", len(starred))
	}
}
