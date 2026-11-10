package pluginapi

import "testing"

func TestInitTimeoutValidation(t *testing.T) {
	mk := func(trust Trust, ms int) *Manifest {
		return &Manifest{
			ID: "demo", Name: "Demo", Version: "0.1.0", APIVersion: APIVersionMajor,
			Trust:   trust,
			Backend: &Backend{Entry: "index.ts", InitTimeoutMS: ms},
		}
	}
	if err := mk(TrustBuiltin, 30000).Validate("demo"); err != nil {
		t.Fatalf("builtin 30s 应通过: %v", err)
	}
	if err := mk(TrustThirdParty, 5000).Validate("demo"); err != nil {
		t.Fatalf("third-party 5s 应通过: %v", err)
	}
	if err := mk(TrustThirdParty, 5001).Validate("demo"); err == nil {
		t.Fatal("third-party 超过 5s 应被拒绝")
	}
	if err := mk(TrustBuiltin, 60001).Validate("demo"); err == nil {
		t.Fatal("超过 60s 应被拒绝")
	}
	if err := mk(TrustBuiltin, -1).Validate("demo"); err == nil {
		t.Fatal("负数应被拒绝")
	}
}
