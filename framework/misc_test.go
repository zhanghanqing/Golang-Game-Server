// Package framework_test holds small tests for the leaf utility packages.
package framework_test

import (
	"testing"
	"testing/fstest"

	"gameserver/framework/config"
	"gameserver/framework/datatable"
	"gameserver/framework/idgen"
	"gameserver/framework/message"
	"gameserver/framework/pb/syspb"
)

func TestSnowflakeUniqueAndOrdered(t *testing.T) {
	g, err := idgen.NewSnowflake(7)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[int64]struct{}, 100000)
	var last int64
	for range 100000 {
		id := g.Next()
		if id <= last {
			t.Fatalf("not increasing: %d after %d", id, last)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %d", id)
		}
		seen[id] = struct{}{}
		last = id
	}
	_, worker, _ := idgen.Parse(last)
	if worker != 7 {
		t.Fatalf("worker %d, want 7", worker)
	}
	if _, err := idgen.NewSnowflake(idgen.MaxWorker + 1); err == nil {
		t.Fatal("expected out of range error")
	}
}

func TestConfigExpandEnv(t *testing.T) {
	t.Setenv("GS_TEST_A", "alpha")
	cases := map[string]string{
		"${GS_TEST_A}":            "alpha",
		"${GS_TEST_MISSING:-def}": "def",
		"${GS_TEST_A:-def}":       "alpha",
		"x-${GS_TEST_MISSING}-y":  "x--y",
	}
	for in, want := range cases {
		if got := config.ExpandEnv(in); got != want {
			t.Errorf("ExpandEnv(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConfigRejectsUnknownKeys(t *testing.T) {
	var out struct {
		Name string `yaml:"name"`
	}
	if err := config.Decode([]byte("name: a\ntypo: b\n"), &out); err == nil {
		t.Fatal("expected error for unknown key")
	}
}

type row struct {
	ID   int32  `json:"id"`
	Name string `json:"name"`
}

func TestDatatable(t *testing.T) {
	fsys := fstest.MapFS{"t.json": {Data: []byte(`[{"id":1,"name":"a"},{"id":2,"name":"b"}]`)}}
	tbl, err := datatable.LoadJSON(fsys, "t.json", func(r *row) int32 { return r.ID })
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := tbl.Get(2); !ok || r.Name != "b" || tbl.Len() != 2 {
		t.Fatalf("bad table: %+v", r)
	}
	if _, err := datatable.ParseJSON([]byte(`[{"id":1},{"id":1}]`), func(r *row) int32 { return r.ID }); err == nil {
		t.Fatal("expected duplicate key error")
	}
}

func TestMessageRegistry(t *testing.T) {
	r := message.NewRegistry()
	message.RegisterSystem(r)
	id, body, err := r.Encode(&syspb.HeartbeatReq{ClientTimeMs: 5})
	if err != nil || id != uint32(syspb.SysMsgID_HEARTBEAT_REQ) {
		t.Fatalf("encode: %d %v", id, err)
	}
	msg, err := r.Decode(id, body)
	if err != nil || msg.(*syspb.HeartbeatReq).ClientTimeMs != 5 {
		t.Fatalf("decode: %v", err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate registration must panic")
		}
	}()
	r.Register(uint32(syspb.SysMsgID_HEARTBEAT_REQ), &syspb.KickPush{})
}
