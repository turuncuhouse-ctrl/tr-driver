package sharedspace

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// A user without any grant must not learn anything about the shared space:
// not its content, and not whether a given path exists.
func TestUserWithoutGrantSeesNothing(t *testing.T) {
	env := newTestEnv(t)

	status := decode[struct {
		Access Access         `json:"access"`
		Quota  map[string]any `json:"quota"`
	}](t, env.do("stranger", http.MethodGet, BasePath+"status", nil))
	if status.Access.Available || status.Access.MaxLevel != "none" || status.Quota != nil {
		t.Fatalf("stranger status = %+v, want unavailable without quota details", status)
	}

	for _, target := range []string{
		"list", "list?path=Filmler", "list?path=Gizli", "list?path=YokBoyleBirKlasor",
		"download?path=kok.txt", "download?path=Filmler/film.mkv", "download?path=Yok/yok.txt",
	} {
		rec := env.do("stranger", http.MethodGet, BasePath+target, nil)
		// Existing and missing paths answer identically: no existence oracle.
		wantStatus(t, rec, http.StatusForbidden)
		for _, secret := range []string{"film", "Gizli", "sir", "kok.txt", "icerigi"} {
			if strings.Contains(rec.Body.String(), secret) {
				t.Fatalf("GET %s leaked %q to a user without access", target, secret)
			}
		}
	}
	wantStatus(t, env.do("stranger", http.MethodPost, BasePath+"upload?path=Filmler&name=x.txt", strings.NewReader("x")), http.StatusForbidden)
	wantStatus(t, env.doJSON("stranger", http.MethodPost, BasePath+"folder", map[string]string{"path": "", "name": "Yeni"}), http.StatusForbidden)
	wantStatus(t, env.doJSON("stranger", http.MethodPost, BasePath+"delete", map[string]string{"path": "Filmler/film.mkv"}), http.StatusForbidden)
	wantStatus(t, env.doJSON("stranger", http.MethodPost, BasePath+"rename", map[string]string{"path": "kok.txt", "name": "y.txt"}), http.StatusForbidden)
	wantStatus(t, env.doJSON("stranger", http.MethodPost, BasePath+"links", LinkInput{Path: "Filmler"}), http.StatusForbidden)
	if env.exists("Filmler/x.txt") || env.exists("Yeni") || !env.exists("Filmler/film.mkv") || !env.exists("kok.txt") {
		t.Fatal("a user without access changed the shared folder")
	}
}

func TestUnauthenticatedRequestIsRejected(t *testing.T) {
	env := newTestEnv(t)
	for _, target := range []string{BasePath + "status", BasePath + "list", BasePath + "download?path=kok.txt", AdminBasePath + "grants"} {
		wantStatus(t, env.do("", http.MethodGet, target, nil), http.StatusUnauthorized)
	}
}

