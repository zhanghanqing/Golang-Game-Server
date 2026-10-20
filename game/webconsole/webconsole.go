// Package webconsole serves a browser-based test client ("web console") from
// the admin HTTP server.
//
// The page talks to the gate over WebSocket using the real binary protocol.
// Instead of shipping generated JavaScript, the server publishes a schema
// (message fields, enums and the msg_id table) built from the Go protobuf
// descriptors at runtime, so the console is always in sync with the server
// and needs no build step or external CDN.
//
// Routes (mounted under /console/):
//
//	GET /console/             the single-page console (embedded static files)
//	GET /console/schema.json  protocol schema + item table + gate WebSocket hint
//
// The console is a development and QA tool. Enable it only where the admin
// port is private.
package webconsole

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strconv"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"gameserver/framework/message"
	"gameserver/game/gamedata"
)

//go:embed static
var staticFiles embed.FS

// Prefix is the URL path the console is mounted on.
const Prefix = "/console/"

// Options configure the console.
type Options struct {
	// WSPort is the gate WebSocket port suggested to the page (e.g. "7001").
	// The page combines it with the host it was loaded from. Empty: 7001.
	WSPort string
	// WSPath is the gate WebSocket path. Empty: "/ws".
	WSPath string
	// Tables provides item names for the bag view. Optional.
	Tables *gamedata.Tables
}

// Field describes one protobuf field for the JavaScript codec.
type Field struct {
	Name     string `json:"name"`
	Number   int32  `json:"number"`
	Kind     string `json:"kind"`           // protoreflect kind: int32, string, message, enum...
	Type     string `json:"type,omitempty"` // full name for message and enum kinds
	Repeated bool   `json:"repeated,omitempty"`
}

// Item is the subset of the item table the console displays.
type Item struct {
	ID     int32  `json:"id"`
	Name   string `json:"name"`
	Usable bool   `json:"usable"`
}

// Schema is served as /console/schema.json.
type Schema struct {
	Messages map[string][]Field          `json:"messages"`
	Enums    map[string]map[string]int32 `json:"enums"`
	IDs      map[string]string           `json:"ids"` // "1001" -> "gs.game.LoginReq"
	Items    []Item                      `json:"items"`
	WSPort   string                      `json:"ws_port"`
	WSPath   string                      `json:"ws_path"`
}

// BuildSchema walks every registered message (and everything it references)
// and returns the schema the browser codec needs.
func BuildSchema(reg *message.Registry, opts Options) *Schema {
	s := &Schema{
		Messages: map[string][]Field{},
		Enums:    map[string]map[string]int32{},
		IDs:      map[string]string{},
		WSPort:   opts.WSPort,
		WSPath:   opts.WSPath,
	}
	if s.WSPort == "" {
		s.WSPort = "7001"
	}
	if s.WSPath == "" {
		s.WSPath = "/ws"
	}
	packages := map[protoreflect.FullName]bool{}
	reg.Each(func(id uint32, mt protoreflect.MessageType) {
		md := mt.Descriptor()
		s.IDs[strconv.FormatUint(uint64(id), 10)] = string(md.FullName())
		s.addMessage(md)
		packages[md.ParentFile().Package()] = true
	})
	// Top-level enums of every file in the involved packages. This includes
	// files without messages, such as msgid.proto with the error code enum.
	for pkg := range packages {
		protoregistry.GlobalFiles.RangeFilesByPackage(pkg, func(f protoreflect.FileDescriptor) bool {
			for i := 0; i < f.Enums().Len(); i++ {
				s.addEnum(f.Enums().Get(i))
			}
			return true
		})
	}
	if opts.Tables != nil {
		opts.Tables.Items.Each(func(_ int32, it *gamedata.Item) {
			s.Items = append(s.Items, Item{ID: it.ID, Name: it.Name, Usable: it.Usable})
		})
	}
	return s
}

func (s *Schema) addMessage(md protoreflect.MessageDescriptor) {
	name := string(md.FullName())
	if _, done := s.Messages[name]; done {
		return
	}
	fields := make([]Field, 0, md.Fields().Len())
	s.Messages[name] = fields // mark before recursion (handles self references)
	for i := 0; i < md.Fields().Len(); i++ {
		fd := md.Fields().Get(i)
		f := Field{
			Name:     string(fd.Name()),
			Number:   int32(fd.Number()),
			Kind:     fd.Kind().String(),
			Repeated: fd.IsList(),
		}
		switch fd.Kind() {
		case protoreflect.MessageKind, protoreflect.GroupKind:
			f.Type = string(fd.Message().FullName())
			s.addMessage(fd.Message())
		case protoreflect.EnumKind:
			f.Type = string(fd.Enum().FullName())
			s.addEnum(fd.Enum())
		}
		fields = append(fields, f)
	}
	s.Messages[name] = fields
}

func (s *Schema) addEnum(ed protoreflect.EnumDescriptor) {
	name := string(ed.FullName())
	if _, done := s.Enums[name]; done {
		return
	}
	values := map[string]int32{}
	for i := 0; i < ed.Values().Len(); i++ {
		v := ed.Values().Get(i)
		values[string(v.Name())] = int32(v.Number())
	}
	s.Enums[name] = values
}

// Handler returns the console handler. Mount it on Prefix.
func Handler(reg *message.Registry, opts Options) http.Handler {
	schemaJSON, err := json.Marshal(BuildSchema(reg, opts))
	if err != nil {
		panic(err) // only plain data types: cannot fail
	}
	static, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	files := http.StripPrefix(Prefix, http.FileServerFS(static))

	mux := http.NewServeMux()
	mux.HandleFunc(Prefix+"schema.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(schemaJSON)
	})
	mux.Handle(Prefix, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache") // always load the console matching this server
		files.ServeHTTP(w, r)
	}))
	return mux
}
