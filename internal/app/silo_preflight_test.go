package app

import (
 "context"
 "errors"
 "fmt"
 "net/http"
 "net/http/httptest"
 "sync/atomic"
 "testing"
 "time"
)

// These releases cannot trigger SiloAvailability's normal inventory read.
func TestSiloPreflightMissingLocalIDs(t *testing.T) {
 for _, mode := range []string{"unreachable", "complete", "malformed-pagination"} {
  t.Run(mode, func(t *testing.T) {
   a := testApp(t)
   var profiles, pages atomic.Int32
   srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet { t.Error("unexpected Silo mutation") }
    switch r.URL.Path {
    case "/api/v2/profiles":
     profiles.Add(1)
     fmt.Fprint(w, `{"items":[{"id":"primary","is_primary":true}]}`)
    case "/api/v2/catalog":
     pages.Add(1)
     if r.URL.Query().Get("cursor") == "second" {
      if mode == "malformed-pagination" { fmt.Fprint(w, `{"items":[],"page":{"has_more":true}}`) } else { fmt.Fprint(w, `{"items":[],"page":{"has_more":false}}`) }
     } else { fmt.Fprint(w, `{"items":[],"page":{"has_more":true,"next_cursor":"second"}}`) }
    default:
     t.Errorf("unexpected Silo path %s", r.URL.Path)
     http.NotFound(w,r)
    }
   }))
   defer srv.Close()
   if mode == "unreachable" { srv.Close() }
   s := defaultSettings()
   s.Silo = SiloSettings{URL:srv.URL, APIKey:"test-key", Enabled:true}
   if err := a.saveSettings(a.ctx,s); err != nil { t.Fatal(err) }
   if _,err := a.db.Exec(a.ctx, `INSERT INTO releases(canonical_key,media_type,data) VALUES ('missing-movie','movie','{}'),('missing-show','show','{}')`); err != nil { t.Fatal(err) }
   ctx,cancel := context.WithTimeout(a.ctx,5*time.Second)
   defer cancel()
   err := a.syncSilo(ctx)
   var warning *siloSyncWarning
   if mode == "complete" {
    if !errors.As(err,&warning) || warning.Unknown != 2 || warning.Unidentified != 2 { t.Fatalf("complete inventory should yield two unidentified titles: %v",err) }
   } else if err == nil || errors.As(err,&warning) { t.Fatalf("incomplete inventory must fail, not complete with coverage warning: %v",err) }
   if mode != "unreachable" && (profiles.Load()!=1 || pages.Load()!=2) { t.Fatalf("must read one complete snapshot: profiles=%d pages=%d",profiles.Load(),pages.Load()) }
  })
 }
}