func TestReadGrantAllowsReadingOnlyTheGrantedFolder(t *testing.T) {
	env := newTestEnv(t)

	// The root shows the way to the granted folder and nothing else.
	root := decode[Listing](t, env.do("reader", http.MethodGet, BasePath+"list", nil))
	if names := entryNames(root); !slices.Equal(names, []string{"Filmler"}) {
		t.Fatalf("root listing for reader = %v, want only [Filmler]", names)
	}
	if root.Level != "none" || root.Entries[0].Level != "read" {
		t.Fatalf("levels = root %q, Filmler %q", root.Level, root.Entries[0].Level)
	}

	films := decode[Listing](t, env.do("reader", http.MethodGet, BasePath+"list?path=Filmler", nil))
	if names := entryNames(films); !slices.Equal(names, []string{"Alt", "film.mkv"}) {
		t.Fatalf("Filmler listing = %v", names)
	}
	rec := env.do("reader", http.MethodGet, BasePath+"download?path=Filmler/film.mkv", nil)
	wantStatus(t, rec, http.StatusOK)
	if rec.Body.String() != "film-icerigi" {
		t.Fatalf("download body = %q", rec.Body.String())
	}
	// The grant is inherited by sub-folders.
	wantStatus(t, env.do("reader", http.MethodGet, BasePath+"download?path=Filmler/Alt/derin.txt", nil), http.StatusOK)

	// Everything outside the grant stays closed.
	wantStatus(t, env.do("reader", http.MethodGet, BasePath+"list?path=Gizli", nil), http.StatusForbidden)
	wantStatus(t, env.do("reader", http.MethodGet, BasePath+"download?path=Gizli/sir.txt", nil), http.StatusForbidden)
	wantStatus(t, env.do("reader", http.MethodGet, BasePath+"download?path=kok.txt", nil), http.StatusForbidden)

	// Read does not include any kind of write.
	wantStatus(t, env.do("reader", http.MethodPost, BasePath+"upload?path=Filmler&name=yeni.txt", strings.NewReader("x")), http.StatusForbidden)
	wantStatus(t, env.doJSON("reader", http.MethodPost, BasePath+"folder", map[string]string{"path": "Filmler", "name": "Yeni"}), http.StatusForbidden)
	wantStatus(t, env.doJSON("reader", http.MethodPost, BasePath+"rename", map[string]string{"path": "Filmler/film.mkv", "name": "x.mkv"}), http.StatusForbidden)
	wantStatus(t, env.doJSON("reader", http.MethodPost, BasePath+"delete", map[string]string{"path": "Filmler/film.mkv"}), http.StatusForbidden)
	if !env.exists("Filmler/film.mkv") || env.exists("Filmler/yeni.txt") {
		t.Fatal("a read-only user changed the folder")
	}
}

func TestWriteGrantAllowsChangesInsideTheGrantedFolder(t *testing.T) {
	env := newTestEnv(t)

	wantStatus(t, env.do("writer", http.MethodPost, BasePath+"upload?path=Filmler&name=yeni.txt", strings.NewReader("yeni-veri")), http.StatusCreated)
	wantStatus(t, env.doJSON("writer", http.MethodPost, BasePath+"folder", map[string]string{"path": "Filmler", "name": "Diziler"}), http.StatusCreated)
	wantStatus(t, env.doJSON("writer", http.MethodPost, BasePath+"rename", map[string]string{"path": "Filmler/yeni.txt", "name": "son.txt"}), http.StatusOK)
	if !env.exists("Filmler/son.txt") || env.exists("Filmler/yeni.txt") || !env.exists("Filmler/Diziler") {
		t.Fatal("upload/rename/folder did not take effect")
	}
	// An existing name is not silently replaced.
	wantStatus(t, env.do("writer", http.MethodPost, BasePath+"upload?path=Filmler&name=son.txt", strings.NewReader("baska")), http.StatusConflict)
	wantStatus(t, env.do("writer", http.MethodPost, BasePath+"upload?path=Filmler&name=son.txt&overwrite=1", strings.NewReader("baska")), http.StatusCreated)
	wantStatus(t, env.doJSON("writer", http.MethodPost, BasePath+"delete", map[string]string{"path": "Filmler/son.txt"}), http.StatusOK)

	// The grant covers the folder's content, not the folder itself or its
	// siblings: those belong to the parent, where writer has no rights.
	wantStatus(t, env.doJSON("writer", http.MethodPost, BasePath+"delete", map[string]string{"path": "Filmler"}), http.StatusForbidden)
	wantStatus(t, env.doJSON("writer", http.MethodPost, BasePath+"rename", map[string]string{"path": "Filmler", "name": "Benim"}), http.StatusForbidden)
	wantStatus(t, env.do("writer", http.MethodPost, BasePath+"upload?path=&name=kokte.txt", strings.NewReader("x")), http.StatusForbidden)
	wantStatus(t, env.do("writer", http.MethodPost, BasePath+"upload?path=Gizli&name=x.txt", strings.NewReader("x")), http.StatusForbidden)
	wantStatus(t, env.doJSON("writer", http.MethodPost, BasePath+"delete", map[string]string{"path": "Gizli/sir.txt"}), http.StatusForbidden)
	if !env.exists("Filmler") || !env.exists("Gizli/sir.txt") || env.exists("kokte.txt") {
		t.Fatal("writer changed something outside the granted folder")
	}
}

