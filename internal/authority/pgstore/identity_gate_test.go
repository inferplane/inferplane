package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/identity"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/policy"
)

func TestIdentityActivationPreservesSharedLiabilityAndLateSettlement(t *testing.T) {
	f := newSharedFixture(t)
	f.team(t, keystore.TeamRecord{Name: "team", BudgetUSDMicros: 10_000})
	putDocument(t, f.db, sharedDoc("person-budget", v1alpha1.Subject{User: "legacy-person"},
		v1alpha1.Rule{Name: "total", FailurePolicy: v1alpha1.FailClosed, Budget: &v1alpha1.BudgetRule{LimitMilliUSD: 1, HardCap: true}},
		sharedQuota("tokens", 1000, v1alpha1.PeriodCalendarMonth)))
	plain, oldRequest := f.key(t, "team", keystore.KeyOptions{Owner: "legacy-person"})
	permit, err := f.a.ReserveShared(context.Background(), oldRequest)
	if err != nil {
		t.Fatal(err)
	}
	var before string
	if err := f.db.QueryRow(context.Background(), `SELECT COALESCE(jsonb_agg(to_jsonb(c) ORDER BY counter_key,window_id)::text,'[]') FROM shared_counters c`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	person, _ := identity.NewHuman("acme", "https://idp.example", "subject")
	cfg := identity.Config{Organization: "acme", Required: true, Bindings: []identity.Binding{{Identity: person, AccountRef: "legacy-person"}}}
	if err := f.keys.ConfigureIdentity(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := f.db.QueryRow(context.Background(), `SELECT COALESCE(jsonb_agg(to_jsonb(c) ORDER BY counter_key,window_id)::text,'[]') FROM shared_counters c`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("identity activation changed financial counters")
	}
	if _, err := f.b.ReserveShared(context.Background(), oldRequest); err == nil {
		t.Fatal("identity-blind or stale authorization admitted after activation")
	}
	updated := f.request(t, plain)
	updated.IdentityFingerprint = cfg.Fingerprint()
	if updated.Subject.User != oldRequest.Subject.User {
		t.Fatal("identity activation renamed the accounting subject")
	}
	if _, err := f.b.ReserveShared(context.Background(), updated); err == nil {
		t.Fatal("identity activation restored the already reserved user budget")
	}
	_, rotated := f.key(t, "team", keystore.KeyOptions{Identity: &person})
	rotated.IdentityFingerprint = cfg.Fingerprint()
	if rotated.Subject.KeyID == oldRequest.Subject.KeyID || rotated.Subject.User != oldRequest.Subject.User {
		t.Fatal("second-device credential did not retain the same person account")
	}
	if _, err := f.b.ReserveShared(context.Background(), rotated); err == nil {
		t.Fatal("key rotation created fresh user authority")
	} else {
		var denial *governance.SharedDenial
		if !errors.As(err, &denial) || denial.Status != 402 {
			t.Fatal("rotation was not denied by the existing money reservation")
		}
	}
	cost, tokens := int64(25), int64(3)
	outcome := governance.SharedSettlement{CostMicroUSD: &cost, Tokens: &tokens, Complete: true}
	// Terminal capability and booking windows belong to the original attempt.
	if err := f.a.FinishShared(context.Background(), permit, outcome); err != nil {
		t.Fatal(err)
	}
	if err := f.b.FinishShared(context.Background(), permit, outcome); err != nil {
		t.Fatal("exact terminal replay changed across identity activation")
	}
	rotated.CostBoundMicroUSD = 976
	if _, err := f.b.ReserveShared(context.Background(), rotated); err == nil {
		t.Fatal("late settlement replay refunded more than proven unused authority")
	}
	rotated.CostBoundMicroUSD = 975
	if _, err := f.b.ReserveShared(context.Background(), rotated); err != nil {
		t.Fatal("proven unused authority was not reusable by the same person")
	}
}

func TestIdentityAdmissionGuardsRejectOldSQLButPreserveExistingGrants(t *testing.T) {
	dsn, db := testDatabase(t)
	putBudget(t, db, 100, 10, true, false)
	s := openStore(t, dsn)
	budget := currentBudget(t, s)
	req := heartbeat("boot")
	req.Requests = []policy.AuthorityGrantRequest{grantRequest(budget, 1)}
	issued := syncOK(t, s, "node", req)
	if len(issued.Authority.Grants) != 1 {
		t.Fatal("initial grant missing")
	}
	keys, err := keystore.OpenPostgres(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer keys.Close()
	cfg := identity.Config{Organization: "acme", Required: true}
	if err := keys.ConfigureIdentity(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sync(context.Background(), "node", req); err == nil {
		t.Fatal("legacy synchronization admitted after activation")
	}
	if _, err := db.Exec(context.Background(), `UPDATE authority_requests SET amount=amount+1 WHERE grant_id=$1`, issued.Authority.Grants[0].ID); err == nil {
		t.Fatal("old SQL path could increase issued authority")
	}
	req.IdentityFingerprint = cfg.Fingerprint()
	replayed := syncOK(t, s, "node", req)
	oldJSON, _ := json.Marshal(issued.Authority.Grants)
	newJSON, _ := json.Marshal(replayed.Authority.Grants)
	if string(oldJSON) != string(newJSON) {
		t.Fatal("identity activation changed a retained grant or its deadline")
	}
	if replayed.IdentityFingerprint != cfg.Fingerprint() || replayed.Authority.IdentityFingerprint != cfg.Fingerprint() {
		t.Fatal("database-owned identity declaration was not returned")
	}
	if err := CheckIdentityConfiguration(context.Background(), dsn, ""); err == nil {
		t.Fatal("unconfigured control plane could downgrade required identity")
	}
}
