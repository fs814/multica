package daemon

import (
	"encoding/json"
	"testing"
)

func TestSupportsKnotRuntimeConfig(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		provider string
		want     bool
	}{
		{provider: "knot", want: true},
		{provider: "knot-http", want: true},
		{provider: "openclaw", want: false},
		{provider: "", want: false},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			t.Parallel()
			if got := supportsKnotRuntimeConfig(tc.provider); got != tc.want {
				t.Fatalf("supportsKnotRuntimeConfig(%q) = %v, want %v", tc.provider, got, tc.want)
			}
		})
	}
}

// TestDecodeKnotRuntimeConfig covers the per-agent Knot agent id read out of an
// agent's runtime_config, and every way that read can go wrong.
//
// The headline property is that EVERY failure mode returns "" rather than an
// error: "" means "no per-agent choice", which leaves the daemon-wide
// MULTICA_KNOT_AGENT_ID in charge. Propagating an error instead would let one
// bad save block every task the agent runs.
func TestDecodeKnotRuntimeConfig(t *testing.T) {
	t.Parallel()
	const valid = "ec4633074fe4413c83218e1f36b8e24d"
	for name, tc := range map[string]struct {
		raw  string
		want string
	}{
		"configured":            {`{"knot":{"agent_id":"` + valid + `"}}`, valid},
		"surrounded by spaces":  {`{"knot":{"agent_id":"  ` + valid + `  "}}`, valid},
		"other providers block": {`{"mode":"gateway","knot":{"agent_id":"` + valid + `"}}`, valid},

		// Nothing configured: every one of these must defer to the daemon-wide default.
		"empty payload":     {``, ""},
		"empty object":      {`{}`, ""},
		"openclaw only":     {`{"mode":"gateway","gateway":{"host":"h"}}`, ""},
		"knot block empty":  {`{"knot":{}}`, ""},
		"agent id empty":    {`{"knot":{"agent_id":""}}`, ""},
		"agent id blank":    {`{"knot":{"agent_id":"   "}}`, ""},
		"null runtime conf": {`null`, ""},

		// Malformed / wrong-typed input must degrade, not panic or propagate.
		"malformed json":   {`{"knot":{"agent_id":`, ""},
		"knot not object":  {`{"knot":"` + valid + `"}`, ""},
		"agent id not str": {`{"knot":{"agent_id":12345}}`, ""},

		// A bad id is rejected HERE rather than passed down, because knot-cli
		// silently substitutes its own default agent for an unknown id — a typo
		// would otherwise quietly bill and behave as a different agent.
		"id too short":  {`{"knot":{"agent_id":"ec4633074fe4413c"}}`, ""},
		"id too long":   {`{"knot":{"agent_id":"` + valid + `ab"}}`, ""},
		"id non hex":    {`{"knot":{"agent_id":"zzzz33074fe4413c83218e1f36b8e24d"}}`, ""},
		"id uppercase":  {`{"knot":{"agent_id":"EC4633074FE4413C83218E1F36B8E24D"}}`, ""},
		"id with dash":  {`{"knot":{"agent_id":"ec4633074fe4413c-3218e1f36b8e24d"}}`, ""},
		"id is a label": {`{"knot":{"agent_id":"全能选手-macbook"}}`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := decodeKnotRuntimeConfig(json.RawMessage(tc.raw), nil)
			if got != tc.want {
				t.Fatalf("decodeKnotRuntimeConfig(%s) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestDecodeKnotRuntimeConfigNilLoggerSafe pins that the decoder tolerates a nil
// logger. The task path always has one, but the warning branches are the only
// place a nil would blow up, and they only run on malformed input — so a crash
// there would surface exactly when things are already going wrong.
func TestDecodeKnotRuntimeConfigNilLoggerSafe(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{"knot":{"agent_id":`, `{"knot":{"agent_id":"nope"}}`} {
		if got := decodeKnotRuntimeConfig(json.RawMessage(raw), nil); got != "" {
			t.Fatalf("decodeKnotRuntimeConfig(%s) = %q, want empty", raw, got)
		}
	}
}

// TestDecodeKnotClientUUID covers the per-agent client-uuid selector: the
// "remote" sentinel, an explicit UUIDv4, and the fail-soft cases that must
// degrade to "" so the backend pins the local host rather than dispatching a
// run to a machine that does not exist.
func TestDecodeKnotClientUUID(t *testing.T) {
	t.Parallel()
	const uuid = "68b7d6d7-8eb5-4598-830e-d71bcc739672"
	const agentID = "ec4633074fe4413c83218e1f36b8e24d"
	for name, tc := range map[string]struct {
		raw  string
		want string
	}{
		"remote sentinel":        {`{"knot":{"client_uuid":"remote"}}`, "remote"},
		"remote case-normalized": {`{"knot":{"client_uuid":"Remote"}}`, "remote"},
		"remote with spaces":     {`{"knot":{"client_uuid":"  remote  "}}`, "remote"},
		"explicit uuid":          {`{"knot":{"client_uuid":"` + uuid + `"}}`, uuid},
		"uuid alongside agent":   {`{"knot":{"agent_id":"` + agentID + `","client_uuid":"` + uuid + `"}}`, uuid},

		// Nothing configured -> pin local (backend default).
		"empty payload":    {``, ""},
		"empty object":     {`{}`, ""},
		"knot block empty": {`{"knot":{}}`, ""},
		"client empty":     {`{"knot":{"client_uuid":""}}`, ""},
		"client blank":     {`{"knot":{"client_uuid":"   "}}`, ""},
		"null runtime":     {`null`, ""},

		// Malformed / wrong shape must degrade, not propagate.
		"malformed json":      {`{"knot":{"client_uuid":`, ""},
		"knot not object":     {`{"knot":"remote"}`, ""},
		"client not string":   {`{"knot":{"client_uuid":123}}`, ""},
		"not uuid not remote": {`{"knot":{"client_uuid":"local"}}`, ""},
		"uuid too short":      {`{"knot":{"client_uuid":"68b7d6d7-8eb5"}}`, ""},
		"uuid bad separator":  {`{"knot":{"client_uuid":"68b7d6d7x8eb5-4598-830e-d71bcc739672"}}`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := decodeKnotClientUUID(json.RawMessage(tc.raw), nil)
			if got != tc.want {
				t.Fatalf("decodeKnotClientUUID(%s) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
