// 临时工具：走 Symedia 完整握手拿 authorize_url（复用生产代码，确保协议一致）
package main

import (
	"context"
	"fmt"
	"time"

	"diy-strm/internal/hdhive"
)

func main() {
	sc := hdhive.NewSymediaClient("", "")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// 方式1：不带 callback
	u1, err := sc.StartOAuth(ctx, "")
	fmt.Printf("no-callback: %v\n", u1)
	if err != nil {
		fmt.Println("err:", err)
	}

	// 方式2：带本站回调
	u2, err := sc.StartOAuth(ctx, "http://134.185.85.200:12333/hive-symedia/callback")
	fmt.Printf("with-callback: %v\n", u2)
	if err != nil {
		fmt.Println("err:", err)
	}
}
