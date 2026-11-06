package services

import (
	"context"
	"testing"
)

func TestQueueEndingRequiresAuthoritativeLastWholeItem(t *testing.T) {
	s, _ := queueRuntimeFixture(t, false)
	session := startQueueInline(t, s)
	mustQueueEvent(t, s, queueEvent(session, 1, "loaded", 0))
	mustQueueEvent(t, s, queueEvent(session, 2, "playing", 0))
	mustQueueEvent(t, s, queueEvent(session, 3, "ended", 120))
	page, err := s.Snapshot(context.Background(), QueueQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Ending != nil {
		t.Fatal("first loaded item is not the end of the whole queue")
	}
	all := queueSnapshot(t, s)
	if err := s.Play(context.Background(), all.Items[2].ID, all.State.Revision); err != nil {
		t.Fatal(err)
	}
	session = waitQueueSnapshot(t, s, func(p *QueueSnapshot) bool { return p.Session != nil && p.Session.Token != session.Token }).Session
	mustQueueEvent(t, s, queueEvent(session, 1, "loaded", 0))
	mustQueueEvent(t, s, queueEvent(session, 2, "playing", 0))
	mustQueueEvent(t, s, queueEvent(session, 3, "ended", 120))
	page = queueSnapshot(t, s)
	if page.Ending == nil || page.Ending.Token != session.Token || page.Ending.Reason != "exhausted" {
		t.Fatalf("missing authoritative ending: %+v", page.Ending)
	}
	if err := s.Edit(context.Background(), QueueEdit{Action: "move", ExpectedRevision: page.State.Revision, EntryID: page.Items[0].ID, BeforeEntryID: 0}); err != nil {
		t.Fatal(err)
	}
	if queueSnapshot(t, s).Ending != nil {
		t.Fatal("queue edit must invalidate the old ending")
	}
}
