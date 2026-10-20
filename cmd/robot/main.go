// Command robot simulates players for smoke and load testing.
//
// Each robot logs in, enters the game, inspects its bag, uses an item, says
// hello in world chat and then walks around randomly until the test ends.
//
//	go run ./cmd/robot -addr 127.0.0.1:7000 -n 100 -duration 30s
//	go run ./cmd/robot -ws ws://127.0.0.1:7001/ws -n 10
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"gameserver/framework/client"
	"gameserver/framework/pb/syspb"
	"gameserver/game/gamesvc"
	"gameserver/game/pb"
	"gameserver/game/protocol"
)

type stats struct {
	loginOK   atomic.Int64
	loginFail atomic.Int64
	requests  atomic.Int64
	errors    atomic.Int64
	pushes    atomic.Int64
	kicked    atomic.Int64
	latencyNs atomic.Int64
}

func main() {
	addr := flag.String("addr", "127.0.0.1:7000", "gate TCP address")
	wsURL := flag.String("ws", "", "gate WebSocket url (overrides -addr), e.g. ws://127.0.0.1:7001/ws")
	n := flag.Int("n", 1, "number of robots")
	prefix := flag.String("prefix", "robot", "account name prefix")
	password := flag.String("password", "robot123", "account password")
	duration := flag.Duration("duration", 15*time.Second, "how long each robot stays online")
	moveEvery := flag.Duration("move-interval", 500*time.Millisecond, "time between moves")
	ramp := flag.Duration("ramp", 5*time.Millisecond, "delay between robot starts")
	flag.Parse()

	st := &stats{}
	var wg sync.WaitGroup
	start := time.Now()
	for i := range *n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := &robot{
				id: i, account: fmt.Sprintf("%s_%d", *prefix, i), password: *password,
				addr: *addr, ws: *wsURL, st: st, moveEvery: *moveEvery,
			}
			if err := r.run(*duration); err != nil {
				slog.Warn("robot failed", "robot", i, "err", err)
			}
		}(i)
		time.Sleep(*ramp)
	}
	wg.Wait()

	req := st.requests.Load()
	avg := time.Duration(0)
	if req > 0 {
		avg = time.Duration(st.latencyNs.Load() / req)
	}
	fmt.Printf("\n==== robot report (%s) ====\n", time.Since(start).Round(time.Millisecond))
	fmt.Printf("robots:       %d\n", *n)
	fmt.Printf("login ok:     %d\n", st.loginOK.Load())
	fmt.Printf("login failed: %d\n", st.loginFail.Load())
	fmt.Printf("requests:     %d (errors %d)\n", req, st.errors.Load())
	fmt.Printf("avg latency:  %s\n", avg)
	fmt.Printf("pushes recv:  %d\n", st.pushes.Load())
	fmt.Printf("kicked:       %d\n", st.kicked.Load())
	if st.loginFail.Load() > 0 {
		os.Exit(1)
	}
}

type robot struct {
	id        int
	account   string
	password  string
	addr, ws  string
	st        *stats
	moveEvery time.Duration
	c         *client.Client
}

func (r *robot) call(req proto.Message) (proto.Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t := time.Now()
	resp, err := r.c.Request(ctx, req)
	r.st.requests.Add(1)
	r.st.latencyNs.Add(int64(time.Since(t)))
	if err != nil {
		r.st.errors.Add(1)
	}
	return resp, err
}

func (r *robot) run(duration time.Duration) error {
	opts := client.Options{
		Registry: protocol.Registry(),
		OnPush: func(id uint32, msg proto.Message) {
			r.st.pushes.Add(1)
			if k, ok := msg.(*syspb.KickPush); ok {
				r.st.kicked.Add(1)
				slog.Info("robot kicked", "robot", r.id, "reason", k.Reason.String())
			}
		},
	}
	var err error
	if r.ws != "" {
		r.c, err = client.DialWS(r.ws, opts)
	} else {
		r.c, err = client.DialTCP(r.addr, opts)
	}
	if err != nil {
		r.st.loginFail.Add(1)
		return err
	}
	defer r.c.Close()
	r.c.StartHeartbeat(10 * time.Second)

	if _, err := r.call(&pb.LoginReq{Account: r.account, Password: r.password, ClientVersion: "robot-1.0"}); err != nil {
		r.st.loginFail.Add(1)
		return fmt.Errorf("login: %w", err)
	}
	resp, err := r.call(&pb.EnterGameReq{Name: r.account})
	if err != nil {
		r.st.loginFail.Add(1)
		return fmt.Errorf("enter: %w", err)
	}
	r.st.loginOK.Add(1)
	enter := resp.(*pb.EnterGameResp)
	x, y := enter.Position.X, enter.Position.Y

	_, _ = r.call(&pb.BagGetReq{})
	_, _ = r.call(&pb.ItemUseReq{ItemId: 1002, Count: 1})
	_, _ = r.call(&pb.ChatSendReq{Channel: pb.ChatChannel_CHAT_CHANNEL_WORLD, Text: "hello from " + r.account})

	deadline := time.After(duration)
	ticker := time.NewTicker(r.moveEvery)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			return nil
		case <-r.c.Done():
			return r.c.Err()
		case <-ticker.C:
			// Random walk inside the world bounds, within the allowed step.
			nx := clamp(x+float32(rand.IntN(41)-20), 0, gamesvc.WorldWidth-1)
			ny := clamp(y+float32(rand.IntN(41)-20), 0, gamesvc.WorldHeight-1)
			if _, err := r.call(&pb.SceneMoveReq{Target: &pb.Vector2{X: nx, Y: ny}}); err == nil {
				x, y = nx, ny
			}
		}
	}
}

func clamp(v, lo, hi float32) float32 { return min(max(v, lo), hi) }
