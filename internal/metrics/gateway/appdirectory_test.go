package metrics

import "testing"

func TestKnownKeyCountsAsResolvedAndPassesTheAppThrough(t *testing.T) {
	dir := NewAppDirectory(staticApps{"sk-live-secret": {Name: "rag-api"}})

	app, ok := dir.AppForKey(t.Context(), "sk-live-secret")

	if !ok || app.Name != "rag-api" {
		t.Fatalf("resolved = %+v (ok=%v), want rag-api passed through unchanged", app, ok)
	}
	if dir.Resolved() != 1 || dir.Refused() != 0 {
		t.Errorf("resolved=%d refused=%d, want only the resolution counted", dir.Resolved(), dir.Refused())
	}
}

func TestUnknownKeyCountsAsRefused(t *testing.T) {
	dir := NewAppDirectory(staticApps{})

	_, ok := dir.AppForKey(t.Context(), "sk-live-secret")

	if ok {
		t.Fatal("an empty directory resolved a key")
	}
	if dir.Refused() != 1 || dir.Resolved() != 0 {
		t.Errorf("resolved=%d refused=%d, want only the refusal counted", dir.Resolved(), dir.Refused())
	}
}
