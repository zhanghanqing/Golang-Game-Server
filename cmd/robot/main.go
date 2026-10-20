// Command robot simulates players for smoke and load testing.
//
// Each robot logs in, enters the game, inspects its bag, uses an item, says
// hello in world chat and then walks around randomly until the test ends.
// With -reconnect, a robot that is kicked or disconnected logs in again, like
// a real client would; this is how players move off a game node that shuts down.
//
//	go run ./cmd/robot -addr 127.0.0.1:7000 -n 100 -duration 30s
//	go run ./cmd/robot -ws ws://127.0.0.1:7001/ws -n 10
//	go run ./cmd/robot -addr 127.0.0.1:7000 -n 200 -duration 10m -reconnect
package main

import (
	"context"
	"errors"
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
	// reconnects counts successful re-entries after a kick or disconnect.
	reconnects atomic.Int64
	// gaveUp counts robots that lost the session and never got back in.
	gaveUp atomic.Int64
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
	reconnect := flag.Bool("reconnect", false, "log in again after a kick or disconnect until -duration ends")
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
			var err error
			if *reconnect {
				err = r.runWithReconnect(*duration)
			} else {
				err = r.run(*duration)
			}
			if err != nil {
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
	if *reconnect {
		fmt.Printf("reconnects:   %d\n", st.reconnects.Load())
		fmt.Printf("gave up:      %d\n", st.gaveUp.Load())
	}
	if st.loginFail.Load() > 0 || st.gaveUp.Load() > 0 {
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

// run plays one session; a failed login or enter counts as a login failure.
func (r *robot) run(duration time.Duration) error {
	entered, err := r.session(time.Now().Add(duration))
	if !entered {
		r.st.loginFail.Add(1)
	}
	return err
}

// runWithReconnect keeps the robot in the game until duration ends, logging
// in again (with a short random backoff) whenever the session ends early.
func (r *robot) runWithReconnect(duration time.Duration) error {
	deadline := time.Now().Add(duration)
	sessions := 0
	var lastErr error
	for time.Now().Before(deadline) {
		entered, err := r.session(deadline)
		if entered {
			if sessions > 0 {
				r.st.reconnects.Add(1)
			}
			sessions++
		}
		if err == nil {
			return nil // played until the deadline
		}
		lastErr = err
		time.Sleep(time.Duration(300+rand.IntN(700)) * time.Millisecond)
	}
	switch {
	case sessions == 0:
		r.st.loginFail.Add(1)
	case lastErr != nil && !errors.Is(lastErr, errSessionEnded):
		// The last attempt was a failed login: the robot ended outside the game.
		r.st.gaveUp.Add(1)
	}
	return lastErr
}

// errSessionEnded means the server closed a session that was in the game.
var errSessionEnded = errors.New("session ended by server")

// session connects, logs in, enters the game and plays until deadline. It
// returns whether the robot entered the game, and nil only if it stayed in
// until the deadline.
func (r *robot) session(deadline time.Time) (bool, error) {
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
		return false, err
	}
	defer r.c.Close()
	r.c.StartHeartbeat(10 * time.Second)

	if _, err := r.call(&pb.LoginReq{Account: r.account, Password: r.password, ClientVersion: "robot-1.0"}); err != nil {
		return false, fmt.Errorf("login: %w", err)
	}
	resp, err := r.call(&pb.EnterGameReq{Name: r.account})
	if err != nil {
		return false, fmt.Errorf("enter: %w", err)
	}
	r.st.loginOK.Add(1)
	enter := resp.(*pb.EnterGameResp)
	x, y := enter.Position.X, enter.Position.Y

	_, _ = r.call(&pb.BagGetReq{})
	_, _ = r.call(&pb.ItemUseReq{ItemId: 1002, Count: 1})
	_, _ = r.call(&pb.ChatSendReq{Channel: pb.ChatChannel_CHAT_CHANNEL_WORLD, Text: "hello from " + r.account})

	timeout := time.After(time.Until(deadline))
	ticker := time.NewTicker(r.moveEvery)
	defer ticker.Stop()
	for {
		select {
		case <-timeout:
			return true, nil
		case <-r.c.Done():
			return true, errSessionEnded
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
