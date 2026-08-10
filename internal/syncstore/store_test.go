package syncstore

import (
	"fmt"
	"testing"
)

func TestStorePersistsRootsNodesAndJobs(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SetMeta("token", "abc"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetMeta("token"); got != "abc" {
		t.Fatalf("got %q", got)
	}
	if err := s.AddRoot(Root{ID: "r", LocalPath: "C:/sync", RemoteParentID: "p"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertNode(Node{ID: "n", RootID: "r", LocalRel: "a.txt", RemoteID: "remote", SyncState: "synced"}); err != nil {
		t.Fatal(err)
	}
	n, err := s.GetNodeByRel("r", "a.txt")
	if err != nil || n.RemoteID != "remote" {
		t.Fatalf("%+v %v", n, err)
	}
	id, err := s.EnqueueJob("r", "upload", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := s.ListDueJobs(1)
	if err != nil || len(jobs) != 1 || jobs[0].ID != id {
		t.Fatalf("%+v %v", jobs, err)
	}
	if err := s.MarkJobDone(id); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveRootDeletesNodesAndJobs(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.AddRoot(Root{ID: "r", LocalPath: "C:/sync"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertNode(Node{ID: "n", RootID: "r", LocalRel: "file.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueJob("r", "upload", `{"path":"file.txt"}`); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveRoot("r"); err != nil {
		t.Fatal(err)
	}
	if roots, err := s.ListRoots(); err != nil || len(roots) != 0 {
		t.Fatalf("roots=%+v err=%v", roots, err)
	}
	if node, err := s.GetNodeByRel("r", "file.txt"); err != nil || node.ID != "" {
		t.Fatalf("node=%+v err=%v", node, err)
	}
	if jobs, err := s.ListDueJobs(10); err != nil || len(jobs) != 0 {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
}

func TestActivitiesAreListedAndPruned(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 201; i++ {
		if err := s.AddActivity("r", "upload", fmt.Sprintf("%03d.txt", i), "Uploaded"); err != nil {
			t.Fatal(err)
		}
	}
	activities, err := s.ListActivities(300)
	if err != nil {
		t.Fatal(err)
	}
	if len(activities) != 200 {
		t.Fatalf("got %d activities", len(activities))
	}
	if activities[0].Path != "200.txt" {
		t.Fatalf("newest activity = %+v", activities[0])
	}
}

func TestEnqueueJobDeduplicatesOpenJobs(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first, err := s.EnqueueJob("r", "upload", `{"path":"file.txt"}`)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.EnqueueJob("r", "upload", `{"path":"file.txt"}`)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("job IDs differ: %d != %d", first, second)
	}
	if pending, err := s.CountPendingJobs(); err != nil || pending != 1 {
		t.Fatalf("pending=%d err=%v", pending, err)
	}
}
