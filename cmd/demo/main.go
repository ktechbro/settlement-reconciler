package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/ktechbro/settlement-reconciler/internal/demodata"
	"github.com/ktechbro/settlement-reconciler/internal/reconcile"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	db := os.Getenv("DATABASE_URL")
	if db == "" {
		db = "postgres://reconciler:reconciler-local@127.0.0.1:55438/reconciler?sslmode=disable"
	}
	store, err := reconcile.Open(ctx, db)
	if err != nil {
		return err
	}
	defer store.Pool.Close()
	now := time.Now().UTC().Truncate(time.Second)
	s := &reconcile.Service{Store: store, Now: func() time.Time { return now }}
	tenant := fmt.Sprintf("demo-%d", time.Now().UnixNano())
	expected, err := demodata.Seed(ctx, s, tenant)
	if err != nil {
		return err
	}
	now = now.Add(time.Hour)
	if _, _, err = s.Write(ctx, tenant, "demo", "reconcile", "final-evaluation", []byte(`{}`)); err != nil {
		return err
	}
	cases, err := store.Cases(ctx, tenant, "", "", 500)
	if err != nil {
		return err
	}
	actual := map[string]string{}
	for _, v := range cases {
		if want, ok := expected[v.Case.Subject]; ok && v.Case.Key == "decision:"+v.Case.Subject {
			actual[v.Case.Subject] = v.Case.Status
			if want != v.Case.Status {
				return fmt.Errorf("oracle mismatch for %s: want %s got %s", v.Case.Subject, want, v.Case.Status)
			}
		}
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("missing scenario results")
	}
	trace, err := store.Trace(ctx, tenant, "matched")
	if err != nil {
		return err
	}
	decision := trace["decision"].(*reconcile.Decision)
	if !decision.Verification.SignatureValid {
		return fmt.Errorf("demo did not verify signed decisions")
	}
	report := map[string]any{"status": "PASS", "tenant": tenant, "scenarios": len(expected), "actual": actual, "signature_mode": "ephemeral Ed25519 demo issuer; not Saifuro tokens", "evidence_trace_available": true, "expired_now_but_valid_at_acceptance": true}
	return json.NewEncoder(os.Stdout).Encode(report)
}
