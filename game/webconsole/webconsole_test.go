package webconsole_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gameserver/game/gamedata"
	"gameserver/game/protocol"
	"gameserver/game/webconsole"
)

func TestSchemaCoversEveryRegisteredMessage(t *testing.T) {
	tables, err := gamedata.Load()
	if err != nil {
		t.Fatal(err)
	}
	s := webconsole.BuildSchema(protocol.Registry(), webconsole.Options{WSPort: "7001", Tables: tables})

	if s.IDs["1001"] != "gs.game.LoginReq" || s.IDs["1"] != "gs.sys.HeartbeatReq" {
		t.Fatalf("id table wrong: 1001=%q 1=%q", s.IDs["1001"], s.IDs["1"])
	}
	for id, name := range s.IDs {
		if _, ok := s.Messages[name]; !ok {
			t.Errorf("id %s -> %s has no message schema", id, name)
		}
	}
	// Every referenced message/enum type must be described too.
	for name, fields := range s.Messages {
		for _, f := range fields {
			switch f.Kind {
			case "message":
				if _, ok := s.Messages[f.Type]; !ok {
					t.Errorf("%s.%s references undescribed message %s", name, f.Name, f.Type)
				}
			case "enum":
				if _, ok := s.Enums[f.Type]; !ok {
					t.Errorf("%s.%s references undescribed enum %s", name, f.Name, f.Type)
				}
			}
		}
	}
	login := map[string]webconsole.Field{}
	for _, f := range s.Messages["gs.game.LoginReq"] {
		login[f.Name] = f
	}
	if f := login["account"]; f.Number != 1 || f.Kind != "string" {
		t.Errorf("LoginReq.account = %+v", f)
	}
	if f := s.Messages["gs.game.SceneEntityLeavePush"][0]; !f.Repeated || f.Kind != "int64" {
		t.Errorf("SceneEntityLeavePush.uids = %+v", f)
	}
	// Error code enums are needed to show readable errors.
	if s.Enums["gs.game.ErrCode"]["ERR_ITEM_NOT_USABLE"] != 2103 || s.Enums["gs.sys.ErrorCode"]["NOT_AUTHENTICATED"] != 4 {
		t.Error("error code enums missing")
	}
	if len(s.Items) != tables.Items.Len() {
		t.Errorf("items %d, want %d", len(s.Items), tables.Items.Len())
	}
}

func TestHandlerServesPageAndSchema(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(webconsole.Prefix, webconsole.Handler(protocol.Registry(), webconsole.Options{WSPort: "17001"}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	get := func(path string) (int, string, string) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
	}

	code, _, body := get("/console/")
	if code != 200 || !strings.Contains(body, `<script type="module" src="app.js">`) {
		t.Fatalf("index: %d", code)
	}
	for _, f := range []string{"app.js", "client.js", "codec.js", "selftest.js"} {
		code, ctype, _ := get("/console/" + f)
		if code != 200 || !strings.Contains(ctype, "javascript") {
			t.Errorf("%s: %d %q (browsers refuse ES modules without a JavaScript MIME type)", f, code, ctype)
		}
	}
	code, ctype, body := get("/console/schema.json")
	if code != 200 || ctype != "application/json" {
		t.Fatalf("schema: %d %q", code, ctype)
	}
	var s webconsole.Schema
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatal(err)
	}
	if s.WSPort != "17001" || s.WSPath != "/ws" {
		t.Errorf("ws hint %q %q", s.WSPort, s.WSPath)
	}
}