func TestGroupAndEveryoneGrants(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	admin := env.actor("admin")

	// "member" reaches Belgeler through the group only.
	wantStatus(t, env.do("member", http.MethodGet, BasePath+"download?path=Belgeler/rapor.pdf", nil), http.StatusOK)
	wantStatus(t, env.do("member", http.MethodGet, BasePath+"list?path=Filmler", nil), http.StatusForbidden)
	wantStatus(t, env.do("reader", http.MethodGet, BasePath+"list?path=Belgeler", nil), http.StatusForbidden)

	// Leaving the group removes the access at once.
	groups, err := env.service.ListGroups(ctx, admin)
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups = %v, %v", groups, err)
	}
	if err := env.service.RemoveGroupMember(ctx, admin, groups[0].ID, "member"); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, env.do("member", http.MethodGet, BasePath+"download?path=Belgeler/rapor.pdf", nil), http.StatusForbidden)

	// A grant to everyone opens the root for reading, to every user.
	if _, err := env.service.PutGrant(ctx, admin, GrantInput{Path: "", SubjectType: SubjectEveryone, Permission: "read"}); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, env.do("stranger", http.MethodGet, BasePath+"download?path=Gizli/sir.txt", nil), http.StatusOK)
	wantStatus(t, env.do("stranger", http.MethodPost, BasePath+"upload?path=&name=x.txt", strings.NewReader("x")), http.StatusForbidden)
	// The higher of two applicable grants wins.
	listing := decode[Listing](t, env.do("writer", http.MethodGet, BasePath+"list", nil))
	for _, entry := range listing.Entries {
		want := "read"
		if entry.Name == "Filmler" {
			want = "write"
		}
		if entry.Level != want {
			t.Errorf("writer level on %s = %q, want %q", entry.Name, entry.Level, want)
		}
	}
}

func TestGrantsFollowRenamesAndDieWithDeletes(t *testing.T) {
	env := newTestEnv(t)
	wantStatus(t, env.doJSON("admin", http.MethodPost, BasePath+"rename", map[string]string{"path": "Filmler", "name": "Sinema"}), http.StatusOK)
	wantStatus(t, env.do("reader", http.MethodGet, BasePath+"download?path=Sinema/film.mkv", nil), http.StatusOK)
	wantStatus(t, env.do("reader", http.MethodGet, BasePath+"list?path=Filmler", nil), http.StatusForbidden)

	wantStatus(t, env.doJSON("admin", http.MethodPost, BasePath+"delete", map[string]string{"path": "Sinema"}), http.StatusOK)
	// A new folder with the old name must not inherit the old grants.
	wantStatus(t, env.doJSON("admin", http.MethodPost, BasePath+"folder", map[string]string{"path": "", "name": "Sinema"}), http.StatusCreated)
	wantStatus(t, env.do("reader", http.MethodGet, BasePath+"list?path=Sinema", nil), http.StatusForbidden)
}

