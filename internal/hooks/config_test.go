package hooks

import (
	"reflect"
	"testing"
	"time"
)

func TestResolvedHook_MatchExact(t *testing.T) {
	h := ResolvedHook{Event: "issue.created", Match: func(s string) bool { return s == "issue.created" }}
	if !h.Match("issue.created") {
		t.Fatal("exact match failed")
	}
	if h.Match("issue.updated") {
		t.Fatal("exact match accepted wrong event")
	}
}

func TestConfig_ZeroValueIsZero(t *testing.T) {
	var c Config
	if c.PoolSize != 0 || c.QueueCap != 0 || c.OutputDiskCap != 0 {
		t.Fatal("zero-value Config should have zero ints")
	}
	if c.QueueFullLogInterval != 0 {
		t.Fatal("zero-value duration must be 0")
	}
}

func TestLoadedConfig_FieldsExist(t *testing.T) {
	lc := LoadedConfig{Snapshot: Snapshot{}, Config: Config{}, UnchangedTunables: nil}
	_ = lc.Snapshot
	_ = lc.Config
	_ = lc.UnchangedTunables
	rt := reflect.TypeOf(lc)
	if _, ok := rt.FieldByName("UnchangedTunables"); !ok {
		t.Fatal("UnchangedTunables field missing")
	}
	_ = time.Second
}
