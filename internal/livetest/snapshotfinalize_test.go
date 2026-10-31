// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package livetest

import (
 "encoding/json"
 "net/http"
 "os"
 "testing"
 "github.com/diggsweden/forge-ci/internal/domain/provider"
)

func TestForgejoSnapshotFinalizeAcknowledged(t *testing.T) {
	RegisterLiveScenario(t)
 var ack struct { Repos []string; Package string; Versions []string }
 if os.Getenv("FORGE_CI_LIVE_SNAPSHOT_FINALIZE_ACK")=="" { t.Skip("exact terminal-run cleanup acknowledgement required") }
 if err:=json.Unmarshal([]byte(os.Getenv("FORGE_CI_LIVE_SNAPSHOT_FINALIZE_ACK")),&ack);err!=nil {t.Fatal(err)}
 target:=Accept(t,provider.ForgeForgejo)
 for _,repo:=range ack.Repos {
  if !inRunNamespace(target,repo) {t.Fatal("repository outside acknowledged namespace")}
  if err:=discard(Context(t),target,http.MethodGet,actionsURL(target,repo,""),nil,http.StatusNotFound);err!=nil {t.Fatal("scratch repository absence not proven")}
  t.Logf("repository absent: %s",repo)
 }
 if !inRunNamespace(target,ack.Package) || len(ack.Versions)==0 {t.Fatal("explicit owned package versions required")}
 packages,err:=publishedPackages(target,ack.Package,"container",ack.Package,packageRequests(Context(t),target));if err!=nil {t.Fatal(err)}
 for _,pkg:=range packages {
  t.Logf("owned container package: id=%d version=%s",pkg.ID,pkg.Version)
  allowed:=false
  for _,version:=range ack.Versions {if pkg.Version==version {allowed=true}}
  if !allowed {t.Errorf("unacknowledged container version retained: id=%d version=%s",pkg.ID,pkg.Version);continue}
  DeletePublishedPackage(t,target,ack.Package,"container",ack.Package,pkg.Version)
 }
 remaining,err:=publishedPackages(target,ack.Package,"container",ack.Package,packageRequests(Context(t),target));if err!=nil {t.Fatal(err)}
 t.Logf("remaining owned container package versions=%d",len(remaining))
 if len(remaining)!=0 {t.Error("container cleanup incomplete")}
}
