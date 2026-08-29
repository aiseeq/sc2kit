// Command ping connects to a running SC2 client and prints its version info.
//
//	ping [host:port]   (default 127.0.0.1:5000)
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aiseeq/sc2kit"
)

func main() {
	addr := "127.0.0.1:5000"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c, err := sc2kit.Dial(ctx, addr)
	if err != nil {
		log.Fatalf("dial: %v", err)
	}
	defer c.Close()

	ping, err := c.Ping(ctx)
	if err != nil {
		log.Fatalf("ping: %v", err)
	}
	fmt.Printf("game_version=%s data_version=%s data_build=%d base_build=%d status=%s\n",
		ping.GetGameVersion(), ping.GetDataVersion(), ping.GetDataBuild(), ping.GetBaseBuild(), c.Status())
}
