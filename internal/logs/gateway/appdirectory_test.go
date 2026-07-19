package logs

import (
	"context"
	"testing"
)

func TestUnknownKeyIsLoggedWithoutTheKeyItself(t *testing.T) {
	log, out := captured(t)
	dir := NewAppDirectory(staticApps{}, log)

	_, ok := dir.AppForKey(context.Background(), "sk-live-secret")

	if ok {
		t.Fatal("an empty directory resolved a key")
	}
	if out.Len() == 0 {
		t.Error("an unknown key left no trace — the core cannot attribute it, so only this log can")
	}
	wantNeverLogged(t, out, "sk-live-secret")
}

func TestKnownKeyResolvesSilentlyAndUnchanged(t *testing.T) {
	log, out := captured(t)
	dir := NewAppDirectory(staticApps{"sk-live-secret": {Name: "rag-api"}}, log)

	app, ok := dir.AppForKey(context.Background(), "sk-live-secret")

	if !ok || app.Name != "rag-api" {
		t.Fatalf("resolved = %+v (ok=%v), want rag-api passed through unchanged", app, ok)
	}
	wantSilence(t, out)
}
