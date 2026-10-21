package app

import (
 "errors"
 "testing"
)

func TestProviderErrorPreservesSafeArrSizeDiagnostic(t *testing.T) {
 for _,kind:=range []string{"sonarr","radarr"} {
  t.Run(kind,func(t *testing.T){
   message:=kind+" response exceeds size limit"
   if got:=providerError(errors.New(message));got!=message {t.Fatalf("safe diagnostic hidden: got %q, want %q",got,message)}
  })
 }
 // A transport error that happens to contain diagnostic text must not expose
 // URL credentials or arbitrary surrounding text.
 if got:=providerError(errors.New("https://user:secret@example.invalid/?apikey=private sonarr response exceeds size limit"));got=="" || got!="provider operation failed; check integration configuration or activity" {t.Fatalf("unsafe transport detail exposed: %q",got)}
}