func TestOnlyAdministratorsManageGrantsGroupsAndQuota(t *testing.T) {
	env := newTestEnv(t)
	for _, user := range []string{"stranger", "reader", "writer"} {
		wantStatus(t, env.do(user, http.MethodGet, AdminBasePath+"grants", nil), http.StatusForbidden)
		wantStatus(t, env.do(user, http.MethodGet, AdminBasePath+"groups", nil), http.StatusForbidden)
		wantStatus(t, env.do(user, http.MethodGet, AdminBasePath+"settings", nil), http.StatusForbidden)
		// A user must not be able to grant access to themselves.
		rec := env.doJSON(user, http.MethodPost, AdminBasePath+"grants", GrantInput{Path: "", SubjectType: SubjectUser, UserID: user, Permission: "write"})
		wantStatus(t, rec, http.StatusForbidden)
		wantStatus(t, env.doJSON(user, http.MethodPut, AdminBasePath+"settings", QuotaSettings{QuotaBytes: 1, WarnPercent: 50}), http.StatusForbidden)
	}
	wantStatus(t, env.do("stranger", http.MethodGet, BasePath+"list", nil), http.StatusForbidden)

	// The service refuses on its own too, independent of the HTTP layer.
	ctx := context.Background()
	writer := env.actor("writer")
	if _, err := env.service.PutGrant(ctx, writer, GrantInput{Path: "", SubjectType: SubjectEveryone, Permission: "write"}); err != ErrForbidden {
		t.Fatalf("PutGrant as non-admin: %v, want ErrForbidden", err)
	}
	if _, err := env.service.CreateGroup(ctx, writer, "x"); err != ErrForbidden {
		t.Fatalf("CreateGroup as non-admin: %v, want ErrForbidden", err)
	}
	if _, err := env.service.SaveSettings(ctx, writer, QuotaSettings{WarnPercent: 50}); err != ErrForbidden {
		t.Fatalf("SaveSettings as non-admin: %v, want ErrForbidden", err)
	}

	// The administrator can do all of it through the API.
	wantStatus(t, env.do("admin", http.MethodGet, AdminBasePath+"grants", nil), http.StatusOK)
	rec := env.doJSON("admin", http.MethodPost, AdminBasePath+"grants", GrantInput{Path: "Gizli", SubjectType: SubjectUser, UserID: "stranger", Permission: "read"})
	wantStatus(t, rec, http.StatusOK)
	wantStatus(t, env.do("stranger", http.MethodGet, BasePath+"download?path=Gizli/sir.txt", nil), http.StatusOK)
	grant := decode[Grant](t, rec)
	wantStatus(t, env.do("admin", http.MethodDelete, AdminBasePath+"grants/"+grant.ID, nil), http.StatusOK)
	wantStatus(t, env.do("stranger", http.MethodGet, BasePath+"download?path=Gizli/sir.txt", nil), http.StatusForbidden)

	// Grants only attach to folders that exist.
	wantStatus(t, env.doJSON("admin", http.MethodPost, AdminBasePath+"grants", GrantInput{Path: "Yok", SubjectType: SubjectEveryone, Permission: "read"}), http.StatusNotFound)
	wantStatus(t, env.doJSON("admin", http.MethodPost, AdminBasePath+"grants", GrantInput{Path: "kok.txt", SubjectType: SubjectEveryone, Permission: "read"}), http.StatusBadRequest)
	wantStatus(t, env.doJSON("admin", http.MethodPost, AdminBasePath+"grants", GrantInput{Path: "Gizli", SubjectType: SubjectEveryone, Permission: "admin"}), http.StatusBadRequest)
}

func TestDownloadsAreNeverRenderedInTheApplicationOrigin(t *testing.T) {
	env := newTestEnv(t)
	env.write("Filmler/sayfa.html", "<script>alert(document.cookie)</script>")
	env.write("Filmler/resim.svg", "<svg onload='alert(1)'/>")
	for _, name := range []string{"sayfa.html", "resim.svg"} {
		rec := env.do("reader", http.MethodGet, BasePath+"download?path=Filmler/"+name, nil)
		wantStatus(t, rec, http.StatusOK)
		header := rec.Header()
		if got := header.Get("Content-Type"); got != "application/octet-stream" {
			t.Errorf("%s Content-Type = %q, want application/octet-stream", name, got)
		}
		if !strings.HasPrefix(header.Get("Content-Disposition"), "attachment") {
			t.Errorf("%s is not served as an attachment: %q", name, header.Get("Content-Disposition"))
		}
		if header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(header.Get("Content-Security-Policy"), "sandbox") {
			t.Errorf("%s is missing the sniffing/sandbox headers: %v", name, header)
		}
	}
}

func TestRequestsAreRateLimitedPerUser(t *testing.T) {
	env := newTestEnv(t)
	limited := false
	for i := 0; i < 400; i++ {
		rec := env.do("reader", http.MethodGet, BasePath+"status", nil)
		if rec.Code == http.StatusTooManyRequests {
			if rec.Header().Get("Retry-After") == "" {
				t.Fatal("429 without Retry-After")
			}
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("400 rapid requests from one user were never rate limited")
	}
	// Another user is unaffected.
	wantStatus(t, env.do("writer", http.MethodGet, BasePath+"status", nil), http.StatusOK)
}
